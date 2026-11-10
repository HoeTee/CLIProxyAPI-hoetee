package management

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/glmquota"
)

func (h *Handler) GetGLMClientQuotas(c *gin.Context) {
	type row struct {
		ID     string                `json:"id"`
		Policy config.GLMClientQuota `json:"policy"`
		glmquota.Snapshot
	}
	h.mu.Lock()
	cfg, host := h.cfg, h.pluginHost
	h.mu.Unlock()
	if cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}
	rows := make([]row, 0, len(cfg.Clients))
	ids := make([]string, 0, len(cfg.Clients))
	includeAll := c.Query("scope") == "all-clients"
	for _, client := range cfg.Clients {
		if client.Quotas.GLM == nil && !includeAll {
			continue
		}
		entry := row{ID: client.ID}
		if client.Quotas.GLM != nil {
			entry.Policy = *client.Quotas.GLM
		}
		rows = append(rows, entry)
		ids = append(ids, client.ID)
	}
	if len(ids) > 0 {
		plugin := host.ClientQuotaPlugin(glmquota.PluginID)
		if plugin == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "GLM quota plugin is unavailable"})
			return
		}
		snapshots, err := plugin.SnapshotClientQuota(c.Request.Context(), ids)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "GLM usage ledger unavailable"})
			return
		}
		for i := range rows {
			if errDecode := json.Unmarshal(snapshots[rows[i].ID], &rows[i].Snapshot); errDecode != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "GLM usage ledger unavailable"})
				return
			}
		}
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"clients": rows})
}

func (h *Handler) PatchGLMClientQuota(c *gin.Context) {
	var body struct {
		ID       string          `json:"id"`
		FiveHour json.RawMessage `json:"five_hour_tokens"`
		Weekly   json.RawMessage `json:"weekly_tokens"`
	}
	var policy config.GLMClientQuota
	if err := c.ShouldBindJSON(&body); err != nil ||
		json.Unmarshal(body.FiveHour, &policy.FiveHourTokens) != nil ||
		json.Unmarshal(body.Weekly, &policy.WeeklyTokens) != nil || !policy.Valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "both GLM token limits must be positive integers or null"})
		return
	}
	snapshot, ok := h.saveGLMClientQuota(c, body.ID, policy)
	if !ok {
		return
	}
	h.reloadConfigAfterManagementSave(context.WithoutCancel(c.Request.Context()), snapshot)
	c.JSON(http.StatusOK, gin.H{"id": body.ID, "policy": policy})
}

func (h *Handler) saveGLMClientQuota(c *gin.Context, id string, policy config.GLMClientQuota) (configReloadSnapshot, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	previous := h.cfg
	if previous == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return configReloadSnapshot{}, false
	}
	if previous.Home.Enabled {
		c.JSON(http.StatusConflict, gin.H{"error": "configuration is managed by Home"})
		return configReloadSnapshot{}, false
	}
	for i := range previous.Clients {
		if previous.Clients[i].ID != id {
			continue
		}
		next := previous.CloneForRuntime()
		next.Clients[i].Quotas.GLM = &policy
		h.cfg = next
		snapshot, saved := h.saveConfigAndSnapshotLocked(c)
		if !saved {
			h.cfg = previous
		}
		return snapshot, saved
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "client not found"})
	return configReloadSnapshot{}, false
}

func (h *Handler) GetGLMPlanQuota(c *gin.Context) {
	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()
	key := glmPlanKey(cfg)
	c.Header("Cache-Control", "no-store")
	if key == "" {
		c.JSON(http.StatusOK, gin.H{"configured": false})
		return
	}
	plan, err := glmquota.FetchPlan(c.Request.Context(), key)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "GLM Coding Plan quota unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"configured": true, "plan": plan})
}

func glmPlanKey(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	for _, provider := range cfg.OpenAICompatibility {
		if provider.Disabled || !strings.EqualFold(strings.TrimSpace(provider.Name), glmquota.ProviderName) ||
			!glmquota.IsOfficialEndpoint(provider.BaseURL) {
			continue
		}
		for _, entry := range provider.APIKeyEntries {
			if key := strings.TrimSpace(entry.APIKey); key != "" {
				return key
			}
		}
		return glmquota.Credential(provider.BaseURL, "")
	}
	return ""
}
