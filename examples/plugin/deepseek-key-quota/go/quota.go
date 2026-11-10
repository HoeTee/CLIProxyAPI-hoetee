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
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

type price struct {
	CacheHit  float64 `yaml:"cache-hit" json:"cache_hit"`
	CacheMiss float64 `yaml:"cache-miss" json:"cache_miss"`
	Output    float64 `yaml:"output" json:"output"`
}
type quotaConfig struct {
	Storage struct {
		Path string `yaml:"path"`
	} `yaml:"storage"`
	Pricing struct {
		OffPeak price `yaml:"off-peak"`
		Peak    price `yaml:"peak"`
	} `yaml:"pricing"`
	ReservationCNY float64 `yaml:"reservation-cny"`
}
type usageEvent struct {
	Time      time.Time `json:"time"`
	RequestID string    `json:"request_id,omitempty"`
	ClientID  string    `json:"client_id"`
	Model     string    `json:"model"`
	Period    string    `json:"period"`
	CacheHit  int64     `json:"cache_hit_tokens"`
	CacheMiss int64     `json:"cache_miss_tokens"`
	Output    int64     `json:"output_tokens"`
	Price     price     `json:"price"`
	CostCNY   float64   `json:"cost_cny"`
}
type reservation struct {
	ClientID string
	CNY      float64
}
type quotaState struct {
	mu           sync.Mutex
	cfg          quotaConfig
	location     *time.Location
	events       []usageEvent
	seen         map[string]struct{}
	reservations map[string]reservation
	file         *os.File
	path         string
	failed       error
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
		cfg.Storage.Path = "logs/deepseek-key-quota/usage.ndjson"
	}
	if cfg.ReservationCNY == 0 {
		cfg.ReservationCNY = 0.01
	}
	if !positive(cfg.ReservationCNY) {
		return errors.New("reservation-cny must be positive")
	}
	if !validPrice(cfg.Pricing.OffPeak) || !validPrice(cfg.Pricing.Peak) {
		return errors.New("pricing off-peak and peak rates must be nonnegative with positive cache-miss and output")
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return err
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
	s.cfg, s.location = cfg, location
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
		if err = json.Unmarshal(scanner.Bytes(), &event); err != nil || event.Time.IsZero() || event.ClientID == "" || !nonnegative(event.CostCNY) {
			_ = file.Close()
			return nil, nil, errors.New("invalid DeepSeek quota ledger")
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
	if !isDeepSeekProvider(req.Provider) {
		return pluginapi.RequestInterceptResponse{}
	}
	clientID, limit := clientQuota(req.Metadata, "deepseek_limit_cny")
	if clientID == "" || !positive(limit) {
		return reject(http.StatusForbidden, "deepseek_quota_unconfigured", "client has no DeepSeek quota")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil || s.file == nil {
		return reject(http.StatusServiceUnavailable, "quota_storage_unavailable", "DeepSeek quota storage is unavailable")
	}
	if s.usedLocked(clientID)+s.reservedLocked(clientID)+s.cfg.ReservationCNY > limit {
		return reject(http.StatusTooManyRequests, "deepseek_quota_exceeded", "client DeepSeek quota is exhausted")
	}
	if req.RequestID != "" {
		s.reservations[req.RequestID] = reservation{clientID, s.cfg.ReservationCNY}
	}
	return pluginapi.RequestInterceptResponse{}
}
func clientQuota(metadata map[string]any, key string) (string, float64) {
	clientID, _ := metadata["client_id"].(string)
	raw, _ := metadata[key].(string)
	var value float64
	_, _ = fmt.Sscan(strings.TrimSpace(raw), &value)
	return strings.TrimSpace(clientID), value
}
func (s *quotaState) usedLocked(clientID string) float64 {
	total := 0.0
	for _, event := range s.events {
		if event.ClientID == clientID {
			total += event.CostCNY
		}
	}
	return total
}
func (s *quotaState) reservedLocked(clientID string) float64 {
	total := 0.0
	for _, item := range s.reservations {
		if item.ClientID == clientID {
			total += item.CNY
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
	if !isDeepSeekProvider(record.Provider) || record.Failed || strings.TrimSpace(record.ClientID) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	detail := record.Detail
	if record.RequestID != "" {
		if _, exists := s.seen[record.RequestID]; exists {
			return nil
		}
	}
	if detail.InputTokens < 0 || detail.OutputTokens < 0 || detail.CacheReadTokens < 0 || detail.CacheMissTokens < 0 {
		s.failed = errors.New("negative usage token count")
		return s.failed
	}
	hit := detail.CacheReadTokens
	if hit == 0 {
		hit = detail.CachedTokens
	}
	miss := detail.CacheMissTokens
	if miss == 0 {
		miss = detail.InputTokens - hit
	}
	if miss < 0 || hit+miss > detail.InputTokens {
		s.failed = errors.New("invalid DeepSeek cache token breakdown")
		return s.failed
	}
	now := record.RequestedAt
	if now.IsZero() {
		now = time.Now()
	}
	now = now.In(s.location)
	period, rate := s.rateAt(now)
	cost := (float64(hit)*rate.CacheHit + float64(miss)*rate.CacheMiss + float64(detail.OutputTokens)*rate.Output) / 1e6
	if !nonnegative(cost) {
		s.failed = errors.New("invalid calculated DeepSeek usage")
		return s.failed
	}
	if cost == 0 {
		return nil
	}
	event := usageEvent{Time: now, RequestID: record.RequestID, ClientID: record.ClientID, Model: record.Model, Period: period, CacheHit: hit, CacheMiss: miss, Output: detail.OutputTokens, Price: rate, CostCNY: cost}
	line, err := json.Marshal(event)
	if err != nil {
		s.failed = err
		return err
	}
	if s.file == nil {
		s.failed = errors.New("ledger is closed")
		return s.failed
	}
	if _, err = s.file.Write(append(line, '\n')); err == nil {
		err = s.file.Sync()
	}
	if err != nil {
		s.failed = err
		return err
	}
	s.events = append(s.events, event)
	if event.RequestID != "" {
		s.seen[event.RequestID] = struct{}{}
	}
	return nil
}
func (s *quotaState) rateAt(now time.Time) (string, price) {
	now = now.In(s.location)
	weekday := now.Weekday()
	hour := now.Hour()
	peak := weekday >= time.Monday && weekday <= time.Friday && ((hour >= 9 && hour < 12) || (hour >= 14 && hour < 18))
	if peak {
		return "peak", s.cfg.Pricing.Peak
	}
	return "off-peak", s.cfg.Pricing.OffPeak
}
func validPrice(p price) bool {
	return nonnegative(p.CacheHit) && positive(p.CacheMiss) && positive(p.Output)
}
func isDeepSeekProvider(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	return provider == "deepseek" || provider == "openai-compatible-deepseek"
}
func positive(v float64) bool    { return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }
func nonnegative(v float64) bool { return v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }
func reject(status int, code, message string) pluginapi.RequestInterceptResponse {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message, "type": "rate_limit_error", "code": code}})
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	return pluginapi.RequestInterceptResponse{Terminate: true, StatusCode: status, ResponseHeaders: headers, ResponseBody: body}
}
func (s *quotaState) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
}
