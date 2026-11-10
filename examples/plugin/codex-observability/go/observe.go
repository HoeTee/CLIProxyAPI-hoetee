package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// tokenSum accumulates every token counter exposed to plugins.
type tokenSum struct {
	Input       int64 `json:"input"`
	Output      int64 `json:"output"`
	Reasoning   int64 `json:"reasoning"`
	Cached      int64 `json:"cached"`
	CacheRead   int64 `json:"cache_read"`
	CacheCreate int64 `json:"cache_create"`
	Total       int64 `json:"total"`
}

func (s *tokenSum) add(detail pluginapi.UsageDetail) {
	s.Input += detail.InputTokens
	s.Output += detail.OutputTokens
	s.Reasoning += detail.ReasoningTokens
	s.Cached += detail.CachedTokens
	s.CacheRead += detail.CacheReadTokens
	s.CacheCreate += detail.CacheCreationTokens
	s.Total += detail.TotalTokens
}

func tokenSumOf(detail pluginapi.UsageDetail) tokenSum {
	var sum tokenSum
	sum.add(detail)
	return sum
}

// keyBucket aggregates one client key's usage inside one time bucket.
type keyBucket struct {
	BucketStart time.Time `json:"bucket_start"`
	BucketEnd   time.Time `json:"bucket_end"`
	Key         string    `json:"key"`
	KeyLabel    string    `json:"key_label,omitempty"`
	AuthID      string    `json:"auth_id,omitempty"`
	Provider    string    `json:"provider,omitempty"`
	Model       string    `json:"model,omitempty"`
	Requests    int64     `json:"requests"`
	Failed      int64     `json:"failed"`
	Units       float64   `json:"units"`
	Mismatched  int64     `json:"units_token_mismatch"`
	Tokens      tokenSum  `json:"tokens"`
}

// windowSample tracks one upstream rate-limit window across a bucket.
type windowSample struct {
	Scope             string
	LimitName         string
	DisplayName       string
	Window            string
	WindowMinutes     int
	First             int
	Last              int
	Min               int
	Max               int
	Samples           int64
	FirstAt           time.Time
	LastAt            time.Time
	ResetAt           string
	ResetAtUnix       int64
	ResetAfterSeconds int64
	Allowed           string
	LimitReached      string
	ResetSeen         bool
}

// observe folds one upstream window reading into the sample.
func (s *windowSample) observe(window windowObservation, at time.Time) {
	percent := window.UsedPercent
	if s.Samples == 0 {
		s.First = percent
		s.Min = percent
		s.Max = percent
		s.FirstAt = at
	} else {
		if percent < s.Last {
			// The window rolled over inside this bucket; the delta is unusable.
			s.ResetSeen = true
		}
		if percent < s.Min {
			s.Min = percent
		}
		if percent > s.Max {
			s.Max = percent
		}
	}
	if window.ResetAt != "" {
		if s.ResetAt != "" && s.ResetAt != window.ResetAt {
			s.ResetSeen = true
		}
		s.ResetAt = window.ResetAt
		if epoch, errParse := strconv.ParseInt(window.ResetAt, 10, 64); errParse == nil {
			s.ResetAtUnix = epoch
		}
	}
	if window.HasResetAfter {
		s.ResetAfterSeconds = window.ResetAfterSeconds
	}
	if window.Allowed != "" {
		s.Allowed = window.Allowed
	}
	if window.LimitReached != "" {
		s.LimitReached = window.LimitReached
	}
	s.Last = percent
	s.LastAt = at
	s.Samples++
}

// creditsSample tracks the upstream credits object across a bucket.
type creditsSample struct {
	HasCredits   string `json:"has_credits,omitempty"`
	Unlimited    string `json:"unlimited,omitempty"`
	BalanceFirst string `json:"balance_first,omitempty"`
	BalanceLast  string `json:"balance_last,omitempty"`
}

// scopeSample tracks limit-wide fields for one quota scope across a bucket.
type scopeSample struct {
	Scope        string
	LimitName    string
	DisplayName  string
	Allowed      string
	LimitReached string
}

// accountBucket accumulates upstream quota observations for one credential.
type accountBucket struct {
	AuthID      string
	PlanType    string
	ActiveLimit string
	Samples     int64
	windows     map[string]*windowSample
	scopes      map[string]*scopeSample
	credits     *creditsSample
}

