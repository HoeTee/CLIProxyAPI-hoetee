package main

import (
	"net/http"
	"strconv"
	"strings"
)

// quotaObservation is one parsed snapshot of the upstream Codex quota signals
// carried by a single response.
type quotaObservation struct {
	PlanType    string
	ActiveLimit string
	Windows     []windowObservation
	Scopes      []scopeObservation
	Credits     *creditsObservation
}

// scopeObservation holds the limit-wide fields that are not tied to a window.
type scopeObservation struct {
	Scope        string
	LimitName    string
	DisplayName  string
	Allowed      string
	LimitReached string
}

// windowObservation is one rate-limit window reported by the upstream.
type windowObservation struct {
	Scope             string
	LimitName         string
	DisplayName       string
	Window            string
	WindowMinutes     int
	UsedPercent       int
	HasPercent        bool
	ResetAt           string
	ResetAfterSeconds int64
	HasResetAfter     bool
	Allowed           string
	LimitReached      string
}

// creditsObservation mirrors the upstream credits object.
type creditsObservation struct {
	HasCredits string
	Unlimited  string
	Balance    string
}

// parseQuotaHeaders extracts every Codex quota signal from upstream response
// headers. It returns false when the response carried no quota information at
// all, which is expected for responses that never reached the rate-limit
// reporting path.
func parseQuotaHeaders(headers http.Header) (*quotaObservation, bool) {
	if len(headers) == 0 {
		return nil, false
	}
	// The header map is keyed by the canonical upstream spelling, and the raw
	// name is sliced so additional-limit identifiers keep the case the upstream
	// used while field names stay lowercase for matching.
	scalars := make(map[string]string, len(headers))
	for name, values := range headers {
		if len(values) == 0 {
			continue
		}
		scalars[strings.ToLower(name)] = strings.TrimSpace(values[0])
	}

	out := &quotaObservation{
		PlanType:    scalars["x-codex-plan-type"],
		ActiveLimit: scalars["x-codex-active-limit"],
	}
	if scalars["x-codex-credits-balance"] != "" || scalars["x-codex-credits-has-credits"] != "" || scalars["x-codex-credits-unlimited"] != "" {
		out.Credits = &creditsObservation{
			HasCredits: scalars["x-codex-credits-has-credits"],
			Unlimited:  scalars["x-codex-credits-unlimited"],
			Balance:    scalars["x-codex-credits-balance"],
		}
	}

	const codexPrefix = "X-Codex-"
	windows := make(map[string]*windowObservation)
	scopes := make(map[string]*scopeObservation)
	for name, values := range headers {
		if len(values) == 0 {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(codexPrefix)) {
			continue
		}
		value := strings.TrimSpace(values[0])
		rest := strings.TrimPrefix(name, codexPrefix)
		if scope, limitName, window, field, okWindow := splitCodexWindowHeader(rest); okWindow {
			key := scope + "|" + limitName + "|" + window
			entry := windows[key]
			if entry == nil {
				entry = &windowObservation{Scope: scope, LimitName: limitName, Window: window}
				windows[key] = entry
			}
			switch field {
			case "used-percent":
				if parsed, errParse := strconv.Atoi(value); errParse == nil {
					entry.UsedPercent = parsed
					entry.HasPercent = true
				}
			case "window-minutes":
				if parsed, errParse := strconv.Atoi(value); errParse == nil {
					entry.WindowMinutes = parsed
				}
			case "reset-at":
				entry.ResetAt = value
			case "reset-after-seconds":
				if parsed, errParse := strconv.ParseInt(value, 10, 64); errParse == nil {
					entry.ResetAfterSeconds = parsed
					entry.HasResetAfter = true
				}
			case "limit-name":
				entry.DisplayName = value
			}
			continue
		}
		scope, limitName, field, okScope := splitCodexScopeHeader(rest)
		if !okScope {
			continue
		}
		key := scope + "|" + limitName
		entry := scopes[key]
		if entry == nil {
			entry = &scopeObservation{Scope: scope, LimitName: limitName}
			scopes[key] = entry
		}
		switch field {
		case "allowed":
			entry.Allowed = value
		case "limit-reached":
			entry.LimitReached = value
		case "limit-name":
			entry.DisplayName = value
		}
	}

	for _, entry := range windows {
		// A window is only usable when both the percentage and the window length
		// are present, matching how the host itself decides to emit these headers.
		if !entry.HasPercent || entry.WindowMinutes <= 0 {
			continue
		}
		if scope := scopes[entry.Scope+"|"+entry.LimitName]; scope != nil {
			entry.Allowed = scope.Allowed
			entry.LimitReached = scope.LimitReached
			if entry.DisplayName == "" {
				entry.DisplayName = scope.DisplayName
			}
		}
		out.Windows = append(out.Windows, *entry)
	}
	for _, entry := range scopes {
		if entry.Allowed == "" && entry.LimitReached == "" && entry.DisplayName == "" {
			continue
		}
		if entry.LimitName != "" && entry.DisplayName == "" {
			// The websocket path already carries the display name inside the
			// namespaced headers, so an empty value here just means the limit
			// was only ever observed on the HTTP short-name path.
			entry.DisplayName = ""
		}
		out.Scopes = append(out.Scopes, *entry)
	}

	if len(out.Windows) == 0 && len(out.Scopes) == 0 && out.PlanType == "" && out.ActiveLimit == "" && out.Credits == nil {
		return nil, false
	}
	return out, true
}

