package gitservice

import (
	"context"
	"errors"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

// DeleteAssignedBranchExact routes the only authorized P-ref deletion through
// the selected source-Git package. The core pins the project, ordinary branch,
// and expected old OID. The broker alone forms the fixed Git command.
func (b *Backend) DeleteAssignedBranchExact(ctx context.Context, project, branch, expected string, beforeEffect func() error) error {
	if !validProject(project) || !validBranch(branch) || expected != "" && (!validOID(expected) || allZero(expected)) || beforeEffect == nil {
		return control.ErrInvalid
	}
	if _, err := b.checkRepository(ctx, project); err != nil {
		return err
	}
	_, err := plugin.RunSourceGit(ctx, b.selection, plugin.GitCommand{Kind: "git.branch.delete", Project: project, Branch: branch, CommitOID: expected},
		&gitBroker{backend: b, project: project, branch: branch, commitOID: expected, beforeDelete: beforeEffect})
	return err
}

func (g *gitBroker) DeleteBranch(ctx context.Context) error {
	if !validBranch(g.branch) || g.commitOID != "" && (!validOID(g.commitOID) || allZero(g.commitOID)) || g.beforeDelete == nil {
		return control.ErrInvalid
	}
	current, exists, err := g.backend.InspectBranchRef(ctx, g.project, g.branch)
	if err != nil {
		return err
	}
	if g.commitOID == "" {
		if exists || current != "" {
			return control.ErrConflict
		}
		if err = g.beforeDelete(); err != nil {
			return err
		}
		return nil
	}
	if !exists || current != g.commitOID {
		return control.ErrConflict
	}
	if err = g.beforeDelete(); err != nil {
		return err
	}
	ref := "refs/heads/" + g.branch
	_, effectErr := g.sourceGit(ctx, "update-ref", "--no-deref", "-d", ref, g.commitOID)
	if effectErr != nil {
		return errors.Join(effectErr, errors.New("exact P ref deletion outcome unavailable"))
	}
	current, exists, err = g.backend.InspectBranchRef(ctx, g.project, g.branch)
	if err != nil || exists || current != "" {
		return errors.Join(err, errors.New("exact P ref deletion postcondition unavailable"))
	}
	return nil
}
