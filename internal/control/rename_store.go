package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/sourcedriver"
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
	Project   string `json:"project"`
	OldBranch string `json:"old_branch"`
	NewBranch string `json:"new_branch"`
	OldTip    string `json:"old_tip"`
	// Plan is the source driver's plan for renaming the session's clone,
	// recorded with the backup's write: every later answer of the rename
	// must return it.
	Plan             string `json:"plan,omitempty"`
	PolicySHA256     string `json:"policy_sha256"`
	InstanceUUID     string `json:"instance_uuid"`
	ImageFingerprint string `json:"image_fingerprint"`
	BaseFingerprint  string `json:"base_fingerprint"`
	IncusUUID        string `json:"incus_uuid"`
	Generation       string `json:"generation"`
	// Edit is the clone edit last sent, which its gate records: an attempt
	// that finds it settles it, and never sends it again.
	Edit RenameEdit `json:"edit,omitzero"`
}

// RenameEdit is one edit of a renamed session's clone: the goal it serves,
// its path in the workspace, and whether it removes the path or writes a
// file there.
type RenameEdit struct {
	Goal   sourcedriver.Goal `json:"goal"`
	Path   string            `json:"path"`
	Remove bool              `json:"remove,omitempty"`
}

func ValidRenameRequest(r RenameRequest) bool { return api.SessionRenameRequest(r).Validate() == nil }

func validRenameEvidence(e RenameEvidence) bool {
	return validProject(e.Project) && validBranch(e.OldBranch) && validBranch(e.NewBranch) && e.OldBranch != e.NewBranch &&
		validOID(e.OldTip) && (e.Plan == "" || api.BoundedName(e.Plan, sourcedriver.MaxPlan)) && validRenameEdit(e.Edit) &&
		validFingerprint(e.PolicySHA256) && validUUID(e.InstanceUUID) && validFingerprint(e.ImageFingerprint) &&
		validFingerprint(e.BaseFingerprint) && validUUID(e.IncusUUID) && validUUID(e.Generation)
}

// maxRenameEditPath bounds a recorded edit's path, so that the evidence's
// 2048 bytes hold it beside the plan, the branches and the identities.
const maxRenameEditPath = 256

func validRenameEdit(e RenameEdit) bool {
	switch e.Goal {
	case "":
		return e == RenameEdit{}
	case sourcedriver.GoalPrepared, sourcedriver.GoalRenamed, sourcedriver.GoalDone:
		return api.BoundedName(e.Path, maxRenameEditPath)
	}
	return false
}

