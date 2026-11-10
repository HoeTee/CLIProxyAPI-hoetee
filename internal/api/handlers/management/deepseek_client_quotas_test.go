package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestDeepSeekClientQuotaDashboard(t *testing.T) {
	dir := t.TempDir()
	configPath, ledgerPath := filepath.Join(dir, "config.yaml"), filepath.Join(dir, "usage.ndjson")
	ledger := "{\"client_id\":\"alice\",\"cost_cny\":0.09}\n{\"client_id\":\"another\",\"cost_cny\":0.50}\n"
	if errWrite := os.WriteFile(ledgerPath, []byte(ledger), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	raw := fmt.Sprintf(`clients:
  - id: alice
    api-key: fixture-client-secret
    quotas:
      codex: {weekly-usd: 2}
      deepseek: {limit-cny: 0.10}
plugins:
  configs:
    deepseek-key-quota:
      storage: {path: %q}
      reservation-cny: 0.01
`, filepath.ToSlash(ledgerPath))
	if errWrite := os.WriteFile(configPath, []byte(raw), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := config.LoadConfig(configPath)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	h := NewHandler(cfg, configPath, nil)
	var reloaded *config.Config
	h.SetConfigReloadHook(func(_ context.Context, next *config.Config) { reloaded = next })
	router := gin.New()
	router.GET("/quotas", h.GetDeepSeekClientQuotas)
	router.PATCH("/quotas", h.PatchDeepSeekClientQuota)
	request := func(method, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/quotas", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := request(http.MethodPatch, `{"id":"alice","limit_cny":0}`); rec.Code != http.StatusBadRequest || reloaded != nil {
		t.Fatalf("invalid update response: %d %s", rec.Code, rec.Body.String())
	}
	if rec := request(http.MethodPatch, `{"id":"alice","limit_cny":0.25}`); rec.Code != http.StatusOK || reloaded == nil {
		t.Fatalf("valid update response: %d %s", rec.Code, rec.Body.String())
	}
	if got := reloaded.Clients[0].Quotas.DeepSeek.LimitCNY; got != 0.25 {
		t.Fatalf("reloaded limit: %v", got)
	}
	onDisk, errRead := config.LoadConfig(configPath)
	if errRead != nil {
		t.Fatal(errRead)
	}
	client := onDisk.Clients[0]
	if client.Quotas.DeepSeek.LimitCNY != 0.25 || client.Quotas.Codex.WeeklyUSD != 2 || client.APIKey != "fixture-client-secret" {
		t.Fatal("saved config did not preserve unrelated fields")
	}
	rec := request(http.MethodGet, "")
	var result struct{ Clients []deepSeekClientQuotaRow }
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "fixture-client-secret") || json.Unmarshal(rec.Body.Bytes(), &result) != nil || len(result.Clients) != 1 {
		t.Fatalf("invalid usage response: status %d", rec.Code)
	}
	if row := result.Clients[0]; row.LimitCNY != 0.25 || row.UsedCNY != 0.09 || row.RemainingCNY != 0.16 {
		t.Fatalf("unexpected usage row: %+v", row)
	}
	if bytes, err := os.ReadFile(ledgerPath); err != nil || string(bytes) != ledger {
		t.Fatal("quota update altered the ledger")
	}
}
