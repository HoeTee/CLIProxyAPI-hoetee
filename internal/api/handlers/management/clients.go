package management

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

var newClientID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

type clientSettings struct {
	ID               string              `json:"id"`
	Disabled         bool                `json:"disabled"`
	AllowedProviders []string            `json:"allowed_providers"`
	Quotas           config.ClientQuotas `json:"quotas"`
}

type clientChange struct {
	ID               string               `json:"id"`
	Disabled         *bool                `json:"disabled"`
	AllowedProviders *[]string            `json:"allowed_providers"`
	Quotas           *config.ClientQuotas `json:"quotas"`
}

func visibleClient(client config.ClientConfig) clientSettings {
	return clientSettings{client.ID, client.Disabled, append([]string{}, client.AllowedProviders...), client.Quotas}
}

func (h *Handler) GetClients(c *gin.Context) {
	h.mu.Lock()
	if h.cfg == nil {
		h.mu.Unlock()
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return
	}
	clients := make([]clientSettings, 0, len(h.runtimeClients))
	for _, client := range h.runtimeClients {
		clients = append(clients, visibleClient(client))
	}
	h.mu.Unlock()
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"clients": clients})
}

func (h *Handler) PostClient(c *gin.Context) {
	var body clientChange
	if err := c.ShouldBindJSON(&body); err != nil || !newClientID.MatchString(body.ID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "client ID must contain 1 to 64 letters, digits, dots, hyphens or underscores, and begin with a letter or digit"})
		return
	}
	h.changeClient(c, body.ID, body, true, true)
}

func (h *Handler) PatchClient(c *gin.Context) {
	var body clientChange
	if err := c.ShouldBindJSON(&body); err != nil || body.ID != "" || (body.Disabled == nil && body.AllowedProviders == nil && body.Quotas == nil) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provide disabled, allowed_providers, or quotas; client ID is fixed"})
		return
	}
	h.changeClient(c, c.Param("id"), body, false, false)
}

func (h *Handler) RotateClientKey(c *gin.Context) {
	h.changeClient(c, c.Param("id"), clientChange{}, false, true)
}

func (h *Handler) changeClient(c *gin.Context, id string, body clientChange, create, issueKey bool) {
	h.clientChangeMu.Lock()
	defer h.clientChangeMu.Unlock()
	var key string
	if issueKey {
		var secret [32]byte
		if _, err := rand.Read(secret[:]); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "could not generate client key"})
			return
		}
		key = "cpa_" + base64.RawURLEncoding.EncodeToString(secret[:])
	}
	client, snapshot, saved := h.saveClientChange(c, id, body, create, key)
	if !saved {
		return
	}
	h.reloadConfigAfterManagementSave(context.WithoutCancel(c.Request.Context()), snapshot)
	if !h.clientChangeApplied(client) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "saved to config.yaml but not applied to the running service"})
		return
	}
	response := gin.H{"client": visibleClient(client)}
	if issueKey {
		response["api_key"] = key
	}
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(status, response)
}

func (h *Handler) clientChangeApplied(want config.ClientConfig) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, actual := range h.runtimeClients {
		if actual.ID == want.ID {
			return actual.APIKey == want.APIKey && actual.APIKeySHA256 == want.APIKeySHA256 && actual.Disabled == want.Disabled &&
				slices.Equal(actual.AllowedProviders, want.AllowedProviders) && reflect.DeepEqual(actual.Quotas, want.Quotas)
		}
	}
	return false
}

func (h *Handler) saveClientChange(c *gin.Context, id string, body clientChange, create bool, key string) (config.ClientConfig, configReloadSnapshot, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration unavailable"})
		return config.ClientConfig{}, configReloadSnapshot{}, false
	}
	if h.cfg.Home.Enabled {
		c.JSON(http.StatusConflict, gin.H{"error": "configuration is managed by Home"})
		return config.ClientConfig{}, configReloadSnapshot{}, false
	}
	if h.configReloadHook == nil || (h.configReloadReady != nil && !h.configReloadReady()) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "configuration reload is not ready; retry shortly"})
		return config.ClientConfig{}, configReloadSnapshot{}, false
	}
	previous := h.cfg
	next := previous.CloneForRuntime()
	index := slices.IndexFunc(next.Clients, func(client config.ClientConfig) bool { return client.ID == id })
	if create {
		if index >= 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "client ID already exists"})
			return config.ClientConfig{}, configReloadSnapshot{}, false
		}
		index = len(next.Clients)
		next.Clients = append(next.Clients, config.ClientConfig{ID: id})
	} else if index < 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "client not found"})
		return config.ClientConfig{}, configReloadSnapshot{}, false
	}
	client := &next.Clients[index]
	if key != "" {
		oldDigest := client.APIKeySHA256
		if client.APIKey != "" {
			oldDigest = config.ClientKeyDigest(client.APIKey)
		}
		if oldDigest != "" {
			// A legacy entry for the same secret must not keep the rotated credential alive.
			next.APIKeys = slices.DeleteFunc(next.APIKeys, func(key string) bool {
				return config.ClientKeyDigest(strings.TrimSpace(key)) == oldDigest
			})
		}
		client.APIKey, client.APIKeySHA256 = "", config.ClientKeyDigest(key)
	}
	if body.Disabled != nil {
		client.Disabled = *body.Disabled
	}
	if body.AllowedProviders != nil {
		client.AllowedProviders = *body.AllowedProviders
	}
	if body.Quotas != nil {
		client.Quotas = *body.Quotas
	}
	if err := next.ValidateClients(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return config.ClientConfig{}, configReloadSnapshot{}, false
	}
	h.cfg = next
	snapshot, saved := h.saveConfigAndSnapshotLocked(c)
	if !saved {
		h.cfg = previous
	}
	return *client, snapshot, saved
}
