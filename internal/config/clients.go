package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strings"
)

// ValidateClients rejects ambiguous identities and invalid quota values.
func (c *SDKConfig) ValidateClients() error {
	if c == nil {
		return nil
	}
	ids := make(map[string]struct{}, len(c.Clients))
	keys := make(map[string]struct{}, len(c.Clients))
	for i := range c.Clients {
		client := &c.Clients[i]
		client.ID = strings.TrimSpace(client.ID)
		client.APIKey = strings.TrimSpace(client.APIKey)
		client.APIKeySHA256 = strings.ToLower(strings.TrimSpace(client.APIKeySHA256))
		if client.ID == "" || (client.APIKey == "") == (client.APIKeySHA256 == "") {
			return fmt.Errorf("clients[%d] requires id and exactly one of api-key or api-key-sha256", i)
		}
		keyDigest := client.APIKeySHA256
		if keyDigest != "" {
			if decoded, err := hex.DecodeString(keyDigest); err != nil || len(decoded) != sha256.Size {
				return fmt.Errorf("clients[%d] has an invalid api-key-sha256", i)
			}
		} else {
			keyDigest = ClientKeyDigest(client.APIKey)
		}
		if _, exists := ids[client.ID]; exists {
			return fmt.Errorf("duplicate client id %q", client.ID)
		}
		if _, exists := keys[keyDigest]; exists {
			return fmt.Errorf("duplicate client api-key at clients[%d]", i)
		}
		ids[client.ID] = struct{}{}
		keys[keyDigest] = struct{}{}
		for j, provider := range client.AllowedProviders {
			provider = strings.ToLower(strings.TrimSpace(provider))
			switch provider {
			case "codex", "deepseek", "glm-coding-plan":
				client.AllowedProviders[j] = provider
			default:
				return fmt.Errorf("client %q has an unsupported allowed provider", client.ID)
			}
		}
		slices.Sort(client.AllowedProviders)
		client.AllowedProviders = slices.Compact(client.AllowedProviders)
		if client.Quotas.Codex != nil && !positiveFinite(client.Quotas.Codex.WeeklyUSD) {
			return fmt.Errorf("client %q codex.weekly-usd must be positive", client.ID)
		}
		if client.Quotas.DeepSeek != nil && !positiveFinite(client.Quotas.DeepSeek.LimitCNY) {
			return fmt.Errorf("client %q deepseek.limit-cny must be positive", client.ID)
		}
		if client.Quotas.GLM != nil && !client.Quotas.GLM.Valid() {
			return fmt.Errorf("client %q GLM token limits must be positive integers or null", client.ID)
		}
	}
	return nil
}

// ClientKeyDigest stores generated credentials without keeping the bearer token.
func ClientKeyDigest(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func positiveFinite(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
