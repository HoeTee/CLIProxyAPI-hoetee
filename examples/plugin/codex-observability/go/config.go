package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const pluginName = "codex-observability"
const pluginVersion = "0.1.0"

// pluginConfig is the YAML block under plugins.configs.<pluginID>.
type pluginConfig struct {
	ModelAllowlist allowlistConfig `yaml:"model-allowlist"`
	Observe        observeConfig   `yaml:"observe"`
}

// allowlistConfig declares the model gate.
type allowlistConfig struct {
	// Enabled turns the gate on. An empty model list also disables it.
	Enabled bool `yaml:"enabled"`
	// Match selects which model name is checked: "any", "requested" or "resolved".
	Match string `yaml:"match"`
	// Models lists allowed model names. A trailing "*" matches by prefix.
	Models []string `yaml:"models"`
	// StatusCode is the downstream HTTP status used for a rejected model.
	StatusCode int `yaml:"status-code"`
	// ErrorCode is the machine-readable error code in the rejection body.
	ErrorCode string `yaml:"error-code"`
	// Message is the human-readable prefix of the rejection message.
	Message string `yaml:"message"`
}

// keyLabel maps a client API key to a human-readable label.
type keyLabel struct {
	Key   string `yaml:"key"`
	Label string `yaml:"label"`
}

// observeConfig declares the observation log.
type observeConfig struct {
	Enabled       bool   `yaml:"enabled"`
	Dir           string `yaml:"dir"`
	BucketMinutes int    `yaml:"bucket-minutes"`
	Timezone      string `yaml:"timezone"`
	// RawEvents writes one line per request in addition to the rollups.
	RawEvents *bool `yaml:"raw-events"`
	// DumpQuotaHeaders writes every x-codex-* response header verbatim so new
	// upstream signals can be discovered without another release.
	DumpQuotaHeaders *bool `yaml:"quota-header-dump"`
	// HashKeys stores a SHA-256 prefix instead of the plaintext API key.
	HashKeys *bool `yaml:"hash-keys"`
	// KeyLabels maps API keys to labels without exposing them in the logs.
	KeyLabels []keyLabel `yaml:"key-labels"`
	// FlushInterval controls how often closed buckets reach disk.
	FlushInterval string `yaml:"flush-interval"`
	// RetentionDays deletes this plugin's own day files older than the cutoff.
	RetentionDays int `yaml:"retention-days"`
	// ComputeUnits adds a locally computed credit estimate to each key bucket.
	ComputeUnits *bool `yaml:"compute-units"`
	// UnitsOverrides overrides the built-in credit rate table.
	UnitsOverrides map[string]rateRow `yaml:"units-overrides"`
}

// allowlist is the compiled model gate used on the request path.
type allowlist struct {
	enabled    bool
	matchAny   bool
	checkName  bool
	patterns   []string
	statusCode int
	errorCode  string
	message    string
}

// compileAllowlist turns configuration into an immutable gate.
func compileAllowlist(cfg allowlistConfig) allowlist {
	gate := allowlist{
		enabled:    cfg.Enabled,
		statusCode: cfg.StatusCode,
		errorCode:  strings.TrimSpace(cfg.ErrorCode),
		message:    strings.TrimSpace(cfg.Message),
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Match)) {
	case "requested":
		gate.checkName = false
	case "resolved":
		gate.checkName = true
	default:
		gate.matchAny = true
	}
	for _, pattern := range cfg.Models {
		trimmed := strings.ToLower(strings.TrimSpace(pattern))
		if trimmed == "" {
			continue
		}
		gate.patterns = append(gate.patterns, trimmed)
	}
	if gate.statusCode < 200 || gate.statusCode > 599 {
		gate.statusCode = 404
	}
	if gate.errorCode == "" {
		gate.errorCode = "model_not_found"
	}
	if gate.message == "" {
		gate.message = "model not allowed"
	}
	return gate
}

// allows reports whether the request may reach an upstream executor.
func (g allowlist) allows(requested, resolved string) bool {
	if !g.enabled || len(g.patterns) == 0 {
		return true
	}
	requested = strings.ToLower(strings.TrimSpace(requested))
	resolved = strings.ToLower(strings.TrimSpace(resolved))
	if requested == "" && resolved == "" {
		// Not a model execution request; leave it alone.
		return true
	}
	if g.matchAny {
		return (requested != "" && matchesAnyPattern(g.patterns, requested)) ||
			(resolved != "" && matchesAnyPattern(g.patterns, resolved))
	}
	name := requested
	if g.checkName {
		name = resolved
	}
	if name == "" {
		name = resolved
	}
	if name == "" {
		name = requested
	}
	return matchesAnyPattern(g.patterns, name)
}

func matchesAnyPattern(patterns []string, name string) bool {
	for _, pattern := range patterns {
		if strings.HasSuffix(pattern, "*") {
			if strings.HasPrefix(name, strings.TrimSuffix(pattern, "*")) {
				return true
			}
			continue
		}
		if pattern == name {
			return true
		}
	}
	return false
}

// pluginState holds the live configuration shared by all host callbacks.
type pluginState struct {
	mu       sync.RWMutex
	gate     allowlist
	observer *observer
	stop     chan struct{}
}

func newPluginState() *pluginState {
	return &pluginState{gate: compileAllowlist(allowlistConfig{})}
}

// configureRequest is the payload of plugin.register and plugin.reconfigure.
type configureRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

// loadConfig parses plugin configuration and swaps in the new runtime state.
func (s *pluginState) loadConfig(raw []byte) error {
	var request configureRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
			return errUnmarshal
		}
	}
	cfg := pluginConfig{}
	if len(request.ConfigYAML) > 0 {
		if errYAML := yaml.Unmarshal(request.ConfigYAML, &cfg); errYAML != nil {
			return errYAML
		}
	}
	location := time.Local
	if name := strings.TrimSpace(cfg.Observe.Timezone); name != "" {
		parsed, errLoad := time.LoadLocation(name)
		if errLoad != nil {
			return fmt.Errorf("invalid observe.timezone %q: %w", name, errLoad)
		}
		location = parsed
	}
	interval := 30 * time.Second
	if rawInterval := strings.TrimSpace(cfg.Observe.FlushInterval); rawInterval != "" {
		parsed, errParse := time.ParseDuration(rawInterval)
		if errParse != nil {
			return fmt.Errorf("invalid observe.flush-interval %q: %w", rawInterval, errParse)
		}
		if parsed > 0 {
			interval = parsed
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.gate = compileAllowlist(cfg.ModelAllowlist)
	s.stopObserverLocked()
	if cfg.Observe.Enabled {
		next := newObserver(cfg.Observe, location)
		s.observer = next
		s.startObserverLocked(next, interval)
	}
	return nil
}

func (s *pluginState) startObserverLocked(target *observer, interval time.Duration) {
	stop := make(chan struct{})
	s.stop = stop
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-ticker.C:
				target.flushClosed(now)
			}
		}
	}()
}

func (s *pluginState) stopObserverLocked() {
	if s.stop != nil {
		close(s.stop)
		s.stop = nil
	}
	if s.observer != nil {
		s.observer.close()
		s.observer = nil
	}
}

// shutdown flushes pending observations and stops background work.
func (s *pluginState) shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopObserverLocked()
}

func (s *pluginState) gateSnapshot() allowlist {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gate
}

func (s *pluginState) observerSnapshot() *observer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.observer
}

func boolOr(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func labelMap(labels []keyLabel) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	out := make(map[string]string, len(labels))
	for _, item := range labels {
		key := strings.TrimSpace(item.Key)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(item.Label)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
