package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

func (l *lifecycle) RenameSession(ctx context.Context, req control.RenameRequest) (control.Operation, error) {
	if !control.ValidRenameRequest(req) {
		return control.Operation{}, control.ErrInvalid
	}
	release, err := l.lockSession(ctx, req.UUID)
	if err != nil {
		return control.Operation{}, err
	}
	defer release()
	if prior, e := l.store.GetOperationByKey(ctx, req.Key); e == nil {
		var saved control.RenameRequest
		if prior.Kind != "session.rename" || json.Unmarshal(prior.Request, &saved) != nil || saved != req {
			return control.Operation{}, control.ErrConflict
		}
		if prior.Status == "running" {
			_ = l.enqueue(prior.ID)
		}
		return prior, nil
	} else if !errors.Is(e, control.ErrNotFound) {
		return control.Operation{}, e
	}
	if err := l.checkNoWorkspaceInspect(ctx, req.UUID); err != nil {
		return control.Operation{}, err
	}
	s, err := l.store.GetSession(ctx, req.UUID)
	if err != nil {
		return control.Operation{}, err
	}
	if s.Registry != "established" || s.Branch == req.NewBranch {
		return control.Operation{}, control.ErrConflict
	}
	if err := l.git.backend.ValidateRenameBranch(ctx, req.NewBranch); err != nil {
		return control.Operation{}, err
	}
	native, err := l.runtimeSession(ctx, s)
	if err != nil {
		return control.Operation{}, err
	}
	observed, err := l.runtime.Inspect(ctx, native)
	if err != nil || !observed.Exists || observed.Status != "Running" && observed.Status != "Stopped" ||
		observed.IncusUUID == "" || observed.Generation == "" {
		return control.Operation{}, errors.Join(err, control.ErrConflict)
	}
	ev := control.RenameEvidence{Project: s.Project, OldBranch: s.Branch, NewBranch: req.NewBranch,
		OldTip: req.ExpectedOldTip, PolicySHA256: s.PolicySHA256, InstanceUUID: l.instanceID,
		ImageFingerprint: native.ImageFingerprint, BaseFingerprint: l.cfg.BaseImageFingerprint,
		IncusUUID: observed.IncusUUID, Generation: observed.Generation, OriginalStatus: observed.Status}
	verify := func(ctx context.Context) error {
		if err := l.git.backend.ValidateRenameBranch(ctx, ev.NewBranch); err != nil {
			return err
		}
		return l.verifyRenamePRefs(ctx, ev, false)
	}
	op, err := l.store.BeginRename(ctx, req, ev, verify)
	if err != nil {
		return control.Operation{}, err
	}
	if err := l.enqueue(op.ID); err != nil {
		return op, err
	}
	return op, nil
}

// The caller either holds gitAuthority or is only making a preflight
// observation. No Store method is called from this callback.
func (l *lifecycle) verifyRenamePRefs(ctx context.Context, ev control.RenameEvidence, oldAbsent bool) error {
	old, exists, err := l.git.backend.InspectBranchRef(ctx, ev.Project, ev.OldBranch)
	if err != nil {
		return err
	}
	if oldAbsent {
		if exists || old != "" {
			return control.ErrConflict
		}
	} else if !exists || old != ev.OldTip {
		return control.ErrConflict
	}
	newTip, exists, err := l.git.backend.InspectBranchRef(ctx, ev.Project, ev.NewBranch)
	if err != nil {
		return err
	}
	if oldAbsent {
		if !exists || newTip != ev.OldTip {
			return control.ErrConflict
		}
	} else if exists || newTip != "" {
		return control.ErrConflict
	}
	return nil
}

func (l *lifecycle) renameNewRefExact(ctx context.Context, ev control.RenameEvidence) error {
	old, exists, err := l.git.backend.InspectBranchRef(ctx, ev.Project, ev.OldBranch)
	if err != nil || !exists || old != ev.OldTip {
		return errors.Join(err, control.ErrConflict)
	}
	newTip, exists, err := l.git.backend.InspectBranchRef(ctx, ev.Project, ev.NewBranch)
	if err != nil || !exists || newTip != ev.OldTip {
		return errors.Join(err, control.ErrConflict)
	}
	return nil
}

func (l *lifecycle) blockRename(op control.Operation, cause error) {
	if l.ctx.Err() != nil {
		return
	}
	message := fmt.Sprintf("rename blocked: %v", cause)
	if len(message) > 900 {
		message = message[:900]
	}
	_ = l.store.AdvanceOperation(l.ctx, op.ID, "blocked", op.Phase, op.Committed, op.Evidence, message)
}

