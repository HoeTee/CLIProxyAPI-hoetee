package main

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func testPointPricing() *pointPricing {
	return &pointPricing{
		Divisor: 10000, OffPeakMultiplier: 0.5, PeakHours: []int{14, 18},
		Models: map[string]pointRates{
			"glm-5.3":       {CacheMiss: 6.9, CacheHit: 1.7, Output: 24},
			"glm-5.3-flash": {CacheMiss: 2.3, CacheHit: 0.56, Output: 8},
		},
	}
}

func TestEstimatedPointsWithModelAndTimeFromResponse(t *testing.T) {
	cached := int64(8_000)
	for _, tc := range []struct {
		name   string
		model  string
		day    int
		hour   int
		minute int
		want   float64
	}{
		{name: "before peak", model: "glm-5.3", day: 9, hour: 13, minute: 59, want: 2.57},
		{name: "peak start", model: "glm-5.3", day: 9, hour: 14, want: 5.14},
		{name: "peak end", model: "glm-5.3", day: 9, hour: 18, want: 2.57},
		{name: "weekend", model: "glm-5.3", day: 8, hour: 15, want: 2.57},
		{name: "flash peak", model: "glm-5.3-flash", day: 9, hour: 15, want: 1.708},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Date(2026, 11, tc.day, tc.hour, tc.minute, 0, 0, beijing)
			usage := &Usage{Model: tc.model, Created: at.Unix(), Input: 10_000, Cached: &cached, Output: 1_000}
			got := testPointPricing().estimate(usage)
			if got == nil || math.Abs(*got-tc.want) > 1e-9 {
				t.Fatalf("estimated points = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPointEstimatesReplayWithoutChangingTokenQuotas(t *testing.T) {
	at := time.Date(2026, 11, 9, 15, 0, 0, 0, beijing)
	path := filepath.Join(t.TempDir(), "usage.ndjson")
	ledger := NewLedger(path)
	ledger.now = func() time.Time { return at }
	ledger.pricing = testPointPricing()
	cached := int64(8_000)
	if err := ledger.Record("alice", &Usage{Model: "glm-5.3", Created: at.Unix(), Input: 10_000, Cached: &cached, Output: 1_000}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Record("alice", &Usage{Input: 200, Output: 100}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewLedger(path).Snapshot("alice", at)
	if err != nil {
		t.Fatal(err)
	}
	for _, window := range []Window{snapshot.FiveHour, snapshot.Weekly} {
		if window.Used != 11_300 || window.EstimatedPoints == nil ||
			math.Abs(*window.EstimatedPoints-5.14) > 1e-9 || window.Unpriced != 1 {
			t.Fatalf("replayed window = %+v", window)
		}
	}
}

func TestReloadReadsPricingFromYAMLForFutureEntries(t *testing.T) {
	previous := ledger.Swap(nil)
	t.Cleanup(func() { ledger.Store(previous) })
	path := filepath.ToSlash(filepath.Join(t.TempDir(), "usage.ndjson"))
	settings := func(output int) []byte {
		t.Helper()
		yaml := fmt.Sprintf(`storage: {path: %q}
pricing:
  divisor: 10
  off-peak-multiplier: 0.25
  peak-hours: [12, 13]
  models:
    glm-5.3: {cache-miss: 1, cache-hit: 2, output: %d}
`, path, output)
		raw, err := json.Marshal(map[string][]byte{"config_yaml": []byte(yaml)})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if err := configure(settings(3)); err != nil {
		t.Fatal(err)
	}
	current := ledger.Load()
	at := time.Date(2026, 11, 9, 12, 30, 0, 0, beijing)
	current.now = func() time.Time { return at }
	cached := int64(1)
	usage := &Usage{Model: "glm-5.3", Created: at.Unix(), Input: 3, Cached: &cached, Output: 2}
	if err := current.Record("alice", usage); err != nil {
		t.Fatal(err)
	}
	if err := configure(settings(6)); err != nil {
		t.Fatal(err)
	}
	if ledger.Load() != current {
		t.Fatal("reloading pricing replaced the ledger")
	}
	if err := current.Record("alice", usage); err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewLedger(path).Snapshot("alice", at)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.FiveHour.Used != 10 || snapshot.FiveHour.EstimatedPoints == nil ||
		math.Abs(*snapshot.FiveHour.EstimatedPoints-2.6) > 1e-9 {
		t.Fatalf("replayed pricing = %+v, want 10 tokens and 1.0 + 1.6 points", snapshot.FiveHour)
	}
}
