package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

// WorkspaceInspectEvidence is the accepted native identity and quiescence
// intent. A helper is always named from the durable operation UUID, never
// from a repository name or an Incus observation.
type WorkspaceInspectEvidence struct {
	InstanceUUID          string          `json:"instance_uuid"`
	ImageFingerprint      string          `json:"image_fingerprint"`
	BaseFingerprint       string          `json:"base_fingerprint"`
	SourceIncusUUID       string          `json:"source_incus_uuid,omitempty"`
	SourceGeneration      string          `json:"source_generation,omitempty"`
	OriginalStatus        string          `json:"original_status"`
	CreatorOperationID    string          `json:"creator_operation_id,omitempty"`
	CreatorRequestSHA256  string          `json:"creator_request_sha256,omitempty"`
	CreatorEvidenceSHA256 string          `json:"creator_evidence_sha256,omitempty"`
	Result                json.RawMessage `json:"result,omitempty"`
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

func (s *Store) BeginWorkspaceLossInspect(ctx context.Context, req WorkspaceInspectRequest, ev WorkspaceInspectEvidence) (api.Operation, error) {
	if len(req.Key) == 0 || len(req.Key) > 128 || !validUUID(req.SessionUUID) || !validUUID(ev.InstanceUUID) ||
		!validFingerprint(ev.ImageFingerprint) || !validFingerprint(ev.BaseFingerprint) || ev.OriginalStatus != "Running" && ev.OriginalStatus != "Stopped" || len(ev.Result) != 0 {
		return api.Operation{}, ErrInvalid
	}
	if !validUUID(ev.SourceIncusUUID) || !validUUID(ev.SourceGeneration) {
		return api.Operation{}, ErrInvalid
	}
	if ev.CreatorOperationID != "" && (!validUUID(ev.CreatorOperationID) ||
		!validFingerprint(ev.CreatorRequestSHA256) || !validFingerprint(ev.CreatorEvidenceSHA256) || ev.OriginalStatus != "Stopped") ||
		ev.CreatorOperationID == "" && (ev.CreatorRequestSHA256 != "" || ev.CreatorEvidenceSHA256 != "") {
		return api.Operation{}, ErrInvalid
	}
	request, _ := json.Marshal(req)
	evidence, _ := json.Marshal(ev)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return api.Operation{}, err
	}
	defer tx.Rollback()
	if replay, err := keyRecordsRequest(ctx, tx, req.Key, KindWorkspaceLossInspect, request); err != nil {
		return api.Operation{}, err
	} else if replay {
		return getOperationTx(ctx, tx, req.Key)
	}
	session, err := getSessionTx(ctx, tx, req.SessionUUID)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Operation{}, ErrNotFound
	}
	if err != nil {
		return api.Operation{}, err
	}
	if ev.CreatorOperationID != "" {
		if err = validateWorkspaceCreatorTx(ctx, tx, session, ev); err != nil {
			return api.Operation{}, err
		}
	} else if session.Registry != "established" {
		return api.Operation{}, ErrConflict
	}
	project := session.Project
	id, err := newUUID()
	if err != nil {
		return api.Operation{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	err = insertOperation(ctx, tx, operationRecord{ID: id, Key: req.Key, Kind: KindWorkspaceLossInspect, Project: project, Session: req.SessionUUID, Request: request, Phase: "helper-intent", Evidence: evidence, Now: now})
	if err != nil {
		return api.Operation{}, classifyWrite(err)
	}
	if err = tx.Commit(); err != nil {
		return api.Operation{}, err
	}
	return api.Operation{ID: id, Key: req.Key, Kind: KindWorkspaceLossInspect, Project: project, SessionUUID: req.SessionUUID,
		Request: request, Status: "running", Phase: "helper-intent", Evidence: evidence, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) ActiveWorkspaceInspect(ctx context.Context, sessionUUID string) (api.Operation, bool, error) {
	if !validUUID(sessionUUID) {
		return api.Operation{}, false, ErrInvalid
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM operations WHERE session_uuid=? AND kind IN ('workspace.loss.inspect','session.create.cleanup','session.discard','session.delete','session.rename','session.repair','session.repair.prepare','session.ref.repair','session.principal.repair','session.record.repair')
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
