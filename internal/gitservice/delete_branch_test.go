package gitservice

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

func selectedDeleteBackend(t *testing.T) (*Backend, string) {
	t.Helper()
	b, repo := snapshotFixture(t)
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("Go compiler unavailable")
	}
	dir := filepath.Join(b.stateDir, "source-package")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plugin.json", "p-git-ssh"} {
		data, err := os.ReadFile(filepath.Join("../../plugins/bundled/source-git", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	build := exec.Command(goPath, "build", "-o", filepath.Join(dir, "git.wasm"), "./../../plugins/bundled/source-git")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOTOOLCHAIN=local")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build selected Git module: %v %s", err, out)
	}
	pkg, err := plugin.Conformance(dir)
	if err != nil {
		t.Fatal(err)
	}
	b.selection = plugin.Active{Package: pkg, Grants: []string{"git.project"}}
	return b, repo
}

func TestDeleteAssignedBranchCASLeavesSiblingAndRefusesMovedTip(t *testing.T) {
	b, repo := selectedDeleteBackend(t)
	tree := snapshotGit(t, repo, nil, "mktree")
	base := snapshotCommit(t, repo, tree, "")
	unique := snapshotCommit(t, repo, tree, base)
	snapshotGit(t, repo, nil, "update-ref", "refs/heads/main", unique)
	snapshotGit(t, repo, nil, "update-ref", "refs/heads/keep", base)
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
	b, repo := selectedDeleteBackend(t)
	marked := false
	g := &gitBroker{backend: b, project: "app", branch: "main", beforeDelete: func() error { marked = true; return nil }}
	if err := g.DeleteBranch(context.Background()); err != nil || !marked {
		t.Fatalf("unborn absence refused: marked=%t err=%v", marked, err)
	}
	base := snapshotCommit(t, repo, snapshotGit(t, repo, nil, "mktree"), "")
	snapshotGit(t, repo, nil, "update-ref", "refs/heads/main", base)
	marked = false
	if err := g.DeleteBranch(context.Background()); !errors.Is(err, control.ErrConflict) || marked {
		t.Fatalf("present unborn ref deleted: marked=%t err=%v", marked, err)
	}
}

func TestRetainedDeleteReviewAndSelectedExactCASOnRealBare(t *testing.T) {
	b, repo := selectedDeleteBackend(t)
	ctx := context.Background()
	tree := snapshotGit(t, repo, nil, "mktree")
	base := snapshotCommit(t, repo, tree, "")
	unique := snapshotCommit(t, repo, tree, base)
	snapshotGit(t, repo, nil, "update-ref", "refs/heads/saved", unique)
	snapshotGit(t, repo, nil, "update-ref", "refs/heads/keep", base)
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
