package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/glmquota"
)

const historyHours = 24

var beijing = time.FixedZone("Asia/Shanghai", 8*60*60)

type event struct {
	Client          string    `json:"client"`
	At              time.Time `json:"at"`
	Usage           *Usage    `json:"usage"`
	EstimatedPoints *float64  `json:"estimated_points,omitempty"`
}

type Usage = glmquota.Usage
type Window = glmquota.Window
type Hour = glmquota.Hour
type Snapshot = glmquota.Snapshot
type QuotaError = glmquota.QuotaError

type clientUsage struct {
	fiveHour Window
	weekly   Window
	hours    [historyHours]Hour
}

type Ledger struct {
	mu          sync.Mutex
	path        string
	initialized bool
	failed      error
	clients     map[string]*clientUsage
	now         func() time.Time
	pricing     *pointPricing
}

func NewLedger(path string) *Ledger {
	return &Ledger{path: path, clients: make(map[string]*clientUsage), now: time.Now}
}

// Admit checks settled usage. Already admitted requests may exceed the limit.
func (l *Ledger) Admit(client string, policy *config.GLMClientQuota) error {
	if client == "" || policy == nil {
		return QuotaError{Message: "GLM client quota is not configured", Status: 403}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.load(); err != nil {
		return QuotaError{Message: "GLM usage ledger unavailable", Status: 503}
	}
	five, week := l.windows(client, l.now())
	if policy.FiveHourTokens != nil && five.Used >= *policy.FiveHourTokens {
		return QuotaError{Message: "GLM local 5-hour token quota exhausted", Status: 429}
	}
	if policy.WeeklyTokens != nil && week.Used >= *policy.WeeklyTokens {
		return QuotaError{Message: "GLM local weekly token quota exhausted", Status: 429}
	}
	return nil
}

func (l *Ledger) Snapshot(client string, now time.Time) (Snapshot, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.load(); err != nil {
		return Snapshot{}, err
	}
	five, week := l.windows(client, now)
	snapshot := Snapshot{FiveHour: five, Weekly: week, Hours: make([]Hour, historyHours)}
	state := l.clients[client]
	last := now.Truncate(time.Hour)
	for i := range snapshot.Hours {
		start := last.Add(time.Duration(i+1-historyHours) * time.Hour)
		snapshot.Hours[i].Start = start
		if state != nil {
			hour := state.hours[hourSlot(start)]
			if hour.Start.Equal(start) {
				snapshot.Hours[i] = hour
			}
		}
	}
	return snapshot, nil
}

// Record appends one terminal entry per upstream attempt. A nil usage records an unknown amount.
func (l *Ledger) Record(client string, usage *Usage) error {
	if client == "" || !validUsage(usage) {
		return errors.New("invalid GLM usage ledger entry")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.load(); err != nil {
		return err
	}
	entry := event{Client: client, At: l.now().UTC(), Usage: usage, EstimatedPoints: l.pricing.estimate(usage)}
	err := withFile(l.path, os.O_WRONLY|os.O_APPEND, func(file *os.File) error {
		if errEncode := json.NewEncoder(file).Encode(entry); errEncode != nil {
			return errEncode
		}
		return file.Sync()
	})
	if err != nil {
		l.failed = err
		return err
	}
	l.apply(entry)
	return nil
}

func (l *Ledger) load() error {
	if l.initialized {
		return l.failed
	}
	l.initialized = true
	if err := os.MkdirAll(filepath.Dir(l.path), 0700); err != nil {
		l.failed = err
		return err
	}
	l.failed = withFile(l.path, os.O_CREATE|os.O_RDWR, func(file *os.File) error {
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			var entry event
			if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil || entry.Client == "" || entry.At.IsZero() {
				return errors.New("invalid GLM usage ledger entry")
			}
			if !validUsage(entry.Usage) {
				return errors.New("invalid GLM usage ledger token counts")
			}
			if points := entry.EstimatedPoints; points != nil && (*points < 0 || math.IsNaN(*points) || math.IsInf(*points, 0)) {
				return errors.New("invalid GLM usage ledger point estimate")
			}
			l.apply(entry)
		}
		return scanner.Err()
	})
	return l.failed
}

func validUsage(u *Usage) bool {
	return u == nil || (u.Input >= 0 && u.Output >= 0 && u.Input <= math.MaxInt64-u.Output &&
		(u.Cached == nil || (*u.Cached >= 0 && *u.Cached <= u.Input)))
}

func withFile(path string, flags int, action func(*os.File) error) (err error) {
	file, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return fmt.Errorf("open GLM usage ledger: %w", err)
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			err = errors.Join(err, errClose)
		}
	}()
	return action(file)
}

func (l *Ledger) apply(entry event) {
	state := l.clients[entry.Client]
	if state == nil {
		state = &clientUsage{}
		l.clients[entry.Client] = state
	}
	if !entry.At.Before(state.fiveHour.ResetAt) {
		state.fiveHour = Window{ResetAt: entry.At.Add(5 * time.Hour)}
	}
	weeklyReset := nextWeek(entry.At)
	if !state.weekly.ResetAt.Equal(weeklyReset) {
		state.weekly = Window{ResetAt: weeklyReset}
	}
	for _, window := range []*Window{&state.fiveHour, &state.weekly} {
		if entry.EstimatedPoints == nil {
			window.Unpriced++
			continue
		}
		total := *entry.EstimatedPoints
		if window.EstimatedPoints != nil {
			total += *window.EstimatedPoints
		}
		window.EstimatedPoints = &total
	}
	start := entry.At.Truncate(time.Hour)
	hour := &state.hours[hourSlot(start)]
	if !hour.Start.Equal(start) {
		*hour = Hour{Start: start}
	}
	u := entry.Usage
	if u == nil {
		state.fiveHour.Unknown++
		state.weekly.Unknown++
		hour.Unknown++
		return
	}
	state.fiveHour.Used += u.Input + u.Output
	state.weekly.Used += u.Input + u.Output
	hour.Output += u.Output
	if u.Cached == nil {
		hour.CacheUnknown += u.Input
	} else {
		hour.CacheHit += *u.Cached
		hour.CacheMiss += u.Input - *u.Cached
	}
}

func (l *Ledger) windows(client string, now time.Time) (five, week Window) {
	week.ResetAt = nextWeek(now)
	if state := l.clients[client]; state != nil {
		if now.Before(state.fiveHour.ResetAt) {
			five = state.fiveHour
		}
		if week.ResetAt.Equal(state.weekly.ResetAt) {
			week = state.weekly
		}
	}
	return five, week
}

func nextWeek(now time.Time) time.Time {
	local := now.In(beijing)
	days := 7 - (int(local.Weekday())+6)%7
	return time.Date(local.Year(), local.Month(), local.Day()+days, 0, 0, 0, 0, beijing)
}

func hourSlot(start time.Time) int {
	return int((start.Unix()/3600%historyHours + historyHours) % historyHours)
}
