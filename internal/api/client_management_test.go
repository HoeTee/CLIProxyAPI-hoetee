package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type clientPolicyExecutor struct {
	codexSearchCaptureExecutor
	provider string
	calls    int
}

func (e *clientPolicyExecutor) Identifier() string { return e.provider }

func (e *clientPolicyExecutor) Execute(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	return coreexecutor.Response{Payload: []byte(`{"id":"fixture","choices":[]}`)}, nil
}

func TestClientManagementAppliesToLiveRequests(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "fixture-management-secret")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := "# Keep this comment\napi-keys: [fixture-legacy-secret]\n"
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AuthDir = dir
	manager := auth.NewManager(nil, nil, nil)
	var server *Server
	skipReload := false
	server = NewServer(cfg, manager, sdkaccess.NewManager(), path, WithConfigReloadHook(func(_ context.Context, _ *config.Config) {
		if skipReload {
			return
		}
		disk, errLoad := config.LoadConfig(path)
		if errLoad != nil {
			t.Fatal(errLoad)
		}
		disk.AuthDir = dir
		server.UpdateClients(disk)
	}))
	executors := map[string]*clientPolicyExecutor{}
	for model, provider := range map[string]string{"fixture-codex-model": "codex", "fixture-deepseek-model": "openai-compatible-deepseek"} {
		id := model + "-credential"
		exec := &clientPolicyExecutor{provider: provider}
		executors[provider] = exec
		manager.RegisterExecutor(exec)
		if _, errRegister := manager.Register(context.Background(), &auth.Auth{ID: id, Provider: provider}); errRegister != nil {
			t.Fatal(errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model, Object: "model"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	call := func(method, path, key, body string, status int) string {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.engine.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s %s: status %d, want %d", method, path, rec.Code, status)
		}
		return rec.Body.String()
	}
	const clients = "/v0/management/clients"
	const chat = "/v1/chat/completions"
	const management = "fixture-management-secret"
	const legacy = "fixture-legacy-secret"
	keyFrom := func(response string) string {
		t.Helper()
		var body struct {
			Key string `json:"api_key"`
		}
		if json.Unmarshal([]byte(response), &body) != nil || !strings.HasPrefix(body.Key, "cpa_") {
			t.Fatal("issue endpoint did not return a generated key")
		}
		return body.Key
	}
	policy := `{"id":"alice","allowed_providers":["deepseek"],"quotas":{"deepseek":{"limit-cny":5},"glm":{"five_hour_tokens":null,"weekly_tokens":20}}}`
	call(http.MethodPost, clients, legacy, policy, http.StatusUnauthorized)
	key := keyFrom(call(http.MethodPost, clients, management, policy, http.StatusCreated))
	call(http.MethodPost, clients, management, policy, http.StatusConflict)
	models := call(http.MethodGet, "/v1/models", key, "", http.StatusOK)
	if !strings.Contains(models, "fixture-deepseek-model") || strings.Contains(models, "fixture-codex-model") {
		t.Fatal("provider-restricted model catalog did not match access policy")
	}
	call(http.MethodPost, chat, key, `{"model":"fixture-codex-model","messages":[{"role":"user","content":"hi"}]}`, http.StatusForbidden)
	call(http.MethodPost, chat, key, `{"model":"fixture-codex-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`, http.StatusForbidden)
	call(http.MethodPost, "/v1/live", key, `{}`, http.StatusForbidden)
	call(http.MethodPost, chat, key, `{"model":"fixture-deepseek-model","messages":[{"role":"user","content":"hi"}]}`, http.StatusOK)
	if executors["codex"].calls != 0 || executors["openai-compatible-deepseek"].calls != 1 {
		t.Fatal("request crossed its provider boundary")
	}
	list := call(http.MethodGet, clients, management, "", http.StatusOK)
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(list, key) || strings.Contains(string(disk), key) || !strings.Contains(string(disk), "# Keep this comment") {
		t.Fatal("generated key leaked or config comment was lost")
	}
	if got := call(http.MethodGet, "/v0/management/deepseek-client-quotas", management, "", http.StatusOK); !strings.Contains(got, `"limit_cny":5`) {
		t.Fatal("quota did not reload into the provider-specific management path")
	}
	call(http.MethodPatch, clients+"/alice", management, `{"quotas":{},"allowed_providers":[]}`, http.StatusOK)
	if got := call(http.MethodGet, clients, management, "", http.StatusOK); !strings.Contains(got, `"quotas":{}`) {
		t.Fatal("old quota remained after clearing the policy")
	}
	if got := call(http.MethodGet, "/v1/models", key, "", http.StatusOK); !strings.Contains(got, "fixture-codex-model") {
		t.Fatal("clearing the provider restriction did not restore model access")
	}
	var secret struct{ Value string }
	if err := json.Unmarshal([]byte(call(http.MethodPost, "/v1/realtime/client_secrets", key, `{"session":{"type":"realtime","model":"gpt-realtime"}}`, http.StatusOK)), &secret); err != nil || secret.Value == "" {
		t.Fatal("unable to issue realtime client secret")
	}
	rotated := keyFrom(call(http.MethodPost, clients+"/alice/rotate", management, "", http.StatusOK))
	call(http.MethodGet, "/v1/models", key, "", http.StatusUnauthorized)
	call(http.MethodGet, "/v1/realtime?model=gpt-realtime", secret.Value, "", http.StatusUnauthorized)
	call(http.MethodGet, "/v1/models", rotated, "", http.StatusOK)
	call(http.MethodPatch, clients+"/alice", management, `{"disabled":true}`, http.StatusOK)
	call(http.MethodGet, "/v1/models", rotated, "", http.StatusUnauthorized)
	call(http.MethodGet, "/v1/models", legacy, "", http.StatusOK)
	call(http.MethodPatch, clients+"/alice", management, `{"disabled":false}`, http.StatusOK)
	call(http.MethodGet, "/v1/models", rotated, "", http.StatusOK)
	skipReload = true
	call(http.MethodPatch, clients+"/alice", management, `{"disabled":true}`, http.StatusServiceUnavailable)
	call(http.MethodGet, "/v1/models", rotated, "", http.StatusOK)
	if got := call(http.MethodGet, clients, management, "", http.StatusOK); !strings.Contains(got, `"disabled":false`) {
		t.Fatal("client inventory reported unapplied settings as active")
	}
}
