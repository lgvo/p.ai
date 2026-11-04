package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/sourcedriver"
)

func (l *lifecycle) RenameRetainedBranch(ctx context.Context, r control.RetainedRenameRequest) (api.Operation, error) {
	if !control.ValidRetainedRenameRequest(r) {
		return api.Operation{}, control.ErrInvalid
	}
	if prior, replayed, err := replayRequest(l, ctx, r.Key, control.KindRetainedRename, r); err != nil || replayed {
		return prior, err
	}
	if _, err := control.CheckNames(ctx, l.git.driver,
		control.NameField{Field: "old_branch", Name: sourcedriver.Name{Kind: sourcedriver.NameRenameBranch, Value: r.OldBranch}},
		control.NameField{Field: "new_branch", Name: sourcedriver.Name{Kind: sourcedriver.NameRenameBranch, Value: r.NewBranch}},
		control.NameField{Field: "expected_old_tip", Name: sourcedriver.Name{Kind: sourcedriver.NameCommit, Value: r.ExpectedOldTip}}); err != nil {
		return api.Operation{}, err
	}
	op, err := l.store.BeginRetainedRename(ctx, r, func(ctx context.Context) error {
		return control.Failed(api.ReasonGit, l.verifyRetainedRenameRefs(ctx, r, "before"))
	})
	if err != nil {
		return api.Operation{}, err
	}
	if err = l.enqueue(op.ID); err != nil {
		return op, err
	}
	return op, nil
}

func (l *lifecycle) verifyRetainedRenameRefs(ctx context.Context, r control.RetainedRenameRequest, phase string) error {
	old, oldExists, err := l.inspectBranch(ctx, r.Project, r.OldBranch)
	if err != nil {
		return err
	}
	newTip, newExists, err := l.inspectBranch(ctx, r.Project, r.NewBranch)
	if errors.Is(err, control.ErrConflict) && phase == "before" {
		// A symbolic ref occupies the new name.
		return control.Conflict(api.ReasonBranchExists, api.TargetBranch(r.Project, r.NewBranch))
	}
	if err != nil {
		return err
	}
	switch phase {
	case "before":
		switch {
		case !oldExists:
			return control.NotFound(api.ReasonBranch, api.TargetBranch(r.Project, r.OldBranch))
		case old != r.ExpectedOldTip:
			return control.Stale(api.ReasonChanged)
		case newExists || newTip != "":
			return control.Conflict(api.ReasonBranchExists, api.TargetBranch(r.Project, r.NewBranch))
		}
		commit, err := l.git.driver.InspectCommit(ctx, r.Project, sourcedriver.OID(r.ExpectedOldTip))
		if err != nil {
			return control.Failed(api.ReasonGit, err)
		}
		if !commit.Present {
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

func (l *lifecycle) blockRetainedRename(op api.Operation, cause error) {
	if l.ctx.Err() != nil {
		return
	}
	message := fmt.Sprintf("retained branch rename blocked: %v", cause)
	_ = l.store.AdvanceOperation(l.ctx, op.ID, "blocked", op.Phase, op.Committed, op.Evidence, control.OperationReason(cause), message)
}

func (l *lifecycle) processRetainedRename(op api.Operation) {
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
			// The gate checks the old head at the expected tip and the new
			// name free, and records the creation's intent.
			issued := false
			err := l.createBranch(ctx, r.Project, r.NewBranch, r.ExpectedOldTip, func() error {
				err := l.store.TransitionRetainedRename(ctx, op.ID, "reserved", "new-ref-create-issued", true, func(ctx context.Context, saved control.RetainedRenameRequest) error {
					// An old branch gone before the effect changed the input, like
					// any other mismatch: the rename fails as stale.
					err := l.verifyRetainedRenameRefs(ctx, saved, "before")
					if errors.Is(err, control.ErrNotFound) {
						return control.Stale(api.ReasonChanged)
					}
					return err
				})
				issued = err == nil
				return err
			})
			if !issued {
				// Nothing was created: a new name taken meanwhile changed the
				// input too.
				if errors.Is(err, control.ErrConflict) || errors.Is(err, sourcedriver.ErrConflict) {
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
			if err != nil {
				l.blockRetainedRename(op, err)
				return
			}
		case "new-ref-create-issued":
			// The creation may have been admitted after its answer was lost:
			// only the source driver's settlement, then the exact heads, prove
			// it.
			if err := l.settleHead(ctx, creation(r.Project, r.NewBranch, r.ExpectedOldTip), nil); err != nil {
				l.blockRetainedRename(op, err)
				return
			}
			if err := l.store.TransitionRetainedRename(ctx, op.ID, "new-ref-create-issued", "new-ref-created", true, func(ctx context.Context, saved control.RetainedRenameRequest) error {
				return l.verifyRetainedRenameRefs(ctx, saved, "created")
			}); err != nil {
				l.blockRetainedRename(op, err)
				return
			}
			op.Phase = "new-ref-created"
		case "new-ref-created":
			issued := false
			err := l.deleteBranch(ctx, r.Project, r.OldBranch, r.ExpectedOldTip, func() error {
				err := l.store.TransitionRetainedRename(ctx, op.ID, "new-ref-created", "old-ref-delete-issued", true, func(ctx context.Context, saved control.RetainedRenameRequest) error {
					return l.verifyRetainedRenameRefs(ctx, saved, "created")
				})
				issued = err == nil
				return err
			})
			if issued {
				op.Phase = "old-ref-delete-issued"
			}
			if err != nil {
				l.blockRetainedRename(op, err)
				return
			}
		case "old-ref-delete-issued":
			if err := l.settleHead(ctx, deletion(r.Project, r.OldBranch, r.ExpectedOldTip), nil); err != nil {
				l.blockRetainedRename(op, err)
				return
			}
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
