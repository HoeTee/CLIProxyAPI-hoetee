package usagehistory

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

type Filter struct {
	From, To                                            time.Time
	Provider, Client, Model, Outcome, RequestID, Bucket string
	Limit, Offset                                       int
}

type Coverage struct {
	CoverageStart   time.Time  `json:"coverage_start"`
	IncompleteSince *time.Time `json:"incomplete_since,omitempty"`
}

type Totals struct {
	Calls        int64  `json:"calls"`
	Succeeded    int64  `json:"succeeded"`
	Failed       int64  `json:"failed"`
	Rejected     int64  `json:"rejected"`
	Canceled     int64  `json:"canceled"`
	Imported     int64  `json:"imported"`
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
	CachedTokens *int64 `json:"cached_tokens"`
}

func (t *Totals) columns() []any {
	return []any{&t.Calls, &t.Succeeded, &t.Failed, &t.Rejected, &t.Canceled, &t.Imported, &t.InputTokens, &t.OutputTokens, &t.CachedTokens}
}

func (t *Totals) add(other Totals) {
	t.Calls += other.Calls
	t.Succeeded += other.Succeeded
	t.Failed += other.Failed
	t.Rejected += other.Rejected
	t.Canceled += other.Canceled
	t.Imported += other.Imported
	addTokens(&t.InputTokens, other.InputTokens)
	addTokens(&t.OutputTokens, other.OutputTokens)
	addTokens(&t.CachedTokens, other.CachedTokens)
}

func addTokens(total **int64, value *int64) {
	if value == nil {
		return
	}
	if *total == nil {
		initial := *value
		*total = &initial
		return
	}
	**total += *value
}

type ClientTotals struct {
	Provider string `json:"provider"`
	ClientID string `json:"client_id"`
	Totals
}

type TimeTotals struct {
	At string `json:"at"`
	Totals
}

type Summary struct {
	Coverage
	Totals   Totals         `json:"totals"`
	Clients  []ClientTotals `json:"clients"`
	Timeline []TimeTotals   `json:"timeline"`
}

type Row struct {
	At           time.Time `json:"at"`
	RequestID    string    `json:"request_id,omitempty"`
	ClientID     string    `json:"client_id"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model,omitempty"`
	Outcome      string    `json:"outcome"`
	StatusCode   int       `json:"status_code,omitempty"`
	UsageOnly    bool      `json:"usage_only,omitempty"`
	InputTokens  *int64    `json:"input_tokens"`
	OutputTokens *int64    `json:"output_tokens"`
	CachedTokens *int64    `json:"cached_tokens"`
}

type Requests struct {
	Coverage
	Rows   []Row `json:"rows"`
	Total  int64 `json:"total"`
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
}

func (s *Store) coverage() Coverage {
	start := s.coverageStart
	if cutoff := time.Now().UTC().AddDate(0, -13, 0); start.Before(cutoff) {
		start = cutoff
	}
	c := Coverage{CoverageStart: start}
	if since := s.incompleteSince.Load(); since != 0 {
		at := time.UnixMilli(since).UTC()
		c.IncompleteSince = &at
	}
	return c
}

func (f Filter) facts() (string, []any) {
	query := `WITH matched AS (
 SELECT * FROM history_request WHERE at >= ? AND at < ?`
	args := []any{f.From.UnixMilli(), f.To.UnixMilli()}
	for _, match := range []struct{ clause, value string }{
		{"client_id = ?", f.Client},
		{"instr(lower(model), lower(?)) > 0", f.Model},
		{"outcome = ?", f.Outcome},
		{"id = ?", requestKey(f.RequestID)},
	} {
		if match.value != "" {
			query += " AND " + match.clause
			args = append(args, match.value)
		}
	}
	query += `
), facts AS (
 SELECT id, at, client_id, provider, model, outcome, status_code, 1 AS primary_row,
        NULL AS input_tokens, NULL AS output_tokens, NULL AS cached_tokens FROM matched
 UNION ALL
 SELECT r.id, r.at, r.client_id, COALESCE(NULLIF(t.provider,''),r.provider), r.model, r.outcome, r.status_code, 0,
        t.input_tokens, t.output_tokens, t.cached_tokens
 FROM matched r JOIN history_tokens t ON t.request_id = r.id
), filtered AS (SELECT * FROM facts`
	if f.Provider != "" {
		query += " WHERE provider = ?"
		args = append(args, providerID(f.Provider))
	}
	return query + ") ", args
}

func requestKey(id string) string {
	if id == "" {
		return ""
	}
	return "live:" + id
}