func (b *accountBucket) observeCredits(credits *creditsObservation) {
	if b.credits == nil {
		b.credits = &creditsSample{
			HasCredits:   credits.HasCredits,
			Unlimited:    credits.Unlimited,
			BalanceFirst: credits.Balance,
			BalanceLast:  credits.Balance,
		}
		return
	}
	if credits.HasCredits != "" {
		b.credits.HasCredits = credits.HasCredits
	}
	if credits.Unlimited != "" {
		b.credits.Unlimited = credits.Unlimited
	}
	b.credits.BalanceLast = credits.Balance
}

// windowReport is the serialized form of one observed window.
type windowReport struct {
	Scope             string    `json:"scope"`
	LimitName         string    `json:"limit_name,omitempty"`
	DisplayName       string    `json:"limit_display_name,omitempty"`
	Window            string    `json:"window"`
	WindowMinutes     int       `json:"window_minutes"`
	First             int       `json:"first"`
	Last              int       `json:"last"`
	Min               int       `json:"min"`
	Max               int       `json:"max"`
	Delta             int       `json:"delta"`
	Samples           int64     `json:"samples"`
	FirstAt           time.Time `json:"first_at"`
	LastAt            time.Time `json:"last_at"`
	ResetAt           string    `json:"reset_at,omitempty"`
	ResetAtUnix       int64     `json:"reset_at_unix,omitempty"`
	ResetAfterSeconds int64     `json:"reset_after_seconds,omitempty"`
	Allowed           string    `json:"allowed,omitempty"`
	LimitReached      string    `json:"limit_reached,omitempty"`
	ResetSeen         bool      `json:"reset_seen"`
}

// scopeReport is the serialized form of one observed quota scope.
type scopeReport struct {
	Scope        string `json:"scope"`
	LimitName    string `json:"limit_name,omitempty"`
	DisplayName  string `json:"limit_display_name,omitempty"`
	Allowed      string `json:"allowed,omitempty"`
	LimitReached string `json:"limit_reached,omitempty"`
}

// accountQuotaRecord is one account-level observation line.
type accountQuotaRecord struct {
	BucketStart time.Time      `json:"bucket_start"`
	BucketEnd   time.Time      `json:"bucket_end"`
	AuthID      string         `json:"auth_id"`
	PlanType    string         `json:"plan_type,omitempty"`
	ActiveLimit string         `json:"active_limit,omitempty"`
	Samples     int64          `json:"samples"`
	Windows     []windowReport `json:"windows"`
	Scopes      []scopeReport  `json:"scopes,omitempty"`
	Credits     *creditsSample `json:"credits,omitempty"`
}

// rawEvent is one request-level observation line.
type rawEvent struct {
	Time          time.Time `json:"ts"`
	Key           string    `json:"key,omitempty"`
	KeyLabel      string    `json:"key_label,omitempty"`
	AuthID        string    `json:"auth_id,omitempty"`
	Provider      string    `json:"provider,omitempty"`
	Model         string    `json:"model,omitempty"`
	RequestedName string    `json:"requested_model,omitempty"`
	Failed        bool      `json:"failed"`
	Stream        bool      `json:"stream"`
	LatencyMS     int64     `json:"latency_ms"`
	Tokens        tokenSum  `json:"tokens"`
	QuotaPresent  bool      `json:"quota_present"`
}

// observer aggregates usage and quota observations and writes them to NDJSON.
type observer struct {
	mu            sync.Mutex
	dir           string
	loc           *time.Location
	bucket        time.Duration
	rawEvents     bool
	hashKeys      bool
	computeUnits  bool
	dumpHeaders   bool
	retentionDays int
	lastPruneDay  string
	closed        bool
	labelOf       map[string]string
	rates         *rateResolver
	keyBuckets    map[time.Time]map[string]*keyBucket
	acctBuckets   map[time.Time]map[string]*accountBucket
	rawWriter     *dayWriter
	keyWriter     *dayWriter
	acctWriter    *dayWriter
	dumpWriter    *dayWriter
}