// splitCodexWindowHeader splits a header suffix such as "primary-used-percent"
// or "additional-<name>-secondary-window-minutes".
//
// The websocket path namespaces additional limits with an "additional-" prefix
// while HTTP responses namespace them with a bare short name, so any prefix that
// is neither empty nor "code-review" is reported as an additional limit and the
// raw namespace text is preserved in limitName.
func splitCodexWindowHeader(rest string) (scope, limitName, window, field string, ok bool) {
	parts := strings.Split(rest, "-")
	index := -1
	for i, part := range parts {
		switch strings.ToLower(part) {
		case "primary", "secondary":
			index = i
			window = strings.ToLower(part)
		}
		if index >= 0 {
			break
		}
	}
	if index < 0 || index == len(parts)-1 {
		return "", "", "", "", false
	}
	field = strings.ToLower(strings.Join(parts[index+1:], "-"))
	scope, limitName = classifyCodexQuotaScope(parts[:index])
	return scope, limitName, window, field, true
}

// splitCodexScopeHeader splits a scope-wide header suffix such as "allowed",
// "code-review-limit-reached" or "additional-<name>-limit-name".
func splitCodexScopeHeader(rest string) (scope, limitName, field string, ok bool) {
	lower := strings.ToLower(rest)
	for _, candidate := range []string{"allowed", "limit-reached", "limit-name"} {
		if lower == candidate {
			return "base", "", candidate, true
		}
		suffix := "-" + candidate
		if !strings.HasSuffix(lower, suffix) {
			continue
		}
		prefix := rest[:len(rest)-len(suffix)]
		scope, limitName = classifyCodexQuotaScope(strings.Split(prefix, "-"))
		return scope, limitName, candidate, true
	}
	return "", "", "", false
}

// classifyCodexQuotaScope maps the namespace parts in front of a window or
// scope field onto a scope name and an additional-limit identifier.
func classifyCodexQuotaScope(prefix []string) (scope, limitName string) {
	switch {
	case len(prefix) == 0:
		return "base", ""
	case strings.EqualFold(strings.Join(prefix, "-"), "code-review"):
		return "code-review", ""
	case strings.EqualFold(prefix[0], "additional") && len(prefix) > 1:
		return "additional", strings.Join(prefix[1:], "-")
	default:
		return "additional", strings.Join(prefix, "-")
	}
}
