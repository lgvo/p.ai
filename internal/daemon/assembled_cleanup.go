package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

func (l *lifecycle) reviewAssembledCleanupLoss(ctx context.Context, session control.Session, creator control.Operation, lossID string) (*control.RemovalRuntimePreview, error) {
	lossOp, err := l.store.GetOperation(ctx, lossID)
	var proof control.WorkspaceInspectEvidence
	var loss workspaceLossResult
	finished, timeErr := time.Parse(time.RFC3339Nano, lossOp.UpdatedAt)
	if err != nil || timeErr != nil || time.Since(finished) < 0 || time.Since(finished) > removalPreviewTTL ||
		lossOp.Kind != "workspace.loss.inspect" || lossOp.Status != "completed" || lossOp.Phase != "inspected" || lossOp.SessionUUID != session.UUID || lossOp.Project != session.Project ||
		json.Unmarshal(lossOp.Evidence, &proof) != nil || proof.CreatorOperationID != creator.ID || proof.CreatorRequestSHA256 != hexDigest(creator.Request) || proof.CreatorEvidenceSHA256 != hexDigest(creator.Evidence) ||
		proof.InstanceUUID != l.instanceID || proof.BaseFingerprint != l.cfg.BaseImageFingerprint || proof.ImageFingerprint != proof.BaseFingerprint || proof.OriginalStatus != "Stopped" ||
		len(proof.Result) == 0 || len(proof.Result) > 12<<10 || json.Unmarshal(proof.Result, &loss) != nil || loss.Schema != "p.workspace-loss/v1" || !loss.RuntimeDataWillBeRemoved || len(loss.Fingerprint) != 64 ||
		len(loss.Worktrees) != 1 || loss.Worktrees[0].Path != "/workspace" || len(loss.ExternalWorktrees) != 0 {
		return nil, errors.New("recent completed creator-bound standalone loss inspection required")
	}
	source, err := l.runtimeSession(ctx, session)
	if err != nil {
		return nil, err
	}
	if err = l.runtime.CheckAssembledCreationSource(ctx, source, creator.ID, proof.SourceIncusUUID, proof.SourceGeneration); err != nil {
		return nil, err
	}
	refs, err := l.git.backend.AssignedBranchSnapshot(ctx, session.Project, session.Branch, false)
	if err != nil || !slices.EqualFunc(loss.PRefs, refs.PRefs, func(a workspaceLossRef, b plugin.GitRef) bool { return a.Name == b.Ref && a.OID == b.OID }) {
		return nil, errors.Join(err, control.ErrConflict)
	}
	return &control.RemovalRuntimePreview{Condition: "present", IncusProject: l.cfg.IncusProject, InstanceName: "p-" + session.UUID,
		IncusUUID: proof.SourceIncusUUID, Generation: proof.SourceGeneration, ImageFingerprint: proof.ImageFingerprint, OriginalStatus: "Stopped",
		LossOperationID: lossOp.ID, ObservedAt: lossOp.UpdatedAt, Fingerprint: loss.Fingerprint, Loss: proof.Result}, nil
}

func assembledCleanupWorkspaceEvidence(ev control.CreateCleanupEvidence) control.WorkspaceInspectEvidence {
	r := ev.Review.Runtime
	return control.WorkspaceInspectEvidence{InstanceUUID: ev.InstanceUUID, BaseFingerprint: ev.Review.ImageFingerprint, ImageFingerprint: ev.Review.ImageFingerprint,
		SourceIncusUUID: r.IncusUUID, SourceGeneration: r.Generation, OriginalStatus: "Stopped", CreatorOperationID: ev.Review.OldOperationID,
		CreatorRequestSHA256: ev.Review.OldRequestSHA256, CreatorEvidenceSHA256: ev.Review.OldEvidenceSHA256}
}

