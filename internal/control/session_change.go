package control

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lgvo/p.ai/internal/api"
)

func validUUID(value string) bool { return api.ValidUUID(value) }

// sessionHolderTx refuses a new operation on session while an unfinished one
// holds it: retry while that operation runs, conflict once it is blocked.
// Both name it.
func sessionHolderTx(ctx context.Context, q queryRower, session string) error {
	var id, status string
	err := q.QueryRowContext(ctx, `SELECT id,status FROM operations WHERE session_uuid=? AND status IN ('running','blocked','unknown') ORDER BY id LIMIT 1`, session).Scan(&id, &status)
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

// ActiveSessionChange returns the unfinished cleanup, discard, delete or
// rename of a session. While one exists, the session is not started, stopped,
// attached or changed by anything else.
func (s *Store) ActiveSessionChange(ctx context.Context, sessionUUID string) (api.Operation, bool, error) {
	if !validUUID(sessionUUID) {
		return api.Operation{}, false, ErrInvalid
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM operations WHERE session_uuid=? AND kind IN ('session.create.cleanup','session.discard','session.delete','session.rename')
	 AND status IN ('running','blocked','unknown')`, sessionUUID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Operation{}, false, nil
	}
	if err != nil {
		return api.Operation{}, false, err
	}
	op, err := s.GetOperation(ctx, id)
	return op, err == nil, err
}
