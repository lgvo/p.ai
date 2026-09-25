package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// WorkspaceInspectEvidence is the accepted native identity and quiescence
// intent. A helper is always named from the durable operation UUID, never
// from a repository name or an Incus observation.
type WorkspaceInspectEvidence struct {
	InstanceUUID     string          `json:"instance_uuid"`
	ImageFingerprint string          `json:"image_fingerprint"`
	BaseFingerprint  string          `json:"base_fingerprint"`
	SourceIncusUUID  string          `json:"source_incus_uuid,omitempty"`
	SourceGeneration string          `json:"source_generation,omitempty"`
	OriginalStatus   string          `json:"original_status"`
	Result           json.RawMessage `json:"result,omitempty"`
}

type WorkspaceInspectRequest struct {
	Key         string `json:"key"`
	SessionUUID string `json:"session_uuid"`
}

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

func (s *Store) BeginWorkspaceInspect(ctx context.Context, req WorkspaceInspectRequest, ev WorkspaceInspectEvidence) (Operation, error) {
	return s.beginWorkspaceRead(ctx, "workspace.inspect", req, ev)
}

func (s *Store) BeginWorkspaceLossInspect(ctx context.Context, req WorkspaceInspectRequest, ev WorkspaceInspectEvidence) (Operation, error) {
	return s.beginWorkspaceRead(ctx, "workspace.loss.inspect", req, ev)
}

func (s *Store) beginWorkspaceRead(ctx context.Context, kind string, req WorkspaceInspectRequest, ev WorkspaceInspectEvidence) (Operation, error) {
	if kind != "workspace.inspect" && kind != "workspace.loss.inspect" {
		return Operation{}, ErrInvalid
	}
	if len(req.Key) == 0 || len(req.Key) > 128 || !validUUID(req.SessionUUID) || !validUUID(ev.InstanceUUID) ||
		!validFingerprint(ev.ImageFingerprint) || !validFingerprint(ev.BaseFingerprint) || ev.OriginalStatus != "Running" && ev.OriginalStatus != "Stopped" || len(ev.Result) != 0 {
		return Operation{}, ErrInvalid
	}
	if kind == "workspace.loss.inspect" && (!validUUID(ev.SourceIncusUUID) || !validUUID(ev.SourceGeneration)) {
		return Operation{}, ErrInvalid
	}
	request, _ := json.Marshal(req)
	evidence, _ := json.Marshal(ev)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback()
	var oldKind, oldHash string
	err = tx.QueryRowContext(ctx, `SELECT kind,request_sha256 FROM operations WHERE idempotency_key=?`, req.Key).Scan(&oldKind, &oldHash)
	if err == nil {
		if oldKind != kind || oldHash != digest(request) {
			return Operation{}, ErrConflict
		}
		return getOperationTx(ctx, tx, req.Key)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, err
	}
	var project string
	err = tx.QueryRowContext(ctx, `SELECT project_path FROM sessions WHERE uuid=? AND registry_state='established'`, req.SessionUUID).Scan(&project)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, err
	}
	id, err := newUUID()
	if err != nil {
		return Operation{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,evidence_json,created_at,updated_at)
	 VALUES(?,?,?,?,?, ?,?,'running','helper-intent',?,?,?)`, id, req.Key, kind, project, req.SessionUUID, string(request), digest(request), string(evidence), now, now)
	if err != nil {
		return Operation{}, classifyWrite(err)
	}
	if err = tx.Commit(); err != nil {
		return Operation{}, err
	}
	return Operation{ID: id, Key: req.Key, Kind: kind, Project: project, SessionUUID: req.SessionUUID,
		Request: request, Status: "running", Phase: "helper-intent", Evidence: evidence, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) ActiveWorkspaceInspect(ctx context.Context, sessionUUID string) (Operation, bool, error) {
	if !validUUID(sessionUUID) {
		return Operation{}, false, ErrInvalid
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM operations WHERE session_uuid=? AND kind IN ('workspace.inspect','workspace.loss.inspect','session.discard','session.delete','session.rename','session.repair','session.repair.prepare','session.ref.repair','session.principal.repair','session.record.repair')
	 AND status IN ('running','blocked','unknown')`, sessionUUID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, false, nil
	}
	if err != nil {
		return Operation{}, false, err
	}
	op, err := s.GetOperation(ctx, id)
	return op, err == nil, err
}

func (s *Store) UnfinishedWorkspaceInspects(ctx context.Context) ([]Operation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM operations WHERE kind IN ('workspace.inspect','workspace.loss.inspect') AND status IN ('running','blocked','unknown') ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Operation, 0, len(ids))
	for _, id := range ids {
		op, e := s.GetOperation(ctx, id)
		if e != nil {
			return nil, e
		}
		out = append(out, op)
	}
	return out, nil
}
