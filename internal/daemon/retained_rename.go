package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lgvo/p.ai/internal/control"
)

func (l *lifecycle) RenameRetainedBranch(ctx context.Context, r control.RetainedRenameRequest) (control.Operation, error) {
	if !control.ValidRetainedRenameRequest(r) {
		return control.Operation{}, control.ErrInvalid
	}
	if prior, err := l.store.GetOperationByKey(ctx, r.Key); err == nil {
		var saved control.RetainedRenameRequest
		if prior.Kind != "project.retained.rename" || json.Unmarshal(prior.Request, &saved) != nil || saved != r {
			return control.Operation{}, control.ErrConflict
		}
		if prior.Status == "running" {
			_ = l.enqueue(prior.ID)
		}
		return prior, nil
	} else if !errors.Is(err, control.ErrNotFound) {
		return control.Operation{}, err
	}
	if err := l.git.backend.ValidateRenameBranch(ctx, r.OldBranch); err != nil {
		return control.Operation{}, err
	}
	if err := l.git.backend.ValidateRenameBranch(ctx, r.NewBranch); err != nil {
		return control.Operation{}, err
	}
	op, err := l.store.BeginRetainedRename(ctx, r, func(ctx context.Context) error { return l.verifyRetainedRenameRefs(ctx, r, "before") })
	if err != nil {
		return control.Operation{}, err
	}
	if err = l.enqueue(op.ID); err != nil {
		return op, err
	}
	return op, nil
}

func (l *lifecycle) verifyRetainedRenameRefs(ctx context.Context, r control.RetainedRenameRequest, phase string) error {
	old, oldExists, err := l.git.backend.InspectBranchRef(ctx, r.Project, r.OldBranch)
	if err != nil {
		return err
	}
	newTip, newExists, err := l.git.backend.InspectBranchRef(ctx, r.Project, r.NewBranch)
	if err != nil {
		return err
	}
	switch phase {
	case "before":
		if !oldExists || old != r.ExpectedOldTip || newExists || newTip != "" {
			return control.ErrConflict
		}
		present, err := l.git.backend.PCommitPresent(ctx, r.Project, r.ExpectedOldTip)
		if err != nil {
			return err
		}
		if !present {
			return control.ErrConflict
		}
	case "created":
		if !oldExists || old != r.ExpectedOldTip || !newExists || newTip != r.ExpectedOldTip {
			return control.ErrConflict
		}
	case "deleted":
		if oldExists || old != "" || !newExists || newTip != r.ExpectedOldTip {
			return control.ErrConflict
		}
	default:
		return control.ErrInvalid
	}
	return nil
}

func (l *lifecycle) blockRetainedRename(op control.Operation, cause error) {
	if l.ctx.Err() != nil {
		return
	}
	message := fmt.Sprintf("retained branch rename blocked: %v", cause)
	if len(message) > 900 {
		message = message[:900]
	}
	_ = l.store.AdvanceOperation(l.ctx, op.ID, "blocked", op.Phase, op.Committed, op.Evidence, message)
}

func (l *lifecycle) processRetainedRename(op control.Operation) {
	if op.Status != "running" {
		return
	}
	var r control.RetainedRenameRequest
	if json.Unmarshal(op.Request, &r) != nil || !control.ValidRetainedRenameRequest(r) || op.Project != r.Project {
		l.blockRetainedRename(op, errors.New("durable retained branch identity unavailable"))
		return
	}
	ctx := l.ctx
	for step := 0; step < 8; step++ {
		switch op.Phase {
		case "reserved":
			err := l.store.TransitionRetainedRename(ctx, op.ID, "reserved", "new-ref-create-issued", true, func(ctx context.Context, saved control.RetainedRenameRequest) error {
				return l.verifyRetainedRenameRefs(ctx, saved, "before")
			})
			if err != nil {
				if errors.Is(err, control.ErrConflict) {
					if e := l.store.FailRetainedRenamePreEffect(ctx, op.ID); e == nil {
						return
					} else {
						err = errors.Join(err, e)
					}
				}
				l.blockRetainedRename(op, err)
				return
			}
			op.Phase, op.Committed = "new-ref-create-issued", true
			if err = l.git.backend.CreateRenameBranchExact(ctx, r.Project, r.OldBranch, r.NewBranch, r.ExpectedOldTip); err != nil {
				l.blockRetainedRename(op, err)
				return
			}
		case "new-ref-create-issued":
			if err := l.store.TransitionRetainedRename(ctx, op.ID, "new-ref-create-issued", "new-ref-created", true, func(ctx context.Context, saved control.RetainedRenameRequest) error {
				return l.verifyRetainedRenameRefs(ctx, saved, "created")
			}); err != nil {
				l.blockRetainedRename(op, err)
				return
			}
			op.Phase = "new-ref-created"
		case "new-ref-created":
			if err := l.store.TransitionRetainedRename(ctx, op.ID, "new-ref-created", "old-ref-delete-issued", true, func(ctx context.Context, saved control.RetainedRenameRequest) error {
				return l.verifyRetainedRenameRefs(ctx, saved, "created")
			}); err != nil {
				l.blockRetainedRename(op, err)
				return
			}
			op.Phase = "old-ref-delete-issued"
			if err := l.git.backend.DeleteAssignedBranchExact(ctx, r.Project, r.OldBranch, r.ExpectedOldTip, func() error { return nil }); err != nil {
				l.blockRetainedRename(op, err)
				return
			}
		case "old-ref-delete-issued":
			if err := l.store.TransitionRetainedRename(ctx, op.ID, "old-ref-delete-issued", "old-ref-deleted", true, func(ctx context.Context, saved control.RetainedRenameRequest) error {
				return l.verifyRetainedRenameRefs(ctx, saved, "deleted")
			}); err != nil {
				l.blockRetainedRename(op, err)
				return
			}
			op.Phase = "old-ref-deleted"
		case "old-ref-deleted":
			if err := l.store.TransitionRetainedRename(ctx, op.ID, "old-ref-deleted", "completed", true, func(ctx context.Context, saved control.RetainedRenameRequest) error {
				return l.verifyRetainedRenameRefs(ctx, saved, "deleted")
			}); err != nil {
				l.blockRetainedRename(op, err)
			}
			return
		default:
			l.blockRetainedRename(op, errors.New("unsupported durable phase"))
			return
		}
	}
	l.blockRetainedRename(op, errors.New("phase sequence exceeded bound"))
}
