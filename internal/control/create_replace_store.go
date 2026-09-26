package control

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// CreateReplaceIntent is limited to a blocked local-source creation whose
// durable evidence proves that no builder or publication was attempted.
type CreateReplaceIntent struct {
	OldOperationID    string
	OldUUID           string
	OldEvidenceSHA256 string
	OldPolicySHA256   string
	NewPolicySHA256   string
	TokenSHA256       string
	ExpiresAt         string
	Cleanup           *CreateReplacementCleanup
	New               ReserveSessionRequest
}

// ReplayCreateReplacement reads exact accepted intent without acquiring a
// mutation lock. Cleanup may own the retired UUID and Git authority while the
// caller merely retrieves the operation it already authorized.
func (s *Store) ReplayCreateReplacement(ctx context.Context, key, oldUUID, tokenSHA256 string) (Operation, bool, error) {
	if key == "" || len(key) > 128 || !validUUID(oldUUID) || !validFingerprint(tokenSHA256) {
		return Operation{}, false, ErrInvalid
	}
	prior, err := s.GetOperationByKey(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return Operation{}, false, nil
	}
	if err != nil {
		return Operation{}, false, err
	}
	var saved CreationEvidence
	if prior.Kind != "session.create" || json.Unmarshal(prior.Evidence, &saved) != nil ||
		saved.SupersedesUUID != oldUUID || saved.ReplacementTokenSHA256 != tokenSHA256 {
		return Operation{}, false, ErrConflict
	}
	return prior, true, nil
}

