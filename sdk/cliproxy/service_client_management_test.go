package cliproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestClientCreationWaitsForRunningConfigWatcher(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "fixture-management-secret")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	const initial = "host: 127.0.0.1\nport: 0\napi-keys: [fixture-legacy-secret]\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Port, cfg.AuthDir = 0, dir
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	listenerStarted := make(chan struct{})
	continueStartup := make(chan struct{})
	service, err := NewBuilder().WithConfig(cfg).WithConfigPath(path).WithHooks(Hooks{
		OnAfterStart: func(_ *Service) {
			close(listenerStarted)
			select {
			case <-continueStartup:
			case <-ctx.Done():
			}
		},
	}).Build()
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- service.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Error("service did not stop")
		}
	})
	select {
	case <-listenerStarted:
	case <-ctx.Done():
		t.Fatal("service did not reach the startup hook")
	}

	call := func(method, route, key, body string, want int) string {
		t.Helper()
		req := httptest.NewRequest(method, route, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		service.server.Handler().ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s status %d, want %d", method, route, rec.Code, want)
		}
		return rec.Body.String()
	}
	const route = "/v0/management/clients"
	const policy = `{"id":"startup-client","allowed_providers":["glm-coding-plan"],"quotas":{"glm":{"five_hour_tokens":null,"weekly_tokens":null}}}`
	call(http.MethodGet, "/", "", "", http.StatusOK)
	call(http.MethodPost, route, "fixture-management-secret", policy, http.StatusServiceUnavailable)
	if disk, errRead := os.ReadFile(path); errRead != nil || string(disk) != initial {
		t.Fatal("the early request changed the config file")
	}
	close(continueStartup)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !service.watcherReady.Load() {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("default config watcher never became ready")
		}
	}
	var issued struct {
		Key string `json:"api_key"`
	}
	if errDecode := json.Unmarshal([]byte(call(http.MethodPost, route, "fixture-management-secret", policy, http.StatusCreated)), &issued); errDecode != nil || issued.Key == "" {
		t.Fatal("client key was not issued")
	}
	call(http.MethodGet, "/v1/models", issued.Key, "", http.StatusOK)
	call(http.MethodPost, "/v1/live", issued.Key, `{}`, http.StatusForbidden)
	saved, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Clients) != 1 || saved.Clients[0].Quotas.GLM == nil || saved.Clients[0].Quotas.GLM.FiveHourTokens != nil || saved.Clients[0].Quotas.GLM.WeeklyTokens != nil {
		t.Fatal("unlimited GLM quota did not survive the disk round trip")
	}
}
