package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// RenameRequest is immutable across idempotent retries. The expected tip is a
// caller-visible stale-input guard, not authority to choose a source ref.
type RenameRequest struct {
	Key            string `json:"key"`
	UUID           string `json:"uuid"`
	NewBranch      string `json:"new_branch"`
	ExpectedOldTip string `json:"expected_old_tip"`
}

type RenameEvidence struct {
	Project           string `json:"project"`
	OldBranch         string `json:"old_branch"`
	NewBranch         string `json:"new_branch"`
	OldTip            string `json:"old_tip"`
	WorkspaceTip      string `json:"workspace_tip,omitempty"`
	ConfigAfterSHA256 string `json:"config_after_sha256,omitempty"`
	PolicySHA256      string `json:"policy_sha256"`
	InstanceUUID      string `json:"instance_uuid"`
	ImageFingerprint  string `json:"image_fingerprint"`
	BaseFingerprint   string `json:"base_fingerprint"`
	IncusUUID         string `json:"incus_uuid"`
	Generation        string `json:"generation"`
	OriginalStatus    string `json:"original_status"`
}

func ValidRenameRequest(r RenameRequest) bool {
	return len(r.Key) >= 1 && len(r.Key) <= 128 && validUUID(r.UUID) && validBranch(r.NewBranch) &&
		validOID(r.ExpectedOldTip) && r.ExpectedOldTip != "0000000000000000000000000000000000000000"
}

func validRenameEvidence(e RenameEvidence) bool {
	return validProject(e.Project) && validBranch(e.OldBranch) && validBranch(e.NewBranch) && e.OldBranch != e.NewBranch &&
		validOID(e.OldTip) && (e.WorkspaceTip == "" || validOID(e.WorkspaceTip)) &&
		(e.ConfigAfterSHA256 == "" || validFingerprint(e.ConfigAfterSHA256)) && validFingerprint(e.PolicySHA256) &&
		validUUID(e.InstanceUUID) && validFingerprint(e.ImageFingerprint) && validFingerprint(e.BaseFingerprint) &&
		validUUID(e.IncusUUID) && validUUID(e.Generation) && (e.OriginalStatus == "Running" || e.OriginalStatus == "Stopped")
}

