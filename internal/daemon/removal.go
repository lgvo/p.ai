package daemon

import (
	"context"
	"errors"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

// DiscardSession answers a discard of session uuid: its plan, or the
// operation of a run.
func (l *lifecycle) DiscardSession(ctx context.Context, uuid string, req api.ChangeRequest) (api.ChangeResult[api.RemovalDetails], error) {
	return l.removal("discard", uuid, req).Do(ctx, req)
}

// DeleteSession answers a delete of session uuid: its plan, or the operation
// of a run.
func (l *lifecycle) DeleteSession(ctx context.Context, uuid string, req api.ChangeRequest) (api.ChangeResult[api.RemovalDetails], error) {
	return l.removal("delete", uuid, req).Do(ctx, req)
}

// removal is the discard or delete (action) of session uuid. P does not look
// inside a present container: the plan states that it and everything in it
// is lost, and a running session is stopped as part of removing it.
func (l *lifecycle) removal(action, uuid string, req api.ChangeRequest) control.Change[api.RemovalDetails] {
	kind := "session." + action
	recorded := control.DiscardRequest{Key: req.Key, UUID: uuid, IfMatch: req.IfMatch}
	return control.Change[api.RemovalDetails]{
		Action: kind, Target: api.TargetSession(uuid), Instance: l.instanceID,
		Hold: l.holdingSession(uuid),
		Plan: func(ctx context.Context, p *api.Plan[api.RemovalDetails]) error {
			return l.planRemoval(ctx, action, uuid, p)
		},
		// Whether the container was running is information only: P stops a
		// running one itself, so a Start or Stop before the run changes nothing
		// the removal does.
		Shown: func(d api.RemovalDetails) api.RemovalDetails {
			d.Runtime.OriginalStatus = ""
			return d
		},
		Replay: func(ctx context.Context) (api.Operation, bool, error) {
			return replayRequest(l, ctx, req.Key, kind, recorded)
		},
		Begin: func(ctx context.Context, run control.Run[api.RemovalDetails]) (api.Operation, error) {
			d := run.Plan.Details
			ev := control.DiscardEvidence{Action: action, Project: d.Project, Branch: d.Branch, AssignedTip: d.AssignedTip,
				PolicySHA256: d.PolicySHA256, IncusProject: d.Runtime.IncusProject, InstanceName: d.Runtime.InstanceName,
				InstanceUUID: l.instanceID, ImageFingerprint: d.Runtime.ImageFingerprint, BaseFingerprint: l.cfg.BaseImageFingerprint,
				IncusUUID: d.Runtime.IncusUUID, Generation: d.Runtime.Generation, MissingRuntime: d.Runtime.Condition == "missing"}
			var op api.Operation
			var err error
			if action == "delete" {
				if ev.DeleteReviewSHA256, err = deleteReviewDigest(d.BranchLoss); err != nil {
					return api.Operation{}, err
				}
				op, err = l.store.BeginDelete(ctx, recorded, ev, run.Hash, run.Recheck)
			} else {
				op, err = l.store.BeginDiscard(ctx, recorded, ev, run.Hash, run.Recheck)
			}
			if err != nil {
				return api.Operation{}, err
			}
			return op, l.enqueue(op.ID)
		},
	}
}

// planRemoval plans the discard or delete (action) of session uuid into p.
func (l *lifecycle) planRemoval(ctx context.Context, action, uuid string, p *api.Plan[api.RemovalDetails]) error {
	target := api.TargetSession(uuid)
	if l.hasAttachment(uuid) {
		return control.Conflict(api.ReasonAttachmentActive, target)
	}
	if err := l.admitSessionWork(ctx, uuid, ""); err != nil {
		return err
	}
	session, err := l.store.GetSession(ctx, uuid)
	if err != nil {
		return err
	}
	d := &p.Details
	*d = api.RemovalDetails{Project: session.Project, Branch: session.Branch, AssignedRef: "refs/heads/" + session.Branch,
		PolicySHA256: session.PolicySHA256, Runtime: api.RemovalRuntime{IncusProject: l.incus.Project, InstanceName: "p-" + session.UUID}}
	if session.Registry != "established" {
		return control.Conflict(api.ReasonSessionNotEstablished, target)
	}
	native, err := l.runtimeSession(ctx, session)
	if err != nil {
		return err
	}
	observed, err := l.runtime.Inspect(ctx, native)
	if err != nil {
		return control.Failed(api.ReasonRuntime, err) // unreachable is not missing
	}
	if !observed.Exists {
		if err := l.runtime.ConfirmSessionRuntimeAbsent(ctx, native); err != nil {
			return runtimeFailure(err, target)
		}
		d.Runtime.Condition = "missing"
		p.Gone = append(p.Gone, missingRuntimeGone(uuid))
	} else {
		switch {
		case observed.Name != d.Runtime.InstanceName || observed.IncusUUID == "" || observed.Generation == "":
			return control.Failed(api.ReasonRuntime, errors.New("session runtime identity incomplete"))
		case !removableStatus(observed.Status):
			return control.Conflict(api.ReasonRuntimeUnsettled, target)
		}
		d.Runtime.Condition, d.Runtime.IncusUUID, d.Runtime.Generation = "present", observed.IncusUUID, observed.Generation
		d.Runtime.ImageFingerprint, d.Runtime.OriginalStatus = observed.Fingerprint, observed.Status
		p.Loss = append(p.Loss, containerLoss(uuid))
	}
	var loss gitservice.BranchRemovalLoss
	if action == "delete" {
		loss, err = l.git.backend.BranchRemovalLoss(ctx, session.Project, session.Branch, true)
	} else {
		loss, err = l.git.backend.AssignedBranchSnapshot(ctx, session.Project, session.Branch, true)
	}
	if err != nil {
		return gitFailure(err)
	}
	d.AssignedTip, d.AssignedRefMissing = loss.AssignedTip, loss.AssignedTip == ""
	if action == "delete" {
		d.BranchLoss = deleteReview(loss)
		p.Loss = append(p.Loss, branchLoss(api.TargetBranch(session.Project, session.Branch), d.BranchLoss)...)
	}
	return nil
}

// removableStatus reports whether removal accepts a present session runtime
// in this Incus status: P stops a running one itself before deleting it.
func removableStatus(status string) bool {
	return status == "Running" || status == "Stopped"
}

// runtimeFailure answers a failed check of a session's runtime: a container of
// the session that is present where its absence was to be proven is a
// conflict on target; anything else is a runtime failure.
func runtimeFailure(err error, target string) error {
	var present *runtimeincus.RuntimePresentError
	if errors.As(err, &present) {
		return &control.Refusal{Kind: api.KindConflict, Reason: api.ReasonRuntimePresent, Target: target, Cause: err}
	}
	return control.Failed(api.ReasonRuntime, err)
}

// gitFailure answers a failed read of P's repository: refs that moved while
// they were read can be read again.
func gitFailure(err error) error {
	if errors.Is(err, gitservice.ErrRefsMoving) {
		return &control.Refusal{Kind: api.KindRetry, Reason: api.ReasonRefsMoved, Cause: err}
	}
	return control.Failed(api.ReasonGit, err)
}
