package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

// BeginSessionChange records a start or stop, kind, of established session
// id. While an operation of the same kind is unfinished it records nothing and
// returns that operation with recorded false; any other unfinished operation
// on the session refuses it. Recording deletes the session's finished start
// and stop operations, so a session keeps only its newest one. A start or
// stop takes no idempotency key: its key column holds its own ID.
func (s *Store) BeginSessionChange(ctx context.Context, kind, id string) (op api.Operation, recorded bool, err error) {
	if kind != KindSessionStart && kind != KindSessionStop || !validUUID(id) {
		return api.Operation{}, false, ErrInvalid
	}
	j, err := s.beginJournal(ctx)
	if err != nil {
		return api.Operation{}, false, err
	}
	defer j.Rollback()
	var project, branch, registry string
	err = j.QueryRowContext(ctx, `SELECT project_path,branch,registry_state FROM sessions WHERE uuid=?`, id).Scan(&project, &branch, &registry)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Operation{}, false, NotFound(api.ReasonSession, api.TargetSession(id))
	}
	if err != nil {
		return api.Operation{}, false, err
	}
	if registry != "established" {
		return api.Operation{}, false, Conflict(api.ReasonSessionNotEstablished, api.TargetSession(id))
	}
	var unfinished string
	err = j.QueryRowContext(ctx, `SELECT id FROM operations WHERE session_uuid=? AND kind=? AND status IN ('running','blocked')`, id, kind).Scan(&unfinished)
	if err == nil {
		op, err = operationByIDTx(ctx, j.Tx, unfinished)
		return op, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return api.Operation{}, false, err
	}
	if err = sessionHolderTx(ctx, j, id, ""); err != nil {
		return api.Operation{}, false, err
	}
	if _, err = j.ExecContext(ctx, `DELETE FROM operations WHERE session_uuid=? AND kind IN (?,?) AND status IN ('completed','failed','superseded')`,
		id, KindSessionStart, KindSessionStop); err != nil {
		return api.Operation{}, false, err
	}
	opID, err := newUUID()
	if err != nil {
		return api.Operation{}, false, err
	}
	request, err := json.Marshal(api.SessionStartRequest{UUID: id})
	if err != nil {
		return api.Operation{}, false, err
	}
	phase := "starting"
	if kind == KindSessionStop {
		phase = "stopping"
	}
	rec := operationRecord{ID: opID, Key: opID, Kind: kind, Project: project, Session: id, Request: request,
		Intent: api.OperationIntent{Project: project, Session: id, Branch: branch}, Phase: phase, Now: time.Now().UTC().Format(time.RFC3339Nano)}
	if err = j.insert(ctx, rec); err != nil {
		return api.Operation{}, false, classifyWrite(err)
	}
	if err = j.commit(ctx); err != nil {
		return api.Operation{}, false, err
	}
	return rec.operation(), true, nil
}

// LatestSessionChange is the start or stop of session id the journal keeps:
// the unfinished one while there is one, otherwise the newest finished one.
// It reads in one transaction, since recording the next one deletes it.
func (s *Store) LatestSessionChange(ctx context.Context, id string) (api.Operation, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return api.Operation{}, false, err
	}
	defer tx.Rollback()
	var opID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM operations WHERE session_uuid=? AND kind IN (?,?) ORDER BY seq DESC LIMIT 1`,
		id, KindSessionStart, KindSessionStop).Scan(&opID)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Operation{}, false, nil
	}
	if err != nil {
		return api.Operation{}, false, err
	}
	op, err := operationByIDTx(ctx, tx, opID)
	return op, err == nil, err
}
