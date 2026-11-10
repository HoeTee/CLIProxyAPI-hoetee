package handlers

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

// ClientCanSeeModel applies the same provider policy to the model catalog as to execution.
func ClientCanSeeModel(c *gin.Context, model string) bool {
	if c == nil {
		return true
	}
	value, _ := c.Get("accessMetadata")
	metadata, _ := value.(map[string]string)
	allowed := metadata[sdkaccess.ClientProvidersMetadataKey]
	if allowed == "" {
		return true
	}
	for _, provider := range util.GetProviderName(strings.TrimPrefix(model, "models/")) {
		if sdkaccess.ClientAllowsProvider(allowed, provider) {
			return true
		}
	}
	return false
}

func FilterClientModels(c *gin.Context, models []map[string]any, idField string) []map[string]any {
	filtered := make([]map[string]any, 0, len(models))
	for _, model := range models {
		id, _ := model[idField].(string)
		if ClientCanSeeModel(c, id) {
			filtered = append(filtered, model)
		}
	}
	return filtered
}
