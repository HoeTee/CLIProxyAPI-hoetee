// Command pluginconfigcheck validates the plugins.configs block of config.yaml
// against the schema expected by the codex-observability plugin, using the
// host's own config parser.
//
// Usage, from the repository root:
//
//	go run ./test/pluginconfigcheck [path/to/config.yaml]
//
// The default path is ./config.yaml.
package main

import (
	"fmt"
	"os"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"gopkg.in/yaml.v3"
)

func main() {
	configPath := "./config.yaml"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}
	raw, errRead := os.ReadFile(configPath)
	if errRead != nil {
		fmt.Println("READ_ERR", errRead)
		os.Exit(1)
	}
	parsed, errParse := config.ParseConfigBytes(raw)
	if errParse != nil {
		fmt.Println("PARSE_ERR", errParse)
		os.Exit(1)
	}
	fmt.Println("plugins.enabled =", parsed.Plugins.Enabled)
	fmt.Println("plugins.dir =", parsed.Plugins.Dir)
	item, ok := parsed.Plugins.Configs["codex-observability"]
	if !ok {
		for _, id := range []string{"codex-key-quota", "deepseek-key-quota"} {
			quotaItem, exists := parsed.Plugins.Configs[id]
			if !exists || quotaItem.Enabled == nil || !*quotaItem.Enabled {
				fmt.Println("MISSING_OR_DISABLED_PLUGIN_CONFIG", id)
				os.Exit(1)
			}
		}
		fmt.Println("QUOTA_PLUGIN_CONFIG_OK")
		return
	}
	enabled := false
	if item.Enabled != nil {
		enabled = *item.Enabled
	}
	fmt.Println("per-plugin enabled =", enabled, "priority =", item.Priority)
	subtree, errMarshal := yaml.Marshal(&item.Raw)
	if errMarshal != nil {
		fmt.Println("MARSHAL_ERR", errMarshal)
		os.Exit(1)
	}
	type rateRow struct {
		Input  float64 `yaml:"input"`
		Cached float64 `yaml:"cached"`
		Output float64 `yaml:"output"`
	}
	type keyLabel struct {
		Key   string `yaml:"key"`
		Label string `yaml:"label"`
	}
	type pluginConfig struct {
		ModelAllowlist struct {
			Enabled    bool     `yaml:"enabled"`
			Match      string   `yaml:"match"`
			Models     []string `yaml:"models"`
			StatusCode int      `yaml:"status-code"`
			ErrorCode  string   `yaml:"error-code"`
			Message    string   `yaml:"message"`
		} `yaml:"model-allowlist"`
		Observe struct {
			Enabled          bool               `yaml:"enabled"`
			Dir              string             `yaml:"dir"`
			BucketMinutes    int                `yaml:"bucket-minutes"`
			Timezone         string             `yaml:"timezone"`
			RawEvents        *bool              `yaml:"raw-events"`
			DumpQuotaHeaders *bool              `yaml:"quota-header-dump"`
			HashKeys         *bool              `yaml:"hash-keys"`
			KeyLabels        []keyLabel         `yaml:"key-labels"`
			FlushInterval    string             `yaml:"flush-interval"`
			RetentionDays    int                `yaml:"retention-days"`
			ComputeUnits     *bool              `yaml:"compute-units"`
			UnitsOverrides   map[string]rateRow `yaml:"units-overrides"`
		} `yaml:"observe"`
	}
	var decoded pluginConfig
	if errDecode := yaml.Unmarshal(subtree, &decoded); errDecode != nil {
		fmt.Println("PLUGIN_DECODE_ERR", errDecode)
		fmt.Println(string(subtree))
		os.Exit(1)
	}
	fmt.Printf("allowlist: enabled=%v match=%q models=%v status=%d code=%q\n",
		decoded.ModelAllowlist.Enabled, decoded.ModelAllowlist.Match, decoded.ModelAllowlist.Models,
		decoded.ModelAllowlist.StatusCode, decoded.ModelAllowlist.ErrorCode)
	fmt.Printf("observe: enabled=%v dir=%q bucket=%d tz=%q flush=%q retention=%d labels=%d\n",
		decoded.Observe.Enabled, decoded.Observe.Dir, decoded.Observe.BucketMinutes, decoded.Observe.Timezone,
		decoded.Observe.FlushInterval, decoded.Observe.RetentionDays, len(decoded.Observe.KeyLabels))
	fmt.Println("PLUGIN_CONFIG_OK")
}
