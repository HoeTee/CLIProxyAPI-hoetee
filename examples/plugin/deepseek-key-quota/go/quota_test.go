package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func configureTestState(t *testing.T, state *quotaState, path string) {
	t.Helper()
	yamlText := fmt.Sprintf(`storage: {path: %q}
pricing:
  off-peak: {cache-hit: 0.02, cache-miss: 1.00, output: 4.00}
  peak: {cache-hit: 0.04, cache-miss: 2.00, output: 8.00}
reservation-cny: 0.000001
`, filepath.ToSlash(path))
	raw, _ := json.Marshal(map[string][]byte{"config_yaml": []byte(yamlText)})
	if err := state.configure(raw); err != nil {
		t.Fatal(err)
	}
}
func request(id string, limit float64) pluginapi.RequestInterceptRequest {
	return pluginapi.RequestInterceptRequest{Provider: "openai-compatible-deepseek", RequestID: id, Model: "deepseek-chat", Metadata: map[string]any{"client_id": "alice", "deepseek_limit_cny": fmt.Sprint(limit)}}
}
func TestDeepSeekThreeBucketBillingAndLifetimeLimit(t *testing.T) {
	state := newQuotaState()
	configureTestState(t, state, filepath.Join(t.TempDir(), "usage.ndjson"))
	defer state.close()
	if got := state.before(request("one", 1)); got.Terminate {
		t.Fatalf("request rejected: %s", got.ResponseBody)
	}
	state.complete(pluginapi.RequestCompletion{RequestID: "one"})
	record := pluginapi.UsageRecord{RequestID: "one", Provider: "openai-compatible-deepseek", ClientID: "alice", Model: "deepseek-chat", Detail: pluginapi.UsageDetail{InputTokens: 12, CacheReadTokens: 7, CacheMissTokens: 5, OutputTokens: 3}}
	if err := state.record(record); err != nil {
		t.Fatal(err)
	}
	if err := state.record(record); err != nil || len(state.events) != 1 {
		t.Fatalf("duplicate usage was recorded: events=%d err=%v", len(state.events), err)
	}
	if state.events[0].CacheHit != 7 || state.events[0].CacheMiss != 5 || state.events[0].Output != 3 {
		t.Fatalf("wrong buckets: %+v", state.events[0])
	}
	if got := state.before(request("two", state.events[0].CostCNY)); !got.Terminate || got.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("lifetime quota accepted: %+v", got)
	}
}
func TestDeepSeekHardcodedBeijingPeriods(t *testing.T) {
	state := newQuotaState()
	configureTestState(t, state, filepath.Join(t.TempDir(), "usage.ndjson"))
	defer state.close()
	period, _ := state.rateAt(time.Date(2026, 9, 28, 9, 0, 0, 0, state.location))
	if period != "peak" {
		t.Fatalf("09:00 weekday = %s", period)
	}
	period, _ = state.rateAt(time.Date(2026, 9, 28, 12, 0, 0, 0, state.location))
	if period != "off-peak" {
		t.Fatalf("12:00 weekday = %s", period)
	}
	period, _ = state.rateAt(time.Date(2026, 9, 27, 10, 0, 0, 0, state.location))
	if period != "off-peak" {
		t.Fatalf("weekend = %s", period)
	}
}
func TestDeepSeekIgnoresCodexAndRequiresQuota(t *testing.T) {
	state := newQuotaState()
	configureTestState(t, state, filepath.Join(t.TempDir(), "usage.ndjson"))
	defer state.close()
	req := request("other", 0)
	req.Provider = "codex"
	if got := state.before(req); got.Terminate {
		t.Fatal("Codex request rejected")
	}
	if got := state.before(request("missing", 0)); !got.Terminate || got.StatusCode != http.StatusForbidden {
		t.Fatalf("missing quota accepted: %+v", got)
	}
}
