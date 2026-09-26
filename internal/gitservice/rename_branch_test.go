package gitservice

import (
	"context"
	"errors"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
)

func TestSelectedRenameRefCreateRequiresOldExactAndNewAbsent(t *testing.T) {
	b, repo := selectedDeleteBackend(t)
	ctx := context.Background()
	tree := snapshotGit(t, repo, nil, "mktree")
	base := snapshotCommit(t, repo, tree, "")
	other := snapshotCommit(t, repo, tree, base)
	snapshotGit(t, repo, nil, "update-ref", "refs/heads/main", base)
	snapshotGit(t, repo, nil, "update-ref", "refs/heads/sibling", base)
	if err := b.CreateRenameBranchExact(ctx, "app", "main", "renamed", other); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("stale old tip accepted: %v", err)
	}
	if _, exists, err := b.InspectBranchRef(ctx, "app", "renamed"); err != nil || exists {
		t.Fatalf("stale create changed destination: %v %v", exists, err)
	}
	if err := b.CreateRenameBranchExact(ctx, "app", "main", "renamed", base); err != nil {
		t.Fatal(err)
	}
	for _, branch := range []string{"main", "renamed", "sibling"} {
		if tip, exists, err := b.InspectBranchRef(ctx, "app", branch); err != nil || !exists || tip != base {
			t.Fatalf("branch %s changed: %q %v %v", branch, tip, exists, err)
		}
	}
	if err := b.CreateRenameBranchExact(ctx, "app", "main", "renamed", base); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("occupied destination accepted: %v", err)
	}
}

func TestSelectedRetainedRenamePreservesSiblingAndNeverTouchesOrigin(t *testing.T) {
	b, repo := selectedDeleteBackend(t)
	ctx := context.Background()
	tree := snapshotGit(t, repo, nil, "mktree")
	base := snapshotCommit(t, repo, tree, "")
	tip := snapshotCommit(t, repo, tree, base)
	snapshotGit(t, repo, nil, "update-ref", "refs/heads/retained", tip)
	snapshotGit(t, repo, nil, "update-ref", "refs/heads/sibling", base)
	if err := b.CreateRenameBranchExact(ctx, "app", "retained", "archive", tip); err != nil {
		t.Fatal(err)
	}
	if err := b.DeleteAssignedBranchExact(ctx, "app", "retained", base, func() error { return nil }); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("wrong source tip deleted: %v", err)
	}
	if err := b.DeleteAssignedBranchExact(ctx, "app", "retained", tip, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"archive": tip, "sibling": base} {
		got, exists, err := b.InspectBranchRef(ctx, "app", name)
		if err != nil || !exists || got != want {
			t.Fatalf("%s: %q %v %v", name, got, exists, err)
		}
	}
	if got, exists, err := b.InspectBranchRef(ctx, "app", "retained"); err != nil || exists || got != "" {
		t.Fatalf("old ref survived: %q %v %v", got, exists, err)
	}
}
