package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

type refRepairPreviewState struct {
	Preview control.RefRepairPreview
	Expiry  time.Time
}

// Workspace loss inspection completes in the inspected phase. Ref repair
// accepts only that terminal result, including after a daemon restart.
func completedRefRepairLoss(op control.Operation, uuid, project string) bool {
	return op.Kind == "workspace.loss.inspect" && op.Status == "completed" &&
		op.Phase == "inspected" && op.SessionUUID == uuid && op.Project == project
}

func (l *lifecycle) PreviewRefRepair(ctx context.Context, uuid, lossID string) (control.RefRepairPreview, error) {
	if len(uuid) != 36 || len(lossID) != 36 {
		return control.RefRepairPreview{}, control.ErrInvalid
	}
	release, err := l.lockSession(ctx, uuid)
	if err != nil {
		return control.RefRepairPreview{}, err
	}
	defer release()
	s, err := l.store.GetSession(ctx, uuid)
	if err != nil || s.Registry != "established" {
		return control.RefRepairPreview{}, errors.Join(err, control.ErrConflict)
	}
	native, err := l.runtimeSession(ctx, s)
	if err != nil {
		return control.RefRepairPreview{}, err
	}
	preview := control.RefRepairPreview{Kind: "missing_assigned_ref", SessionUUID: uuid, Project: s.Project,
		Branch: s.Branch, AssignedRef: "refs/heads/" + s.Branch, AssignedRefStatus: "unknown",
		IncusProject: l.cfg.IncusProject, InstanceName: "p-" + uuid, ImageFingerprint: native.ImageFingerprint,
		PolicySHA256: s.PolicySHA256, LossOperationID: lossID, UnsafeReasons: []string{}}
	unsafe := func(reason string) { preview.UnsafeReasons = append(preview.UnsafeReasons, reason) }
	if l.policyCondition(ctx, s) != "current" {
		unsafe("policy_changed")
	}
	lossOp, err := l.store.GetOperation(ctx, lossID)
	if err != nil || !completedRefRepairLoss(lossOp, uuid, s.Project) {
		unsafe("loss_snapshot_unavailable")
		return preview, nil
	}
	var lossEv control.WorkspaceInspectEvidence
	var loss workspaceLossResult
	if json.Unmarshal(lossOp.Evidence, &lossEv) != nil || json.Unmarshal(lossEv.Result, &loss) != nil ||
		loss.Schema != "p.workspace-loss/v1" || len(loss.Fingerprint) != 64 ||
		lossEv.InstanceUUID != l.instanceID || lossEv.ImageFingerprint != native.ImageFingerprint {
		unsafe("loss_snapshot_unavailable")
		return preview, nil
	}
	preview.LossFingerprint = loss.Fingerprint
	if len(loss.Worktrees) != 1 || len(loss.ExternalWorktrees) != 0 || len(loss.Worktrees[0].Changes) > 4096 ||
		loss.Worktrees[0].Path != "/workspace" || loss.Worktrees[0].Location != "runtime" ||
		loss.Worktrees[0].Branch != s.Branch || loss.Worktrees[0].HeadOID == "" {
		unsafe("local_worktree_unsupported")
		return preview, nil
	}
	tree := loss.Worktrees[0]
	preview.LocalTip = tree.HeadOID
	preview.Changes = make([]control.RefRepairChange, 0, len(tree.Changes))
	for _, change := range tree.Changes {
		preview.Changes = append(preview.Changes, control.RefRepairChange{Code: change.Code, Path: change.Path})
	}
	preview.Ignored = control.RefRepairIgnored{Count: tree.Ignored.Count, LogicalBytes: tree.Ignored.LogicalBytes}
	localRef := 0
	for _, ref := range loss.LocalRefs {
		if ref.Name == preview.AssignedRef {
			localRef++
			if ref.OID != preview.LocalTip {
				unsafe("local_branch_tip_mismatch")
			}
		}
	}
	if localRef != 1 {
		unsafe("local_branch_unavailable")
	}
	observed, err := l.runtime.Inspect(ctx, native)
	if err != nil {
		return control.RefRepairPreview{}, err
	}
	if !observed.Exists || observed.IncusUUID != lossEv.SourceIncusUUID || observed.Generation != lossEv.SourceGeneration ||
		observed.Fingerprint != lossEv.ImageFingerprint || observed.Status != lossEv.OriginalStatus ||
		observed.Status != "Running" && observed.Status != "Stopped" {
		unsafe("runtime_changed")
	} else {
		preview.IncusUUID, preview.Generation, preview.RuntimeStatus = observed.IncusUUID, observed.Generation, observed.Status
	}
	current, exists, err := l.git.backend.InspectBranchRef(ctx, s.Project, s.Branch)
	if err != nil {
		return control.RefRepairPreview{}, err
	}
	if exists || current != "" {
		preview.AssignedRefStatus = "present"
		unsafe("assigned_ref_present")
	} else {
		preview.AssignedRefStatus = "missing"
	}
	refs, _, err := l.git.backend.RetainedLossRefs(ctx, s.Project, nil)
	if err != nil {
		return control.RefRepairPreview{}, err
	}
	if !reflect.DeepEqual(loss.PRefs, workspaceLossRefs(refs)) {
		unsafe("p_refs_changed")
	}
	present, err := l.git.backend.PCommitPresent(ctx, s.Project, preview.LocalTip)
	if err != nil {
		return control.RefRepairPreview{}, err
	}
	if !present {
		unsafe("p_object_missing")
	}
	principal, registered, err := l.store.SessionGitPrincipal(ctx, uuid)
	if err != nil {
		return control.RefRepairPreview{}, err
	}
	active, err := l.store.IsSessionGitPrincipalActive(ctx, uuid)
	if err != nil {
		return control.RefRepairPreview{}, err
	}
	if !registered || !active {
		unsafe("credential_unavailable")
	} else {
		preview.CredentialFingerprint = principal
		if key, e := inspectRegisteredSessionKey(l.store.StateDir(), uuid); e != nil || key != principal {
			unsafe("credential_unavailable")
		}
	}
	if err = l.checkNoWorkspaceInspect(ctx, uuid); err != nil {
		unsafe("operation_active")
	}
	l.mu.Lock()
	attached := l.hasAttachmentLocked(uuid)
	l.mu.Unlock()
	if attached {
		unsafe("attachment_active")
	}
	completed, err := time.Parse(time.RFC3339Nano, lossOp.UpdatedAt)
	if err != nil || !time.Now().Before(completed.Add(repairPreviewTTL)) {
		unsafe("loss_snapshot_expired")
	}
	if len(preview.UnsafeReasons) != 0 {
		return preview, nil
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return control.RefRepairPreview{}, err
	}
	preview.Eligible = true
	preview.ConfirmationToken = hex.EncodeToString(token[:])
	expiry := time.Now().Add(repairPreviewTTL)
	if limit := completed.Add(repairPreviewTTL); limit.Before(expiry) {
		expiry = limit
	}
	preview.ExpiresAt = expiry.UTC().Format(time.RFC3339Nano)
	encoded, err := json.Marshal(map[string]any{"v": 1, "preview": preview})
	if err != nil || len(encoded) > control.MaxFrameBytes-1024 {
		return control.RefRepairPreview{}, errors.New("ref repair preview exceeds bounded response")
	}
	l.mu.Lock()
	if l.refRepairPreviews == nil {
		l.refRepairPreviews = map[string]refRepairPreviewState{}
	}
	for key, state := range l.refRepairPreviews {
		if !time.Now().Before(state.Expiry) {
			delete(l.refRepairPreviews, key)
		}
	}
	if len(l.refRepairPreviews) >= 128 {
		l.mu.Unlock()
		return control.RefRepairPreview{}, errors.New("ref repair preview capacity unavailable")
	}
	l.refRepairPreviews[preview.ConfirmationToken] = refRepairPreviewState{Preview: preview, Expiry: expiry}
	l.mu.Unlock()
	return preview, nil
}

