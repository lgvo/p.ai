package gitservice

import (
	"context"
	"errors"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
)

func TestDeleteAssignedBranchCASLeavesSiblingAndRefusesMovedTip(t *testing.T) {
	b, repo := selectedBackend(t)
	tree := bareGit(t, repo, nil, "mktree")
	base := bareCommit(t, repo, tree, "")
	unique := bareCommit(t, repo, tree, base)
	bareGit(t, repo, nil, "update-ref", "refs/heads/main", unique)
	bareGit(t, repo, nil, "update-ref", "refs/heads/keep", base)
	marked := false
	g := &gitBroker{backend: b, project: "app", branch: "main", commitOID: base, beforeDelete: func() error { marked = true; return nil }}
	if err := g.DeleteBranch(context.Background()); !errors.Is(err, control.ErrConflict) || marked {
		t.Fatalf("moved tip reached effect: marked=%t err=%v", marked, err)
	}
	g.commitOID = unique
	if err := g.DeleteBranch(context.Background()); err != nil || !marked {
		t.Fatalf("exact CAS failed: marked=%t err=%v", marked, err)
	}
	if tip, exists, err := b.InspectBranchRef(context.Background(), "app", "main"); err != nil || exists || tip != "" {
		t.Fatalf("deleted ref remains: %q %t %v", tip, exists, err)
	}
	if tip, exists, err := b.InspectBranchRef(context.Background(), "app", "keep"); err != nil || !exists || tip != base {
		t.Fatalf("sibling changed: %q %t %v", tip, exists, err)
	}
}

func TestDeleteUnbornRefProvesAbsenceWithoutMutation(t *testing.T) {
	b, repo := selectedBackend(t)
	marked := false
	g := &gitBroker{backend: b, project: "app", branch: "main", beforeDelete: func() error { marked = true; return nil }}
	if err := g.DeleteBranch(context.Background()); err != nil || !marked {
		t.Fatalf("unborn absence refused: marked=%t err=%v", marked, err)
	}
	base := bareCommit(t, repo, bareGit(t, repo, nil, "mktree"), "")
	bareGit(t, repo, nil, "update-ref", "refs/heads/main", base)
	marked = false
	if err := g.DeleteBranch(context.Background()); !errors.Is(err, control.ErrConflict) || marked {
		t.Fatalf("present unborn ref deleted: marked=%t err=%v", marked, err)
	}
}

func TestRetainedDeleteReviewAndSelectedExactCASOnRealBare(t *testing.T) {
	b, repo := selectedBackend(t)
	ctx := context.Background()
	tree := bareGit(t, repo, nil, "mktree")
	base := bareCommit(t, repo, tree, "")
	unique := bareCommit(t, repo, tree, base)
	bareGit(t, repo, nil, "update-ref", "refs/heads/saved", unique)
	bareGit(t, repo, nil, "update-ref", "refs/heads/keep", base)
	loss, err := b.BranchRemovalLoss(ctx, "app", "saved", false)
	if err != nil || loss.AssignedTip != unique || len(loss.CommitsLosingPReachability) != 1 || loss.CommitsLosingPReachability[0] != unique {
		t.Fatalf("retained loss: %+v %v", loss, err)
	}
	if err := b.DeleteAssignedBranchExact(ctx, "app", "saved", base, func() error { return nil }); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("stale tip deleted: %v", err)
	}
	if err := b.DeleteAssignedBranchExact(ctx, "app", "saved", unique, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if tip, exists, err := b.InspectBranchRef(ctx, "app", "saved"); err != nil || exists || tip != "" {
		t.Fatalf("target survived: %q %v %v", tip, exists, err)
	}
	if tip, exists, err := b.InspectBranchRef(ctx, "app", "keep"); err != nil || !exists || tip != base {
		t.Fatalf("sibling changed: %q %v %v", tip, exists, err)
	}
}
