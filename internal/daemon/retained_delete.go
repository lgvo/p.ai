package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
)

// DeleteRetainedBranch answers a deletion of retained branch branch of
// project: its plan, or the operation of a run. It does not contact the
// origin: whether the origin keeps the branch's commits is the user's
// concern.
func (l *lifecycle) DeleteRetainedBranch(ctx context.Context, project, branch string, req api.ChangeRequest) (api.ChangeResult[api.RetainedDeleteDetails], error) {
	recorded := control.RetainedDeleteRequest{Key: req.Key, Project: project, Branch: branch, IfMatch: req.IfMatch}
	change := control.Change[api.RetainedDeleteDetails]{
		Action: api.ProjectRetainedDelete.Name, Target: api.TargetBranch(project, branch), Instance: l.instanceID,
		// BeginRetainedDelete plans again under the Git authority, and the
		// worker checks the branch again before it deletes it.
		Hold: func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) },
		Plan: func(ctx context.Context, p *api.Plan[api.RetainedDeleteDetails]) error {
			return l.planRetainedDelete(ctx, project, branch, p)
		},
		Replay: func(ctx context.Context) (api.Operation, bool, error) {
			return replayRequest(l, ctx, req.Key, control.KindRetainedDelete, recorded)
		},
		Begin: func(ctx context.Context, run control.Run[api.RetainedDeleteDetails]) (api.Operation, error) {
			d := run.Plan.Details
			digest, err := deleteReviewDigest(d.BranchLoss)
			if err != nil {
				return api.Operation{}, err
			}
			ev := control.RetainedDeleteEvidence{Project: project, Branch: branch, Tip: d.Tip, ReviewSHA256: digest, InstanceUUID: l.instanceID}
			op, err := l.store.BeginRetainedDelete(ctx, recorded, ev, run.Hash, run.Recheck)
			if err != nil {
				return api.Operation{}, err
			}
			return op, l.enqueue(op.ID)
		},
	}
	return change.Do(ctx, req)
}

// planRetainedDelete plans deleting retained branch branch of project into p.
func (l *lifecycle) planRetainedDelete(ctx context.Context, project, branch string, p *api.Plan[api.RetainedDeleteDetails]) error {
	if err := l.checkBranchName(ctx, "branch", branch); err != nil {
		return err
	}
	p.Details = api.RetainedDeleteDetails{Project: project, Branch: branch, Ref: "refs/heads/" + branch}
	retained, err := l.store.IsRetainedBranch(ctx, project, branch)
	if errors.Is(err, control.ErrNotFound) {
		return control.NotFound(api.ReasonProject, api.TargetProject(project))
	}
	if err != nil {
		return err
	}
	if !retained {
		return control.Conflict(api.ReasonBranchAssigned, api.TargetBranch(project, branch))
	}
	if err = l.store.CheckBranchUnreserved(ctx, project, branch); err != nil {
		return err
	}
	review, err := l.freshRetainedDeleteReview(ctx, project, branch)
	if err != nil {
		return err
	}
	p.Details.Ref, p.Details.Tip, p.Details.BranchLoss = review.AssignedRef, review.AssignedTip, review
	p.Loss = branchLoss(api.TargetBranch(project, branch), review)
	return nil
}

// freshRetainedDeleteReview reviews deleting branch afresh. A branch that is
// gone answers not_found; a failure to review it is returned as it is.
func (l *lifecycle) freshRetainedDeleteReview(ctx context.Context, project, branch string) (*api.RemovalBranchLoss, error) {
	loss, err := l.git.backend.BranchRemovalLoss(ctx, project, branch, true)
	if err != nil {
		return nil, gitFailure(err)
	}
	if loss.AssignedTip == "" {
		return nil, control.NotFound(api.ReasonBranch, api.TargetBranch(project, branch))
	}
	return deleteReview(loss), nil
}