// BeginRename serializes ref observation, session reservation and both guards
// with receive-pack. observe must inspect the exact old tip and new absence;
// it must not call back into Store while this lock is held.
func (s *Store) BeginRename(ctx context.Context, r RenameRequest, e RenameEvidence, observe func(context.Context) error) (api.Operation, error) {
	if !ValidRenameRequest(r) || !validRenameEvidence(e) || e.NewBranch != r.NewBranch || e.OldTip != r.ExpectedOldTip || observe == nil {
		return api.Operation{}, ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return api.Operation{}, err
	}
	defer s.gitAuthority.Unlock()
	request, _ := json.Marshal(r)
	if replay, err := keyRecordsRequest(ctx, s.db, r.Key, KindSessionRename, request); err != nil {
		return api.Operation{}, err
	} else if replay {
		return s.GetOperationByKey(ctx, r.Key)
	}
	if err := observe(ctx); err != nil {
		return api.Operation{}, err
	}
	evidence, _ := json.Marshal(e)
	if len(evidence) > 2048 {
		return api.Operation{}, ErrInvalid
	}
	j, err := s.beginJournal(ctx)
	if err != nil {
		return api.Operation{}, err
	}
	defer j.Rollback()
	tx := j.Tx
	var project, branch, registry, policy string
	err = tx.QueryRowContext(ctx, `SELECT project_path,branch,registry_state,policy_sha256 FROM sessions WHERE uuid=?`, r.UUID).Scan(&project, &branch, &registry, &policy)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Operation{}, NotFound(api.ReasonSession, api.TargetSession(r.UUID))
	}
	if err != nil {
		return api.Operation{}, err
	}
	if registry != "established" {
		return api.Operation{}, Conflict(api.ReasonSessionNotEstablished, api.TargetSession(r.UUID))
	}
	if project != e.Project || branch != e.OldBranch || policy != e.PolicySHA256 {
		return api.Operation{}, ErrConflict
	}
	var occupied int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE project_path=? AND branch=?`, project, e.NewBranch).Scan(&occupied)
	if err == nil {
		return api.Operation{}, Conflict(api.ReasonBranchAssigned, api.TargetBranch(project, e.NewBranch))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return api.Operation{}, err
	}
	id, err := newUUID()
	if err != nil {
		return api.Operation{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rec := operationRecord{ID: id, Key: r.Key, Kind: KindSessionRename, Project: project, Session: r.UUID, Request: request,
		Intent: api.OperationIntent{Project: project, Session: r.UUID, Branch: branch, NewBranch: r.NewBranch}, Phase: "reserved", Evidence: evidence, Now: now}
	err = j.insert(ctx, rec)
	if err != nil {
		return api.Operation{}, classifyWrite(err)
	}
	for _, name := range []string{e.OldBranch, e.NewBranch} {
		if _, err = tx.ExecContext(ctx, `INSERT INTO git_ref_guards(project_path,branch,operation_id) VALUES(?,?,?)`, project, name, id); err != nil {
			return api.Operation{}, withTarget(classifyWrite(err), api.TargetBranch(project, name))
		}
	}
	if err = j.commit(ctx); err != nil {
		return api.Operation{}, err
	}
	return rec.operation(), nil
}

// FailRenamePrecommit is only for a positively known pre-commit refusal after
// the caller has removed its workspace backup.
// A create-issued request has an unknown possible Git effect and cannot use
// this rollback path.
func (s *Store) FailRenamePrecommit(ctx context.Context, opID, reason, diagnostic string) error {
	if !validReason(reason) {
		return ErrInvalid
	}
	diagnostic = api.BoundDiagnostic(diagnostic)
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	j, err := s.beginJournal(ctx)
	if err != nil {
		return err
	}
	defer j.Rollback()
	tx := j.Tx
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
		phase != "reserved" && phase != "guarded" && phase != "workspace-prepared" {
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
	if _, err = j.update(ctx, opID, `UPDATE operations SET status='failed',phase='stale',reason=?,diagnostic=?,updated_at=? WHERE id=?`, reason, diagnostic, time.Now().UTC().Format(time.RFC3339Nano), opID); err != nil {
		return err
	}
	return j.commit(ctx)
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
	j, err := s.beginJournal(ctx)
	if err != nil {
		return err
	}
	defer j.Rollback()
	tx := j.Tx
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
	if _, err = j.update(ctx, opID, `UPDATE operations SET phase='assignment-updated',updated_at=? WHERE id=? AND phase='workspace-renamed'`, time.Now().UTC().Format(time.RFC3339Nano), opID); err != nil {
		return err
	}
	return j.commit(ctx)
}

// CompleteRename releases both guards only after the caller proves the old P
// ref absent, the new P ref exact, the workspace mapping and the runtime still
// stopped. Neither a blocked nor an ambiguous operation is made terminal here.
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
	if op.Kind != "session.rename" || op.Status != "running" || op.Phase != "backup-absent" || !op.Committed || json.Unmarshal(op.Evidence, &e) != nil || !validRenameEvidence(e) {
		return ErrConflict
	}
	if err := verify(ctx, e); err != nil {
		return err
	}
	j, err := s.beginJournal(ctx)
	if err != nil {
		return err
	}
	defer j.Rollback()
	tx := j.Tx
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
	if _, err := j.update(ctx, opID, `UPDATE operations SET status='completed',phase='completed',reason='',diagnostic='',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), opID); err != nil {
		return err
	}
	return j.commit(ctx)
}
