package helps

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/glmquota"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

func AdmitGLMRequest(ctx context.Context, provider string, cfg *config.Config, opts executor.Options, quotaPlugin func() pluginapi.ClientQuotaPlugin) (func(*glmquota.Usage), error) {
	if provider != glmquota.ProviderKey {
		return nil, nil
	}
	unavailable := glmquota.QuotaError{Message: "GLM quota plugin is unavailable", Status: http.StatusServiceUnavailable}
	if quotaPlugin == nil {
		return nil, unavailable
	}
	plugin := quotaPlugin()
	if plugin == nil {
		return nil, unavailable
	}
	clientID, _ := opts.Metadata[executor.ClientIDMetadataKey].(string)
	var policy *config.GLMClientQuota
	if cfg != nil && clientID != "" {
		for _, client := range cfg.Clients {
			if client.ID == clientID {
				policy = client.Quotas.GLM
				break
			}
		}
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return nil, unavailable
	}
	decision, err := plugin.AdmitClientQuota(ctx, pluginapi.ClientQuotaRequest{ClientID: clientID, Policy: raw})
	if err != nil {
		log.WithError(err).Warn("could not check GLM client quota")
		return nil, unavailable
	}
	if decision.Status != 0 {
		if decision.Status < 400 || decision.Status > 599 {
			return nil, unavailable
		}
		return nil, glmquota.QuotaError{Message: decision.Message, Status: decision.Status}
	}
	return func(usage *glmquota.Usage) {
		record := pluginapi.ClientQuotaRecord{ClientID: clientID, Usage: usage}
		if errRecord := plugin.RecordClientQuota(context.WithoutCancel(ctx), record); errRecord != nil {
			log.WithError(errRecord).Error("could not write GLM usage ledger")
		}
	}, nil
}
