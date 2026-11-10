package configaccess

import (
	"context"
	"net/http"
	"testing"

	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestClientAuthenticationReturnsQuotaMetadata(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{Clients: []sdkconfig.ClientConfig{{
		ID: "alice", APIKey: "secret",
		Quotas: sdkconfig.ClientQuotas{
			Codex:    &sdkconfig.CodexClientQuota{WeeklyUSD: 50},
			DeepSeek: &sdkconfig.DeepSeekClientQuota{LimitCNY: 100},
		},
	}}}
	provider := newProvider("config", normalizeEntries(cfg))
	request, _ := http.NewRequest(http.MethodGet, "http://localhost/v1/models", nil)
	request.Header.Set("Authorization", "Bearer secret")
	result, authErr := provider.Authenticate(context.Background(), request)
	if authErr != nil {
		t.Fatal(authErr)
	}
	if result.Metadata["client_id"] != "alice" || result.Metadata["codex_weekly_usd"] != "50" || result.Metadata["deepseek_limit_cny"] != "100" {
		t.Fatalf("unexpected metadata: %+v", result.Metadata)
	}
}
