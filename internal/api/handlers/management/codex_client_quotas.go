package management

import (
	"context"
	"encoding/json"
	"math"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func (h *Handler) GetCodexClientQuotas(c *gin.Context) {
	type row struct {
		ID        string          `json:"id"`
		WeeklyUSD float64         `json:"weekly_usd"`
		Usage     json.RawMessage `json:"usage"`
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
		if client.Quotas.Codex == nil && !includeAll {
			continue
		}
		entry := row{ID: client.ID}
		if client.Quotas.Codex != nil {
			entry.WeeklyUSD = client.Quotas.Codex.WeeklyUSD
		}
		rows = append(rows, entry)
		ids = append(ids, client.ID)
	}
	if len(ids) > 0 {
		plugin := host.ClientQuotaPlugin("codex-key-quota")
		if plugin == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Codex quota plugin is unavailable"})
			return
		}
		snapshots, err := plugin.SnapshotClientQuota(c.Request.Context(), ids)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Codex usage ledger unavailable"})
			return
		}
		for i := range rows {
			rows[i].Usage = snapshots[rows[i].ID]
		}
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"clients": rows})
}

func (h *Handler) PatchCodexClientQuota(c *gin.Context) {
	var body struct {
		ID        string  `json:"id"`
		WeeklyUSD float64 `json:"weekly_usd"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.WeeklyUSD <= 0 ||
		math.IsNaN(body.WeeklyUSD) || math.IsInf(body.WeeklyUSD, 0) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "weekly_usd must be a positive number"})
		return
	}
	snapshot, ok := h.saveCodexClientQuota(c, body.ID, body.WeeklyUSD)
	if !ok {
		return
	}
	h.reloadConfigAfterManagementSave(context.WithoutCancel(c.Request.Context()), snapshot)
	c.JSON(http.StatusOK, gin.H{"id": body.ID, "weekly_usd": body.WeeklyUSD})
}

func (h *Handler) saveCodexClientQuota(c *gin.Context, id string, limit float64) (configReloadSnapshot, bool) {
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
		next.Clients[i].Quotas.Codex = &config.CodexClientQuota{WeeklyUSD: limit}
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