func newObserver(cfg observeConfig, loc *time.Location) *observer {
	dir := strings.TrimSpace(cfg.Dir)
	if dir == "" {
		dir = "logs/quota-obs"
	}
	bucketMinutes := cfg.BucketMinutes
	if bucketMinutes <= 0 {
		bucketMinutes = 30
	}
	retentionDays := cfg.RetentionDays
	if retentionDays <= 0 {
		retentionDays = 30
	}
	return &observer{
		dir:           dir,
		loc:           loc,
		bucket:        time.Duration(bucketMinutes) * time.Minute,
		rawEvents:     boolOr(cfg.RawEvents, true),
		hashKeys:      boolOr(cfg.HashKeys, true),
		computeUnits:  boolOr(cfg.ComputeUnits, true),
		dumpHeaders:   boolOr(cfg.DumpQuotaHeaders, false),
		retentionDays: retentionDays,
		labelOf:       labelMap(cfg.KeyLabels),
		rates:         newRateResolver(cfg.UnitsOverrides),
		keyBuckets:    make(map[time.Time]map[string]*keyBucket),
		acctBuckets:   make(map[time.Time]map[string]*accountBucket),
		rawWriter:     newDayWriter(dir, "raw"),
		keyWriter:     newDayWriter(dir, "key-usage"),
		acctWriter:    newDayWriter(dir, "account-quota"),
		dumpWriter:    newDayWriter(dir, "quota-headers"),
	}
}

// bucketStart snaps a timestamp to the start of its bucket in the configured zone.
func (o *observer) bucketStart(at time.Time) time.Time {
	local := at.In(o.loc)
	dayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, o.loc)
	return dayStart.Add(local.Sub(dayStart) / o.bucket * o.bucket)
}

// keyIdentity returns the stored key form plus an optional label.
func (o *observer) keyIdentity(apiKey string) (string, string) {
	trimmed := strings.TrimSpace(apiKey)
	if trimmed == "" {
		return "", ""
	}
	label := o.labelOf[trimmed]
	if o.hashKeys {
		return hashKey(trimmed), label
	}
	return trimmed, label
}

func hashKey(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])[:12]
}

func (o *observer) keyBucketLocked(start time.Time, key, label, authID, provider, model string) *keyBucket {
	buckets := o.keyBuckets[start]
	if buckets == nil {
		buckets = make(map[string]*keyBucket)
		o.keyBuckets[start] = buckets
	}
	id := key + "\x00" + authID + "\x00" + model
	entry := buckets[id]
	if entry == nil {
		entry = &keyBucket{
			BucketStart: start,
			BucketEnd:   start.Add(o.bucket),
			Key:         key,
			KeyLabel:    label,
			AuthID:      authID,
			Provider:    provider,
			Model:       model,
		}
		buckets[id] = entry
	}
	return entry
}

func (o *observer) accountBucketLocked(start time.Time, authID string) *accountBucket {
	buckets := o.acctBuckets[start]
	if buckets == nil {
		buckets = make(map[string]*accountBucket)
		o.acctBuckets[start] = buckets
	}
	entry := buckets[authID]
	if entry == nil {
		entry = &accountBucket{
			AuthID:  authID,
			windows: make(map[string]*windowSample),
			scopes:  make(map[string]*scopeSample),
		}
		buckets[authID] = entry
	}
	return entry
}

