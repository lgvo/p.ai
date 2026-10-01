package gitservice

import (
	"context"
	"errors"
	"os/exec"

	"github.com/lgvo/p.ai/internal/control"
)

func (b *Backend) ValidateRenameBranch(ctx context.Context, name string) error {
	if !validBranch(name) || len(name) > 100 {
		return control.ErrInvalid
	}
	if err := b.revalidate(); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, b.gitPath, "check-ref-format", "--branch", name)
	cmd.Env = b.gitEnv()
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return control.ErrInvalid
	}
	return nil
}

// CreateRenameBranchExact is the one absent-ref CAS authorized by a guarded
// rename. The old P head must still name the exact captured commit. The
// selected source-Git package performs the create; the caller persists its
// attempt marker before invoking this method.
func (b *Backend) CreateRenameBranchExact(ctx context.Context, project, oldBranch, newBranch, expected string) error {
	if !validProject(project) || !validBranch(oldBranch) || !validBranch(newBranch) || oldBranch == newBranch || !validOID(expected) || allZero(expected) {
		return control.ErrInvalid
	}
	if err := b.ValidateRenameBranch(ctx, newBranch); err != nil {
		return err
	}
	old, exists, err := b.InspectBranchRef(ctx, project, oldBranch)
	if err != nil || !exists || old != expected {
		return errors.Join(err, control.ErrConflict)
	}
	_, exists, err = b.InspectBranchRef(ctx, project, newBranch)
	if err != nil || exists {
		return errors.Join(err, control.ErrConflict)
	}
	// The old ordinary P ref is the captured source proof. The broker still
	// verifies commit type and performs an absent-ref zero-old CAS.
	if err := b.createBranch(ctx, project, newBranch, expected, true); err != nil {
		return err
	}
	old, exists, err = b.InspectBranchRef(ctx, project, oldBranch)
	if err != nil || !exists || old != expected {
		return errors.Join(err, errors.New("old P ref changed during rename create"))
	}
	created, exists, err := b.InspectBranchRef(ctx, project, newBranch)
	if err != nil || !exists || created != expected {
		return errors.Join(err, errors.New("new P ref create postcondition unavailable"))
	}
	return nil
}
