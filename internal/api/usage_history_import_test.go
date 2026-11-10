package api

import "testing"

func TestParseQuotaLedgerEntryTokenAccounting(t *testing.T) {
	for _, tt := range []struct {
		provider, row string
		input, output int64
		cache         int64
		hasCache      bool
	}{
		{"codex", `{"time":"2026-10-01T03:00:00+08:00","client_id":"one","model":"gpt","input_tokens":20,"cache_read_tokens":70,"cache_write_tokens":10,"output_tokens":30}`, 100, 30, 70, true},
		{"deepseek", `{"time":"2026-10-01T00:00:00Z","client_id":"one","model":"deepseek","cache_hit_tokens":25,"cache_miss_tokens":75,"output_tokens":15}`, 100, 15, 25, true},
		{"glm", `{"at":"2026-10-01T00:00:00Z","client":"one","usage":{"model":"glm","input_tokens":100,"output_tokens":20}}`, 100, 20, 0, false},
		{"glm", `{"at":"2026-10-01T00:00:00Z","client":"one","usage":{"input_tokens":100,"output_tokens":20,"cached_tokens":0}}`, 100, 20, 0, true},
	} {
		t.Run(tt.provider, func(t *testing.T) {
			entry, err := parseQuotaLedgerEntry(tt.provider, []byte(tt.row))
			if err != nil {
				t.Fatal(err)
			}
			if entry.Provider != tt.provider || entry.ClientID != "one" || entry.InputTokens == nil ||
				*entry.InputTokens != tt.input || entry.OutputTokens == nil || *entry.OutputTokens != tt.output {
				t.Fatalf("unexpected mapped usage: %+v", entry)
			}
			if (entry.CachedTokens != nil) != tt.hasCache || (tt.hasCache && *entry.CachedTokens != tt.cache) {
				t.Fatalf("unexpected cache tokens: %v", entry.CachedTokens)
			}
		})
	}
	entry, err := parseQuotaLedgerEntry("glm", []byte(`{"at":"2026-10-01T00:00:00Z","client":"one","usage":null}`))
	if err != nil || entry.InputTokens != nil || entry.OutputTokens != nil || entry.CachedTokens != nil {
		t.Fatalf("unknown upstream usage must stay unknown: %+v, %v", entry, err)
	}
	if _, err := parseQuotaLedgerEntry("codex", []byte(`{"time":"2026-10-01T00:00:00Z","client_id":"one","input_tokens":9223372036854775807,"cache_read_tokens":1,"cache_write_tokens":0}`)); err == nil {
		t.Fatal("overflowing legacy token total was accepted")
	}
}