// record folds one usage record into the open buckets.
func (o *observer) record(record pluginapi.UsageRecord) {
	if o == nil {
		return
	}
	at := record.RequestedAt
	if at.IsZero() {
		at = time.Now()
	}
	at = at.In(o.loc)
	start := o.bucketStart(at)

	key, label := o.keyIdentity(record.APIKey)
	model := strings.TrimSpace(record.Model)
	if model == "" {
		model = strings.TrimSpace(record.ResponseModel)
	}
	authID := strings.TrimSpace(record.AuthID)
	counts := tokenSumOf(record.Detail)

	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}

	entry := o.keyBucketLocked(start, key, label, authID, record.Provider, model)
	entry.Requests++
	if record.Failed {
		entry.Failed++
	}
	entry.Tokens.add(record.Detail)
	if o.computeUnits {
		entry.Units += o.rates.resolve(strings.ToLower(model)).unitCost(counts)
		if record.Detail.TotalTokens > 0 && record.Detail.TotalTokens != record.Detail.InputTokens+record.Detail.OutputTokens {
			entry.Mismatched++
		}
	}

	quota, okQuota := parseQuotaHeaders(record.ResponseHeaders)
	if okQuota {
		account := o.accountBucketLocked(start, authID)
		account.Samples++
		if quota.PlanType != "" {
			account.PlanType = quota.PlanType
		}
		if quota.ActiveLimit != "" {
			account.ActiveLimit = quota.ActiveLimit
		}
		for _, window := range quota.Windows {
			id := window.Scope + "|" + window.LimitName + "|" + window.Window
			sample := account.windows[id]
			if sample == nil {
				sample = &windowSample{
					Scope:       window.Scope,
					LimitName:   window.LimitName,
					DisplayName: window.DisplayName,
					Window:      window.Window,
				}
				account.windows[id] = sample
			}
			sample.WindowMinutes = window.WindowMinutes
			if sample.DisplayName == "" {
				sample.DisplayName = window.DisplayName
			}
			sample.observe(window, at)
		}
		for _, scope := range quota.Scopes {
			id := scope.Scope + "|" + scope.LimitName
			sample := account.scopes[id]
			if sample == nil {
				sample = &scopeSample{Scope: scope.Scope, LimitName: scope.LimitName}
				account.scopes[id] = sample
			}
			if scope.DisplayName != "" {
				sample.DisplayName = scope.DisplayName
			}
			if scope.Allowed != "" {
				sample.Allowed = scope.Allowed
			}
			if scope.LimitReached != "" {
				sample.LimitReached = scope.LimitReached
			}
		}
		if quota.Credits != nil {
			account.observeCredits(quota.Credits)
		}
	}

	if o.dumpHeaders {
		o.dumpQuotaHeadersLocked(start, at, key, authID, record.ResponseHeaders)
	}

	if o.rawEvents {
		line, errMarshal := json.Marshal(rawEvent{
			Time:          at,
			Key:           key,
			KeyLabel:      label,
			AuthID:        authID,
			Provider:      record.Provider,
			Model:         model,
			RequestedName: strings.TrimSpace(record.Alias),
			Failed:        record.Failed,
			Stream:        record.Stream,
			LatencyMS:     record.Latency.Milliseconds(),
			Tokens:        counts,
			QuotaPresent:  okQuota,
		})
		if errMarshal == nil {
			_ = o.rawWriter.writeLine(start.Format("2006-01-02"), append(line, '\n'))
		}
	}
}

// quotaHeaderDump is one verbatim x-codex-* header snapshot.
type quotaHeaderDump struct {
	Time    time.Time         `json:"ts"`
	Key     string            `json:"key,omitempty"`
	AuthID  string            `json:"auth_id,omitempty"`
	Headers map[string]string `json:"headers"`
}

// dumpQuotaHeadersLocked records every x-codex-* header exactly as received.
func (o *observer) dumpQuotaHeadersLocked(day time.Time, at time.Time, key, authID string, headers http.Header) {
	if len(headers) == 0 {
		return
	}
	selected := make(map[string]string)
	for name, values := range headers {
		if !strings.HasPrefix(strings.ToLower(name), "x-codex-") || len(values) == 0 {
			continue
		}
		selected[name] = strings.TrimSpace(values[0])
	}
	if len(selected) == 0 {
		return
	}
	line, errMarshal := json.Marshal(quotaHeaderDump{Time: at, Key: key, AuthID: authID, Headers: selected})
	if errMarshal != nil {
		return
	}
	_ = o.dumpWriter.writeLine(day.Format("2006-01-02"), append(line, '\n'))
}

// flushClosed writes every bucket that started before the current bucket.
func (o *observer) flushClosed(now time.Time) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	o.flushBucketsLocked(o.bucketStart(now.In(o.loc)))
}

// close flushes all outstanding buckets and releases file handles.
func (o *observer) close() {
	if o == nil {
		return
	}
	o.mu.Lock()
	if !o.closed {
		o.closed = true
		o.flushBucketsLocked(time.Now().Add(365 * 24 * time.Hour))
	}
	o.mu.Unlock()
	o.rawWriter.close()
	o.keyWriter.close()
	o.acctWriter.close()
	o.dumpWriter.close()
}

func (o *observer) flushBucketsLocked(before time.Time) {
	for _, start := range sortedStarts(o.keyBuckets, before) {
		o.writeKeyBucketsLocked(start)
		delete(o.keyBuckets, start)
	}
	for _, start := range sortedStarts(o.acctBuckets, before) {
		o.writeAccountBucketsLocked(start)
		delete(o.acctBuckets, start)
	}
	o.pruneLocked()
}

