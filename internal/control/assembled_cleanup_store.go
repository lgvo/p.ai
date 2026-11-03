package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

// AssembledCreationForCleanup admits only the ordinary local base-image
// creator whose stopped assembly completed before startup failed. Native
// ownership and stopped state still require fresh Incus observations.
func AssembledCreationForCleanup(session Session, op api.Operation, base string) error {
	ev, err := Evidence(op)
	var req ReserveSessionRequest
	if err != nil || json.Unmarshal(op.Request, &req) != nil || !ValidSessionCreateRequest(req) ||
		op.Kind != "session.create" || op.Status != "blocked" || op.Phase != "assembly-ready" || !op.Committed ||
		session.Registry != "creating" || op.SessionUUID != session.UUID || op.Project != session.Project ||
		req.Project != session.Project || req.Branch != session.Branch || req.Key != op.Key ||
		!validOID(ev.CapturedOID) || !validFingerprint(base) || ev.ImageFingerprint != base || !ev.Selection.Valid() ||
		ev.RuntimeInitState != "attempted" || ev.BranchExisted != (req.Choice == "existing") || ev.RefCASIntent != (req.Choice == "new") ||
		req.OriginRef != "" || ev.OriginURL != "" || ev.OriginRef != "" ||
		ev.Environment != nil || ev.EnvironmentBuilder != nil ||
		ev.SupersedesOperationID != "" || ev.SupersedesUUID != "" || ev.ReplacementCleanup != nil || ev.ReplacementTokenSHA256 != "" {
		return fmt.Errorf("%w: assembled failed creation cleanup requires a blocked local base-image assembly-ready creator with recorded init", ErrConflict)
	}
	policy, err := ParseStoredProjectPolicy(session.Policy)
	sha, hashErr := StoredProjectPolicySHA(session.Policy)
	if err != nil || hashErr != nil || sha != session.PolicySHA256 || sha != ev.PolicySHA256 ||
		policy.Network != "none" || len(policy.FilesystemMounts) != 0 {
		return fmt.Errorf("%w: assembled failed creation cleanup requires a valid immutable standalone policy", ErrConflict)
	}
	return nil
}

func validAssembledCleanupReview(ev CreateCleanupEvidence) bool {
	p, r := ev.Review, ev.Review.Runtime
	return r != nil && validFingerprint(p.OldRequestSHA256) && p.OldPhase == "assembly-ready" &&
		r.Condition == "present" && r.OriginalStatus == "Stopped" && validUUID(r.IncusUUID) && validUUID(r.Generation) &&
		r.InstanceName == "p-"+p.UUID && r.IncusProject != "" && r.ImageFingerprint == p.ImageFingerprint && r.RuntimeDataLost &&
		len(p.LossWarnings) == 3 && p.Provisional.Cleanup != nil && !ev.RuntimeAbsent && !ev.LocalComplete
}

// ValidateAssembledCleanupAuthority keeps creator/request/policy and the exact
// session/ref guard bound through restart, before or after atomic retirement.
func (s *Store) ValidateAssembledCleanupAuthority(ctx context.Context, observed api.Operation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := operationByIDTx(ctx, tx, observed.ID)
	if err != nil || current.Kind != "session.create.cleanup" || digest(current.Evidence) != digest(observed.Evidence) || current.Phase != observed.Phase || current.Committed != observed.Committed || current.Status != "running" && current.Status != "blocked" {
		return ErrConflict
	}
	return validateAssembledCleanupAuthorityTx(ctx, tx, current)
}