const sumColumns = `COUNT(*) FILTER (WHERE primary_row = 1 AND outcome != 'imported'),
COUNT(*) FILTER (WHERE primary_row = 1 AND outcome = 'succeeded'),
COUNT(*) FILTER (WHERE primary_row = 1 AND outcome = 'failed'),
COUNT(*) FILTER (WHERE primary_row = 1 AND outcome = 'rejected'),
COUNT(*) FILTER (WHERE primary_row = 1 AND outcome = 'canceled'),
COUNT(*) FILTER (WHERE primary_row = 1 AND outcome = 'imported'),
SUM(input_tokens), SUM(output_tokens), SUM(cached_tokens)`

func (s *Store) Summary(ctx context.Context, f Filter) (Summary, error) {
	result := Summary{Coverage: s.coverage(), Clients: []ClientTotals{}, Timeline: []TimeTotals{}}
	query, args := f.facts()
	format := "%Y-%m-%dT00:00:00+08:00"
	switch f.Bucket {
	case "hour":
		format = "%Y-%m-%dT%H:00:00+08:00"
	case "month":
		format = "%Y-%m-01T00:00:00+08:00"
	}
	type clientKey struct{ provider, id string }
	clients := make(map[clientKey]*Totals)
	timeline := make(map[string]*Totals)
	const buckets = `SELECT provider, client_id, strftime(?, at / 1000, 'unixepoch', '+8 hours') AS bucket_at, ` + sumColumns + `
FROM filtered GROUP BY provider, client_id, bucket_at`
	err := eachRow(ctx, s.db, query+buckets, append(args, format), func(rows *sql.Rows) error {
		var client clientKey
		var at string
		var counts Totals
		if err := rows.Scan(append([]any{&client.provider, &client.id, &at}, counts.columns()...)...); err != nil {
			return err
		}
		if clients[client] == nil {
			clients[client] = &Totals{}
		}
		if timeline[at] == nil {
			timeline[at] = &Totals{}
		}
		clients[client].add(counts)
		timeline[at].add(counts)
		result.Totals.add(counts)
		return nil
	})
	if err != nil {
		return result, err
	}
	for client, totals := range clients {
		result.Clients = append(result.Clients, ClientTotals{Provider: client.provider, ClientID: client.id, Totals: *totals})
	}
	slices.SortFunc(result.Clients, func(a, b ClientTotals) int {
		if byProvider := strings.Compare(a.Provider, b.Provider); byProvider != 0 {
			return byProvider
		}
		return strings.Compare(a.ClientID, b.ClientID)
	})
	for at, totals := range timeline {
		result.Timeline = append(result.Timeline, TimeTotals{At: at, Totals: *totals})
	}
	slices.SortFunc(result.Timeline, func(a, b TimeTotals) int { return strings.Compare(a.At, b.At) })
	return result, nil
}

func (s *Store) Requests(ctx context.Context, f Filter) (Requests, error) {
	result := Requests{Coverage: s.coverage(), Rows: []Row{}, Limit: f.Limit, Offset: f.Offset}
	query, args := f.facts()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer rollbackQuery(tx)
	const page = `SELECT id, at, client_id, provider, model, outcome, status_code, MAX(primary_row),
SUM(input_tokens), SUM(output_tokens), SUM(cached_tokens), COUNT(*) OVER ()
FROM filtered GROUP BY id, provider ORDER BY at DESC, id DESC, provider LIMIT ? OFFSET ?`
	err = eachRow(ctx, tx, query+page, append(args, f.Limit, f.Offset), func(rows *sql.Rows) error {
		var item Row
		var key string
		var at int64
		var primary int
		if err := rows.Scan(&key, &at, &item.ClientID, &item.Provider, &item.Model, &item.Outcome, &item.StatusCode, &primary,
			&item.InputTokens, &item.OutputTokens, &item.CachedTokens, &result.Total); err != nil {
			return err
		}
		item.At = time.UnixMilli(at).UTC()
		item.UsageOnly = primary == 0
		item.RequestID, _ = strings.CutPrefix(key, "live:")
		if item.Outcome == "imported" {
			item.RequestID = ""
		}
		result.Rows = append(result.Rows, item)
		return nil
	})
	if err != nil {
		return result, err
	}
	if len(result.Rows) == 0 && f.Offset != 0 {
		if err := tx.QueryRowContext(ctx, query+`SELECT COUNT(*) FROM (SELECT 1 FROM filtered GROUP BY id, provider)`, args...).Scan(&result.Total); err != nil {
			return result, err
		}
	}
	return result, tx.Commit()
}

type rowQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func eachRow(ctx context.Context, db rowQueryer, query string, args []any, scan func(*sql.Rows) error) error {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() {
		if err := rows.Close(); err != nil {
			log.WithError(err).Debug("usage history: close query rows")
		}
	}()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func rollbackQuery(tx *sql.Tx) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		log.WithError(err).Warn("usage history: rollback query")
	}
}
