package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestUnitCostSplitsCachedFromInput(t *testing.T) {
	rate := modelRate{Match: "sol", Input: 100, Cached: 10, Output: 500}
	tokens := tokenSum{Input: 1_000_000, CacheRead: 1_000_000, Output: 0}
	// Cached input is a subset of input, so the whole prompt is billed at the
	// cached rate instead of being charged twice.
	if got := rate.unitCost(tokens); got != 10 {
		t.Errorf("cached unitCost = %v, want 10", got)
	}
	tokens = tokenSum{Input: 1_000_000, Output: 1_000_000}
	if got := rate.unitCost(tokens); got != 600 {
		t.Errorf("uncached unitCost = %v, want 600", got)
	}
	tokens = tokenSum{Input: 500_000, CacheRead: 200_000, Output: 100_000}
	want := (300_000*100.0 + 200_000*10.0 + 100_000*500.0) / 1e6
	if got := rate.unitCost(tokens); got != want {
		t.Errorf("mixed unitCost = %v, want %v", got, want)
	}
}

func TestRateResolverPrefersOverridesAndSpecificBuiltins(t *testing.T) {
	resolver := newRateResolver(map[string]rateRow{"my-model": {Input: 1, Cached: 0.1, Output: 2}})
	if got := resolver.resolve("my-model-v2"); got.Input != 1 {
		t.Errorf("override not applied: %+v", got)
	}
	if got := resolver.resolve("gpt-5.4-mini"); got.Input != 18.75 {
		t.Errorf("specific builtin not applied: %+v", got)
	}
	if got := resolver.resolve("unknown-model"); got != defaultRate {
		t.Errorf("default rate not applied: %+v", got)
	}
}

func TestBucketStartAlignsToHalfHour(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	o := &observer{loc: loc, bucket: 30 * time.Minute}
	at := time.Date(2026, 9, 22, 14, 47, 13, 0, loc)
	start := o.bucketStart(at)
	want := time.Date(2026, 9, 22, 14, 30, 0, 0, loc)
	if !start.Equal(want) {
		t.Errorf("bucketStart = %v, want %v", start, want)
	}
	exact := o.bucketStart(want)
	if !exact.Equal(want) {
		t.Errorf("bucketStart of aligned time = %v", exact)
	}
}

func TestObserverWritesBucketsAndDetectsReset(t *testing.T) {
	dir := t.TempDir()
	loc := time.FixedZone("CST", 8*3600)
	cfg := observeConfig{
		Enabled:       true,
		Dir:           dir,
		BucketMinutes: 30,
		KeyLabels:     []keyLabel{{Key: "sk-alice", Label: "alice"}},
	}
	o := newObserver(cfg, loc)
	base := time.Date(2026, 9, 22, 10, 5, 0, 0, loc)

	headers := func(percent string) map[string][]string {
		return map[string][]string{
			"X-Codex-Plan-Type":              {"pro"},
			"X-Codex-Primary-Used-Percent":   {percent},
			"X-Codex-Primary-Window-Minutes": {"300"},
			"X-Codex-Primary-Reset-At":       {"2026-09-22T15:00:00Z"},
		}
	}
	record := func(at time.Time, key string, tokens int64, percent string) {
		o.record(pluginapi.UsageRecord{
			Provider:        "codex",
			Model:           "gpt-5.2-codex",
			APIKey:          key,
			AuthID:          "auth-1",
			RequestedAt:     at,
			Latency:         1500 * time.Millisecond,
			Detail:          pluginapi.UsageDetail{InputTokens: tokens, OutputTokens: 0},
			ResponseHeaders: headers(percent),
		})
	}

	record(base, "sk-alice", 1_000_000, "10")
	record(base.Add(2*time.Minute), "sk-alice", 1_000_000, "12")
	// A lower percentage inside the same bucket means the upstream window rolled over.
	record(base.Add(4*time.Minute), "sk-alice", 100, "1")
	// A later bucket must stay separate.
	next := base.Add(31 * time.Minute)
	record(next, "sk-alice", 500_000, "3")

	o.close()

	rawPath := filepath.Join(dir, "raw-2026-09-22.ndjson")
	rawLines := readLines(t, rawPath)
	if len(rawLines) != 4 {
		t.Fatalf("raw lines = %d, want 4", len(rawLines))
	}

	keyLines := readLines(t, filepath.Join(dir, "key-usage-2026-09-22.ndjson"))
	if len(keyLines) != 2 {
		t.Fatalf("key-usage lines = %d, want 2\n%s", len(keyLines), strings.Join(keyLines, "\n"))
	}
	var first keyBucket
	if errUnmarshal := json.Unmarshal([]byte(keyLines[0]), &first); errUnmarshal != nil {
		t.Fatalf("decode key bucket: %v", errUnmarshal)
	}
	if first.Requests != 3 || first.Tokens.Input != 2_000_100 {
		t.Errorf("first bucket = %+v", first)
	}
	if first.KeyLabel != "alice" {
		t.Errorf("key label = %q", first.KeyLabel)
	}
	if !strings.HasPrefix(first.Key, "0") && len(first.Key) != 12 {
		t.Errorf("hashed key = %q", first.Key)
	}
	if first.Key == "sk-alice" {
		t.Error("raw api key leaked into the log")
	}
	if first.Units <= 0 {
		t.Errorf("units = %v, want > 0", first.Units)
	}

	acctLines := readLines(t, filepath.Join(dir, "account-quota-2026-09-22.ndjson"))
	if len(acctLines) != 2 {
		t.Fatalf("account-quota lines = %d, want 2", len(acctLines))
	}
	var account accountQuotaRecord
	if errUnmarshal := json.Unmarshal([]byte(acctLines[0]), &account); errUnmarshal != nil {
		t.Fatalf("decode account bucket: %v", errUnmarshal)
	}
	if account.AuthID != "auth-1" || account.PlanType != "pro" {
		t.Errorf("account bucket = %+v", account)
	}
	if len(account.Windows) != 1 {
		t.Fatalf("windows = %+v", account.Windows)
	}
	window := account.Windows[0]
	if window.First != 10 || window.Last != 1 || window.Min != 1 || window.Max != 12 {
		t.Errorf("window samples = %+v", window)
	}
	if !window.ResetSeen {
		t.Errorf("reset not detected: %+v", window)
	}
	if window.Delta != -9 {
		t.Errorf("delta = %d, want -9", window.Delta)
	}
	if window.WindowMinutes != 300 || window.ResetAt != "2026-09-22T15:00:00Z" {
		t.Errorf("window metadata = %+v", window)
	}
}

func TestObserverPrunesOnlyOwnDayFiles(t *testing.T) {
	dir := t.TempDir()
	loc := time.UTC
	own := filepath.Join(dir, "raw-2020-01-01.ndjson")
	foreign := filepath.Join(dir, "keepme.ndjson")
	for _, path := range []string{own, foreign} {
		if errWrite := os.WriteFile(path, []byte("x\n"), 0o644); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	pruneOwnedFiles(dir, time.Now().AddDate(0, 0, -30))
	if _, errStat := os.Stat(own); !os.IsNotExist(errStat) {
		t.Error("own stale file was not pruned")
	}
	if _, errStat := os.Stat(foreign); errStat != nil {
		t.Error("foreign file was pruned")
	}
	_ = loc
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read %s: %v", path, errRead)
	}
	text := strings.TrimRight(string(raw), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
