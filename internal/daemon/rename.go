package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

func (l *lifecycle) RenameSession(ctx context.Context, req control.RenameRequest) (api.Operation, error) {
	if !control.ValidRenameRequest(req) {
		return api.Operation{}, control.ErrInvalid
	}
	release, err := l.lockSession(ctx, req.UUID)
	if err != nil {
		return api.Operation{}, err
	}
	defer release()
	if prior, replayed, err := replayRequest(l, ctx, req.Key, control.KindSessionRename, req); err != nil || replayed {
		return prior, err
	}
	if err := l.admitSessionWork(ctx, req.UUID, ""); err != nil {
		return api.Operation{}, err
	}
	s, err := l.store.GetSession(ctx, req.UUID)
	if err != nil {
		return api.Operation{}, err
	}
	if s.Registry != "established" {
		return api.Operation{}, control.Conflict(api.ReasonSessionNotEstablished, api.TargetSession(req.UUID))
	}
	if s.Branch == req.NewBranch {
		return api.Operation{}, control.Conflict(api.ReasonBranchExists, api.TargetBranch(s.Project, req.NewBranch))
	}
	if err := l.checkBranchName(ctx, "new_branch", req.NewBranch); err != nil {
		return api.Operation{}, err
	}
	native, err := l.nativeSession(ctx, s)
	if err != nil {
		return api.Operation{}, err
	}
	observed, err := l.runtime.Inspect(ctx, native)
	if err != nil {
		return api.Operation{}, control.Failed(api.ReasonRuntime, err)
	}
	if err := renameSourceRefusal(observed, req.UUID); err != nil {
		return api.Operation{}, err
	}
	ev := control.RenameEvidence{Project: s.Project, OldBranch: s.Branch, NewBranch: req.NewBranch,
		OldTip: req.ExpectedOldTip, PolicySHA256: s.PolicySHA256, InstanceUUID: l.instanceID,
		ImageFingerprint: native.ImageFingerprint, BaseFingerprint: l.cfg.BaseImageFingerprint,
		IncusUUID: observed.IncusUUID, Generation: observed.Generation}
	verify := func(ctx context.Context) error {
		if err := l.checkBranchName(ctx, "new_branch", ev.NewBranch); err != nil {
			return err
		}
		return control.Failed(api.ReasonGit, l.verifyRenamePRefs(ctx, ev, false))
	}
	op, err := l.store.BeginRename(ctx, req, ev, verify)
	if err != nil {
		return api.Operation{}, err
	}
	if err := l.enqueue(op.ID); err != nil {
		return op, err
	}
	return op, nil
}

// checkBranchName has Git check name, the request's field: a name Git
// refuses answers invalid naming field, and a failure to check it answers
// failed with reason git.
func (l *lifecycle) checkBranchName(ctx context.Context, field, name string) error {
	err := l.git.backend.ValidateRenameBranch(ctx, name)
	if errors.Is(err, control.ErrInvalid) {
		return control.Invalid(field)
	}
	return control.Failed(api.ReasonGit, err)
}

// renameSourceRefusal refuses to rename session whose runtime was observed
// as o: its container is missing, or it is not stopped.
func renameSourceRefusal(o runtimeincus.Observation, session string) error {
	switch {
	case !o.Exists:
		return control.Conflict(api.ReasonRuntimeMissing, api.TargetSession(session))
	case !renameSourceStopped(o):
		return control.Conflict(api.ReasonSessionNotStopped, api.TargetSession(session))
	}
	return nil
}

// renameSourceStopped reports whether rename may change the workspace of the
// observed runtime: P changes a workspace only while its session is stopped.
func renameSourceStopped(o runtimeincus.Observation) bool {
	return o.Exists && o.Status == "Stopped" && o.IncusUUID != "" && o.Generation != ""
}

// renameSourceExact reports whether o is the stopped runtime the rename
// recorded.
func renameSourceExact(o runtimeincus.Observation, ev control.RenameEvidence) bool {
	return renameSourceStopped(o) && o.IncusUUID == ev.IncusUUID && o.Generation == ev.Generation
}