func (l *lifecycle) advanceRename(op *control.Operation, ev control.RenameEvidence, phase string, committed bool) error {
	raw, err := json.Marshal(ev)
	if err != nil || len(raw) > 2048 {
		return control.ErrInvalid
	}
	if err := l.store.AdvanceOperation(l.ctx, op.ID, "running", phase, committed, raw, ""); err != nil {
		return err
	}
	op.Phase, op.Evidence, op.Committed = phase, raw, committed
	return nil
}

func (l *lifecycle) rollbackRenamePrecommit(op control.Operation, ev control.RenameEvidence, source runtimeincus.Session, cause error) {
	ctx := l.ctx
	if op.Phase == "freeze-intent" || op.Phase == "prepare-issued" || op.Phase == "new-ref-create-issued" || op.Committed {
		l.blockRename(op, cause)
		return
	}
	if op.Phase == "workspace-prepared" || op.Phase == "quiesced" {
		if err := l.runtime.RemoveWorkspaceRenameBackup(ctx, source, ev.IncusUUID, ev.Generation, op.ID); err != nil {
			l.blockRename(op, errors.Join(cause, err))
			return
		}
	}
	if ev.OriginalStatus == "Running" && (op.Phase == "quiesced" || op.Phase == "workspace-prepared") {
		if err := l.runtime.ThawWorkspaceSourceExact(ctx, source, ev.IncusUUID, ev.Generation); err != nil {
			l.blockRename(op, errors.Join(cause, err))
			return
		}
	}
	// Before a native effect, an independently stopped/replaced source does
	// not justify forcing its old status. It does not prevent releasing these
	// two ref guards after a definite no-effect precommit refusal.
	if err := l.store.FailRenamePrecommit(ctx, op.ID, "rename precommit input changed"); err != nil {
		l.blockRename(op, errors.Join(cause, err))
	}
}

