package management

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func (h *Handler) GetDeepSeekBalance(c *gin.Context) {
	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()
	c.Header("Cache-Control", "no-store")
	entry := deepSeekBalanceCredential(cfg)
	if entry == nil {
		c.JSON(http.StatusOK, gin.H{"configured": false})
		return
	}
	ctx := c.Request.Context()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.deepseek.com/user/balance", nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "DeepSeek account balance unavailable"})
		return
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(entry.APIKey))
	client := helps.NewProxyAwareHTTPClient(ctx, cfg, &cliproxyauth.Auth{ProxyURL: entry.ProxyURL}, 0)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "DeepSeek account balance unavailable"})
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			c.Error(errClose)
		}
	}()
	var balance struct {
		Available *bool `json:"is_available"`
		Items     []struct {
			Currency string `json:"currency"`
			Total    string `json:"total_balance"`
			Granted  string `json:"granted_balance"`
			ToppedUp string `json:"topped_up_balance"`
		} `json:"balance_infos"`
	}
	if resp.StatusCode != http.StatusOK ||
		json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&balance) != nil ||
		balance.Available == nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "DeepSeek account balance unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"configured": true, "balance": balance})
}

func deepSeekBalanceCredential(cfg *config.Config) *config.OpenAICompatibilityAPIKey {
	if cfg == nil {
		return nil
	}
	for _, provider := range cfg.OpenAICompatibility {
		baseURL := strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
		if provider.Disabled || !strings.EqualFold(strings.TrimSpace(provider.Name), "deepseek") ||
			(baseURL != "https://api.deepseek.com" && baseURL != "https://api.deepseek.com/v1") {
			continue
		}
		for _, entry := range provider.APIKeyEntries {
			if strings.TrimSpace(entry.APIKey) != "" && (entry.Weight == nil || *entry.Weight > 0) {
				return &entry
			}
		}
	}
	return nil
}
