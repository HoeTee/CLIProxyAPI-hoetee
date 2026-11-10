package management

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

type overviewProvider struct {
	ID          string               `json:"id"`
	Monitored   bool                 `json:"monitored"`
	Credentials []overviewCredential `json:"credentials"`
}

type overviewCredential struct {
	Kind     string   `json:"kind"`
	Name     string   `json:"name,omitempty"`
	Disabled bool     `json:"disabled"`
	Models   []string `json:"models"`
}

// GetProviderOverview exposes the effective model registry and client policies without API keys.
func (h *Handler) GetProviderOverview(c *gin.Context) {
	h.mu.Lock()
	cfg, manager := h.cfg, h.authManager
	h.mu.Unlock()
	if cfg == nil || manager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "provider overview unavailable"})
		return
	}
	providers := []overviewProvider{
		{ID: "codex", Monitored: true, Credentials: []overviewCredential{}},
		{ID: "deepseek", Monitored: deepSeekBalanceCredential(cfg) != nil, Credentials: []overviewCredential{}},
		{ID: "glm-coding-plan", Monitored: glmPlanKey(cfg) != "", Credentials: []overviewCredential{}},
	}
	byID := make(map[string]*overviewProvider, len(providers))
	for i := range providers {
		byID[providers[i].ID] = &providers[i]
	}
	reg := registry.GetGlobalRegistry()
	for _, auth := range manager.List() {
		if auth == nil {
			continue
		}
		id := apiKeyUsageProviderKey(auth)
		provider := byID[id]
		if provider == nil {
			continue
		}
		kind, _ := auth.AccountInfo()
		credential := overviewCredential{Kind: kind, Disabled: auth.Disabled, Models: []string{}}
		if kind == "oauth" {
			credential.Name = auth.FileName
		}
		for _, model := range reg.GetModelsForClient(auth.ID) {
			credential.Models = append(credential.Models, model.ID)
		}
		sort.Strings(credential.Models)
		provider.Credentials = append(provider.Credentials, credential)
	}
	for i := range providers {
		credentials := providers[i].Credentials
		sort.SliceStable(credentials, func(i, j int) bool {
			a, b := credentials[i], credentials[j]
			if a.Kind != b.Kind {
				return a.Kind < b.Kind
			}
			if a.Name != b.Name {
				return a.Name < b.Name
			}
			return strings.Join(a.Models, "\x00") < strings.Join(b.Models, "\x00")
		})
	}
	type clientPolicy struct {
		ID     string              `json:"id"`
		Quotas config.ClientQuotas `json:"quotas"`
	}
	clients := make([]clientPolicy, 0, len(cfg.Clients))
	for _, client := range cfg.Clients {
		clients = append(clients, clientPolicy{ID: client.ID, Quotas: client.Quotas})
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"providers": providers, "clients": clients, "legacy_api_key_count": len(cfg.APIKeys),
		"model_count": len(reg.GetAvailableModels("openai")),
	})
}
