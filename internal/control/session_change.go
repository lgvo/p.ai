package control

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lgvo/p.ai/internal/api"
)

func validUUID(value string) bool { return api.ValidUUID(value) }

// The journal's one admission check: work that would compete with an
// unfinished operation on the same session is refused, retry naming it while
// it runs, conflict once it is blocked and waits for Retry. Requests that act
// on that operation itself (a key replay, Retry, the replacement or cleanup of
// a blocked creation) name it as except and pass.

// sessionHolderTx refuses work on session while an unfinished operation other
// than except holds it: a running one is named before a blocked one.
func sessionHolderTx(ctx context.Context, q queryRower, session, except string) error {
	var id, status string
	err := q.QueryRowContext(ctx, `SELECT id,status FROM operations WHERE session_uuid=? AND id!=? AND status IN ('running','blocked')
	 ORDER BY status='running' DESC, seq LIMIT 1`, session, except).Scan(&id, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status == "running" {
		return Retry(api.ReasonSessionHeld, api.TargetOperation(id))
	}
	return Conflict(api.ReasonOperationBlocked, api.TargetOperation(id))
}

// AdmitSessionWork refuses work on session while an unfinished operation
// other than except holds it.
func (s *Store) AdmitSessionWork(ctx context.Context, session, except string) error {
	if !validUUID(session) {
		return ErrInvalid
	}
	return sessionHolderTx(ctx, s.db, session, except)
}