func workspaceLossRefs(refs []plugin.GitRef) []workspaceLossRef {
	out := make([]workspaceLossRef, 0, len(refs))
	for _, ref := range refs {
		out = append(out, workspaceLossRef{Name: ref.Ref, OID: ref.OID})
	}
	return out
}

func refRepairDigest(raw []byte) string {
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func (l *lifecycle) blockRefRepair(op control.Operation, cause error) {
	if l.ctx.Err() != nil {
		return
	}
	message := fmt.Sprintf("ref repair blocked: %v", cause)
	if len(message) > 900 {
		message = message[:900]
	}
	_ = l.store.AdvanceOperation(l.ctx, op.ID, "blocked", op.Phase, op.Committed, op.Evidence, message)
}

func (l *lifecycle) advanceRefRepair(op *control.Operation, ev control.RefRepairEvidence, phase string, committed bool) error {
	raw, err := json.Marshal(ev)
	if err != nil || len(raw) > 2048 {
		return control.ErrInvalid
	}
	if err = l.store.AdvanceOperation(l.ctx, op.ID, "running", phase, committed, raw, ""); err != nil {
		return err
	}
	op.Phase, op.Evidence, op.Committed = phase, raw, committed
	return nil
}

func (l *lifecycle) ConfirmRefRepair(ctx context.Context, key, uuid, token string) (control.Operation, error) {
	if len(key) == 0 || len(key) > 128 || len(uuid) != 36 || len(token) != 32 {
		return control.Operation{}, control.ErrInvalid
	}
	release, err := l.lockSession(ctx, uuid)
	if err != nil {
		return control.Operation{}, err
	}
	defer release()
	if prior, e := l.store.GetOperationByKey(ctx, key); e == nil {
		var saved control.RefRepairRequest
		hash := sha256.Sum256([]byte(token))
		if prior.Kind != "session.ref.repair" || json.Unmarshal(prior.Request, &saved) != nil ||
			saved.Key != key || saved.UUID != uuid || saved.TokenSHA256 != hex.EncodeToString(hash[:]) {
			return control.Operation{}, control.ErrConflict
		}
		if prior.Status == "running" {
			_ = l.enqueue(prior.ID)
		}
		return prior, nil
	} else if !errors.Is(e, control.ErrNotFound) {
		return control.Operation{}, e
	}
	l.mu.Lock()
	state, found := l.refRepairPreviews[token]
	l.mu.Unlock()
	if !found || !time.Now().Before(state.Expiry) || !state.Preview.Eligible || state.Preview.SessionUUID != uuid {
		return control.Operation{}, control.ErrConflict
	}
	p := state.Preview
	s, err := l.store.GetSession(ctx, uuid)
	if err != nil || s.Registry != "established" || s.Project != p.Project || s.Branch != p.Branch || s.PolicySHA256 != p.PolicySHA256 ||
		l.policyCondition(ctx, s) != "current" {
		return control.Operation{}, control.ErrConflict
	}
	native, err := l.runtimeSession(ctx, s)
	if err != nil || native.ImageFingerprint != p.ImageFingerprint {
		return control.Operation{}, control.ErrConflict
	}
	observed, err := l.runtime.Inspect(ctx, native)
	if err != nil || !observed.Exists || observed.Status != p.RuntimeStatus || observed.IncusUUID != p.IncusUUID ||
		observed.Generation != p.Generation || observed.Fingerprint != p.ImageFingerprint {
		return control.Operation{}, errors.Join(err, control.ErrConflict)
	}
	principal, registered, err := l.store.SessionGitPrincipal(ctx, uuid)
	if err != nil || !registered || principal != p.CredentialFingerprint {
		return control.Operation{}, control.ErrConflict
	}
	active, err := l.store.IsSessionGitPrincipalActive(ctx, uuid)
	if err != nil || !active {
		return control.Operation{}, control.ErrConflict
	}
	if keyFP, e := inspectRegisteredSessionKey(l.store.StateDir(), uuid); e != nil || keyFP != principal {
		return control.Operation{}, control.ErrConflict
	}
	if err := l.checkNoWorkspaceInspect(ctx, uuid); err != nil {
		return control.Operation{}, err
	}
	l.mu.Lock()
	attached := l.hasAttachmentLocked(uuid)
	l.mu.Unlock()
	if attached {
		return control.Operation{}, control.ErrConflict
	}
	lossOp, err := l.store.GetOperation(ctx, p.LossOperationID)
	if err != nil || !completedRefRepairLoss(lossOp, uuid, p.Project) {
		return control.Operation{}, control.ErrConflict
	}
	var lossEv control.WorkspaceInspectEvidence
	var loss workspaceLossResult
	if json.Unmarshal(lossOp.Evidence, &lossEv) != nil || json.Unmarshal(lossEv.Result, &loss) != nil ||
		loss.Fingerprint != p.LossFingerprint || lossEv.SourceIncusUUID != p.IncusUUID || lossEv.SourceGeneration != p.Generation {
		return control.Operation{}, control.ErrConflict
	}
	hash := sha256.Sum256([]byte(token))
	req := control.RefRepairRequest{Key: key, UUID: uuid, TokenSHA256: hex.EncodeToString(hash[:])}
	ev := control.RefRepairEvidence{Project: p.Project, Branch: p.Branch, Tip: p.LocalTip, PolicySHA256: p.PolicySHA256,
		CredentialFingerprint: principal, InstanceUUID: l.instanceID, IncusProject: p.IncusProject,
		InstanceName: p.InstanceName, ImageFingerprint: p.ImageFingerprint, IncusUUID: p.IncusUUID,
		Generation: p.Generation, OriginalStatus: p.RuntimeStatus, LossOperationID: p.LossOperationID,
		LossFingerprint: p.LossFingerprint, LossResultSHA256: refRepairDigest(lossOp.Evidence),
		PreviewExpiresAt: state.Expiry.UTC().Format(time.RFC3339Nano)}
	verify := func(call context.Context) error {
		current, exists, e := l.git.backend.InspectBranchRef(call, ev.Project, ev.Branch)
		if e != nil || exists || current != "" {
			return errors.Join(e, control.ErrConflict)
		}
		refs, _, e := l.git.backend.RetainedLossRefs(call, ev.Project, nil)
		if e != nil || !reflect.DeepEqual(workspaceLossRefs(refs), loss.PRefs) {
			return errors.Join(e, control.ErrConflict)
		}
		present, e := l.git.backend.PCommitPresent(call, ev.Project, ev.Tip)
		if e != nil || !present {
			return errors.Join(e, control.ErrConflict)
		}
		return nil
	}
	op, err := l.store.BeginRefRepair(ctx, req, ev, verify)
	if err != nil {
		return control.Operation{}, err
	}
	l.mu.Lock()
	delete(l.refRepairPreviews, token)
	l.mu.Unlock()
	if err = l.enqueue(op.ID); err != nil {
		return op, err
	}
	return op, nil
}

func (l *lifecycle) refRepairRef(ctx context.Context, ev control.RefRepairEvidence, created bool) error {
	current, exists, err := l.git.backend.InspectBranchRef(ctx, ev.Project, ev.Branch)
	if err != nil {
		return err
	}
	if created && (!exists || current != ev.Tip) || !created && (exists || current != "") {
		return control.ErrConflict
	}
	return nil
}

func (l *lifecycle) refRepairSource(ctx context.Context, source runtimeincus.Session, ev control.RefRepairEvidence, status string) error {
	observed, err := l.runtime.Inspect(ctx, source)
	if err != nil {
		return err
	}
	return refRepairSourceMatches(observed, ev, status)
}

func refRepairSourceMatches(observed runtimeincus.Observation, ev control.RefRepairEvidence, status string) error {
	if !observed.Exists || observed.Name != ev.InstanceName || observed.IncusUUID != ev.IncusUUID ||
		observed.Generation != ev.Generation || observed.Fingerprint != ev.ImageFingerprint || observed.Status != status {
		return control.ErrConflict
	}
	return nil
}

func (l *lifecycle) rollbackRefRepair(op control.Operation, ev control.RefRepairEvidence, source runtimeincus.Session, cause error) {
	if op.Phase != "guarded" && op.Phase != "quiesced" {
		l.blockRefRepair(op, cause)
		return
	}
	if op.Phase == "quiesced" && ev.OriginalStatus == "Running" {
		if err := l.runtime.ThawWorkspaceSourceExact(l.ctx, source, ev.IncusUUID, ev.Generation); err != nil {
			l.blockRefRepair(op, errors.Join(cause, err))
			return
		}
	}
	if err := l.store.EndRefRepair(l.ctx, op.ID, false, func(ctx context.Context, got control.RefRepairEvidence) error {
		if err := l.refRepairRef(ctx, got, false); err != nil {
			return err
		}
		return l.refRepairSource(ctx, source, got, got.OriginalStatus)
	}); err != nil {
		l.blockRefRepair(op, errors.Join(cause, err))
	}
}

func (l *lifecycle) processRefRepair(op control.Operation) {
	if op.Status != "running" {
		return
	}
	var ev control.RefRepairEvidence
	if json.Unmarshal(op.Evidence, &ev) != nil || ev.InstanceUUID != l.instanceID || ev.IncusProject != l.cfg.IncusProject ||
		ev.InstanceName != "p-"+op.SessionUUID || ev.Project != op.Project {
		l.blockRefRepair(op, errors.New("ref repair durable identity unavailable"))
		return
	}
	ctx := l.ctx
	s, err := l.store.GetSession(ctx, op.SessionUUID)
	if err != nil || s.Registry != "established" || s.Project != ev.Project || s.Branch != ev.Branch || s.PolicySHA256 != ev.PolicySHA256 ||
		l.policyCondition(ctx, s) != "current" {
		l.blockRefRepair(op, errors.Join(err, errors.New("ref repair assignment changed")))
		return
	}
	source, err := l.runtimeSession(ctx, s)
	if err != nil || source.ImageFingerprint != ev.ImageFingerprint {
		l.blockRefRepair(op, errors.Join(err, errors.New("ref repair runtime selection changed")))
		return
	}
	lossOp, err := l.store.GetOperation(ctx, ev.LossOperationID)
	var lossEv control.WorkspaceInspectEvidence
	var loss workspaceLossResult
	if err != nil || !completedRefRepairLoss(lossOp, op.SessionUUID, ev.Project) ||
		refRepairDigest(lossOp.Evidence) != ev.LossResultSHA256 ||
		json.Unmarshal(lossOp.Evidence, &lossEv) != nil || json.Unmarshal(lossEv.Result, &loss) != nil ||
		loss.Fingerprint != ev.LossFingerprint {
		l.blockRefRepair(op, errors.New("ref repair source snapshot unavailable"))
		return
	}
	entryPhase := op.Phase
	for step := 0; step < 9; step++ {
		switch op.Phase {
		case "guarded":
			if err := l.refRepairRef(ctx, ev, false); err != nil {
				l.rollbackRefRepair(op, ev, source, err)
				return
			}
			if err := l.refRepairSource(ctx, source, ev, ev.OriginalStatus); err != nil {
				l.rollbackRefRepair(op, ev, source, err)
				return
			}
			if ev.OriginalStatus == "Running" {
				err := l.runtime.FreezeWorkspaceSourceExact(ctx, source, ev.IncusUUID, ev.Generation, func() error {
					return l.advanceRefRepair(&op, ev, "freeze-intent", false)
				})
				if err != nil {
					if op.Phase == "guarded" {
						l.rollbackRefRepair(op, ev, source, err)
					} else {
						l.blockRefRepair(op, err)
					}
					return
				}
			}
			if err := l.advanceRefRepair(&op, ev, "quiesced", false); err != nil {
				l.blockRefRepair(op, err)
				return
			}
		case "freeze-intent":
			if entryPhase == "freeze-intent" {
				if err := l.runtime.ReconcileWorkspaceFreezeExact(ctx, source, ev.IncusUUID, ev.Generation); err != nil {
					l.blockRefRepair(op, err)
					return
				}
			}
			if err := l.advanceRefRepair(&op, ev, "quiesced", false); err != nil {
				l.blockRefRepair(op, err)
				return
			}
		case "quiesced":
			quiescentStatus := "Stopped"
			if ev.OriginalStatus == "Running" {
				quiescentStatus = "Frozen"
			}
			if err := l.refRepairSource(ctx, source, ev, quiescentStatus); err != nil {
				l.blockRefRepair(op, errors.Join(err, errors.New("exact quiesced source changed before export")))
				return
			}
			fresh, err := l.runtime.ExportWorkspaceLoss(ctx, source)
			if err != nil {
				l.rollbackRefRepair(op, ev, source, err)
				return
			}
			if err := l.refRepairSource(ctx, source, ev, quiescentStatus); err != nil {
				l.blockRefRepair(op, errors.Join(err, errors.New("exact quiesced source changed after export")))
				return
			}
			reviewed := loss
			reviewed.Fingerprint = ""
			fingerprint, err := workspaceLossFingerprint(s, ev.IncusProject, ev.InstanceName, lossEv, fresh, reviewed)
			if err != nil || fingerprint != ev.LossFingerprint {
				l.rollbackRefRepair(op, ev, source, errors.Join(err, errors.New("workspace bytes changed since review")))
				return
			}
			issued, err := l.store.RefRepairGuardedCreate(ctx, op.ID, func(call context.Context, got control.RefRepairEvidence) error {
				if err := l.refRepairRef(call, got, false); err != nil {
					return err
				}
				refs, _, e := l.git.backend.RetainedLossRefs(call, got.Project, nil)
				if e != nil || !reflect.DeepEqual(workspaceLossRefs(refs), loss.PRefs) {
					return errors.Join(e, control.ErrConflict)
				}
				present, e := l.git.backend.PCommitPresent(call, got.Project, got.Tip)
				if e != nil || !present {
					return errors.Join(e, control.ErrConflict)
				}
				return nil
			}, func(call context.Context, got control.RefRepairEvidence) error {
				return l.git.backend.CreateRestoredAssignedRefExact(call, op.ID, got.Project, got.Branch, got.Tip)
			})
			if err != nil {
				if issued {
					op.Phase = "ref-create-issued"
					l.blockRefRepair(op, err)
				} else {
					l.rollbackRefRepair(op, ev, source, err)
				}
				return
			}
			op.Phase = "ref-create-issued"
			if err := l.advanceRefRepair(&op, ev, "ref-created", true); err != nil {
				l.blockRefRepair(op, err)
				return
			}
		case "ref-create-issued":
			// The native request can arrive after timeout. A missing ref does not
			// authorize a second attempt; only exact positive presence advances.
			if err := l.refRepairRef(ctx, ev, true); err != nil {
				l.blockRefRepair(op, err)
				return
			}
			if err := l.advanceRefRepair(&op, ev, "ref-created", true); err != nil {
				l.blockRefRepair(op, err)
				return
			}
		case "ref-created":
			if err := l.refRepairRef(ctx, ev, true); err != nil {
				l.blockRefRepair(op, err)
				return
			}
			if ev.OriginalStatus == "Running" {
				if err := l.runtime.ThawWorkspaceSourceExact(ctx, source, ev.IncusUUID, ev.Generation); err != nil {
					l.blockRefRepair(op, err)
					return
				}
			}
			if err := l.refRepairSource(ctx, source, ev, ev.OriginalStatus); err != nil {
				l.blockRefRepair(op, err)
				return
			}
			if err := l.advanceRefRepair(&op, ev, "source-restored", true); err != nil {
				l.blockRefRepair(op, err)
				return
			}
		case "source-restored":
			if err := l.store.EndRefRepair(ctx, op.ID, true, func(call context.Context, got control.RefRepairEvidence) error {
				if err := l.refRepairRef(call, got, true); err != nil {
					return err
				}
				return l.refRepairSource(call, source, got, got.OriginalStatus)
			}); err != nil {
				l.blockRefRepair(op, err)
			}
			return
		default:
			l.blockRefRepair(op, control.ErrConflict)
			return
		}
	}
	l.blockRefRepair(op, errors.New("ref repair phase bound exceeded"))
}
