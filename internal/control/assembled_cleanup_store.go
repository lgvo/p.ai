package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

func validAssembledCleanupReview(ev CreateCleanupEvidence) bool {
	p, r := ev.Review, ev.Review.Runtime
	return r != nil && validFingerprint(p.OldRequestSHA256) && p.OldPhase == "assembly-ready" &&
		r.Condition == "present" && r.OriginalStatus == "Stopped" && validUUID(r.IncusUUID) && validUUID(r.Generation) &&
		r.InstanceName == "p-"+p.UUID && r.IncusProject != "" && r.ImageFingerprint == p.ImageFingerprint &&
		validUUID(r.LossOperationID) && validFingerprint(r.Fingerprint) && len(r.Loss) > 0 && len(r.Loss) <= 12<<10 &&
		len(p.LossWarnings) == 3 && p.Provisional.Cleanup != nil && !ev.RuntimeAbsent && !ev.LocalComplete
}

func validateAssembledCleanupLossTx(ctx context.Context, tx *sql.Tx, creator Operation, ev CreateCleanupEvidence) error {
	r := ev.Review.Runtime
	loss, err := operationByIDTx(ctx, tx, r.LossOperationID)
	var proof WorkspaceInspectEvidence
	finished, timeErr := time.Parse(time.RFC3339Nano, loss.UpdatedAt)
	if err != nil || timeErr != nil || time.Since(finished) < 0 || time.Since(finished) > 2*time.Minute ||
		loss.Kind != "workspace.loss.inspect" || loss.Status != "completed" || loss.Phase != "inspected" || loss.SessionUUID != creator.SessionUUID || loss.Project != creator.Project ||
		json.Unmarshal(loss.Evidence, &proof) != nil || proof.CreatorOperationID != creator.ID || proof.CreatorRequestSHA256 != digest(creator.Request) ||
		proof.CreatorEvidenceSHA256 != digest(creator.Evidence) || proof.InstanceUUID != ev.InstanceUUID || proof.BaseFingerprint != ev.Review.ImageFingerprint ||
		proof.ImageFingerprint != r.ImageFingerprint || proof.SourceIncusUUID != r.IncusUUID || proof.SourceGeneration != r.Generation ||
		proof.OriginalStatus != "Stopped" || string(proof.Result) != string(r.Loss) || loss.UpdatedAt != r.ObservedAt {
		return ErrConflict
	}
	var result struct {
		Schema                   string `json:"schema"`
		Fingerprint              string `json:"fingerprint"`
		RuntimeDataWillBeRemoved bool   `json:"runtime_data_will_be_removed"`
	}
	if json.Unmarshal(r.Loss, &result) != nil || result.Schema != "p.workspace-loss/v1" || result.Fingerprint != r.Fingerprint || !result.RuntimeDataWillBeRemoved {
		return ErrConflict
	}
	return nil
}

// ValidateAssembledCleanupAuthority keeps creator/request/policy and the exact
// session/ref guard bound through restart, before or after atomic retirement.
func (s *Store) ValidateAssembledCleanupAuthority(ctx context.Context, observed Operation) error {
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

func validateAssembledCleanupAuthorityTx(ctx context.Context, tx *sql.Tx, op Operation) error {
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
	if AssembledCreationForLoss(session, creator, p.ImageFingerprint) != nil {
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
func (s *Store) CommitAssembledCleanupDelete(ctx context.Context, observed Operation, verify func(context.Context) error) error {
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET committed=1,phase='delete-issued',updated_at=? WHERE id=?`, now, op.ID); err != nil {
		return classifyWrite(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET status='superseded',phase='superseded',diagnostic=?,updated_at=? WHERE id=?`, "cleanup by "+op.ID, now, ev.Review.OldOperationID); err != nil {
		return classifyWrite(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sessions SET registry_state='removing' WHERE uuid=?`, op.SessionUUID); err != nil {
		return classifyWrite(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE git_principals SET active=0 WHERE role='session' AND session_uuid=?`, op.SessionUUID); err != nil {
		return err
	}
	return tx.Commit()
}

// FailStaleAssembledCleanup releases only this precommit intent. The caller
// must positively settle helper absence; the original creator stays blocked.
func (s *Store) FailStaleAssembledCleanup(ctx context.Context, observed Operation, diagnostic string) error {
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
	if len(diagnostic) > 512 {
		diagnostic = diagnostic[:512]
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET status='failed',phase='stale',diagnostic=?,updated_at=? WHERE id=?`, diagnostic, time.Now().UTC().Format(time.RFC3339Nano), op.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func assembledCleanupPhaseRank(phase string) int {
	switch phase {
	case "helper-intent":
		return 0
	case "init-issued":
		return 1
	case "helper-ready":
		return 2
	case "quiescent":
		return 3
	case "analyzed":
		return 4
	case "validated":
		return 5
	case "delete-issued":
		return 6
	case "runtime-absent":
		return 7
	case "local-complete":
		return 8
	case "stale-cleanup":
		return 9
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