// BeginRename serializes ref observation, session reservation and both guards
// with receive-pack. observe must inspect the exact old tip and new absence;
// it must not call back into Store while this lock is held.
func (s *Store) BeginRename(ctx context.Context, r RenameRequest, e RenameEvidence, observe func(context.Context) error) (Operation, error) {
	if !ValidRenameRequest(r) || !validRenameEvidence(e) || e.NewBranch != r.NewBranch || e.OldTip != r.ExpectedOldTip || observe == nil {
		return Operation{}, ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return Operation{}, err
	}
	defer s.gitAuthority.Unlock()
	request, _ := json.Marshal(r)
	var kind, oldHash string
	err := s.db.QueryRowContext(ctx, `SELECT kind,request_sha256 FROM operations WHERE idempotency_key=?`, r.Key).Scan(&kind, &oldHash)
	if err == nil {
		if kind != "session.rename" || oldHash != digest(request) {
			return Operation{}, ErrConflict
		}
		return s.GetOperationByKey(ctx, r.Key)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, err
	}
	if err := observe(ctx); err != nil {
		return Operation{}, err
	}
	evidence, _ := json.Marshal(e)
	if len(evidence) > 2048 {
		return Operation{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback()
	var project, branch, registry, policy string
	err = tx.QueryRowContext(ctx, `SELECT project_path,branch,registry_state,policy_sha256 FROM sessions WHERE uuid=?`, r.UUID).Scan(&project, &branch, &registry, &policy)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, err
	}
	if project != e.Project || branch != e.OldBranch || registry != "established" || policy != e.PolicySHA256 {
		return Operation{}, ErrConflict
	}
	var occupied int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE project_path=? AND branch=?`, project, e.NewBranch).Scan(&occupied)
	if err == nil {
		return Operation{}, ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, err
	}
	id, err := newUUID()
	if err != nil {
		return Operation{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,committed,evidence_json,created_at,updated_at)
	 VALUES(?,?,?,?,?,?,?,'running','reserved',0,?,?,?)`, id, r.Key, "session.rename", project, r.UUID, string(request), digest(request), string(evidence), now, now)
	if err != nil {
		return Operation{}, classifyWrite(err)
	}
	for _, name := range []string{e.OldBranch, e.NewBranch} {
		if _, err = tx.ExecContext(ctx, `INSERT INTO git_ref_guards(project_path,branch,operation_id) VALUES(?,?,?)`, project, name, id); err != nil {
			return Operation{}, classifyWrite(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return Operation{}, err
	}
	return Operation{ID: id, Key: r.Key, Kind: "session.rename", Project: project, SessionUUID: r.UUID,
		Request: request, Status: "running", Phase: "reserved", Evidence: evidence, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) UnfinishedRenames(ctx context.Context) ([]Operation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM operations WHERE kind='session.rename' AND status IN ('running','blocked','unknown') ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]Operation, 0, len(ids))
	for _, id := range ids {
		op, err := s.GetOperation(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, op)
	}
	return result, nil
}

// FailRenamePrecommit is only for a positively known pre-commit refusal after
// the caller has restored the exact original source and removed its backup.
// A create-issued request has an unknown possible Git effect and cannot use
// this rollback path.
func (s *Store) FailRenamePrecommit(ctx context.Context, opID, diagnostic string) error {
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
	var project, phase, evRaw string
	var committed int
	err = tx.QueryRowContext(ctx, `SELECT project_path,phase,committed,evidence_json FROM operations WHERE id=? AND kind='session.rename' AND status IN ('running','blocked')`, opID).Scan(&project, &phase, &committed, &evRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var e RenameEvidence
	if json.Unmarshal([]byte(evRaw), &e) != nil || !validRenameEvidence(e) || project != e.Project || committed != 0 ||
		phase != "reserved" && phase != "guarded" && phase != "quiesced" && phase != "workspace-prepared" {
		return ErrConflict
	}
	for _, branch := range []string{e.OldBranch, e.NewBranch} {
		result, err := tx.ExecContext(ctx, `DELETE FROM git_ref_guards WHERE project_path=? AND branch=? AND operation_id=?`, project, branch, opID)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return ErrConflict
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET status='failed',phase='stale',diagnostic=?,updated_at=? WHERE id=?`, diagnostic, time.Now().UTC().Format(time.RFC3339Nano), opID); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateRenameAssignment is after the new P ref and workspace mapping prove
// exact. The same authority lock excludes receive-pack during the SQLite CAS.
func (s *Store) UpdateRenameAssignment(ctx context.Context, opID string, verify func(context.Context, RenameEvidence) error) error {
	if verify == nil {
		return ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	op, err := s.GetOperation(ctx, opID)
	if err != nil {
		return err
	}
	var e RenameEvidence
	if op.Kind != "session.rename" || op.Status != "running" || op.Phase != "workspace-renamed" || !op.Committed || json.Unmarshal(op.Evidence, &e) != nil || !validRenameEvidence(e) {
		return ErrConflict
	}
	if err := verify(ctx, e); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, branch := range []string{e.OldBranch, e.NewBranch} {
		var owner string
		if err := tx.QueryRowContext(ctx, `SELECT operation_id FROM git_ref_guards WHERE project_path=? AND branch=?`, e.Project, branch).Scan(&owner); err != nil || owner != opID {
			return ErrConflict
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET branch=? WHERE uuid=? AND project_path=? AND branch=? AND registry_state='established' AND policy_sha256=?`, e.NewBranch, op.SessionUUID, e.Project, e.OldBranch, e.PolicySHA256)
	if err != nil {
		return classifyWrite(err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET phase='assignment-updated',updated_at=? WHERE id=? AND phase='workspace-renamed'`, time.Now().UTC().Format(time.RFC3339Nano), opID); err != nil {
		return err
	}
	return tx.Commit()
}

// CompleteRename releases both guards only after the caller proves the old P
// ref absent, the new P ref exact, the workspace mapping and original runtime
// status. Neither a blocked nor an ambiguous operation is made terminal here.
func (s *Store) CompleteRename(ctx context.Context, opID string, verify func(context.Context, RenameEvidence) error) error {
	if verify == nil {
		return ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	op, err := s.GetOperation(ctx, opID)
	if err != nil {
		return err
	}
	var e RenameEvidence
	if op.Kind != "session.rename" || op.Status != "running" || op.Phase != "source-restored" || !op.Committed || json.Unmarshal(op.Evidence, &e) != nil || !validRenameEvidence(e) {
		return ErrConflict
	}
	if err := verify(ctx, e); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var branch, registry string
	if err := tx.QueryRowContext(ctx, `SELECT branch,registry_state FROM sessions WHERE uuid=? AND project_path=?`, op.SessionUUID, e.Project).Scan(&branch, &registry); err != nil || branch != e.NewBranch || registry != "established" {
		return ErrConflict
	}
	for _, name := range []string{e.OldBranch, e.NewBranch} {
		result, err := tx.ExecContext(ctx, `DELETE FROM git_ref_guards WHERE project_path=? AND branch=? AND operation_id=?`, e.Project, name, opID)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return ErrConflict
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE operations SET status='completed',phase='completed',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), opID); err != nil {
		return err
	}
	return tx.Commit()
}
