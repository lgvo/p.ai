package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// AssembledCreationForLoss admits only the ordinary local base-image creator
// whose stopped assembly completed before startup failed. Native ownership,
// stopped state and workspace layout still require fresh Incus observations.
func AssembledCreationForLoss(session Session, op Operation, base string) error {
	ev, err := Evidence(op)
	var req ReserveSessionRequest
	if err != nil || json.Unmarshal(op.Request, &req) != nil || !ValidSessionCreateRequest(req) ||
		op.Kind != "session.create" || op.Status != "blocked" || op.Phase != "assembly-ready" || !op.Committed ||
		session.Registry != "creating" || op.SessionUUID != session.UUID || op.Project != session.Project ||
		req.Project != session.Project || req.Branch != session.Branch || req.Key != op.Key ||
		!validOID(ev.CapturedOID) || !validFingerprint(base) || ev.ImageFingerprint != base || !ev.Selection.Valid() ||
		ev.RuntimeInitState != "attempted" || ev.BranchExisted != (req.Choice == "existing") || ev.RefCASIntent != (req.Choice == "new") ||
		req.OriginRef != "" || ev.OriginURL != "" || ev.OriginRef != "" ||
		ev.Environment != nil || ev.EnvironmentState != nil || ev.EnvironmentBuilder != nil || ev.BuilderTreeOID != "" ||
		ev.SupersedesOperationID != "" || ev.SupersedesUUID != "" || ev.ReplacementCleanup != nil || ev.ReplacementTokenSHA256 != "" {
		return fmt.Errorf("%w: failed creation loss inspection requires a blocked local base-image assembly-ready creator with recorded init", ErrConflict)
	}
	policy, err := ParseStoredProjectPolicy(session.Policy)
	sha, hashErr := StoredProjectPolicySHA(session.Policy)
	if err != nil || hashErr != nil || sha != session.PolicySHA256 || sha != ev.PolicySHA256 ||
		policy.Network != "none" || len(policy.FilesystemMounts) != 0 {
		return fmt.Errorf("%w: failed creation loss inspection requires a valid immutable standalone policy", ErrConflict)
	}
	return nil
}

// BindWorkspaceCreator names the exact durable creator snapshot. The store
// repeats this proof in the transaction which installs the inspection guard.
func BindWorkspaceCreator(ev *WorkspaceInspectEvidence, creator Operation) {
	ev.CreatorOperationID = creator.ID
	ev.CreatorRequestSHA256 = digest(creator.Request)
	ev.CreatorEvidenceSHA256 = digest(creator.Evidence)
}

func validateWorkspaceCreatorTx(ctx context.Context, tx *sql.Tx, session Session, ev WorkspaceInspectEvidence) error {
	var key string
	if err := tx.QueryRowContext(ctx, `SELECT idempotency_key FROM operations WHERE id=?`, ev.CreatorOperationID).Scan(&key); err != nil {
		return ErrConflict
	}
	creator, err := getOperationTx(ctx, tx, key)
	if err != nil || digest(creator.Request) != ev.CreatorRequestSHA256 || digest(creator.Evidence) != ev.CreatorEvidenceSHA256 ||
		ev.ImageFingerprint != ev.BaseFingerprint || ev.OriginalStatus != "Stopped" {
		return ErrConflict
	}
	return AssembledCreationForLoss(session, creator, ev.BaseFingerprint)
}

// Immutable inspection source evidence cannot be replaced by a stale worker
// or Retry. Only the bounded accepted result may be added.
func immutableWorkspaceSource(oldRaw, nextRaw json.RawMessage) error {
	var old, next WorkspaceInspectEvidence
	if json.Unmarshal(oldRaw, &old) != nil || json.Unmarshal(nextRaw, &next) != nil {
		return ErrInvalid
	}
	oldResult, nextResult := old.Result, next.Result
	old.Result, next.Result = nil, nil
	a, _ := json.Marshal(old)
	b, _ := json.Marshal(next)
	if string(a) != string(b) || len(oldResult) != 0 && string(oldResult) != string(nextResult) {
		return ErrConflict
	}
	return nil
}
