package daemon

import (
	"context"
	"errors"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
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
		// Whether the container was running and the driver's name for it are
		// information only: P stops a running one itself, so a Start or Stop
		// before the run changes nothing the removal does, and the removal
		// acts on the container's incarnation, not its name.
		Shown: func(d api.RemovalDetails) api.RemovalDetails {
			d.Runtime.OriginalStatus, d.Runtime.Name = "", ""
			return d
		},
		Replay: func(ctx context.Context) (api.Operation, bool, error) {
			return replayRequest(l, ctx, req.Key, kind, recorded)
		},
		Begin: func(ctx context.Context, run control.Run[api.RemovalDetails]) (api.Operation, error) {
			d := run.Plan.Details
			ev := control.DiscardEvidence{Action: action, Project: d.Project, Branch: d.Branch, AssignedTip: d.AssignedTip,
				PolicySHA256: d.PolicySHA256, InstanceUUID: l.instanceID, ImageFingerprint: d.Runtime.ImageFingerprint,
				BaseFingerprint: l.cfg.BaseImageFingerprint, Incarnation: d.Runtime.Incarnation, MissingRuntime: d.Runtime.Condition == "missing"}
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
		PolicySHA256: session.PolicySHA256}
	if session.Registry != "established" {
		return control.Conflict(api.ReasonSessionNotEstablished, target)
	}
	spec, err := l.sessionSpec(ctx, session)
	if err != nil {
		return err
	}
	observed, err := l.driver.Inspect(ctx, spec)
	if err != nil {
		return control.Failed(api.ReasonRuntime, err) // unreachable is not missing
	}
	if observed.State == runtimedriver.StateAbsent {
		if err := proveRuntimeAbsent(ctx, l.driver, spec); err != nil {
			return runtimeFailure(err, target)
		}
		d.Runtime.Condition = "missing"
		p.Gone = append(p.Gone, missingRuntimeGone(uuid))
	} else {
		switch {
		case observed.Incarnation == "":
			return control.Failed(api.ReasonRuntime, errors.New("session runtime identity incomplete"))
		case !removableState(observed.State):
			return control.Conflict(api.ReasonRuntimeUnsettled, target)
		}
		d.Runtime.Condition, d.Runtime.Name, d.Runtime.Incarnation = "present", observed.Name, string(observed.Incarnation)
		d.Runtime.ImageFingerprint, d.Runtime.OriginalStatus = string(spec.Image), originalStatus(observed.State)
		p.Loss = append(p.Loss, containerLoss(uuid))
	}
	review, err := l.branchRemovalReview(ctx, session.Project, session.Branch, action == "delete")
	if err != nil {
		return err
	}
	d.AssignedTip, d.AssignedRefMissing = review.AssignedTip, review.AssignedTip == ""
	if action == "delete" {
		d.BranchLoss = review
		p.Loss = append(p.Loss, branchLoss(api.TargetBranch(session.Project, session.Branch), d.BranchLoss)...)
	}
	return nil
}

// removableState reports whether removal accepts a present session runtime
// in this state: P stops a running one itself before deleting it.
func removableState(state runtimedriver.State) bool {
	return state == runtimedriver.StateRunning || state == runtimedriver.StateStopped
}

// originalStatus is how a plan shows the state of the runtime it found.
func originalStatus(state runtimedriver.State) string {
	if state == runtimedriver.StateRunning {
		return "Running"
	}
	return "Stopped"
}

// runtimeFailure answers a failed check of a session's runtime: a container of
// the session that is present where its absence was to be proven is a
// conflict on target; anything else is a runtime failure.
func runtimeFailure(err error, target string) error {
	var present *runtimePresentError
	if errors.As(err, &present) {
		return &control.Refusal{Kind: api.KindConflict, Reason: api.ReasonRuntimePresent, Target: target, Cause: err}
	}
	return control.Failed(api.ReasonRuntime, err)
}
