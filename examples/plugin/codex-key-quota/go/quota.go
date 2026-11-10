package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

type price struct {
	Input       float64 `yaml:"input" json:"input"`
	CachedInput float64 `yaml:"cached-input" json:"cached_input"`
	CacheWrite  float64 `yaml:"cache-write" json:"cache_write"`
	Output      float64 `yaml:"output" json:"output"`
	Coefficient float64 `yaml:"coefficient" json:"coefficient"`
}

type pricingConfig map[string]price

func (p *pricingConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return errors.New("pricing must be a model map")
	}
	var wrapped struct {
		Models map[string]price `yaml:"models"`
	}
	if err := node.Decode(&wrapped); err != nil {
		return err
	}
	if wrapped.Models != nil {
		*p = wrapped.Models
		return nil
	}
	var direct map[string]price
	if err := node.Decode(&direct); err != nil {
		return err
	}
	*p = direct
	return nil
}

type quotaConfig struct {
	Storage struct {
		Path string `yaml:"path"`
	} `yaml:"storage"`
	Window struct {
		Timezone          string `yaml:"timezone"`
		WeeklyResetAnchor string `yaml:"weekly-reset-anchor"`
	} `yaml:"window"`
	WeeklyResetAnchor string        `yaml:"weekly-reset-anchor"`
	Pricing           pricingConfig `yaml:"pricing"`
	AccountProtection struct {
		StopUsedPercent *int `yaml:"stop-used-percent"`
	} `yaml:"account-protection"`
	ReservationUSD float64 `yaml:"reservation-usd"`
}

type usageEvent struct {
	Time       time.Time `json:"time"`
	RequestID  string    `json:"request_id,omitempty"`
	ClientID   string    `json:"client_id"`
	Model      string    `json:"model"`
	Input      int64     `json:"input_tokens"`
	CacheRead  int64     `json:"cache_read_tokens"`
	CacheWrite int64     `json:"cache_write_tokens"`
	Output     int64     `json:"output_tokens"`
	Price      price     `json:"price"`
	CostUSD    float64   `json:"cost_usd"`
}
type reservation struct {
	ClientID string
	USD      float64
}
type quotaState struct {
	mu            sync.Mutex
	cfg           quotaConfig
	location      *time.Location
	anchor        time.Time
	prices        map[string]price
	events        []usageEvent
	seen          map[string]struct{}
	reservations  map[string]reservation
	file          *os.File
	path          string
	failed        error
	weeklyPercent int
	weeklyResetAt time.Time
}

func newQuotaState() *quotaState {
	return &quotaState{reservations: make(map[string]reservation), seen: make(map[string]struct{})}
}

