package usagehistory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const schema = `
PRAGMA journal_mode = DELETE;
PRAGMA busy_timeout = 5000;
CREATE TABLE IF NOT EXISTS history_meta (key TEXT PRIMARY KEY, value INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS history_request (
  id TEXT PRIMARY KEY,
  at INTEGER NOT NULL,
  client_id TEXT NOT NULL,
  provider TEXT NOT NULL,
  model TEXT NOT NULL,
  outcome TEXT NOT NULL,
  status_code INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS history_request_at ON history_request (at DESC, id DESC);
CREATE INDEX IF NOT EXISTS history_request_client ON history_request (client_id, at DESC);
CREATE TABLE IF NOT EXISTS history_tokens (
  request_id TEXT NOT NULL,
  at INTEGER NOT NULL,
  provider TEXT NOT NULL,
  input_tokens INTEGER,
  output_tokens INTEGER,
  cached_tokens INTEGER
);
CREATE INDEX IF NOT EXISTS history_tokens_request ON history_tokens (request_id);
CREATE INDEX IF NOT EXISTS history_tokens_at ON history_tokens (at);
`

type Store struct {
	db              *sql.DB
	coverageStart   time.Time
	incompleteSince atomic.Int64
	stop            chan struct{}
	done            chan struct{}
	closeOnce       sync.Once
	closeErr        error
}

type ImportedUsage struct {
	ID                                      string
	At                                      time.Time
	ClientID, Provider, Model               string
	InputTokens, OutputTokens, CachedTokens *int64
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store, err := openStore(db)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return store, nil
}

func openStore(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("initialize usage history: %w", err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO history_meta VALUES ('coverage_start', ?)`, time.Now().UnixMilli()); err != nil {
		return nil, err
	}
	var started, incomplete int64
	if err := db.QueryRow(`SELECT value FROM history_meta WHERE key = 'coverage_start'`).Scan(&started); err != nil {
		return nil, err
	}
	if err := db.QueryRow(`SELECT COALESCE((SELECT value FROM history_meta WHERE key = 'incomplete_since'), 0)`).Scan(&incomplete); err != nil {
		return nil, err
	}
	s := &Store{db: db, coverageStart: time.UnixMilli(started).UTC(), stop: make(chan struct{}), done: make(chan struct{})}
	s.incompleteSince.Store(incomplete)
	if err := s.prune(time.Now()); err != nil {
		return nil, err
	}
	go s.retention()
	return s, nil
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		close(s.stop)
		<-s.done
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}

func (s *Store) retention() {
	defer close(s.done)
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case now := <-ticker.C:
			if err := s.prune(now); err != nil {
				log.WithError(err).Warn("usage history: retention cleanup failed")
			}
		}
	}
}

func (s *Store) prune(now time.Time) error {
	cutoff := now.UTC().AddDate(0, -13, 0).UnixMilli()
	_, err := s.db.Exec(`DELETE FROM history_tokens WHERE at < ?;`, cutoff)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM history_request WHERE at < ?;`, cutoff)
	return err
}

func providerID(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	provider = strings.TrimPrefix(provider, "openai-compatible-")
	if provider == "glm-coding-plan" {
		return "glm"
	}
	return provider
}

func (s *Store) RecordRequest(result usage.HistoryResult) {
	_, err := s.db.Exec(`INSERT INTO history_request VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
		"live:"+result.ID, result.At.UnixMilli(), result.ClientID, providerID(result.Provider), result.Model, result.Outcome, result.StatusCode)
	s.recordHealth(result.At, err)
}

func (s *Store) RecordTokens(id string, at time.Time, record usage.Record) {
	detail := record.Detail
	input := reported(detail.InputTokens, detail.TokenFields, usage.ReportedInput)
	output := reported(detail.OutputTokens, detail.TokenFields, usage.ReportedOutput)
	cached := reported(max(detail.CacheReadTokens, detail.CachedTokens), detail.TokenFields, usage.ReportedCacheRead)
	if input == nil && output == nil && cached == nil {
		return
	}
	_, err := s.db.Exec(`INSERT INTO history_tokens VALUES (?, ?, ?, ?, ?, ?)`,
		"live:"+id, at.UnixMilli(), providerID(record.Provider), input, output, cached)
	s.recordHealth(at, err)
}

func reported(n int64, fields usage.ReportedTokenFields, flag usage.ReportedTokenFields) *int64 {
	if n > 0 || (n == 0 && fields&flag != 0) {
		return &n
	}
	return nil
}

func (s *Store) recordHealth(at time.Time, err error) {
	if err != nil {
		if s.incompleteSince.CompareAndSwap(0, at.UnixMilli()) {
			log.WithError(err).Error("usage history: recording incomplete")
		}
		return
	}
	if since := s.incompleteSince.Load(); since != 0 {
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO history_meta VALUES ('incomplete_since', ?)`, since); err != nil {
			log.WithError(err).Debug("usage history: could not save coverage status")
		}
	}
}

func (s *Store) Import(ctx context.Context, entries []ImportedUsage) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			log.WithError(err).Warn("usage history: rollback failed")
		}
	}()
	cutoff := time.Now().UTC().AddDate(0, -13, 0)
	for _, entry := range entries {
		if entry.ID == "" || entry.ClientID == "" || entry.Provider == "" || entry.At.Before(cutoff) || !entry.At.Before(s.coverageStart) ||
			(entry.InputTokens == nil && entry.OutputTokens == nil && entry.CachedTokens == nil) {
			continue
		}
		provider := providerID(entry.Provider)
		id := "import:" + provider + ":" + entry.ID
		result, err := tx.ExecContext(ctx, `INSERT INTO history_request VALUES (?, ?, ?, ?, ?, 'imported', 0) ON CONFLICT(id) DO NOTHING`,
			id, entry.At.UnixMilli(), entry.ClientID, provider, entry.Model)
		if err != nil {
			return err
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if inserted != 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO history_tokens VALUES (?, ?, ?, ?, ?, ?)`, id, entry.At.UnixMilli(), provider,
				entry.InputTokens, entry.OutputTokens, entry.CachedTokens); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