func (l *lifecycle) processRename(op control.Operation) {
	if op.Status != "running" {
		return
	}
	var ev control.RenameEvidence
	if json.Unmarshal(op.Evidence, &ev) != nil || ev.Project != op.Project || ev.InstanceUUID != l.instanceID ||
		ev.BaseFingerprint != l.cfg.BaseImageFingerprint || ev.OldBranch == "" || ev.NewBranch == "" {
		l.blockRename(op, errors.New("rename durable identity unavailable"))
		return
	}
	ctx := l.ctx
	s, err := l.store.GetSession(ctx, op.SessionUUID)
	if err != nil || s.Project != ev.Project || s.PolicySHA256 != ev.PolicySHA256 || s.Registry != "established" ||
		(s.Branch != ev.OldBranch && s.Branch != ev.NewBranch) {
		l.blockRename(op, errors.Join(err, errors.New("rename assignment changed")))
		return
	}
	source, err := l.runtimeSession(ctx, s)
	if err != nil || source.ImageFingerprint != ev.ImageFingerprint {
		l.blockRename(op, errors.Join(err, errors.New("rename runtime selection changed")))
		return
	}
	entryPhase := op.Phase
	for step := 0; step < 18; step++ {
		switch op.Phase {
		case "reserved":
			if err := l.advanceRename(&op, ev, "guarded", false); err != nil {
				l.blockRename(op, err)
				return
			}
		case "guarded":
			if err := l.verifyRenamePRefs(ctx, ev, false); err != nil {
				l.rollbackRenamePrecommit(op, ev, source, err)
				return
			}
			observed, err := l.runtime.Inspect(ctx, source)
			if err != nil || !observed.Exists || observed.IncusUUID != ev.IncusUUID || observed.Generation != ev.Generation || observed.Status != ev.OriginalStatus {
				l.rollbackRenamePrecommit(op, ev, source, errors.Join(err, errors.New("rename source changed before pause")))
				return
			}
			if ev.OriginalStatus == "Running" {
				err = l.runtime.FreezeWorkspaceSourceExact(ctx, source, ev.IncusUUID, ev.Generation, func() error {
					return l.advanceRename(&op, ev, "freeze-intent", false)
				})
				if err != nil {
					if op.Phase == "guarded" {
						// Freeze refused before the durable effect marker. No pause was sent.
						l.rollbackRenamePrecommit(op, ev, source, err)
					} else {
						l.blockRename(op, err)
					}
					return
				}
			}
			if err := l.advanceRename(&op, ev, "quiesced", false); err != nil {
				l.blockRename(op, err)
				return
			}
		case "freeze-intent":
			if entryPhase == "freeze-intent" {
				if err := l.runtime.ReconcileWorkspaceFreezeExact(ctx, source, ev.IncusUUID, ev.Generation); err != nil {
					l.blockRename(op, err)
					return
				}
			}
			if err := l.advanceRename(&op, ev, "quiesced", false); err != nil {
				l.blockRename(op, err)
				return
			}
		case "quiesced":
			plan, err := l.runtime.PrepareWorkspaceRename(ctx, source, ev.IncusUUID, ev.Generation, op.ID, ev.OldBranch, ev.NewBranch,
				func() error { return l.advanceRename(&op, ev, "prepare-issued", false) })
			if err != nil {
				l.rollbackRenamePrecommit(op, ev, source, err)
				return
			}
			ev.WorkspaceTip, ev.ConfigAfterSHA256 = plan.HeadOID, plan.ConfigAfterSHA256
			if err := l.advanceRename(&op, ev, "workspace-prepared", false); err != nil {
				l.blockRename(op, err)
				return
			}
		case "prepare-issued":
			// A timed-out Incus file POST can register after an immediate miss.
			// Only the exact already-written backup authorizes forward motion;
			// absence remains guarded for explicit resolution.
			plan, err := l.runtime.VerifyPreparedWorkspaceRename(ctx, source, ev.IncusUUID, ev.Generation, op.ID, ev.OldBranch, ev.NewBranch)
			if err != nil {
				l.blockRename(op, err)
				return
			}
			ev.WorkspaceTip, ev.ConfigAfterSHA256 = plan.HeadOID, plan.ConfigAfterSHA256
			if err := l.advanceRename(&op, ev, "workspace-prepared", false); err != nil {
				l.blockRename(op, err)
				return
			}
		case "workspace-prepared":
			if err := l.verifyRenamePRefs(ctx, ev, false); err != nil {
				l.rollbackRenamePrecommit(op, ev, source, err)
				return
			}
			plan, err := l.runtime.VerifyPreparedWorkspaceRename(ctx, source, ev.IncusUUID, ev.Generation, op.ID, ev.OldBranch, ev.NewBranch)
			if err != nil || plan.HeadOID != ev.WorkspaceTip || plan.ConfigAfterSHA256 != ev.ConfigAfterSHA256 {
				l.rollbackRenamePrecommit(op, ev, source, errors.Join(err, errors.New("prepared workspace changed")))
				return
			}
			if err := l.advanceRename(&op, ev, "new-ref-create-issued", false); err != nil {
				l.blockRename(op, err)
				return
			}
			if err := l.git.backend.CreateRenameBranchExact(ctx, ev.Project, ev.OldBranch, ev.NewBranch, ev.OldTip); err != nil {
				l.blockRename(op, err)
				return
			}
			if err := l.advanceRename(&op, ev, "new-ref-created", true); err != nil {
				l.blockRename(op, err)
				return
			}
		case "new-ref-create-issued":
			// A request may have been admitted after a timeout. Positive exact
			// new-ref presence is the only automatic forward proof.
			if err := l.renameNewRefExact(ctx, ev); err != nil {
				l.blockRename(op, err)
				return
			}
			if err := l.advanceRename(&op, ev, "new-ref-created", true); err != nil {
				l.blockRename(op, err)
				return
			}
		case "new-ref-created":
			if err := l.renameNewRefExact(ctx, ev); err != nil {
				l.blockRename(op, err)
				return
			}
			if err := l.advanceRename(&op, ev, "workspace-rename-issued", true); err != nil {
				l.blockRename(op, err)
				return
			}
			plan, err := l.runtime.ApplyWorkspaceRename(ctx, source, ev.IncusUUID, ev.Generation, op.ID, ev.OldBranch, ev.NewBranch, true)
			if err != nil || plan.HeadOID != ev.WorkspaceTip || plan.ConfigAfterSHA256 != ev.ConfigAfterSHA256 {
				l.blockRename(op, errors.Join(err, errors.New("workspace rename postcondition unavailable")))
				return
			}
			if err := l.advanceRename(&op, ev, "workspace-renamed", true); err != nil {
				l.blockRename(op, err)
				return
			}
		case "workspace-rename-issued":
			plan, err := l.runtime.ApplyWorkspaceRename(ctx, source, ev.IncusUUID, ev.Generation, op.ID, ev.OldBranch, ev.NewBranch, false)
			if err != nil || plan.HeadOID != ev.WorkspaceTip || plan.ConfigAfterSHA256 != ev.ConfigAfterSHA256 {
				l.blockRename(op, errors.Join(err, errors.New("workspace rename effect ambiguous")))
				return
			}
			if err := l.advanceRename(&op, ev, "workspace-renamed", true); err != nil {
				l.blockRename(op, err)
				return
			}
		case "workspace-renamed":
			if err := l.renameNewRefExact(ctx, ev); err != nil {
				l.blockRename(op, err)
				return
			}
			if err := l.runtime.VerifyWorkspaceRenamed(ctx, source, ev.IncusUUID, ev.Generation, op.ID, ev.OldBranch, ev.NewBranch,
				runtimeincus.WorkspaceRenamePlan{HeadOID: ev.WorkspaceTip, ConfigAfterSHA256: ev.ConfigAfterSHA256}); err != nil {
				l.blockRename(op, err)
				return
			}
			if err := l.store.UpdateRenameAssignment(ctx, op.ID, func(ctx context.Context, ev control.RenameEvidence) error { return l.renameNewRefExact(ctx, ev) }); err != nil {
				l.blockRename(op, err)
				return
			}
			op.Phase = "assignment-updated"
		case "assignment-updated":
			if err := l.renameNewRefExact(ctx, ev); err != nil {
				l.blockRename(op, err)
				return
			}
			err := l.git.backend.DeleteAssignedBranchExact(ctx, ev.Project, ev.OldBranch, ev.OldTip, func() error {
				return l.advanceRename(&op, ev, "old-ref-delete-issued", true)
			})
			if err != nil {
				l.blockRename(op, err)
				return
			}
			if err := l.advanceRename(&op, ev, "old-ref-deleted", true); err != nil {
				l.blockRename(op, err)
				return
			}
		case "old-ref-delete-issued":
			if err := l.verifyRenamePRefs(ctx, ev, true); err != nil {
				l.blockRename(op, err)
				return
			}
			if err := l.advanceRename(&op, ev, "old-ref-deleted", true); err != nil {
				l.blockRename(op, err)
				return
			}
		case "old-ref-deleted":
			if err := l.runtime.VerifyWorkspaceRenamed(ctx, source, ev.IncusUUID, ev.Generation, op.ID, ev.OldBranch, ev.NewBranch,
				runtimeincus.WorkspaceRenamePlan{HeadOID: ev.WorkspaceTip, ConfigAfterSHA256: ev.ConfigAfterSHA256}); err != nil {
				l.blockRename(op, err)
				return
			}
			if err := l.runtime.RemoveWorkspaceRenameBackup(ctx, source, ev.IncusUUID, ev.Generation, op.ID); err != nil {
				l.blockRename(op, err)
				return
			}
			if err := l.advanceRename(&op, ev, "backup-absent", true); err != nil {
				l.blockRename(op, err)
				return
			}
		case "backup-absent":
			if ev.OriginalStatus == "Running" {
				if err := l.runtime.ThawWorkspaceSourceExact(ctx, source, ev.IncusUUID, ev.Generation); err != nil {
					l.blockRename(op, err)
					return
				}
			}
			observed, err := l.runtime.Inspect(ctx, source)
			if err != nil || !observed.Exists || observed.Status != ev.OriginalStatus || observed.IncusUUID != ev.IncusUUID || observed.Generation != ev.Generation {
				l.blockRename(op, errors.Join(err, errors.New("rename source restoration unavailable")))
				return
			}
			if err := l.advanceRename(&op, ev, "source-restored", true); err != nil {
				l.blockRename(op, err)
				return
			}
		case "source-restored":
			if err := l.verifyRenamePRefs(ctx, ev, true); err != nil {
				l.blockRename(op, err)
				return
			}
			observed, err := l.runtime.Inspect(ctx, source)
			if err != nil || !observed.Exists || observed.Status != ev.OriginalStatus || observed.IncusUUID != ev.IncusUUID || observed.Generation != ev.Generation {
				l.blockRename(op, errors.Join(err, errors.New("rename source changed before completion")))
				return
			}
			if err := l.store.CompleteRename(ctx, op.ID, func(ctx context.Context, ev control.RenameEvidence) error { return l.verifyRenamePRefs(ctx, ev, true) }); err != nil {
				l.blockRename(op, err)
				return
			}
			return
		default:
			l.blockRename(op, errors.New("rename phase unsupported"))
			return
		}
	}
	l.blockRename(op, errors.New("rename phase sequence exceeded bound"))
}
