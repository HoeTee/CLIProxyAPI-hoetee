package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func configureTestState(t *testing.T, state *quotaState, path string) {
	t.Helper()
	yamlText := fmt.Sprintf(`storage: {path: %q}
weekly-reset-anchor: "2025-10-05T09:00:00+08:00"
pricing:
  gpt-test: {input: 1, cached-input: 0.1, cache-write: 1.25, output: 4, coefficient: 2}
reservation-usd: 0.000001
`, filepath.ToSlash(path))
	raw, _ := json.Marshal(map[string][]byte{"config_yaml": []byte(yamlText)})
	if err := state.configure(raw); err != nil {
		t.Fatal(err)
	}
	if state.location.String() != "Asia/Shanghai" || *state.cfg.AccountProtection.StopUsedPercent != 90 {
		t.Fatalf("defaults were not applied: timezone=%s stop=%d", state.location, *state.cfg.AccountProtection.StopUsedPercent)
	}
}

func codexRequest(id string, limit float64) pluginapi.RequestInterceptRequest {
	return pluginapi.RequestInterceptRequest{Provider: "codex", RequestID: id, Model: "gpt-test", Metadata: map[string]any{"client_id": "alice", "codex_weekly_usd": fmt.Sprint(limit)}}
}

func TestCodexTinyUsageAndLimitReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.ndjson")
	state := newQuotaState()
	configureTestState(t, state, path)
	defer state.close()
	if got := state.before(codexRequest("one", 0.001)); got.Terminate {
		t.Fatalf("request rejected: %s", got.ResponseBody)
	}
	state.complete(pluginapi.RequestCompletion{RequestID: "one"})
	record := pluginapi.UsageRecord{RequestID: "one", Provider: "codex", ClientID: "alice", Model: "gpt-test", Detail: pluginapi.UsageDetail{InputTokens: 2, OutputTokens: 1}}
	if err := state.record(record); err != nil {
		t.Fatal(err)
	}
	if err := state.record(record); err != nil || len(state.events) != 1 {
		t.Fatalf("duplicate usage was recorded: events=%d err=%v", len(state.events), err)
	}
	// ((2 * $1) + (1 * $4)) / 1M * coefficient 2 = $0.000012.
	if got := state.events[0].CostUSD; got != 0.000012 {
		t.Fatalf("cost = %.9f", got)
	}
	if got := state.before(codexRequest("two", 0.000012)); !got.Terminate || got.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("exhausted quota accepted: %+v", got)
	}
	if got := state.before(codexRequest("three", 0.001)); got.Terminate {
		t.Fatalf("raised limit rejected: %s", got.ResponseBody)
	}
}

func TestClientSnapshotKeepsWeeklyUsageSeparate(t *testing.T) {
	state := newQuotaState()
	configureTestState(t, state, filepath.Join(t.TempDir(), "usage.ndjson"))
	t.Cleanup(state.close)
	now := state.anchor.Add(10 * 24 * time.Hour)
	start := state.cycleStart(now)
	state.events = []usageEvent{
		{ClientID: "owner", Time: start.Add(-time.Second), CostUSD: 100},
		{ClientID: "owner", Time: start, CostUSD: 1},
		{ClientID: "client-2", Time: now, CostUSD: 2},
		{ClientID: "other", Time: now, CostUSD: 3},
	}
	rows, err := state.snapshot([]string{"owner", "client-2", "new"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows["owner"].UsedUSD != 1 || rows["client-2"].UsedUSD != 2 ||
		rows["new"].UsedUSD != 0 || !rows["new"].ResetAt.Equal(start.Add(7*24*time.Hour)) {
		t.Fatalf("unexpected weekly client snapshots: %+v", rows)
	}
}

func TestCodexCycleUsesHistoricalAnchor(t *testing.T) {
	state := newQuotaState()
	configureTestState(t, state, filepath.Join(t.TempDir(), "usage.ndjson"))
	defer state.close()
	start := state.cycleStart(time.Date(2026, 10, 4, 9, 0, 1, 0, time.FixedZone("+08", 8*3600)))
	if want := time.Date(2026, 10, 4, 9, 0, 0, 0, state.location); !start.Equal(want) {
		t.Fatalf("cycle start = %v, want %v", start, want)
	}
}

func TestGPT61SolStandardAPIRates(t *testing.T) {
	state := newQuotaState()
	configureTestState(t, state, filepath.Join(t.TempDir(), "usage.ndjson"))
	defer state.close()
	state.prices["gpt-6.1-sol"] = price{Input: 2, CachedInput: 0.10, CacheWrite: 2.50, Output: 10, Coefficient: 1}
	for _, tc := range []struct {
		id    string
		input int64
		want  float64
	}{
		{id: "at-threshold", input: 272_000, want: 0.544_582},
		{id: "above-threshold", input: 272_001, want: 1.088_868},
	} {
		err := state.record(pluginapi.UsageRecord{
			RequestID: tc.id, Provider: "codex", ClientID: "alice", Model: "gpt-6.1-sol",
			Detail: pluginapi.UsageDetail{InputTokens: tc.input, CacheReadTokens: 20, CacheCreationTokens: 40, OutputTokens: 60},
		})
		if err != nil {
			t.Fatal(err)
		}
		got := state.events[len(state.events)-1].CostUSD
		if math.Abs(got-tc.want) > 1e-12 {
			t.Errorf("%s cost = %.9f, want %.9f", tc.id, got, tc.want)
		}
	}
}

func TestCodexIgnoresOtherProviderAndRequiresQuota(t *testing.T) {
	state := newQuotaState()
	configureTestState(t, state, filepath.Join(t.TempDir(), "usage.ndjson"))
	defer state.close()
	req := codexRequest("other", 0)
	req.Provider = "deepseek"
	if got := state.before(req); got.Terminate {
		t.Fatal("other provider was rejected")
	}
	if got := state.before(codexRequest("missing", 0)); !got.Terminate || got.StatusCode != http.StatusForbidden {
		t.Fatalf("missing quota accepted: %+v", got)
	}
}

func TestCodexAccountPercentGuard(t *testing.T) {
	state := newQuotaState()
	configureTestState(t, state, filepath.Join(t.TempDir(), "usage.ndjson"))
	defer state.close()
	headers := make(http.Header)
	headers.Set("X-Codex-Secondary-Used-Percent", "92")
	headers.Set("X-Codex-Secondary-Reset-After-Seconds", "3600")
	if err := state.record(pluginapi.UsageRecord{Provider: "codex", ClientID: "alice", Model: "gpt-test", ResponseHeaders: headers}); err != nil {
		t.Fatal(err)
	}
	if got := state.before(codexRequest("guard", 1)); !got.Terminate || got.StatusCode != http.StatusTooManyRequests {
		t.Fatal("account guard did not reject")
	}
}