// The caller owns the session lock and reloads the durable operation before
// entering. It never starts the source and uses only the existing loss helper.
func (l *lifecycle) processAssembledCreateCleanup(op control.Operation, ev control.CreateCleanupEvidence) {
	ctx := l.ctx
	r := ev.Review.Runtime
	block := func(cause error) {
		if ctx.Err() != nil {
			return
		}
		message := fmt.Sprintf("assembled failed creation cleanup blocked: %v", cause)
		if len(message) > 900 {
			message = message[:900]
		}
		_ = l.store.AdvanceOperation(ctx, op.ID, "blocked", op.Phase, op.Committed, op.Evidence, message)
	}
	if r == nil || ev.InstanceUUID != l.instanceID || r.IncusProject != l.cfg.IncusProject || ev.Review.ImageFingerprint != l.cfg.BaseImageFingerprint || r.OriginalStatus != "Stopped" ||
		l.store.ValidateAssembledCleanupAuthority(ctx, op) != nil {
		block(errors.New("durable creator/session/ref authority changed"))
		return
	}
	session, err := l.store.GetSession(ctx, op.SessionUUID)
	if err != nil {
		block(err)
		return
	}
	creator, err := l.store.GetOperation(ctx, ev.Review.OldOperationID)
	created, evidenceErr := control.Evidence(creator)
	if err != nil || evidenceErr != nil || created.Selection != l.selection() {
		block(errors.New("pinned creator module selection unavailable"))
		return
	}
	source, err := l.runtimeSession(ctx, session)
	if err != nil {
		block(err)
		return
	}
	helper := runtimeincus.WorkspaceHelper(ev.InstanceUUID, op.ID, op.SessionUUID, op.Project, ev.Review.ImageFingerprint)
	entry := op.Phase
	advance := func(phase string) error {
		raw, e := json.Marshal(ev)
		if e != nil || len(raw) > 16384 {
			return errors.New("assembled cleanup evidence exceeded bound")
		}
		if e = l.store.AdvanceOperation(ctx, op.ID, "running", phase, op.Committed, raw, ""); e != nil {
			return e
		}
		op.Phase, op.Evidence, op.Status = phase, raw, "running"
		return nil
	}
	proveSource := func(call context.Context) error {
		return l.runtime.CheckAssembledCreationSource(call, source, ev.Review.OldOperationID, r.IncusUUID, r.Generation)
	}
	proveAbsent := func(call context.Context) error { return l.createCleanupNativeAbsent(call, ev.Review, ev.InstanceUUID) }
	verifyRefs := func(call context.Context) error {
		refs, e := l.git.backend.AssignedBranchSnapshot(call, op.Project, session.Branch, false)
		var reviewed workspaceLossResult
		if e != nil {
			return e
		}
		if json.Unmarshal(r.Loss, &reviewed) != nil || refs.AssignedTip != ev.Review.AssignedBranch.OID || !slices.EqualFunc(reviewed.PRefs, refs.PRefs, func(a workspaceLossRef, b plugin.GitRef) bool { return a.Name == b.Ref && a.OID == b.OID }) {
			return control.ErrConflict
		}
		return nil
	}
	verifyLocal := func() error {
		l.endpoints.mu.Lock()
		e := checkReplacementEndpointsLocked(l.endpoints, ev.Review.Provisional.Cleanup, false)
		l.endpoints.mu.Unlock()
		if e != nil {
			return e
		}
		return checkReplacementKey(l.store.StateDir(), ev.Review.Provisional.Cleanup, false)
	}
	stale := func(reason error) {
		if op.Committed {
			block(reason)
			return
		}
		if op.Phase != "stale-cleanup" {
			if e := advance("stale-cleanup"); e != nil {
				block(e)
				return
			}
		}
		if e := l.runtime.DeleteWorkspaceHelper(ctx, helper); e != nil {
			block(e)
			return
		}
		if observed, e := l.runtime.InspectWorkspaceHelper(ctx, helper); e != nil || observed.Exists {
			block(errors.Join(e, errors.New("stale cleanup helper absence unavailable")))
			return
		}
		if e := l.store.FailStaleAssembledCleanup(ctx, op, "assembled cleanup confirmation stale; preserve source and review fresh loss: "+reason.Error()); e != nil {
			block(e)
			return
		}
		l.recordProgress(op, "failed")
	}
	if op.Status == "blocked" {
		if err = advance(op.Phase); err != nil {
			return
		}
	}
	if !op.Committed && op.Phase != "stale-cleanup" && op.Phase != "init-issued" {
		if err = proveSource(ctx); err != nil {
			stale(err)
			return
		}
		if err = verifyRefs(ctx); err != nil {
			stale(err)
			return
		}
		if err = verifyLocal(); err != nil {
			stale(err)
			return
		}
	}
	switch op.Phase {
	case "helper-intent":
		if err = l.runtime.CheckWorkspaceHelperCapacity(ctx); err != nil {
			stale(err)
			return
		}
		observed, e := l.runtime.InspectWorkspaceHelper(ctx, helper)
		if e != nil || observed.Exists {
			block(errors.Join(e, errors.New("cleanup helper unexpectedly present before init marker")))
			return
		}
		if _, e = l.runtime.CreateWorkspaceHelper(ctx, helper, func() error { return advance("init-issued") }); e != nil {
			block(e)
			return
		}
		fallthrough
	case "init-issued":
		var observed runtimeincus.Observation
		var e error
		if entry == "init-issued" {
			observed, e = l.runtime.ReconcileWorkspaceHelperInit(ctx, helper)
		} else {
			observed, e = l.runtime.InspectWorkspaceHelper(ctx, helper)
		}
		if e != nil || !observed.Exists {
			block(errors.Join(e, errors.New("cleanup helper init outcome unresolved")))
			return
		}
		if observed.Status == "Stopped" {
			e = l.runtime.StartWorkspaceHelper(ctx, helper)
		} else if observed.Status == "Running" {
			e = l.runtime.VerifyWorkspaceHelperBoot(ctx, helper)
		} else {
			e = errors.New("cleanup helper status unavailable")
		}
		if e != nil {
			block(e)
			return
		}
		if e = advance("helper-ready"); e != nil {
			block(e)
			return
		}
		fallthrough
	case "helper-ready":
		if err = proveSource(ctx); err != nil {
			stale(err)
			return
		}
		if err = advance("quiescent"); err != nil {
			block(err)
			return
		}
		fallthrough
	case "quiescent":
		if entry == "quiescent" {
			stale(errors.New("fresh workspace analysis interrupted before commitment"))
			return
		}
		fresh, e := l.inspectWorkspaceLossResult(ctx, session, source, helper, assembledCleanupWorkspaceEvidence(ev))
		if e != nil {
			stale(e)
			return
		}
		var loss workspaceLossResult
		if json.Unmarshal(fresh, &loss) != nil || loss.Fingerprint != r.Fingerprint {
			stale(errors.New("workspace bytes or P refs changed"))
			return
		}
		if e = advance("analyzed"); e != nil {
			block(e)
			return
		}
		fallthrough
	case "analyzed":
		if entry == "analyzed" {
			stale(errors.New("accepted analysis interrupted before deletion; review fresh loss"))
			return
		}
		if err = l.runtime.DeleteWorkspaceHelper(ctx, helper); err != nil {
			block(err)
			return
		}
		if err = advance("validated"); err != nil {
			block(err)
			return
		}
		fallthrough
	case "validated":
		if entry == "validated" {
			stale(errors.New("pre-deletion validation interrupted; review fresh loss"))
			return
		}
		if err = verifyRefs(ctx); err != nil {
			stale(err)
			return
		}
		if err = proveSource(ctx); err != nil {
			stale(err)
			return
		}
		if err = verifyLocal(); err != nil {
			stale(err)
			return
		}
		if observed, e := l.runtime.InspectWorkspaceHelper(ctx, helper); e != nil || observed.Exists {
			block(errors.Join(e, errors.New("cleanup helper absence unavailable before commitment")))
			return
		}
		err = l.runtime.DeleteForDiscardExact(ctx, source, r.IncusUUID, r.Generation, func() error {
			e := l.store.CommitAssembledCleanupDelete(ctx, op, func(call context.Context) error {
				if e := proveSource(call); e != nil {
					return e
				}
				if e := verifyRefs(call); e != nil {
					return e
				}
				return verifyLocal()
			})
			if e == nil {
				op.Committed, op.Phase = true, "delete-issued"
			}
			return e
		})
		if err != nil {
			if !op.Committed {
				stale(err)
			} else {
				block(err)
			}
			return
		}
		ev.RuntimeAbsent = true
		if err = advance("runtime-absent"); err != nil {
			block(err)
			return
		}
		fallthrough
	case "runtime-absent":
		if err = proveAbsent(ctx); err != nil {
			block(err)
			return
		}
		if err = l.runtime.DeleteWorkspaceHelper(ctx, helper); err != nil {
			block(err)
			return
		}
		if err = cleanupReplacementLocal(ctx, l.store.StateDir(), l.endpoints, ev.Review.Provisional.Cleanup, proveAbsent); err != nil {
			block(err)
			return
		}
		ev.LocalComplete = true
		if err = advance("local-complete"); err != nil {
			block(err)
			return
		}
		fallthrough
	case "local-complete":
		err = l.store.CompleteCreateCleanup(ctx, op.ID, func(call context.Context, accepted control.CreateCleanupEvidence) error {
			if !reflect.DeepEqual(accepted, ev) {
				return control.ErrConflict
			}
			if e := proveAbsent(call); e != nil {
				return e
			}
			if e := l.createCleanupLocalAbsent(op.SessionUUID); e != nil {
				return e
			}
			tip, exists, e := l.git.backend.InspectBranchRef(call, op.Project, session.Branch)
			if e != nil || !exists || tip != ev.Review.AssignedBranch.OID {
				return errors.Join(e, control.ErrConflict)
			}
			return nil
		})
		if err != nil {
			block(err)
			return
		}
		l.recordProgress(op, "completed")
	case "delete-issued":
		block(fmt.Errorf("native delete outcome unresolved for %s UUID %s generation %s; exact administrative reconciliation required, no name-based replay", r.InstanceName, r.IncusUUID, r.Generation))
	case "stale-cleanup":
		stale(errors.New("prior pre-deletion review stale"))
	default:
		block(errors.New("assembled cleanup phase unavailable"))
	}
}