func (s *quotaState) configure(raw []byte) error {
	var request struct {
		ConfigYAML []byte `json:"config_yaml"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return err
	}
	var cfg quotaConfig
	if err := yaml.Unmarshal(request.ConfigYAML, &cfg); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Storage.Path) == "" {
		cfg.Storage.Path = "logs/codex-key-quota/usage.ndjson"
	}
	timezone := strings.TrimSpace(cfg.Window.Timezone)
	if timezone == "" {
		timezone = "Asia/Shanghai"
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return fmt.Errorf("window.timezone: %w", err)
	}
	anchorValue := strings.TrimSpace(cfg.WeeklyResetAnchor)
	if anchorValue == "" {
		anchorValue = strings.TrimSpace(cfg.Window.WeeklyResetAnchor)
	}
	anchor, err := time.Parse(time.RFC3339, anchorValue)
	if err != nil {
		return fmt.Errorf("weekly-reset-anchor must be RFC3339: %w", err)
	}
	if cfg.ReservationUSD == 0 {
		cfg.ReservationUSD = 0.01
	}
	if !positive(cfg.ReservationUSD) {
		return errors.New("reservation-usd must be positive")
	}
	if cfg.AccountProtection.StopUsedPercent == nil {
		value := 90
		cfg.AccountProtection.StopUsedPercent = &value
	}
	if *cfg.AccountProtection.StopUsedPercent < 0 || *cfg.AccountProtection.StopUsedPercent > 100 {
		return errors.New("account-protection.stop-used-percent must be between 0 and 100")
	}
	if len(cfg.Pricing) == 0 {
		return errors.New("pricing is required")
	}
	prices := make(map[string]price, len(cfg.Pricing))
	for model, item := range cfg.Pricing {
		model = strings.ToLower(strings.TrimSpace(model))
		if item.Coefficient == 0 {
			item.Coefficient = 1
		}
		if model == "" || !positive(item.Input) || !positive(item.Output) || !nonnegative(item.CachedInput) || !nonnegative(item.CacheWrite) || !positive(item.Coefficient) {
			return fmt.Errorf("invalid model price %q", model)
		}
		prices[model] = item
	}
	path, err := filepath.Abs(cfg.Storage.Path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil && path != s.path {
		return errors.New("storage.path cannot change during reload")
	}
	if s.file == nil {
		file, events, errOpen := openLedger(path)
		if errOpen != nil {
			return errOpen
		}
		s.file, s.events, s.path = file, events, path
		for _, event := range events {
			if event.RequestID != "" {
				s.seen[event.RequestID] = struct{}{}
			}
		}
	}
	s.cfg, s.location, s.anchor, s.prices = cfg, location, anchor.In(location), prices
	return nil
}

func openLedger(path string) (*os.File, []usageEvent, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return nil, nil, err
	}
	if _, err = file.Seek(0, 0); err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	var events []usageEvent
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var event usageEvent
		if err = json.Unmarshal(scanner.Bytes(), &event); err != nil || event.Time.IsZero() || event.ClientID == "" || !nonnegative(event.CostUSD) {
			_ = file.Close()
			return nil, nil, errors.New("invalid Codex quota ledger")
		}
		events = append(events, event)
	}
	if err = scanner.Err(); err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, events, nil
}

func (s *quotaState) before(req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	if !strings.EqualFold(strings.TrimSpace(req.Provider), "codex") {
		return pluginapi.RequestInterceptResponse{}
	}
	clientID, limit := clientQuota(req.Metadata, "codex_weekly_usd")
	if clientID == "" || !positive(limit) {
		return reject(http.StatusForbidden, "codex_quota_unconfigured", "client has no Codex quota")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil || s.file == nil {
		return reject(http.StatusServiceUnavailable, "quota_storage_unavailable", "Codex quota storage is unavailable")
	}
	if _, ok := s.prices[strings.ToLower(strings.TrimSpace(req.Model))]; !ok {
		return reject(http.StatusForbidden, "codex_model_unpriced", "Codex model has no configured price")
	}
	now := time.Now().In(s.location)
	stopPercent := *s.cfg.AccountProtection.StopUsedPercent
	if stopPercent > 0 && now.Before(s.weeklyResetAt) && s.weeklyPercent >= stopPercent {
		return reject(http.StatusTooManyRequests, "codex_account_quota_exceeded", "upstream Codex weekly quota is nearly exhausted")
	}
	if s.usedLocked(clientID, s.cycleStart(now))+s.reservedLocked(clientID)+s.cfg.ReservationUSD > limit {
		return reject(http.StatusTooManyRequests, "codex_quota_exceeded", "client Codex weekly quota is exhausted")
	}
	if req.RequestID != "" {
		s.reservations[req.RequestID] = reservation{clientID, s.cfg.ReservationUSD}
	}
	return pluginapi.RequestInterceptResponse{}
}

func clientQuota(metadata map[string]any, key string) (string, float64) {
	clientID, _ := metadata["client_id"].(string)
	clientID = strings.TrimSpace(clientID)
	raw, _ := metadata[key].(string)
	value, _ := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return clientID, value
}

func (s *quotaState) cycleStart(now time.Time) time.Time {
	anchor := s.anchor.In(s.location)
	now = now.In(s.location)
	if now.Before(anchor) {
		weeks := int64(math.Ceil(anchor.Sub(now).Hours() / 168))
		return anchor.Add(-time.Duration(weeks) * 7 * 24 * time.Hour)
	}
	weeks := int64(now.Sub(anchor) / (7 * 24 * time.Hour))
	return anchor.Add(time.Duration(weeks) * 7 * 24 * time.Hour)
}
func (s *quotaState) usedLocked(clientID string, start time.Time) float64 {
	total := 0.0
	for _, event := range s.events {
		if event.ClientID == clientID && !event.Time.Before(start) {
			total += event.CostUSD
		}
	}
	return total
}

type clientSnapshot struct {
	UsedUSD float64          `json:"used_usd"`
	ResetAt time.Time        `json:"reset_at"`
	Hours   usage.TokenHours `json:"hours"`
}

func (s *quotaState) snapshot(ids []string, now time.Time) (map[string]clientSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil || s.file == nil {
		return nil, errors.New("Codex quota storage is unavailable")
	}
	start := s.cycleStart(now)
	end := start.Add(7 * 24 * time.Hour)
	rows := make(map[string]clientSnapshot, len(ids))
	for _, id := range ids {
		rows[id] = clientSnapshot{ResetAt: end, Hours: usage.NewTokenHours(now)}
	}
	for _, event := range s.events {
		row, ok := rows[event.ClientID]
		if !ok {
			continue
		}
		if !event.Time.Before(start) {
			row.UsedUSD += event.CostUSD
		}
		row.Hours.Add(event.Time, event.CacheRead, event.Input, event.CacheWrite, event.Output)
		rows[event.ClientID] = row
	}
	return rows, nil
}

func (s *quotaState) reservedLocked(clientID string) float64 {
	total := 0.0
	for _, item := range s.reservations {
		if item.ClientID == clientID {
			total += item.USD
		}
	}
	return total
}
func (s *quotaState) complete(event pluginapi.RequestCompletion) {
	s.mu.Lock()
	delete(s.reservations, event.RequestID)
	s.mu.Unlock()
}

func (s *quotaState) record(record pluginapi.UsageRecord) error {
	if !strings.EqualFold(strings.TrimSpace(record.Provider), "codex") || record.Failed || strings.TrimSpace(record.ClientID) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	model := strings.ToLower(strings.TrimSpace(record.Model))
	if record.RequestID != "" {
		if _, exists := s.seen[record.RequestID]; exists {
			return nil
		}
	}
	rate, ok := s.prices[model]
	if !ok {
		s.failed = fmt.Errorf("usage model %q has no configured price", model)
		return s.failed
	}
	detail := record.Detail
	if detail.InputTokens < 0 || detail.OutputTokens < 0 || detail.CacheReadTokens < 0 || detail.CacheCreationTokens < 0 {
		s.failed = errors.New("negative usage token count")
		return s.failed
	}
	cacheRead := detail.CacheReadTokens
	if cacheRead == 0 {
		cacheRead = detail.CachedTokens
	}
	input := detail.InputTokens - cacheRead - detail.CacheCreationTokens
	if input < 0 {
		s.failed = errors.New("cached Codex input exceeds input tokens")
		return s.failed
	}
	// GPT-6.1 Sol's long-context API rate applies to the entire request.
	if model == "gpt-6.1-sol" && detail.InputTokens > 272_000 {
		rate.Input *= 2
		rate.CachedInput *= 2
		rate.CacheWrite *= 2
		rate.Output *= 1.5
	}
	cost := (float64(input)*rate.Input + float64(cacheRead)*rate.CachedInput + float64(detail.CacheCreationTokens)*rate.CacheWrite + float64(detail.OutputTokens)*rate.Output) / 1e6 * rate.Coefficient
	if !nonnegative(cost) {
		s.failed = errors.New("invalid calculated Codex usage")
		return s.failed
	}
	if cost > 0 {
		eventTime := record.RequestedAt
		if eventTime.IsZero() {
			eventTime = time.Now()
		}
		event := usageEvent{Time: eventTime.In(s.location), RequestID: record.RequestID, ClientID: record.ClientID, Model: model, Input: input, CacheRead: cacheRead, CacheWrite: detail.CacheCreationTokens, Output: detail.OutputTokens, Price: rate, CostUSD: cost}
		if err := s.appendLocked(event); err != nil {
			s.failed = err
			return err
		}
	}
	if percent, ok := percentHeader(record.ResponseHeaders, "X-Codex-Secondary-Used-Percent"); ok {
		s.weeklyPercent = percent
		s.weeklyResetAt = quotaExpiry(record.ResponseHeaders, "Secondary", time.Now(), 7*24*time.Hour)
	}
	return nil
}

func (s *quotaState) appendLocked(event usageEvent) error {
	if s.file == nil {
		return errors.New("ledger is closed")
	}
	line, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err = s.file.Write(append(line, '\n')); err != nil {
		return err
	}
	if err = s.file.Sync(); err != nil {
		return err
	}
	s.events = append(s.events, event)
	if event.RequestID != "" {
		s.seen[event.RequestID] = struct{}{}
	}
	return nil
}
func positive(v float64) bool    { return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }
func nonnegative(v float64) bool { return v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }
func quotaExpiry(headers http.Header, window string, now time.Time, fallback time.Duration) time.Time {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(headers.Get("X-Codex-"+window+"-Reset-At")), 10, 64); err == nil && seconds > now.Unix() {
		return time.Unix(seconds, 0)
	}
	if seconds, err := strconv.ParseInt(strings.TrimSpace(headers.Get("X-Codex-"+window+"-Reset-After-Seconds")), 10, 64); err == nil && seconds > 0 {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	return now.Add(fallback)
}
func percentHeader(headers http.Header, name string) (int, bool) {
	value, err := strconv.Atoi(strings.TrimSpace(headers.Get(name)))
	return value, err == nil && value >= 0 && value <= 100
}
func reject(status int, code, message string) pluginapi.RequestInterceptResponse {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message, "type": "rate_limit_error", "code": code}})
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return pluginapi.RequestInterceptResponse{Terminate: true, StatusCode: status, ResponseHeaders: header, ResponseBody: body}
}
func (s *quotaState) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
}