func sortedStarts[V any](buckets map[time.Time]map[string]V, before time.Time) []time.Time {
	starts := make([]time.Time, 0, len(buckets))
	for start := range buckets {
		if start.Before(before) {
			starts = append(starts, start)
		}
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
	return starts
}

func (o *observer) writeKeyBucketsLocked(start time.Time) {
	buckets := o.keyBuckets[start]
	if len(buckets) == 0 {
		return
	}
	ids := make([]string, 0, len(buckets))
	for id := range buckets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	day := start.Format("2006-01-02")
	for _, id := range ids {
		line, errMarshal := json.Marshal(buckets[id])
		if errMarshal != nil {
			continue
		}
		_ = o.keyWriter.writeLine(day, append(line, '\n'))
	}
}

func (o *observer) writeAccountBucketsLocked(start time.Time) {
	buckets := o.acctBuckets[start]
	if len(buckets) == 0 {
		return
	}
	authIDs := make([]string, 0, len(buckets))
	for authID := range buckets {
		authIDs = append(authIDs, authID)
	}
	sort.Strings(authIDs)
	day := start.Format("2006-01-02")
	for _, authID := range authIDs {
		source := buckets[authID]
		record := accountQuotaRecord{
			BucketStart: start,
			BucketEnd:   start.Add(o.bucket),
			AuthID:      source.AuthID,
			PlanType:    source.PlanType,
			ActiveLimit: source.ActiveLimit,
			Samples:     source.Samples,
			Credits:     source.credits,
			Windows:     make([]windowReport, 0, len(source.windows)),
			Scopes:      make([]scopeReport, 0, len(source.scopes)),
		}
		for _, sample := range source.windows {
			record.Windows = append(record.Windows, windowReport{
				Scope:             sample.Scope,
				LimitName:         sample.LimitName,
				DisplayName:       sample.DisplayName,
				Window:            sample.Window,
				WindowMinutes:     sample.WindowMinutes,
				First:             sample.First,
				Last:              sample.Last,
				Min:               sample.Min,
				Max:               sample.Max,
				Delta:             sample.Last - sample.First,
				Samples:           sample.Samples,
				FirstAt:           sample.FirstAt,
				LastAt:            sample.LastAt,
				ResetAt:           sample.ResetAt,
				ResetAtUnix:       sample.ResetAtUnix,
				ResetAfterSeconds: sample.ResetAfterSeconds,
				Allowed:           sample.Allowed,
				LimitReached:      sample.LimitReached,
				ResetSeen:         sample.ResetSeen,
			})
		}
		sort.Slice(record.Windows, func(i, j int) bool {
			if record.Windows[i].Scope != record.Windows[j].Scope {
				return record.Windows[i].Scope < record.Windows[j].Scope
			}
			if record.Windows[i].LimitName != record.Windows[j].LimitName {
				return record.Windows[i].LimitName < record.Windows[j].LimitName
			}
			return record.Windows[i].Window < record.Windows[j].Window
		})
		for _, sample := range source.scopes {
			record.Scopes = append(record.Scopes, scopeReport{
				Scope:        sample.Scope,
				LimitName:    sample.LimitName,
				DisplayName:  sample.DisplayName,
				Allowed:      sample.Allowed,
				LimitReached: sample.LimitReached,
			})
		}
		sort.Slice(record.Scopes, func(i, j int) bool {
			if record.Scopes[i].Scope != record.Scopes[j].Scope {
				return record.Scopes[i].Scope < record.Scopes[j].Scope
			}
			return record.Scopes[i].LimitName < record.Scopes[j].LimitName
		})
		line, errMarshal := json.Marshal(record)
		if errMarshal != nil {
			continue
		}
		_ = o.acctWriter.writeLine(day, append(line, '\n'))
	}
}

func (o *observer) pruneLocked() {
	if o.retentionDays <= 0 {
		return
	}
	now := time.Now().In(o.loc)
	day := now.Format("2006-01-02")
	if day == o.lastPruneDay {
		return
	}
	o.lastPruneDay = day
	pruneOwnedFiles(o.dir, now.AddDate(0, 0, -o.retentionDays))
}
