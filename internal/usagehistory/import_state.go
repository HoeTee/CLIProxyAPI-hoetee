package usagehistory

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Store) Imported(ctx context.Context, source string) (bool, error) {
	var marker int
	err := s.db.QueryRowContext(ctx, "SELECT value FROM history_meta WHERE key = ?", "imported:"+source).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) MarkImported(ctx context.Context, source string) error {
	_, err := s.db.ExecContext(ctx, "INSERT OR IGNORE INTO history_meta (key, value) VALUES (?, 1)", "imported:"+source)
	return err
}
