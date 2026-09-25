package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type DiscardRequest struct {
	Key         string `json:"key"`
	UUID        string `json:"uuid"`
	TokenSHA256 string `json:"token_sha256"`
}

// DiscardEvidence contains only the authority needed to recompute and finish
// an already confirmed Discard. The displayed loss snapshot remains in the
// preview; it is not copied into the durable operation or sent to plugins.
type DiscardEvidence struct {
	Action             string `json:"action"`
	Project            string `json:"project"`
	Branch             string `json:"branch"`
	AssignedTip        string `json:"assigned_tip,omitempty"`
	PolicySHA256       string `json:"policy_sha256"`
	IncusProject       string `json:"incus_project"`
	InstanceName       string `json:"instance_name"`
	InstanceUUID       string `json:"instance_uuid"` // P instance, distinct from Incus generation
	ImageFingerprint   string `json:"image_fingerprint,omitempty"`
	BaseFingerprint    string `json:"base_fingerprint"`
	IncusUUID          string `json:"incus_uuid,omitempty"`
	Generation         string `json:"generation,omitempty"`
	OriginalStatus     string `json:"original_status,omitempty"`
	LossOperationID    string `json:"loss_operation_id,omitempty"`
	LossFingerprint    string `json:"loss_fingerprint,omitempty"`
	PRefsDigest        string `json:"p_refs_digest,omitempty"`
	AllowUnborn        bool   `json:"allow_unborn,omitempty"`
	PreviewExpiresAt   string `json:"preview_expires_at"`
	DeleteReviewSHA256 string `json:"delete_review_sha256,omitempty"`
	MissingRuntime     bool   `json:"missing_runtime,omitempty"`
	StaleNeedsThaw     bool   `json:"stale_needs_thaw,omitempty"`
}

func validDiscardEvidence(ev DiscardEvidence) bool {
	if (ev.Action != "discard" && ev.Action != "delete") || ev.Action == "delete" && !validFingerprint(ev.DeleteReviewSHA256) || ev.Action == "discard" && ev.DeleteReviewSHA256 != "" ||
		!validProject(ev.Project) || !validBranch(ev.Branch) || !validFingerprint(ev.PolicySHA256) ||
		ev.IncusProject == "" || ev.InstanceName == "" || !validUUID(ev.InstanceUUID) || !validFingerprint(ev.BaseFingerprint) ||
		(ev.AssignedTip != "" && !validOID(ev.AssignedTip)) {
		return false
	}
	if ev.MissingRuntime {
		return ev.IncusUUID == "" && ev.Generation == "" && ev.OriginalStatus == "" && ev.LossOperationID == "" && ev.LossFingerprint == ""
	}
	return validUUID(ev.IncusUUID) && validUUID(ev.Generation) && validFingerprint(ev.ImageFingerprint) &&
		(ev.OriginalStatus == "Running" || ev.OriginalStatus == "Stopped") && validUUID(ev.LossOperationID) && validFingerprint(ev.LossFingerprint)
}

func (s *Store) BeginDiscard(ctx context.Context, req DiscardRequest, ev DiscardEvidence) (Operation, error) {
	if ev.Action != "discard" {
		return Operation{}, ErrInvalid
	}
	return s.beginRemoval(ctx, req, ev)
}

func (s *Store) BeginDelete(ctx context.Context, req DiscardRequest, ev DiscardEvidence) (Operation, error) {
	if ev.Action != "delete" {
		return Operation{}, ErrInvalid
	}
	return s.beginRemoval(ctx, req, ev)
}

