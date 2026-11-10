package management

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	log "github.com/sirupsen/logrus"
)

const deepSeekQuotaPluginID = "deepseek-key-quota"

type deepSeekClientQuotaRow struct {
	ID           string           `json:"id"`
	LimitCNY     float64          `json:"limit_cny"`
	UsedCNY      float64          `json:"used_cny"`
	RemainingCNY float64          `json:"remaining_cny"`
	Hours        usage.TokenHours `json:"hours"`
}

// GetDeepSeekClientQuotas reports persisted usage without returning credentials.
func (h *Handler) GetDeepSeekClientQuotas(c *gin.Context) {
	h.mu.Lock()
	cfg, host := h.cfg, h.pluginHost
	if cfg == nil {
		h.mu.Unlock()
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}
	instance := cfg.Plugins.Configs[deepSeekQuotaPluginID]
	configured := cfg.Plugins.Enabled && instance.Enabled != nil && *instance.Enabled
	rows := make([]deepSeekClientQuotaRow, 0, len(cfg.Clients))
	now := time.Now()
	for _, client := range cfg.Clients {
		row := deepSeekClientQuotaRow{ID: client.ID, Hours: usage.NewTokenHours(now)}
		if client.Quotas.DeepSeek != nil {
			row.LimitCNY = client.Quotas.DeepSeek.LimitCNY
		}
		rows = append(rows, row)
	}
	h.mu.Unlock()
	var settings struct {
		Storage struct {
			Path string `yaml:"path"`
		} `yaml:"storage"`
		ReservationCNY float64 `yaml:"reservation-cny"`
	}
	if instance.Raw.Kind != 0 {
		if errDecode := instance.Raw.Decode(&settings); errDecode != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "invalid DeepSeek plugin configuration"})
			return
		}
	}
	if strings.TrimSpace(settings.Storage.Path) == "" {
		settings.Storage.Path = "logs/deepseek-key-quota/usage.ndjson"
	}
	if settings.ReservationCNY == 0 {
		settings.ReservationCNY = 0.01
	}
	active := configured && host.PluginLoaded(deepSeekQuotaPluginID)
	byID := make(map[string]*deepSeekClientQuotaRow, len(rows))
	for i := range rows {
		byID[rows[i].ID] = &rows[i]
	}
	err := readDeepSeekQuotaUsage(settings.Storage.Path, byID)
	if err != nil && !(errors.Is(err, os.ErrNotExist) && !active) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "DeepSeek usage ledger unavailable"})
		return
	}
	for i := range rows {
		rows[i].RemainingCNY = math.Max(0, rows[i].LimitCNY-rows[i].UsedCNY)
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"active": active, "reservation_cny": settings.ReservationCNY, "clients": rows})
}

func readDeepSeekQuotaUsage(path string, rows map[string]*deepSeekClientQuotaRow) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			log.WithError(errClose).Warn("failed to close DeepSeek usage ledger")
		}
	}()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var event struct {
			Time      time.Time `json:"time"`
			ClientID  string    `json:"client_id"`
			CostCNY   float64   `json:"cost_cny"`
			CacheHit  int64     `json:"cache_hit_tokens"`
			CacheMiss int64     `json:"cache_miss_tokens"`
			Output    int64     `json:"output_tokens"`
		}
		if errDecode := json.Unmarshal(scanner.Bytes(), &event); errDecode != nil || event.ClientID == "" || event.CostCNY < 0 {
			return errors.New("invalid DeepSeek usage event")
		}
		if row := rows[event.ClientID]; row != nil {
			row.UsedCNY += event.CostCNY
			row.Hours.Add(event.Time, event.CacheHit, event.CacheMiss, 0, event.Output)
		}
	}
	return scanner.Err()
}

// PatchDeepSeekClientQuota updates one existing client without altering the ledger.
func (h *Handler) PatchDeepSeekClientQuota(c *gin.Context) {
	var body struct {
		ID       string  `json:"id"`
		LimitCNY float64 `json:"limit_cny"`
	}
	if errBind := c.ShouldBindJSON(&body); errBind != nil || body.LimitCNY <= 0 || math.IsNaN(body.LimitCNY) || math.IsInf(body.LimitCNY, 0) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "limit_cny must be a positive number"})
		return
	}
	snapshot, ok := h.saveDeepSeekClientQuota(c, body.ID, body.LimitCNY)
	if !ok {
		return
	}
	h.reloadConfigAfterManagementSave(context.WithoutCancel(c.Request.Context()), snapshot)
	c.JSON(http.StatusOK, gin.H{"id": body.ID, "limit_cny": body.LimitCNY})
}

func (h *Handler) saveDeepSeekClientQuota(c *gin.Context, id string, limit float64) (configReloadSnapshot, bool) {
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
		next.Clients[i].Quotas.DeepSeek = &config.DeepSeekClientQuota{LimitCNY: limit}
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
