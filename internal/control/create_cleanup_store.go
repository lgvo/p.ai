package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func (s *Store) BeginCreateCleanup(ctx context.Context, req DiscardRequest, ev CreateCleanupEvidence, verify func(context.Context) error) (Operation, error) {
	p := ev.Review
	if req.Key == "" || len(req.Key) > 128 || !validUUID(req.UUID) || !validFingerprint(req.TokenSHA256) || !validUUID(ev.InstanceUUID) || req.UUID != p.UUID || !p.Eligible || p.ConfirmationToken != "" || !validUUID(p.OldOperationID) || !validFingerprint(p.OldEvidenceSHA256) || !validFingerprint(p.PolicySHA256) || !validFingerprint(p.ImageFingerprint) || !p.AssignedBranch.Observed || !p.AssignedBranch.Exists || !validOID(p.AssignedBranch.OID) || verify == nil {
		return Operation{}, ErrInvalid
	}
	expiry, err := time.Parse(time.RFC3339Nano, p.ExpiresAt)
	if err != nil {
		return Operation{}, ErrInvalid
	}
	if err = s.lockGitAuthority(ctx); err != nil {
		return Operation{}, err
	}
	defer s.gitAuthority.Unlock()
	raw, _ := json.Marshal(req)
	if prior, e := s.GetOperationByKey(ctx, req.Key); e == nil {
		if prior.Kind != "session.create.cleanup" || digest(prior.Request) != digest(raw) {
			return Operation{}, ErrConflict
		}
		return prior, nil
	} else if !errors.Is(e, ErrNotFound) {
		return Operation{}, e
	}
	if !time.Now().Before(expiry) {
		return Operation{}, ErrConflict
	}
	if err = verify(ctx); err != nil {
		return Operation{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback()
	old, err := operationByIDTx(ctx, tx, p.OldOperationID)
	if err != nil {
		return Operation{}, err
	}
	var request ReserveSessionRequest
	oldEv, e := Evidence(old)
	if e != nil || old.Kind != "session.create" || old.Status != "blocked" || old.SessionUUID != p.UUID || old.Project != p.OldRequest.Project || old.Phase != p.OldPhase || digest(old.Evidence) != p.OldEvidenceSHA256 || json.Unmarshal(old.Request, &request) != nil || request != p.OldRequest || !SafeCreateCleanupEvidence(request, oldEv) || !SafeCreateCleanupPhase(old) || oldEv.PolicySHA256 != p.PolicySHA256 || oldEv.ImageFingerprint != p.ImageFingerprint {
		return Operation{}, ErrConflict
	}
	var registry, branch, policy string
	if err = tx.QueryRowContext(ctx, `SELECT registry_state,branch,policy_sha256 FROM sessions WHERE uuid=? AND project_path=?`, p.UUID, old.Project).Scan(&registry, &branch, &policy); err != nil || registry != "creating" || branch != request.Branch || p.AssignedBranch.Ref != "refs/heads/"+branch || policy != p.PolicySHA256 {
		return Operation{}, ErrConflict
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM git_ref_guards WHERE project_path=? AND branch=?`, old.Project, branch).Scan(&count); err != nil || count != 0 {
		return Operation{}, ErrConflict
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM git_principals WHERE session_uuid=?`, p.UUID).Scan(&count); err != nil {
		return Operation{}, err
	}
	local := p.Provisional.Cleanup
	if local == nil {
		if count != 0 {
			return Operation{}, ErrConflict
		}
	} else {
		if !local.Valid() || local.Completed || local.OldUUID != p.UUID || local.OldOperationID != old.ID || local.OldImageFingerprint != p.ImageFingerprint || count != 1 {
			return Operation{}, ErrConflict
		}
		var fp, role, project string
		var active int
		if err = tx.QueryRowContext(ctx, `SELECT fingerprint,role,project_path,active FROM git_principals WHERE session_uuid=?`, p.UUID).Scan(&fp, &role, &project, &active); err != nil || fp != local.KeyFingerprint || role != "session" || project != old.Project || active != 1 {
			return Operation{}, ErrConflict
		}
	}
	if !time.Now().Before(expiry) {
		return Operation{}, ErrConflict
	}
	id, err := newUUID()
	if err != nil {
		return Operation{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET status='superseded',phase='superseded',diagnostic=?,updated_at=? WHERE id=? AND status='blocked'`, "cleanup by "+id, now, old.ID); err != nil {
		return Operation{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sessions SET registry_state='removing' WHERE uuid=? AND registry_state='creating'`, p.UUID); err != nil {
		return Operation{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE git_principals SET active=0 WHERE session_uuid=? AND role='session'`, p.UUID); err != nil {
		return Operation{}, err
	}
	evidence, _ := json.Marshal(ev)
	if len(evidence) > 16384 {
		return Operation{}, ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,committed,evidence_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'running','local-cleanup',1,?,?,?)`, id, req.Key, "session.create.cleanup", old.Project, p.UUID, string(raw), digest(raw), string(evidence), now, now); err != nil {
		return Operation{}, classifyWrite(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO git_ref_guards(project_path,branch,operation_id) VALUES(?,?,?)`, old.Project, branch, id); err != nil {
		return Operation{}, classifyWrite(err)
	}
	if err = tx.Commit(); err != nil {
		return Operation{}, err
	}
	return Operation{ID: id, Key: req.Key, Kind: "session.create.cleanup", Project: old.Project, SessionUUID: p.UUID, Request: raw, Status: "running", Phase: "local-cleanup", Committed: true, Evidence: evidence, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) CompleteCreateCleanup(ctx context.Context, id string, verify func(context.Context, CreateCleanupEvidence) error) error {
	if verify == nil {
		return ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	// Resource/Git readers may query this Store, so proof precedes the SQLite
	// transaction while the caller retains the session and Git authority locks.
	observed, err := s.GetOperation(ctx, id)
	if err != nil {
		return err
	}
	if observed.Status == "completed" && observed.Phase == "cleaned" {
		return nil
	}
	var ev CreateCleanupEvidence
	if observed.Kind != "session.create.cleanup" || observed.Status != "running" || observed.Phase != "local-complete" || json.Unmarshal(observed.Evidence, &ev) != nil || !ev.LocalComplete {
		return ErrConflict
	}
	if err = verify(ctx, ev); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := operationByIDTx(ctx, tx, id)
	if err != nil || current.Status != "running" || current.Phase != "local-complete" || digest(current.Evidence) != digest(observed.Evidence) {
		return ErrConflict
	}
	var registry, branch, owner string
	if err = tx.QueryRowContext(ctx, `SELECT registry_state,branch FROM sessions WHERE uuid=? AND project_path=?`, observed.SessionUUID, observed.Project).Scan(&registry, &branch); err != nil || registry != "removing" || branch != ev.Review.OldRequest.Branch {
		return ErrConflict
	}
	if err = tx.QueryRowContext(ctx, `SELECT operation_id FROM git_ref_guards WHERE project_path=? AND branch=?`, observed.Project, branch).Scan(&owner); err != nil || owner != id {
		return ErrConflict
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM git_principals WHERE session_uuid=? AND active=1`, observed.SessionUUID).Scan(&active); err != nil || active != 0 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM git_principals WHERE session_uuid=? AND active=0 AND role='session'`, observed.SessionUUID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE uuid=? AND registry_state='removing'`, observed.SessionUUID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM git_ref_guards WHERE operation_id=?`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET status='completed',phase='cleaned',diagnostic='',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	return tx.Commit()
}

func operationByIDTx(ctx context.Context, tx *sql.Tx, id string) (Operation, error) {
	var key string
	if err := tx.QueryRowContext(ctx, `SELECT idempotency_key FROM operations WHERE id=?`, id).Scan(&key); err != nil {
		return Operation{}, err
	}
	return getOperationTx(ctx, tx, key)
}
func monotonicCreateCleanupEvidence(oldRaw, newRaw json.RawMessage) error {
	var old, next CreateCleanupEvidence
	if json.Unmarshal(oldRaw, &old) != nil || json.Unmarshal(newRaw, &next) != nil {
		return ErrInvalid
	}
	if old.LocalComplete && !next.LocalComplete {
		return ErrConflict
	}
	old.LocalComplete, next.LocalComplete = false, false
	a, _ := json.Marshal(old)
	b, _ := json.Marshal(next)
	if digest(a) != digest(b) {
		return ErrConflict
	}
	return nil
}
