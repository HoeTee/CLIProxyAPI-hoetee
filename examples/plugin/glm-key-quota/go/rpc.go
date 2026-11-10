package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/glmquota"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

var ledger atomic.Pointer[Ledger]
var configureMu sync.Mutex

func main() {}

func configure(raw []byte) error {
	var req struct {
		ConfigYAML []byte `json:"config_yaml"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return err
	}
	var cfg struct {
		Storage struct {
			Path string `yaml:"path"`
		} `yaml:"storage"`
		Pricing *pointPricing `yaml:"pricing"`
	}
	if err := yaml.Unmarshal(req.ConfigYAML, &cfg); err != nil {
		return err
	}
	if errValidate := cfg.Pricing.validate(); errValidate != nil {
		return errValidate
	}
	path := strings.TrimSpace(cfg.Storage.Path)
	if path == "" {
		path = filepath.Join("logs", glmquota.ProviderName, "usage.ndjson")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	configureMu.Lock()
	defer configureMu.Unlock()
	if current := ledger.Load(); current != nil {
		if current.path != path {
			return errors.New("GLM ledger storage.path requires a service restart")
		}
		current.mu.Lock()
		current.pricing = cfg.Pricing
		current.mu.Unlock()
		return nil
	}
	next := NewLedger(path)
	next.pricing = cfg.Pricing
	if errLoad := next.load(); errLoad != nil {
		return errLoad
	}
	ledger.Store(next)
	return nil
}

func handleMethod(method string, raw []byte) ([]byte, error) {
	if method == pluginabi.MethodPluginRegister || method == pluginabi.MethodPluginReconfigure {
		if err := configure(raw); err != nil {
			return nil, err
		}
		return okEnvelope(struct {
			SchemaVersion uint32             `json:"schema_version"`
			Metadata      pluginapi.Metadata `json:"metadata"`
			Capabilities  map[string]bool    `json:"capabilities"`
		}{
			SchemaVersion: pluginabi.SchemaVersion,
			Metadata: pluginapi.Metadata{
				Name: glmquota.PluginID, Version: "0.2.0", Author: "router-for-me",
				GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			},
			Capabilities: map[string]bool{"client_quota": true},
		})
	}

	current := ledger.Load()
	if current == nil {
		return nil, errors.New("GLM quota plugin has no active ledger")
	}
	switch method {
	case pluginabi.MethodClientQuotaAdmit:
		var req pluginapi.ClientQuotaRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		var policy *config.GLMClientQuota
		if len(req.Policy) > 0 {
			if err := json.Unmarshal(req.Policy, &policy); err != nil {
				return nil, err
			}
		}
		if policy != nil && !policy.Valid() {
			return nil, errors.New("invalid GLM quota policy")
		}
		var decision pluginapi.ClientQuotaDecision
		if err := current.Admit(req.ClientID, policy); err != nil {
			var rejection QuotaError
			if !errors.As(err, &rejection) {
				return nil, err
			}
			decision = pluginapi.ClientQuotaDecision{Status: rejection.Status, Message: rejection.Message}
		}
		return okEnvelope(decision)
	case pluginabi.MethodClientQuotaRecord:
		var req pluginapi.ClientQuotaRecord
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		if err := current.Record(req.ClientID, req.Usage); err != nil {
			return nil, err
		}
		return okEnvelope(struct{}{})
	case pluginabi.MethodClientQuotaSnapshot:
		var clients []string
		if err := json.Unmarshal(raw, &clients); err != nil {
			return nil, err
		}
		snapshots := make(map[string]Snapshot, len(clients))
		now := time.Now()
		for _, client := range clients {
			snapshot, err := current.Snapshot(client, now)
			if err != nil {
				return nil, err
			}
			snapshots[client] = snapshot
		}
		return okEnvelope(snapshots)
	default:
		return errorEnvelope("unknown_method", "unknown plugin method"), nil
	}
}

func okEnvelope(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(pluginabi.Envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(pluginabi.Envelope{Error: &pluginabi.Error{Code: code, Message: message}})
	return raw
}
