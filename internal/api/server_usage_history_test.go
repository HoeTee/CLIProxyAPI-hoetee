package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagehistory"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type usageHistoryTestExecutor struct{ codexSearchCaptureExecutor }

func (e *usageHistoryTestExecutor) Execute(ctx context.Context, a *auth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	if strings.Contains(string(req.Payload), "blocked-prompt") {
		return coreexecutor.Response{}, &coreexecutor.RequestTerminatedError{HTTPStatus: http.StatusTooManyRequests, Body: []byte(`{"error":"quota exceeded"}`)}
	}
	body := []byte(`{"id":"response-id","object":"chat.completion","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":0}}}`)
	helps.NewUsageReporter(ctx, "codex", req.Model, a).Publish(ctx, helps.ParseOpenAIUsage(body))
	return coreexecutor.Response{Payload: body}, nil
}

func (e *usageHistoryTestExecutor) ExecuteStream(ctx context.Context, a *auth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	body := []byte(`{"id":"stream-id","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	helps.NewUsageReporter(ctx, "codex", req.Model, a).Publish(ctx, helps.ParseOpenAIUsage(body))
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: body}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func TestAuthenticatedUsageHistoryRecordsProxyRequests(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "history-management-secret")
	root := t.TempDir()
	cfg := &config.Config{SDKConfig: config.SDKConfig{Clients: []config.ClientConfig{
		{ID: "history-user", APIKeySHA256: config.ClientKeyDigest("history-client-secret")},
		{ID: "blocked-user", APIKeySHA256: config.ClientKeyDigest("history-blocked-secret"), AllowedProviders: []string{"deepseek"}},
	}}}
	cfg.AuthDir = root
	manager := auth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&usageHistoryTestExecutor{})
	credential := &auth.Auth{ID: "history-test-credential", Provider: "codex", Status: auth.StatusActive, Attributes: map[string]string{auth.AttributeAPIKey: "upstream-test-key"}}
	if _, err := manager.Register(context.Background(), credential); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(credential.ID, "codex", []*registry.ModelInfo{{ID: "history-test-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(credential.ID) })
	s := NewServer(cfg, manager, sdkaccess.NewManager(), filepath.Join(root, "config.yaml"), WithUsageHistory(filepath.Join(root, "data", "usage.sqlite")))
	t.Cleanup(func() { _ = s.Stop(context.Background()) })

	request := func(method, path, key, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, req)
		return response
	}
	const proxyPath = "/v1/chat/completions"
	normal := request(http.MethodPost, proxyPath, "history-client-secret", `{"model":"history-test-model","messages":[{"role":"user","content":"normal-prompt"}]}`)
	if normal.Code != http.StatusOK {
		t.Fatalf("proxy request: HTTP %d: %s", normal.Code, normal.Body)
	}
	stream := request(http.MethodPost, proxyPath, "history-client-secret", `{"model":"history-test-model","stream":true,"messages":[{"role":"user","content":"stream-prompt"}]}`)
	if stream.Code != http.StatusOK || !strings.Contains(stream.Body.String(), "stream-id") {
		t.Fatalf("stream: HTTP %d: %s", stream.Code, stream.Body)
	}
	blocked := request(http.MethodPost, proxyPath, "history-client-secret", `{"model":"history-test-model","messages":[{"role":"user","content":"blocked-prompt"}]}`)
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked request: HTTP %d: %s", blocked.Code, blocked.Body)
	}
	denied := request(http.MethodPost, proxyPath, "history-blocked-secret", `{"model":"history-test-model","messages":[{"role":"user","content":"denied-prompt"}]}`)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("denied request: HTTP %d: %s", denied.Code, denied.Body)
	}
	const rowsPath = "/v0/management/usage-history/requests"
	if response := request(http.MethodGet, rowsPath, "history-client-secret", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("client key read management history: HTTP %d", response.Code)
	}
	response := request(http.MethodGet, rowsPath+"?provider=codex&client=history-user&outcome=rejected", "history-management-secret", "")
	var details usagehistory.Requests
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &details) != nil || len(details.Rows) != 1 || details.Rows[0].InputTokens != nil || details.Rows[0].StatusCode != 429 {
		t.Fatalf("rejected history: HTTP %d: %s", response.Code, response.Body)
	}
	response = request(http.MethodGet, rowsPath+"?provider=codex&client=blocked-user&outcome=rejected", "history-management-secret", "")
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &details) != nil || len(details.Rows) != 1 || details.Rows[0].InputTokens != nil || details.Rows[0].StatusCode != 403 {
		t.Fatalf("denied history: HTTP %d: %s", response.Code, response.Body)
	}
	response = request(http.MethodGet, "/v0/management/usage-history/summary?provider=codex&client=history-user&model=HISTORY", "history-management-secret", "")
	var summary usagehistory.Summary
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &summary) != nil || summary.Totals.Calls != 3 || summary.Totals.Succeeded != 2 || summary.Totals.Rejected != 1 || summary.Totals.InputTokens == nil || *summary.Totals.InputTokens != 10 || summary.Totals.CachedTokens == nil || *summary.Totals.CachedTokens != 0 {
		t.Fatalf("history summary: HTTP %d: %s", response.Code, response.Body)
	}
	if strings.Contains(response.Body.String(), "normal-prompt") || strings.Contains(response.Body.String(), "history-client-secret") {
		t.Fatal("history disclosed a prompt or key")
	}
	response = request(http.MethodGet, rowsPath+"?limit=-1", "history-management-secret", "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("bad pagination was accepted: HTTP %d: %s", response.Code, response.Body)
	}
}
