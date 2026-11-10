package configaccess

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// Register ensures the config-access provider is available to the access manager.
func Register(cfg *sdkconfig.SDKConfig) {
	if cfg == nil {
		sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeConfigAPIKey)
		return
	}

	entries := normalizeEntries(cfg)
	if len(entries) == 0 {
		sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeConfigAPIKey)
		return
	}

	sdkaccess.RegisterProvider(
		sdkaccess.AccessProviderTypeConfigAPIKey,
		newProvider(sdkaccess.DefaultAccessProviderName, entries),
	)
}

type provider struct {
	name string
	keys map[string]clientEntry
}

type clientEntry struct {
	clientID         string
	disabled         bool
	allowedProviders string
	codexWeeklyUSD   float64
	deepseekLimitCNY float64
}

func newProvider(name string, entries map[string]clientEntry) *provider {
	providerName := strings.TrimSpace(name)
	if providerName == "" {
		providerName = sdkaccess.DefaultAccessProviderName
	}
	keySet := make(map[string]clientEntry, len(entries))
	for key, entry := range entries {
		keySet[key] = entry
	}
	return &provider{name: providerName, keys: keySet}
}

func (p *provider) Identifier() string {
	if p == nil || p.name == "" {
		return sdkaccess.DefaultAccessProviderName
	}
	return p.name
}

func (p *provider) Authenticate(_ context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if p == nil {
		return nil, sdkaccess.NewNotHandledError()
	}
	if len(p.keys) == 0 {
		return nil, sdkaccess.NewNotHandledError()
	}
	authHeader := r.Header.Get("Authorization")
	authHeaderGoogle := r.Header.Get("X-Goog-Api-Key")
	authHeaderAnthropic := r.Header.Get("X-Api-Key")
	queryKey := ""
	queryAuthToken := ""
	if r.URL != nil {
		queryKey = r.URL.Query().Get("key")
		queryAuthToken = r.URL.Query().Get("auth_token")
	}
	if authHeader == "" && authHeaderGoogle == "" && authHeaderAnthropic == "" && queryKey == "" && queryAuthToken == "" {
		return nil, sdkaccess.NewNoCredentialsError()
	}

	apiKey := extractBearerToken(authHeader)

	candidates := []struct {
		value  string
		source string
	}{
		{apiKey, "authorization"},
		{authHeaderGoogle, "x-goog-api-key"},
		{authHeaderAnthropic, "x-api-key"},
		{queryKey, "query-key"},
		{queryAuthToken, "query-auth-token"},
	}

	for _, candidate := range candidates {
		if candidate.value == "" {
			continue
		}
		if entry, ok := p.keys[config.ClientKeyDigest(candidate.value)]; ok {
			if entry.disabled {
				return nil, &sdkaccess.AuthError{Code: "client_disabled", Message: "Invalid API key", StatusCode: http.StatusUnauthorized}
			}
			metadata := map[string]string{"source": candidate.source}
			if entry.clientID != "" {
				metadata["client_id"] = entry.clientID
				if entry.allowedProviders != "" {
					metadata[sdkaccess.ClientProvidersMetadataKey] = entry.allowedProviders
				}
				if entry.codexWeeklyUSD > 0 {
					metadata["codex_weekly_usd"] = strconv.FormatFloat(entry.codexWeeklyUSD, 'g', -1, 64)
				}
				if entry.deepseekLimitCNY > 0 {
					metadata["deepseek_limit_cny"] = strconv.FormatFloat(entry.deepseekLimitCNY, 'g', -1, 64)
				}
			}
			return &sdkaccess.Result{
				Provider:  p.Identifier(),
				Principal: candidate.value,
				Metadata:  metadata,
			}, nil
		}
	}

	return nil, sdkaccess.NewInvalidCredentialError()
}

func normalizeEntries(cfg *sdkconfig.SDKConfig) map[string]clientEntry {
	entries := make(map[string]clientEntry, len(cfg.APIKeys)+len(cfg.Clients))
	for _, key := range normalizeKeys(cfg.APIKeys) {
		entries[config.ClientKeyDigest(key)] = clientEntry{}
	}
	for _, client := range cfg.Clients {
		key := client.APIKeySHA256
		if key == "" {
			if plaintext := strings.TrimSpace(client.APIKey); plaintext != "" {
				key = config.ClientKeyDigest(plaintext)
			} else {
				continue
			}
		}
		entry := clientEntry{
			clientID: strings.TrimSpace(client.ID), disabled: client.Disabled,
			allowedProviders: strings.Join(client.AllowedProviders, ","),
		}
		if client.Quotas.Codex != nil {
			entry.codexWeeklyUSD = client.Quotas.Codex.WeeklyUSD
		}
		if client.Quotas.DeepSeek != nil {
			entry.deepseekLimitCNY = client.Quotas.DeepSeek.LimitCNY
		}
		entries[key] = entry
	}
	return entries
}

func extractBearerToken(header string) string {
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return header
	}
	if strings.ToLower(parts[0]) != "bearer" {
		return header
	}
	return strings.TrimSpace(parts[1])
}

func normalizeKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		if _, exists := seen[trimmedKey]; exists {
			continue
		}
		seen[trimmedKey] = struct{}{}
		normalized = append(normalized, trimmedKey)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}