// ReplaceBlockedCreate transfers one branch reservation to a new immutable
// creation in one transaction. verify runs under the Git authority boundary
// before that transaction, so it may query durable facts on this Store.
func (s *Store) ReplaceBlockedCreate(ctx context.Context, intent CreateReplaceIntent, image string,
	selection CreationSelection, environment *EnvironmentIntent, observe CapacityObserver,
	verify func(context.Context) (string, error)) (Operation, error) {
	if !validUUID(intent.OldOperationID) || !validUUID(intent.OldUUID) ||
		!validFingerprint(intent.OldEvidenceSHA256) || !validFingerprint(intent.OldPolicySHA256) || !validFingerprint(intent.NewPolicySHA256) ||
		!validFingerprint(intent.TokenSHA256) || !ValidSessionCreateRequest(intent.New) ||
		intent.New.OriginRef != "" || !validFingerprint(image) || !selection.Valid() ||
		observe == nil || verify == nil || intent.Cleanup != nil && (!intent.Cleanup.Valid() || intent.Cleanup.Completed || intent.Cleanup.OldUUID != intent.OldUUID || intent.Cleanup.OldOperationID != intent.OldOperationID) || environment != nil && (!environment.Valid() || environment.BaseFingerprint != image) {
		return Operation{}, ErrInvalid
	}
	expiry, err := time.Parse(time.RFC3339Nano, intent.ExpiresAt)
	if err != nil {
		return Operation{}, ErrInvalid
	}
	if err = s.lockGitAuthority(ctx); err != nil {
		return Operation{}, err
	}
	defer s.gitAuthority.Unlock()
	request, _ := json.Marshal(intent.New)
	// Replays do not depend on the old session row, which has been removed.
	if prior, e := s.GetOperationByKey(ctx, intent.New.Key); e == nil {
		var saved CreationEvidence
		if prior.Kind != "session.create" || digest(prior.Request) != digest(request) ||
			json.Unmarshal(prior.Evidence, &saved) != nil ||
			saved.SupersedesOperationID != intent.OldOperationID || saved.SupersedesUUID != intent.OldUUID ||
			saved.ReplacementTokenSHA256 != intent.TokenSHA256 {
			return Operation{}, ErrConflict
		}
		return prior, nil
	} else if !errors.Is(e, ErrNotFound) {
		return Operation{}, e
	}
	if !time.Now().Before(expiry) {
		return Operation{}, ErrConflict
	}
	if err = s.CheckOriginKeyConflict(ctx, intent.New.Key); err != nil {
		return Operation{}, err
	}
	if err = s.checkSessionRepairCapacity(ctx, observe); err != nil {
		return Operation{}, err
	}
	tip, err := verify(ctx)
	if err != nil {
		return Operation{}, err
	}
	if !validOID(tip) || !time.Now().Before(expiry) {
		return Operation{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback()
	var oldKind, oldStatus, oldPhase, oldProject, oldSession, oldEvidence string
	var oldCommitted int
	err = tx.QueryRowContext(ctx, `SELECT kind,status,phase,project_path,COALESCE(session_uuid,''),COALESCE(evidence_json,''),committed
	 FROM operations WHERE id=?`, intent.OldOperationID).Scan(&oldKind, &oldStatus, &oldPhase, &oldProject, &oldSession, &oldEvidence, &oldCommitted)
	if err != nil || oldKind != "session.create" || oldStatus != "blocked" ||
		(oldPhase != "source-ready" && oldPhase != "branch-assigned" && oldPhase != "principals-ready") || oldProject != intent.New.Project ||
		oldSession != intent.OldUUID || digest([]byte(oldEvidence)) != intent.OldEvidenceSHA256 ||
		(oldPhase == "source-ready" && oldCommitted != 0 || oldPhase != "source-ready" && oldCommitted != 1) {
		return Operation{}, errors.Join(err, ErrConflict)
	}
	var oldRequest ReserveSessionRequest
	var oldEv CreationEvidence
	if tx.QueryRowContext(ctx, `SELECT request_json FROM operations WHERE id=?`, intent.OldOperationID).Scan(&oldEvidence) != nil ||
		json.Unmarshal([]byte(oldEvidence), &oldRequest) != nil || !ValidSessionCreateRequest(oldRequest) ||
		oldRequest.Project != intent.New.Project ||
		(oldRequest.Choice == "existing" && (intent.New.Choice != "existing" || oldRequest.Branch != intent.New.Branch)) ||
		(oldRequest.Choice == "new" && intent.New.Choice == "existing" && oldRequest.Branch != intent.New.Branch) {
		return Operation{}, ErrConflict
	}
	if tx.QueryRowContext(ctx, `SELECT evidence_json FROM operations WHERE id=?`, intent.OldOperationID).Scan(&oldEvidence) != nil ||
		json.Unmarshal([]byte(oldEvidence), &oldEv) != nil || !ReplaceableCreationEvidence(oldRequest, oldEv) || oldEv.PolicySHA256 != intent.OldPolicySHA256 || oldEv.ReplacementCleanup != nil && !oldEv.ReplacementCleanup.Completed ||
		(oldPhase == "principals-ready" && (intent.Cleanup == nil || oldEv.Environment != nil || oldEv.RuntimeInitState != "not-attempted" || intent.Cleanup.OldImageFingerprint != oldEv.ImageFingerprint)) || (oldPhase != "principals-ready" && intent.Cleanup != nil) {
		return Operation{}, ErrConflict
	}
	var project, branch, policy, registry string
	if err = tx.QueryRowContext(ctx, `SELECT project_path,branch,policy_sha256,registry_state FROM sessions WHERE uuid=?`, intent.OldUUID).Scan(&project, &branch, &policy, &registry); err != nil ||
		project != intent.New.Project || branch != oldRequest.Branch || policy != intent.OldPolicySHA256 || registry != "creating" {
		return Operation{}, errors.Join(err, ErrConflict)
	}
	var principals, guards int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM git_principals WHERE session_uuid=?`, intent.OldUUID).Scan(&principals); err != nil || intent.Cleanup == nil && principals != 0 || intent.Cleanup != nil && principals != 1 {
		return Operation{}, ErrConflict
	}
	if intent.Cleanup != nil {
		var fp, role, boundProject string
		var active int
		if err = tx.QueryRowContext(ctx, `SELECT fingerprint,role,project_path,active FROM git_principals WHERE session_uuid=?`, intent.OldUUID).Scan(&fp, &role, &boundProject, &active); err != nil || fp != intent.Cleanup.KeyFingerprint || role != "session" || boundProject != project || active != 1 {
			return Operation{}, ErrConflict
		}
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM git_ref_guards WHERE project_path=? AND branch IN (?,?)`, project, branch, intent.New.Branch).Scan(&guards); err != nil || guards != 0 {
		return Operation{}, ErrConflict
	}
	var assigned int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE project_path=? AND branch=? AND uuid<>?`, project, intent.New.Branch, intent.OldUUID).Scan(&assigned); err != nil || assigned != 0 {
		return Operation{}, ErrConflict
	}
	var newPolicy, newHash string
	if err = tx.QueryRowContext(ctx, `SELECT policy_json,policy_sha256 FROM projects WHERE path=? AND registry_state='active'`, project).Scan(&newPolicy, &newHash); err != nil {
		return Operation{}, errors.Join(err, ErrConflict)
	}
	if newHash != intent.NewPolicySHA256 || tip == oldEv.CapturedOID && newHash == intent.OldPolicySHA256 && SameCreateSelection(oldRequest, intent.New) {
		return Operation{}, ErrConflict
	}
	newSessionID, err := newUUID()
	if err != nil {
		return Operation{}, err
	}
	newID, err := newUUID()
	if err != nil {
		return Operation{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET status='superseded',phase='superseded',diagnostic=?,updated_at=? WHERE id=? AND status='blocked'`, "replaced by "+newID, now, intent.OldOperationID); err != nil {
		return Operation{}, err
	}
	if intent.Cleanup != nil {
		if _, err = tx.ExecContext(ctx, `DELETE FROM git_principals WHERE session_uuid=? AND fingerprint=? AND role='session' AND active=1`, intent.OldUUID, intent.Cleanup.KeyFingerprint); err != nil {
			return Operation{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE uuid=? AND registry_state='creating'`, intent.OldUUID); err != nil {
		return Operation{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256) VALUES(?,?,?,'creating',?,?)`, newSessionID, project, intent.New.Branch, newPolicy, newHash); err != nil {
		return Operation{}, classifyWrite(err)
	}
	if err = reservePublicAddressTx(ctx, tx, newSessionID, json.RawMessage(newPolicy)); err != nil {
		return Operation{}, err
	}
	ev := CreationEvidence{RuntimeInitState: "not-attempted", CapturedOID: tip, BranchExisted: intent.New.Choice == "existing", RefCASIntent: intent.New.Choice == "new", ImageFingerprint: image, PolicySHA256: newHash,
		Selection: selection, ReplacementCleanup: intent.Cleanup, SupersedesOperationID: intent.OldOperationID, SupersedesUUID: intent.OldUUID,
		ReplacementTokenSHA256: intent.TokenSHA256}
	if environment != nil {
		copy := *environment
		ev.Environment = &copy
	}
	phase := "source-ready"
	if intent.Cleanup != nil {
		phase = "replacement-cleanup"
	}
	evidence, _ := json.Marshal(ev)
	if len(evidence) > 16384 {
		return Operation{}, ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,committed,evidence_json,created_at,updated_at)
	 VALUES(?,?,?,?,?,?,?,'running',?,0,?,?,?)`, newID, intent.New.Key, "session.create", project, newSessionID, string(request), digest(request), phase, string(evidence), now, now); err != nil {
		return Operation{}, classifyWrite(err)
	}
	if err = tx.Commit(); err != nil {
		return Operation{}, err
	}
	return Operation{ID: newID, Key: intent.New.Key, Kind: "session.create", Project: project, SessionUUID: newSessionID,
		Request: request, Status: "running", Phase: phase, Evidence: evidence, CreatedAt: now, UpdatedAt: now}, nil
}

// CheckCreateReplacementTarget is read-only preview evidence. Admission repeats
// the assignment and lifecycle-guard checks in its atomic handoff transaction.
func (s *Store) CheckCreateReplacementTarget(ctx context.Context, project, branch, oldUUID string) error {
	if !validProject(project) || !validBranch(branch) || !validUUID(oldUUID) {
		return ErrInvalid
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE project_path=? AND branch=? AND uuid<>?`, project, branch, oldUUID).Scan(&count); err != nil {
		return err
	} else if count != 0 {
		return ErrConflict
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM git_ref_guards WHERE project_path=? AND branch=?`, project, branch).Scan(&count); err != nil {
		return err
	} else if count != 0 {
		return ErrConflict
	}
	return nil
}

// CheckCreateReplacementPrincipal refuses extra/revoked local authority during
// preview; the handoff transaction repeats the exact check before retirement.
func (s *Store) CheckCreateReplacementPrincipal(ctx context.Context, uuid, project, fingerprint string) error {
	if !validUUID(uuid) || !validProject(project) || !validFingerprint(fingerprint) {
		return ErrInvalid
	}
	var count, matching int
	err := s.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(CASE WHEN fingerprint=? AND role='session' AND project_path=? AND active=1 THEN 1 ELSE 0 END),0) FROM git_principals WHERE session_uuid=?`, fingerprint, project, uuid).Scan(&count, &matching)
	if err != nil || count != 1 || matching != 1 {
		return errors.Join(err, ErrConflict)
	}
	return nil
}