// verifyRetainedDeleteReview checks that a fresh review of the deletion still
// matches the planned one. A changed review answers precondition_failed and
// a failure to review answers failed with reason git; both keep ErrConflict,
// so a deletion whose review fails before its effect fails as stale.
func (l *lifecycle) verifyRetainedDeleteReview(ctx context.Context, e control.RetainedDeleteEvidence) error {
	if e.InstanceUUID != l.instanceID {
		return control.ErrConflict
	}
	current, exists, err := l.git.backend.InspectBranchRef(ctx, e.Project, e.Branch)
	if err != nil {
		return control.Failed(api.ReasonGit, err)
	}
	if !exists || current != e.Tip {
		return control.Stale(api.ReasonChanged)
	}
	fresh, err := l.freshRetainedDeleteReview(ctx, e.Project, e.Branch)
	if errors.Is(err, control.ErrNotFound) {
		return control.Stale(api.ReasonChanged)
	}
	if err != nil {
		// The worker fails a review it cannot repeat as stale.
		return errors.Join(err, control.ErrConflict)
	}
	return matchRetainedDeleteReview(fresh, e.ReviewSHA256)
}

func matchRetainedDeleteReview(fresh *api.RemovalBranchLoss, reviewedSHA256 string) error {
	digest, err := deleteReviewDigest(fresh)
	if err != nil {
		return err
	}
	if digest != reviewedSHA256 {
		return errors.Join(errDeleteReviewChanged, control.Stale(api.ReasonChanged))
	}
	return nil
}

func (l *lifecycle) blockRetainedDelete(id string, cause error) {
	if l.ctx.Err() != nil {
		return
	}
	op, err := l.store.GetOperation(l.ctx, id)
	if err != nil {
		return
	}
	message := fmt.Sprintf("retained delete blocked: %v", cause)
	_ = l.store.AdvanceOperation(l.ctx, id, "blocked", op.Phase, op.Committed, op.Evidence, control.OperationReason(cause), message)
}

func (l *lifecycle) processRetainedDelete(op api.Operation) {
	if op.Status != "running" {
		return
	}
	var e control.RetainedDeleteEvidence
	if json.Unmarshal(op.Evidence, &e) != nil || e.Project != op.Project || e.InstanceUUID != l.instanceID {
		l.blockRetainedDelete(op.ID, errors.New("durable retained delete identity unavailable"))
		return
	}
	ctx := l.ctx
	switch op.Phase {
	case "guarded":
		var issued bool
		err := l.store.IssueRetainedDelete(ctx, op.ID, l.verifyRetainedDeleteReview, func() error {
			issued = true
			return l.git.backend.DeleteAssignedBranchExact(ctx, e.Project, e.Branch, e.Tip, func() error { return nil })
		})
		if err != nil {
			if !issued && (errors.Is(err, control.ErrConflict) || errors.Is(err, errDeleteReviewChanged)) {
				if staleErr := l.store.FailRetainedDeleteStale(ctx, op.ID); staleErr == nil {
					return
				} else {
					err = errors.Join(err, staleErr)
				}
			}
			l.blockRetainedDelete(op.ID, err)
			return
		}
		fallthrough
	case "ref-delete-issued":
		current, exists, err := l.git.backend.InspectBranchRef(ctx, e.Project, e.Branch)
		if err != nil || exists || current != "" {
			l.blockRetainedDelete(op.ID, errors.Join(err, errors.New("exact retained P ref absence unavailable")))
			return
		}
		if err = l.store.CompleteRetainedDelete(ctx, op.ID, func(ctx context.Context, saved control.RetainedDeleteEvidence) error {
			current, exists, err := l.git.backend.InspectBranchRef(ctx, saved.Project, saved.Branch)
			if err != nil {
				return err
			}
			if exists || current != "" {
				return control.ErrConflict
			}
			return nil
		}); err != nil {
			l.blockRetainedDelete(op.ID, err)
		}
	default:
		l.blockRetainedDelete(op.ID, errors.New("unsupported durable phase"))
	}
}