func validateAssembledCleanupAuthorityTx(ctx context.Context, tx *sql.Tx, op api.Operation) error {
	var ev CreateCleanupEvidence
	if json.Unmarshal(op.Evidence, &ev) != nil || ev.Review.Runtime == nil || ev.Review.Provisional.Cleanup == nil || !ev.Review.Provisional.Cleanup.Valid() {
		return ErrInvalid
	}
	p := ev.Review
	creator, err := operationByIDTx(ctx, tx, p.OldOperationID)
	if err != nil || creator.Kind != "session.create" || creator.SessionUUID != op.SessionUUID || creator.Project != op.Project || digest(creator.Request) != p.OldRequestSHA256 || digest(creator.Evidence) != p.OldEvidenceSHA256 {
		return ErrConflict
	}
	if op.Committed {
		if creator.Status != "superseded" || creator.Phase != "superseded" {
			return ErrConflict
		}
	} else if creator.Status != "blocked" || creator.Phase != p.OldPhase {
		return ErrConflict
	}
	session, err := getSessionTx(ctx, tx, op.SessionUUID)
	if err != nil || session.Project != op.Project || session.Branch != p.OldRequest.Branch || session.PolicySHA256 != p.PolicySHA256 ||
		!op.Committed && session.Registry != "creating" || op.Committed && session.Registry != "removing" {
		return ErrConflict
	}
	// Retired state changes only the coarse authority state; its immutable
	// creator evidence must still meet the original accepted narrow proof.
	creator.Status, creator.Phase, session.Registry = "blocked", p.OldPhase, "creating"
	if AssembledCreationForCleanup(session, creator, p.ImageFingerprint) != nil {
		return ErrConflict
	}
	var owner string
	if err = tx.QueryRowContext(ctx, `SELECT operation_id FROM git_ref_guards WHERE project_path=? AND branch=?`, op.Project, session.Branch).Scan(&owner); err != nil || owner != op.ID {
		return ErrConflict
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT active FROM git_principals WHERE role='session' AND session_uuid=? AND fingerprint=?`, op.SessionUUID, p.Provisional.Cleanup.KeyFingerprint).Scan(&active); err != nil || active != boolInt(!op.Committed) {
		return ErrConflict
	}
	return nil
}

// CommitAssembledCleanupDelete retires the original creator/session authority
// and records the one DELETE dispatch boundary in the same SQLite transaction.
func (s *Store) CommitAssembledCleanupDelete(ctx context.Context, observed api.Operation, verify func(context.Context) error) error {
	if verify == nil {
		return ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	if err := verify(ctx); err != nil {
		return err
	}
	j, err := s.beginJournal(ctx)
	if err != nil {
		return err
	}
	defer j.Rollback()
	tx := j.Tx
	op, err := operationByIDTx(ctx, tx, observed.ID)
	if err != nil || op.Status != "running" || op.Committed || op.Phase != "validated" || digest(op.Evidence) != digest(observed.Evidence) {
		return ErrConflict
	}
	if err = validateAssembledCleanupAuthorityTx(ctx, tx, op); err != nil {
		return err
	}
	var ev CreateCleanupEvidence
	_ = json.Unmarshal(op.Evidence, &ev)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = j.update(ctx, op.ID, `UPDATE operations SET committed=1,phase='delete-issued',updated_at=? WHERE id=?`, now, op.ID); err != nil {
		return classifyWrite(err)
	}
	if _, err = j.update(ctx, ev.Review.OldOperationID, `UPDATE operations SET status='superseded',phase='superseded',reason='',diagnostic=?,updated_at=? WHERE id=?`, "cleanup by "+op.ID, now, ev.Review.OldOperationID); err != nil {
		return classifyWrite(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sessions SET registry_state='removing' WHERE uuid=?`, op.SessionUUID); err != nil {
		return classifyWrite(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE git_principals SET active=0 WHERE role='session' AND session_uuid=?`, op.SessionUUID); err != nil {
		return err
	}
	return j.commit(ctx)
}

// FailStaleAssembledCleanup releases only this precommit intent; the original
// creator stays blocked.
func (s *Store) FailStaleAssembledCleanup(ctx context.Context, observed api.Operation, diagnostic string) error {
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
	op, err := operationByIDTx(ctx, tx, observed.ID)
	if err != nil || op.Committed || op.Phase != "stale-cleanup" || digest(op.Evidence) != digest(observed.Evidence) {
		return ErrConflict
	}
	if err = validateAssembledCleanupAuthorityTx(ctx, tx, op); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM git_ref_guards WHERE operation_id=?`, op.ID); err != nil {
		return err
	}
	diagnostic = api.BoundDiagnostic(diagnostic)
	if _, err = j.update(ctx, op.ID, `UPDATE operations SET status='failed',phase='stale',reason='changed',diagnostic=?,updated_at=? WHERE id=?`, diagnostic, time.Now().UTC().Format(time.RFC3339Nano), op.ID); err != nil {
		return err
	}
	return j.commit(ctx)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func assembledCleanupPhaseRank(phase string) int {
	switch phase {
	case "validated":
		return 0
	case "delete-issued":
		return 1
	case "runtime-absent":
		return 2
	case "local-complete":
		return 3
	case "stale-cleanup":
		return 4
	}
	return -1
}

func validAssembledCleanupAdvance(oldPhase, phase string, wasCommitted, committed bool, next CreateCleanupEvidence) bool {
	a, b := assembledCleanupPhaseRank(oldPhase), assembledCleanupPhaseRank(phase)
	if a < 0 || b < a || b < 0 || b > a+1 && phase != "stale-cleanup" || wasCommitted != committed || next.LocalComplete != (phase == "local-complete") ||
		next.RuntimeAbsent != (phase == "runtime-absent" || phase == "local-complete") {
		return false
	}
	if committed {
		return phase == "delete-issued" || phase == "runtime-absent" || phase == "local-complete"
	}
	return phase != "delete-issued" && phase != "runtime-absent" && phase != "local-complete"
}
