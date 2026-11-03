package control

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lgvo/p.ai/internal/api"
)

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, c := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if c < '0' || c > '9' && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
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