func (s *Store) beginRemoval(ctx context.Context, req DiscardRequest, ev DiscardEvidence) (Operation, error) {
	if len(req.Key) < 1 || len(req.Key) > 128 || !validUUID(req.UUID) || !validFingerprint(req.TokenSHA256) || !validDiscardEvidence(ev) {
		return Operation{}, ErrInvalid
	}
	expires, err := time.Parse(time.RFC3339Nano, ev.PreviewExpiresAt)
	if err != nil {
		return Operation{}, ErrInvalid
	}
	request, _ := json.Marshal(req)
	evidence, _ := json.Marshal(ev)
	if len(evidence) > 4096 {
		return Operation{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback()
	var kind, oldHash string
	err = tx.QueryRowContext(ctx, `SELECT kind,request_sha256 FROM operations WHERE idempotency_key=?`, req.Key).Scan(&kind, &oldHash)
	if err == nil {
		if kind != "session."+ev.Action || oldHash != digest(request) {
			return Operation{}, ErrConflict
		}
		return getOperationTx(ctx, tx, req.Key)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, err
	}
	var project, branch, registry, policy string
	err = tx.QueryRowContext(ctx, `SELECT project_path,branch,registry_state,policy_sha256 FROM sessions WHERE uuid=?`, req.UUID).Scan(&project, &branch, &registry, &policy)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, err
	}
	if project != ev.Project || branch != ev.Branch || policy != ev.PolicySHA256 || registry != "established" {
		return Operation{}, ErrConflict
	}
	// Check inside the admission transaction after any SQLite connection wait.
	// A confirmed intent cannot be accepted after its review token expired.
	if !time.Now().Before(expires) {
		return Operation{}, ErrConflict
	}
	id, err := newUUID()
	if err != nil {
		return Operation{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,committed,evidence_json,created_at,updated_at)
	 VALUES(?,?,?,?,?,?,?,'running','guard-pending',0,?,?,?)`, id, req.Key, "session."+ev.Action, ev.Project, req.UUID, string(request), digest(request), string(evidence), now, now)
	if err != nil {
		return Operation{}, classifyWrite(err)
	}
	if err = tx.Commit(); err != nil {
		return Operation{}, err
	}
	return Operation{ID: id, Key: req.Key, Kind: "session." + ev.Action, Project: ev.Project, SessionUUID: req.UUID, Request: request, Status: "running", Phase: "guard-pending", Evidence: evidence, CreatedAt: now, UpdatedAt: now}, nil
}

// CommitDiscard is the irreversible local authority boundary. The guarded
// branch already prevented receive-pack; this transaction disables both Git
// and session RPC authority before any Incus delete request is issued.
func (s *Store) CommitDiscard(ctx context.Context, opID string, verifyRefs func(context.Context, DiscardEvidence) error) error {
	return s.commitRemoval(ctx, opID, "discard", verifyRefs)
}

func (s *Store) CommitDelete(ctx context.Context, opID string, verifyRefs func(context.Context, DiscardEvidence) error) error {
	return s.commitRemoval(ctx, opID, "delete", verifyRefs)
}

func (s *Store) commitRemoval(ctx context.Context, opID, action string, verifyRefs func(context.Context, DiscardEvidence) error) error {
	if verifyRefs == nil {
		return ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sessionID, project, phase, status, evRaw string
	err = tx.QueryRowContext(ctx, `SELECT session_uuid,project_path,phase,status,evidence_json FROM operations WHERE id=? AND kind=?`, opID, "session."+action).Scan(&sessionID, &project, &phase, &status, &evRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if phase == "removal-committed" || phase == "stop-issued" || phase == "source-stopped" || phase == "delete-issued" || phase == "runtime-absent" || phase == "secrets-absent" || phase == "branch-delete-issued" || phase == "branch-absent" {
		return nil
	}
	if status != "running" || phase != "validated" {
		return ErrConflict
	}
	var ev DiscardEvidence
	if json.Unmarshal([]byte(evRaw), &ev) != nil || ev.Project != project || ev.Action != action && !(action == "discard" && ev.Action == "") {
		return ErrInvalid
	}
	if !validFingerprint(ev.PRefsDigest) {
		return ErrInvalid
	}
	var owner string
	if err = tx.QueryRowContext(ctx, `SELECT operation_id FROM git_ref_guards WHERE project_path=? AND branch=?`, project, ev.Branch).Scan(&owner); err != nil || owner != opID {
		return ErrConflict
	}
	if err = verifyRefs(ctx, ev); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET registry_state='removing' WHERE uuid=? AND project_path=? AND registry_state='established'`, sessionID, project)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE git_principals SET active=0 WHERE role='session' AND session_uuid=?`, sessionID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET phase='removal-committed',committed=1,updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), opID); err != nil {
		return err
	}
	return tx.Commit()
}

// Stale precommit confirmation restores the established assignment and
// releases only this operation's guard. Native helper/thaw cleanup is a
// prerequisite checked by the caller before this transaction.
func (s *Store) FailStaleDiscard(ctx context.Context, opID string, diagnostic string) error {
	return s.failStaleRemoval(ctx, opID, "discard", diagnostic)
}
func (s *Store) FailStaleDelete(ctx context.Context, opID string, diagnostic string) error {
	return s.failStaleRemoval(ctx, opID, "delete", diagnostic)
}
func (s *Store) failStaleRemoval(ctx context.Context, opID, action, diagnostic string) error {
	if len(diagnostic) > 512 {
		diagnostic = diagnostic[:512]
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var project, sessionID, phase, evRaw string
	var committed int
	err = tx.QueryRowContext(ctx, `SELECT project_path,session_uuid,phase,committed,evidence_json FROM operations WHERE id=? AND kind=?`, opID, "session."+action).Scan(&project, &sessionID, &phase, &committed, &evRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if committed != 0 || phase == "removal-committed" {
		return ErrConflict
	}
	var ev DiscardEvidence
	if json.Unmarshal([]byte(evRaw), &ev) != nil || ev.Action != action && !(action == "discard" && ev.Action == "") {
		return ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM git_ref_guards WHERE project_path=? AND branch=? AND operation_id=?`, project, ev.Branch, opID); err != nil {
		return err
	}
	var registry string
	if err = tx.QueryRowContext(ctx, `SELECT registry_state FROM sessions WHERE uuid=?`, sessionID).Scan(&registry); err != nil || registry != "established" {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET status='failed',phase='stale',diagnostic=?,updated_at=? WHERE id=?`, diagnostic, time.Now().UTC().Format(time.RFC3339Nano), opID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UnfinishedDiscards(ctx context.Context) ([]Operation, error) {
	return s.UnfinishedRemovals(ctx)
}
func (s *Store) UnfinishedRemovals(ctx context.Context) ([]Operation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM operations WHERE kind IN ('session.discard','session.delete') AND status IN ('running','blocked','unknown') ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
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

// CompleteDiscard releases the assignment only after the guarded P ref still
// has the confirmed tip. The callback runs under Git authority's write lock,
// so no P receive-pack can race this comparison and completion transaction.
func (s *Store) CompleteDiscard(ctx context.Context, opID string, verifyRef func(context.Context, string, string, string) error) error {
	return s.completeRemoval(ctx, opID, "discard", verifyRef)
}
func (s *Store) CompleteDelete(ctx context.Context, opID string, verifyRef func(context.Context, string, string, string) error) error {
	return s.completeRemoval(ctx, opID, "delete", verifyRef)
}
func (s *Store) completeRemoval(ctx context.Context, opID, action string, verifyRef func(context.Context, string, string, string) error) error {
	if verifyRef == nil {
		return ErrInvalid
	}
	wantPhase, terminal := "secrets-absent", "discarded"
	if action == "delete" {
		wantPhase, terminal = "branch-absent", "deleted"
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sessionID, project, phase, status, evRaw string
	err = tx.QueryRowContext(ctx, `SELECT session_uuid,project_path,phase,status,evidence_json FROM operations WHERE id=? AND kind=?`, opID, "session."+action).Scan(&sessionID, &project, &phase, &status, &evRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if status == "completed" && phase == terminal {
		return nil
	}
	if status != "running" || phase != wantPhase {
		return ErrConflict
	}
	var ev DiscardEvidence
	if json.Unmarshal([]byte(evRaw), &ev) != nil || ev.Project != project || ev.Action != action && !(action == "discard" && ev.Action == "") {
		return ErrInvalid
	}
	var owner string
	if err = tx.QueryRowContext(ctx, `SELECT operation_id FROM git_ref_guards WHERE project_path=? AND branch=?`, project, ev.Branch).Scan(&owner); err != nil || owner != opID {
		return ErrConflict
	}
	var registry, branch string
	if err = tx.QueryRowContext(ctx, `SELECT registry_state,branch FROM sessions WHERE uuid=? AND project_path=?`, sessionID, project).Scan(&registry, &branch); err != nil || registry != "removing" || branch != ev.Branch {
		return ErrConflict
	}
	if err = verifyRef(ctx, project, ev.Branch, ev.AssignedTip); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM git_unborn_grants WHERE session_uuid=?`, sessionID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM git_principals WHERE session_uuid=? AND role='session' AND active=0`, sessionID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE uuid=? AND registry_state='removing'`, sessionID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM git_ref_guards WHERE project_path=? AND branch=? AND operation_id=?`, project, ev.Branch, opID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET status='completed',phase=?,committed=1,diagnostic='',updated_at=? WHERE id=?`, terminal, time.Now().UTC().Format(time.RFC3339Nano), opID); err != nil {
		return err
	}
	return tx.Commit()
}
