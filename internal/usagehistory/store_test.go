package usagehistory

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestHistoryRetainsRequestsAndObservedTokensAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	started := s.coverageStart
	prior := started.Add(-time.Hour)
	backfill := []ImportedUsage{{ID: "historical", At: prior, ClientID: "client-a", Provider: "deepseek", InputTokens: pointer(5)}}
	for i := 0; i < 2; i++ {
		if err := s.Import(context.Background(), backfill); err != nil {
			t.Fatal(err)
		}
	}
	ctx, req := usage.BeginHistoryRequest(context.Background(), s, usage.HistoryResult{
		ID: "a", At: started.Add(time.Minute), ClientID: "client-a", Provider: "deepseek", Model: "Alias-A",
	})
	usage.ObserveHistoryProvider(ctx, "deepseek")
	usage.PublishRecord(ctx, usage.Record{Provider: "deepseek", Failed: true})
	usage.PublishRecord(ctx, usage.Record{Provider: "deepseek", Detail: usage.Detail{InputTokens: 10, OutputTokens: 2, TokenFields: usage.ReportedCacheRead}})
	nested, nestedRequest := usage.BeginHistoryRequest(ctx, s, usage.HistoryResult{ID: "child", At: started, ClientID: "client-a", Provider: "codex"})
	if nestedRequest != nil {
		t.Fatal("nested request would count as a separate client call")
	}
	usage.ObserveHistoryProvider(nested, "codex")
	usage.PublishRecord(nested, usage.Record{Provider: "codex", Detail: usage.Detail{InputTokens: 3}})
	req.Complete("succeeded", 200)
	req.Complete("succeeded", 200)
	s.RecordRequest(usage.HistoryResult{ID: "b", At: started.Add(2 * time.Minute), ClientID: "client-a", Provider: "openai-compatible-glm-coding-plan", Model: "glm-model", Outcome: "rejected", StatusCode: 429})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	filter := Filter{From: prior, To: started.Add(time.Hour), Bucket: "hour", Limit: 1}
	summary, err := s.Summary(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	if !summary.CoverageStart.Equal(started) || summary.Totals.Calls != 2 || summary.Totals.Succeeded != 1 || summary.Totals.Rejected != 1 || summary.Totals.Imported != 1 ||
		summary.Totals.InputTokens == nil || *summary.Totals.InputTokens != 18 || summary.Totals.CachedTokens == nil || *summary.Totals.CachedTokens != 0 || len(summary.Clients) != 3 {
		t.Fatalf("incorrect summary: %+v", summary)
	}
	if len(summary.Timeline) == 0 || !timeBucketUsesBeijing(summary.Timeline[0].At) {
		t.Fatalf("incorrect timeline: %+v", summary.Timeline)
	}
	rows, err := s.Requests(context.Background(), filter)
	if err != nil || rows.Total != 4 || len(rows.Rows) != 1 || rows.Rows[0].Provider != "glm" || rows.Rows[0].Outcome != "rejected" || rows.Rows[0].InputTokens != nil {
		t.Fatalf("first page: %+v, err: %v", rows, err)
	}
	filter.Provider, filter.Model, filter.RequestID = "codex", "alias-a", "a"
	rows, err = s.Requests(context.Background(), filter)
	if err != nil || rows.Total != 1 || len(rows.Rows) != 1 || !rows.Rows[0].UsageOnly || rows.Rows[0].CachedTokens != nil {
		t.Fatalf("cross-provider row: %+v, err: %v", rows, err)
	}
	filter.Provider, filter.Model, filter.RequestID = "deepseek", "", ""
	filter.Offset = 1
	rows, err = s.Requests(context.Background(), filter)
	if err != nil || rows.Total != 2 || len(rows.Rows) != 1 || rows.Rows[0].Outcome != "imported" || rows.Rows[0].RequestID != "" {
		t.Fatalf("second page: %+v, err: %v", rows, err)
	}
}

func TestHistoryRetentionDeletesOnlyExpiredRows(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "history.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	now := time.Now().UTC()
	boundary := now.AddDate(0, -13, 0).Truncate(time.Millisecond)
	s.RecordRequest(usage.HistoryResult{ID: "old", At: boundary.Add(-time.Millisecond), ClientID: "a", Provider: "codex", Outcome: "succeeded"})
	s.RecordRequest(usage.HistoryResult{ID: "kept", At: boundary, ClientID: "a", Provider: "codex", Outcome: "succeeded"})
	if err := s.prune(now); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Requests(context.Background(), Filter{From: boundary.Add(-time.Hour), To: now, Limit: 50})
	if err != nil || rows.Total != 1 || len(rows.Rows) != 1 || rows.Rows[0].RequestID != "kept" {
		t.Fatalf("retention rows: %+v, err: %v", rows, err)
	}
}

func pointer(n int64) *int64 { return &n }

func timeBucketUsesBeijing(raw string) bool {
	parsed, err := time.Parse(time.RFC3339, raw)
	_, offset := parsed.Zone()
	return err == nil && offset == 8*60*60 && parsed.Minute() == 0
}

func BenchmarkHistoryOneYear(b *testing.B) {
	const records = 100_000
	ctx := context.Background()
	s, err := Open(filepath.Join(b.TempDir(), "usage.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	now := time.Now().UTC()
	start := now.AddDate(-1, 0, 0)
	const seedRequests = `WITH RECURSIVE n(i) AS (VALUES(0) UNION ALL SELECT i+1 FROM n WHERE i+1<?)
INSERT INTO history_request
SELECT 'live:'||i, ?+i*?, 'client-'||(i%8), CASE i%3 WHEN 0 THEN 'codex' WHEN 1 THEN 'deepseek' ELSE 'glm' END,
       'test-model', 'succeeded', 200 FROM n;`
	if _, err := s.db.Exec(seedRequests, records, start.UnixMilli(), now.Sub(start).Milliseconds()/records); err != nil {
		b.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO history_tokens SELECT id, at, provider, 20, 10, 5 FROM history_request`); err != nil {
		b.Fatal(err)
	}
	f := Filter{From: start, To: now, Bucket: "month", Limit: 50}
	b.Run("Summary", func(b *testing.B) {
		for b.Loop() {
			result, err := s.Summary(ctx, f)
			if err != nil || result.Totals.Calls != records || result.Totals.InputTokens == nil || *result.Totals.InputTokens != 20*records {
				b.Fatalf("incorrect totals: calls %d, error %v", result.Totals.Calls, err)
			}
		}
	})
	b.Run("FirstPage", func(b *testing.B) {
		for b.Loop() {
			result, err := s.Requests(ctx, f)
			if err != nil || result.Total != records || len(result.Rows) != f.Limit {
				b.Fatalf("incorrect page: total %d, rows %d, error %v", result.Total, len(result.Rows), err)
			}
		}
	})
}