// The caller either holds gitAuthority or is only making a preflight
// observation. No Store method is called from this callback. Before the
// rename (oldAbsent false), an old ref that no longer holds the expected tip
// is a changed precondition and a taken new name a conflict; after it, a
// mismatch breaks the rename's own invariant.
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
		return control.Stale(api.ReasonChanged)
	}
	newTip, exists, err := l.git.backend.InspectBranchRef(ctx, ev.Project, ev.NewBranch)
	if errors.Is(err, control.ErrConflict) && !oldAbsent {
		// A symbolic ref occupies the new name.
		return control.Conflict(api.ReasonBranchExists, api.TargetBranch(ev.Project, ev.NewBranch))
	}
	if err != nil {
		return err
	}
	if oldAbsent {
		if !exists || newTip != ev.OldTip {
			return control.ErrConflict
		}
	} else if exists || newTip != "" {
		return control.Conflict(api.ReasonBranchExists, api.TargetBranch(ev.Project, ev.NewBranch))
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

func (l *lifecycle) blockRename(op api.Operation, cause error) {
	if l.ctx.Err() != nil {
		return
	}
	message := fmt.Sprintf("rename blocked: %v", cause)
	_ = l.store.AdvanceOperation(l.ctx, op.ID, "blocked", op.Phase, op.Committed, op.Evidence, control.OperationReason(cause), message)
}

func (l *lifecycle) advanceRename(op *api.Operation, ev control.RenameEvidence, phase string, committed bool) error {
	raw, err := json.Marshal(ev)
	if err != nil || len(raw) > 2048 {
		return control.ErrInvalid
	}
	if err := l.store.AdvanceOperation(l.ctx, op.ID, "running", phase, committed, raw, "", ""); err != nil {
		return err
	}
	op.Phase, op.Evidence, op.Committed = phase, raw, committed
	return nil
}

func (l *lifecycle) rollbackRenamePrecommit(op api.Operation, ev control.RenameEvidence, source runtimeincus.Session, cause error) {
	ctx := l.ctx
	if op.Phase == "prepare-issued" || op.Phase == "new-ref-create-issued" || op.Committed {
		l.blockRename(op, cause)
		return
	}
	if op.Phase == "workspace-prepared" {
		if err := l.runtime.RemoveWorkspaceRenameBackup(ctx, source, ev.IncusUUID, ev.Generation, op.ID); err != nil {
			l.blockRename(op, errors.Join(cause, err))
			return
		}
	}
	// A replaced or started source does not prevent releasing these two ref
	// guards after a definite no-effect precommit refusal.
	reason, diagnostic := api.ReasonChanged, "rename precommit input changed"
	var mismatch *runtimeincus.WorkspaceAssignmentMismatchError
	if errors.As(cause, &mismatch) {
		reason, diagnostic = api.ReasonWorkspaceMismatch, mismatch.Error()
	}
	if err := l.store.FailRenamePrecommit(ctx, op.ID, reason, diagnostic); err != nil {
		l.blockRename(op, errors.Join(cause, err))
	}
}

func (l *lifecycle) processRename(op api.Operation) {
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
	source, err := l.nativeSession(ctx, s)
	if err != nil || source.ImageFingerprint != ev.ImageFingerprint {
		l.blockRename(op, errors.Join(err, errors.New("rename runtime selection changed")))
		return
	}
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
			if err != nil || !renameSourceExact(observed, ev) {
				l.rollbackRenamePrecommit(op, ev, source, errors.Join(err, errors.New("rename source changed or started")))
				return
			}
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
			if err := l.verifyRenamePRefs(ctx, ev, true); err != nil {
				l.blockRename(op, err)
				return
			}
			observed, err := l.runtime.Inspect(ctx, source)
			if err != nil || !renameSourceExact(observed, ev) {
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
