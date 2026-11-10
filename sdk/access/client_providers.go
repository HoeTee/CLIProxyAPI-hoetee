package access

import (
	"strings"
)

const ClientProvidersMetadataKey = "client_allowed_providers"

// ClientAllowsProvider compares a runtime provider with the client's configured provider IDs.
func ClientAllowsProvider(allowed, provider string) bool {
	if allowed == "" {
		return true
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "openai-compatible-deepseek":
		provider = "deepseek"
	case "openai-compatible-glm-coding-plan":
		provider = "glm-coding-plan"
	}
	for candidate := range strings.SplitSeq(allowed, ",") {
		if candidate == provider {
			return true
		}
	}
	return false
}
