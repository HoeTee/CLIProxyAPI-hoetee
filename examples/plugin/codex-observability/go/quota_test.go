package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestSplitCodexWindowHeader(t *testing.T) {
	cases := []struct {
		rest      string
		scope     string
		limitName string
		window    string
		field     string
		ok        bool
	}{
		{"primary-used-percent", "base", "", "primary", "used-percent", true},
		{"secondary-window-minutes", "base", "", "secondary", "window-minutes", true},
		{"primary-reset-after-seconds", "base", "", "primary", "reset-after-seconds", true},
		{"code-review-primary-used-percent", "code-review", "", "primary", "used-percent", true},
		{"bengalfox-primary-used-percent", "additional", "bengalfox", "primary", "used-percent", true},
		{"additional-gpt-5.3-codex-spark-secondary-window-minutes", "additional", "gpt-5.3-codex-spark", "secondary", "window-minutes", true},
		{"plan-type", "", "", "", "", false},
		{"allowed", "", "", "", "", false},
		{"primary", "", "", "", "", false},
	}
	for _, tc := range cases {
		scope, limitName, window, field, ok := splitCodexWindowHeader(tc.rest)
		if ok != tc.ok || scope != tc.scope || limitName != tc.limitName || window != tc.window || field != tc.field {
			t.Errorf("splitCodexWindowHeader(%q) = (%q,%q,%q,%q,%v), want (%q,%q,%q,%q,%v)",
				tc.rest, scope, limitName, window, field, ok, tc.scope, tc.limitName, tc.window, tc.field, tc.ok)
		}
	}
}

func TestSplitCodexScopeHeader(t *testing.T) {
	cases := []struct {
		rest      string
		scope     string
		limitName string
		field     string
		ok        bool
	}{
		{"allowed", "base", "", "allowed", true},
		{"limit-reached", "base", "", "limit-reached", true},
		{"code-review-allowed", "code-review", "", "allowed", true},
		{"additional-gpt-5.3-codex-spark-limit-name", "additional", "gpt-5.3-codex-spark", "limit-name", true},
		{"bengalfox-limit-reached", "additional", "bengalfox", "limit-reached", true},
		{"primary-used-percent", "", "", "", false},
		{"plan-type", "", "", "", false},
	}
	for _, tc := range cases {
		scope, limitName, field, ok := splitCodexScopeHeader(tc.rest)
		if ok != tc.ok || scope != tc.scope || limitName != tc.limitName || field != tc.field {
			t.Errorf("splitCodexScopeHeader(%q) = (%q,%q,%q,%v), want (%q,%q,%q,%v)",
				tc.rest, scope, limitName, field, ok, tc.scope, tc.limitName, tc.field, tc.ok)
		}
	}
}

func TestParseQuotaHeadersHTTPPath(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-Codex-Plan-Type", "pro")
	headers.Set("X-Codex-Primary-Used-Percent", "37")
	headers.Set("X-Codex-Primary-Window-Minutes", "300")
	headers.Set("X-Codex-Primary-Reset-At", "2026-09-22T10:00:00Z")
	headers.Set("X-Codex-Secondary-Used-Percent", "64")
	headers.Set("X-Codex-Secondary-Window-Minutes", "10080")
	headers.Set("X-Codex-Secondary-Reset-At", "2026-09-25T00:00:00Z")
	headers.Set("X-Codex-Allowed", "true")
	headers.Set("X-Codex-Limit-Reached", "false")
	headers.Set("X-Codex-Credits-Has-Credits", "true")
	headers.Set("X-Codex-Credits-Unlimited", "false")
	headers.Set("X-Codex-Credits-Balance", "1234")

	quota, ok := parseQuotaHeaders(headers)
	if !ok {
		t.Fatal("parseQuotaHeaders returned ok=false")
	}
	if quota.PlanType != "pro" {
		t.Errorf("plan type = %q", quota.PlanType)
	}
	if quota.Credits == nil || quota.Credits.Balance != "1234" {
		t.Errorf("credits = %+v", quota.Credits)
	}
	if len(quota.Windows) != 2 {
		t.Fatalf("windows = %d, want 2", len(quota.Windows))
	}
	primary := findWindow(quota.Windows, "base", "", "primary")
	if primary == nil || primary.UsedPercent != 37 || primary.WindowMinutes != 300 {
		t.Fatalf("primary window = %+v", primary)
	}
	if primary.Allowed != "true" || primary.LimitReached != "false" {
		t.Errorf("scope fields not merged into window: %+v", primary)
	}
	secondary := findWindow(quota.Windows, "base", "", "secondary")
	if secondary == nil || secondary.WindowMinutes != 10080 {
		t.Fatalf("secondary window = %+v", secondary)
	}
}

func TestParseQuotaHeadersWebsocketAdditional(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-Codex-Additional-GPT-5.3-Codex-Spark-Primary-Used-Percent", "12")
	headers.Set("X-Codex-Additional-GPT-5.3-Codex-Spark-Primary-Window-Minutes", "300")
	headers.Set("X-Codex-Additional-GPT-5.3-Codex-Spark-Primary-Reset-After-Seconds", "900")
	headers.Set("X-Codex-Additional-GPT-5.3-Codex-Spark-Limit-Name", "GPT-5.3-Codex-Spark")
	headers.Set("X-Codex-Active-Limit", "GPT-5.3-Codex-Spark")

	quota, ok := parseQuotaHeaders(headers)
	if !ok {
		t.Fatal("parseQuotaHeaders returned ok=false")
	}
	if quota.ActiveLimit != "GPT-5.3-Codex-Spark" {
		t.Errorf("active limit = %q", quota.ActiveLimit)
	}
	if len(quota.Windows) != 1 {
		t.Fatalf("windows = %+v", quota.Windows)
	}
	window := quota.Windows[0]
	// Go canonicalizes header keys, so the namespaced identifier is compared
	// case-insensitively; the display name carries the exact upstream spelling.
	if window.Scope != "additional" || !strings.EqualFold(window.LimitName, "GPT-5.3-Codex-Spark") {
		t.Errorf("window scope = %+v", window)
	}
	if window.DisplayName != "GPT-5.3-Codex-Spark" {
		t.Errorf("display name = %q", window.DisplayName)
	}
	if !window.HasResetAfter || window.ResetAfterSeconds != 900 {
		t.Errorf("reset after = %+v", window)
	}
}

func TestParseQuotaHeadersEmpty(t *testing.T) {
	if _, ok := parseQuotaHeaders(http.Header{}); ok {
		t.Error("empty headers reported quota")
	}
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	if _, ok := parseQuotaHeaders(headers); ok {
		t.Error("non-quota headers reported quota")
	}
	// A window without window-minutes is unusable on its own.
	incomplete := http.Header{}
	incomplete.Set("X-Codex-Primary-Used-Percent", "10")
	if _, ok := parseQuotaHeaders(incomplete); ok {
		t.Error("incomplete window reported quota")
	}
}

func findWindow(windows []windowObservation, scope, limitName, window string) *windowObservation {
	for i := range windows {
		if windows[i].Scope == scope && windows[i].LimitName == limitName && windows[i].Window == window {
			return &windows[i]
		}
	}
	return nil
}
