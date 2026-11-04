package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// PinSourceBackend records backend, the source driver's backend, when the
// daemon starts. A driver of another backend would find none of the
// instance's repositories, so it is refused while any project exists;
// with none, it replaces the pin. The driver's version is not pinned: a new
// version of the same backend keeps the repositories it finds.
func (s *Store) PinSourceBackend(ctx context.Context, backend string) error {
	if backend == "" || len(backend) > 128 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var pinned string
	err = tx.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='source_backend'`).Scan(&pinned)
	switch {
	case err == nil && pinned == backend:
		return nil
	case err == nil:
		var projects int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&projects); err != nil {
			return err
		}
		if projects > 0 {
			return fmt.Errorf("the source driver's backend is %q, but the instance's %d projects were made with %q: %w", backend, projects, pinned, ErrConflict)
		}
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES('source_backend',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, backend); err != nil {
		return err
	}
	return tx.Commit()
}
