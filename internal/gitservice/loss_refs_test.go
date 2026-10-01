package gitservice

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

func TestRetainedLossRefsWithRealBareRepository(t *testing.T) {
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Fatal("Go compiler required by package check")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("Git required by package check")
	}
	state := t.TempDir()
	pkgDir := filepath.Join(state, "source-package")
	if err := os.Mkdir(pkgDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plugin.json", "p-git-ssh"} {
		data, err := os.ReadFile(filepath.Join("../../plugins/bundled/source-git", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pkgDir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	build := exec.Command(goPath, "build", "-o", filepath.Join(pkgDir, "git.wasm"), "./../../plugins/bundled/source-git")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOTOOLCHAIN=local")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build selected source plugin: %v: %s", err, output)
	}
	pkg, err := plugin.Conformance(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	b := &Backend{stateDir: state, gitPath: gitPath, selection: plugin.Active{Package: pkg, Grants: []string{"git.project"}}}
	if err := b.EnsureBare(context.Background(), "loss"); err != nil {
		t.Fatal(err)
	}
	repo, err := b.RepositoryPath("loss")
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(state, "work")
	gitTest(t, nil, "init", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "tracked"), []byte("seed"), 0600); err != nil {
		t.Fatal(err)
	}
	identity := []string{"GIT_AUTHOR_NAME=P Test", "GIT_AUTHOR_EMAIL=p@example.test", "GIT_COMMITTER_NAME=P Test", "GIT_COMMITTER_EMAIL=p@example.test"}
	gitTest(t, identity, "-C", work, "add", "tracked")
	gitTest(t, identity, "-C", work, "commit", "-m", "seed")
	retained := gitTest(t, nil, "-C", work, "rev-parse", "HEAD")
	gitTest(t, nil, "-C", work, "push", repo, "HEAD:refs/heads/main")
	if err := os.WriteFile(filepath.Join(work, "tracked"), []byte("local"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, identity, "-C", work, "commit", "-am", "local")
	missing := gitTest(t, nil, "-C", work, "rev-parse", "HEAD")
	dangling := gitTest(t, identity, "-C", repo, "commit-tree", gitTest(t, nil, "-C", repo, "rev-parse", "refs/heads/main^{tree}"), "-m", "dangling")
	refs, local, err := b.RetainedLossRefs(context.Background(), "loss", []string{retained, missing, dangling})
	if err != nil || len(refs) != 1 || refs[0].Ref != "refs/heads/main" || refs[0].OID != retained ||
		!reflect.DeepEqual(local, []string{dangling, missing}) && !reflect.DeepEqual(local, []string{missing, dangling}) {
		t.Fatalf("retention comparison: refs=%+v local=%v err=%v", refs, local, err)
	}
	branchLoss, err := b.BranchRemovalLoss(context.Background(), "loss", "main", false)
	if err != nil || branchLoss.AssignedTip != retained || !reflect.DeepEqual(branchLoss.CommitsLosingPReachability, []string{retained}) {
		t.Fatalf("delete branch loss: %+v %v", branchLoss, err)
	}
	gitTest(t, nil, "-C", repo, "branch", "keep", retained)
	branchLoss, err = b.BranchRemovalLoss(context.Background(), "loss", "main", false)
	if err != nil || len(branchLoss.CommitsLosingPReachability) != 0 {
		t.Fatalf("retained side branch still reported as loss: %+v %v", branchLoss, err)
	}
	// Unreferenced objects do not change the semantics of an unborn assigned
	// branch. The object may be left by a prior fetch or aborted operation.
	if err := b.EnsureBare(context.Background(), "unborn"); err != nil {
		t.Fatal(err)
	}
	unbornRepo, err := b.RepositoryPath("unborn")
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, nil, "-C", unbornRepo, "fetch", work, retained)
	unborn, err := b.BranchRemovalLoss(context.Background(), "unborn", "main", true)
	if err != nil || unborn.AssignedTip != "" || len(unborn.CommitsLosingPReachability) != 0 || len(unborn.PRefs) != 0 {
		t.Fatalf("unborn branch with dangling fetched object unavailable: %+v %v", unborn, err)
	}
	originLoss, err := b.CompareOriginRemoval(context.Background(), "loss", []plugin.GitOriginRef{{Ref: "refs/heads/origin-main", CommitOID: retained}}, []string{retained})
	if err != nil || originLoss.Status != "observed" || !reflect.DeepEqual(originLoss.ContainingBranches, []string{"refs/heads/origin-main"}) || len(originLoss.ObservedRefsDigest) != 64 {
		t.Fatalf("fresh origin containment: %+v %v", originLoss, err)
	}
	originLoss, err = b.CompareOriginRemoval(context.Background(), "loss", []plugin.GitOriginRef{{Ref: "refs/heads/unfetched", CommitOID: strings.Repeat("f", 40)}}, []string{retained})
	if err != nil || originLoss.Status != "unknown" || !reflect.DeepEqual(originLoss.UnresolvedRefs, []string{"refs/heads/unfetched"}) {
		t.Fatalf("unfetched origin commit misclassified: %+v %v", originLoss, err)
	}
	if _, err := b.CompareOriginRemoval(context.Background(), "loss", []plugin.GitOriginRef{{Ref: "invalid", CommitOID: retained}}, nil); err == nil {
		t.Fatal("malformed advertised origin ref accepted when no commits would be lost")
	}
	// A workspace loss inspection must remain complete when its assigned P
	// head was removed externally. The local commit is still a valid object,
	// but no P head now retains it.
	gitTest(t, nil, "-C", repo, "update-ref", "-d", "refs/heads/main")
	gitTest(t, nil, "-C", repo, "update-ref", "-d", "refs/heads/keep")
	refs, local, err = b.RetainedLossRefs(context.Background(), "loss", []string{retained, missing, dangling})
	if err != nil || len(refs) != 0 || len(local) != 3 {
		t.Fatalf("missing assigned P ref made workspace loss unavailable: refs=%+v local=%v err=%v", refs, local, err)
	}
	if present, err := b.PCommitPresent(context.Background(), "loss", retained); err != nil || !present {
		t.Fatalf("unreferenced but present P commit unavailable: %v %v", present, err)
	}
	if present, err := b.PCommitPresent(context.Background(), "loss", missing); err != nil || present {
		t.Fatalf("runtime-only commit misclassified as P-owned: %v %v", present, err)
	}
	if err := b.createBranch(context.Background(), "loss", "main", retained, true); err != nil {
		t.Fatalf("selected source-Git absent-ref CAS refused bare-present commit: %v", err)
	}
	if tip, exists, err := b.InspectBranchRef(context.Background(), "loss", "main"); err != nil || !exists || tip != retained {
		t.Fatalf("restored P head differs: %q %v %v", tip, exists, err)
	}
	if err := b.createBranch(context.Background(), "loss", "main", dangling, true); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("occupied restored head accepted replacement: %v", err)
	}
	// A broken P tip cannot be considered a retained object even when its
	// literal OID matches a local object.
	bogus := strings.Repeat("1", 40)
	if err := os.WriteFile(filepath.Join(repo, "refs", "heads", "main"), []byte(bogus+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.RetainedLossRefs(context.Background(), "loss", []string{retained}); err == nil {
		t.Fatal("missing P tip object accepted as retention")
	}
	// A nonzero native batch query is unavailable; it is never interpreted
	// as another missing commit.
	if err := os.WriteFile(filepath.Join(repo, "refs", "heads", "main"), []byte(retained+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(state, "git-fail-batch")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nif [ \"$3\" = cat-file ]; then exit 9; fi\nexec "+gitPath+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	b.gitPath = wrapper
	if _, _, err := b.RetainedLossRefs(context.Background(), "loss", []string{retained}); err == nil {
		t.Fatal("failed P batch observation treated as absent")
	}
}
