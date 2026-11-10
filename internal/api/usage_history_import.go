package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/glmquota"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagehistory"
	log "github.com/sirupsen/logrus"
)

func (s *Server) importUsageHistory(cfg *config.Config) {
	if s.usageHistory == nil || cfg == nil {
		return
	}
	for _, source := range []struct{ provider, plugin, path string }{
		{"codex", "codex-key-quota", "logs/codex-key-quota/usage.ndjson"},
		{"deepseek", "deepseek-key-quota", "logs/deepseek-key-quota/usage.ndjson"},
		{"glm", glmquota.PluginID, "logs/glm-coding-plan/usage.ndjson"},
	} {
		raw := cfg.Plugins.Configs[source.plugin].Raw
		var settings struct {
			Storage struct {
				Path string `yaml:"path"`
			} `yaml:"storage"`
		}
		if raw.Kind != 0 {
			if err := raw.Decode(&settings); err != nil {
				log.WithField("provider", source.provider).Warn("cannot read quota storage settings for usage history")
				continue
			}
		}
		if path := strings.TrimSpace(settings.Storage.Path); path != "" {
			source.path = path
		}
		skipped, err := importQuotaLedger(context.Background(), s.usageHistory, source.provider, source.path)
		if err != nil || skipped > 0 {
			log.WithError(err).WithFields(log.Fields{"provider": source.provider, "skipped": skipped}).Warn("quota ledger history import incomplete")
		}
	}
}

func importQuotaLedger(ctx context.Context, store *usagehistory.Store, provider, path string) (int, error) {
	done, err := store.Imported(ctx, provider)
	if err != nil || done {
		return 0, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			log.WithError(errClose).Warn("cannot close quota ledger")
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New("quota ledger is not a regular file")
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	batch := make([]usagehistory.ImportedUsage, 0, 256)
	skipped := 0
	for line := 1; scanner.Scan(); line++ {
		raw := scanner.Bytes()
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		entry, errParse := parseQuotaLedgerEntry(provider, raw)
		if errParse != nil {
			skipped++
			continue
		}
		if entry.InputTokens == nil && entry.OutputTokens == nil && entry.CachedTokens == nil {
			continue
		}
		hash := sha256.Sum256(raw)
		entry.ID = "quota:" + provider + ":" + strconv.Itoa(line) + ":" + hex.EncodeToString(hash[:])
		batch = append(batch, entry)
		if len(batch) == cap(batch) {
			if errImport := store.Import(ctx, batch); errImport != nil {
				return skipped, errImport
			}
			batch = batch[:0]
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		return skipped, errScan
	}
	if errImport := store.Import(ctx, batch); errImport != nil {
		return skipped, errImport
	}
	return skipped, store.MarkImported(ctx, provider)
}

func parseQuotaLedgerEntry(provider string, raw []byte) (entry usagehistory.ImportedUsage, err error) {
	entry.Provider = provider
	switch provider {
	case "glm":
		var row struct {
			Client string    `json:"client"`
			At     time.Time `json:"at"`
			Usage  *struct {
				Model  string `json:"model"`
				Input  *int64 `json:"input_tokens"`
				Output *int64 `json:"output_tokens"`
				Cached *int64 `json:"cached_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			return entry, err
		}
		entry.ClientID, entry.At = row.Client, row.At
		if row.Usage != nil {
			entry.Model = row.Usage.Model
			entry.InputTokens, entry.OutputTokens, entry.CachedTokens = row.Usage.Input, row.Usage.Output, row.Usage.Cached
		}
	case "codex", "deepseek":
		var row struct {
			At     time.Time `json:"time"`
			Client string    `json:"client_id"`
			Model  string    `json:"model"`
			Input  *int64    `json:"input_tokens"`
			Output *int64    `json:"output_tokens"`
			Read   *int64    `json:"cache_read_tokens"`
			Write  *int64    `json:"cache_write_tokens"`
			Hit    *int64    `json:"cache_hit_tokens"`
			Miss   *int64    `json:"cache_miss_tokens"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			return entry, err
		}
		entry.ClientID, entry.At, entry.Model, entry.OutputTokens = row.Client, row.At, row.Model, row.Output
		parts := []*int64{row.Input, row.Read, row.Write}
		entry.CachedTokens = row.Read
		if provider == "deepseek" {
			parts = []*int64{row.Hit, row.Miss}
			entry.CachedTokens = row.Hit
		}
		var total int64
		for _, part := range parts {
			if part == nil || *part < 0 || *part > math.MaxInt64-total {
				return entry, errors.New("invalid legacy input token breakdown")
			}
			total += *part
		}
		entry.InputTokens = &total
	default:
		return entry, errors.New("unknown legacy quota provider")
	}
	if entry.At.IsZero() || strings.TrimSpace(entry.ClientID) == "" {
		return entry, errors.New("missing quota ledger event time or client")
	}
	for _, amount := range []*int64{entry.InputTokens, entry.OutputTokens, entry.CachedTokens} {
		if amount != nil && *amount < 0 {
			return entry, errors.New("negative legacy token count")
		}
	}
	if entry.InputTokens != nil && entry.CachedTokens != nil && *entry.CachedTokens > *entry.InputTokens {
		return entry, errors.New("legacy cache hits exceed input tokens")
	}
	return entry, nil
}
