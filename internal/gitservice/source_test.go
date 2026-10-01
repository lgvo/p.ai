package gitservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

func TestSelectedWASIOriginCaptureCreatesFirstPHeadAfterRestart(t *testing.T) {
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("Go compiler unavailable")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git unavailable")
	}
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	packageDir := filepath.Join(state, "source-package")
	if err := os.Mkdir(packageDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plugin.json", "p-git-ssh"} {
		data, err := os.ReadFile(filepath.Join("../../plugins/bundled/source-git", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(packageDir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	build := exec.Command(goPath, "build", "-o", filepath.Join(packageDir, "git.wasm"), "./../../plugins/bundled/source-git")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOTOOLCHAIN=local")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build source module: %v %s", err, out)
	}
	pkg, err := plugin.Conformance(packageDir)
	if err != nil {
		t.Fatal(err)
	}
	selection := plugin.Active{Package: pkg, Grants: []string{"git.project"}}
	b := &Backend{stateDir: state, gitPath: gitPath, selection: selection}
	if err := os.Mkdir(filepath.Join(state, "repositories"), 0700); err != nil {
		t.Fatal(err)
	}
	repo, err := b.repository("app")
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, nil, "init", "--bare", "--initial-branch=main", repo)
	origin := filepath.Join(state, "origin.git")
	gitTest(t, nil, "init", "--bare", "--initial-branch=main", origin)
	tree := gitTest(t, nil, "-C", origin, "mktree")
	author := []string{"GIT_AUTHOR_NAME=P Test", "GIT_AUTHOR_EMAIL=p@example.test", "GIT_COMMITTER_NAME=P Test", "GIT_COMMITTER_EMAIL=p@example.test"}
	captured := gitTest(t, author, "-C", origin, "commit-tree", tree, "-m", "captured")
	gitTest(t, nil, "-C", origin, "update-ref", "refs/heads/main", captured)
	gitTest(t, nil, "-C", repo, "fetch", "--no-tags", "--no-write-fetch-head", origin, "refs/heads/main")
	if got := gitTest(t, nil, "-C", repo, "for-each-ref", "--format=%(refname)"); got != "" {
		t.Fatalf("origin fetch created ordinary P ref: %q", got)
	}
	ctx := context.Background()
	if _, err := b.ObserveSource(ctx, "app", plugin.GitSourceSelector{Kind: "commit", Value: captured}); err == nil {
		t.Fatal("public source lookup accepted origin-only commit")
	}
	if err := b.CreateBranch(ctx, "app", "arbitrary", captured); err == nil {
		t.Fatal("public branch creation accepted origin-only commit")
	}
	if err := b.CreateCapturedOriginBranch(ctx, "caller-chosen", "app", "arbitrary", captured); err == nil {
		t.Fatal("missing durable store authorized captured origin commit")
	}
	// Exercise the production durable authority gate, not a manually trusted
	// broker. Nix build sandboxes can lack the trusted ancestors required by
	// OpenStore; the same test runs unskipped on the normal host and in VM gates.
	statePath := state
	if err := control.CheckTrustedAncestors(statePath); err != nil {
		t.Skipf("production OpenStore requires trusted filesystem ancestry: %v", err)
	}
	store, err := control.OpenStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if err = store.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	fingerprint := strings.Repeat("a", 64)
	selected := control.CreationSelection{RuntimeID: "runtime", RuntimeSHA256: fingerprint, HostID: "host", HostSHA256: fingerprint, SourceID: pkg.Manifest.ID, SourceSHA256: pkg.SHA256}
	req := control.ReserveSessionRequest{Key: "captured-source", Project: "app", Branch: "from-origin", Choice: "new", OriginRef: "refs/heads/main", ExpectedCommitOID: captured}
	op, session, err := store.BeginSessionCreateCaptured(ctx, req, fingerprint, selected, func(context.Context, control.ReserveSessionRequest) (control.CapturedSource, error) {
		return control.CapturedSource{OID: captured, OriginURL: "ssh://origin.invalid/repo", OriginRef: req.OriginRef}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.AdvanceOperation(ctx, op.ID, "blocked", "source-ready", false, op.Evidence, "interrupted before branch CAS"); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	moved := gitTest(t, author, "-C", origin, "commit-tree", tree, "-m", "moved")
	gitTest(t, nil, "-C", origin, "update-ref", "refs/heads/main", moved)
	// No transport can refetch the changed origin after this restart.
	if err = os.Rename(origin, origin+".unavailable"); err != nil {
		t.Fatal(err)
	}
	store, err = control.OpenStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	b, err = New(store, state, []plugin.Active{selection})
	if err != nil {
		t.Fatal(err)
	}
	replayed, again, err := store.BeginSessionCreateCaptured(ctx, req, "", control.CreationSelection{}, func(context.Context, control.ReserveSessionRequest) (control.CapturedSource, error) {
		t.Fatal("retry recaptured origin")
		return control.CapturedSource{}, nil
	})
	if err != nil || replayed.ID != op.ID || again.UUID != session.UUID {
		t.Fatalf("durable replay: %+v %+v %v", replayed, again, err)
	}
	assertDenied := func(id, project, branch, oid string) {
		t.Helper()
		if err := b.CreateCapturedOriginBranch(ctx, id, project, branch, oid); err == nil {
			t.Fatalf("unauthorized capture accepted: %s %s %s %s", id, project, branch, oid)
		}
		if refs := gitTest(t, nil, "-C", repo, "for-each-ref", "--format=%(refname)"); refs != "" {
			t.Fatalf("denial mutated refs: %s", refs)
		}
	}
	assertDenied("unknown-operation", "app", req.Branch, captured)
	assertDenied(op.ID, "different-project", req.Branch, captured)
	assertDenied(op.ID, "app", "arbitrary-branch", captured)
	assertDenied(op.ID, "app", req.Branch, moved)
	for _, invalid := range []struct{ status, phase string }{{"running", "reserved"}, {"unknown", "source-ready"}} {
		if err := store.AdvanceOperation(ctx, op.ID, invalid.status, invalid.phase, false, op.Evidence, "test invalid authority"); err != nil {
			t.Fatal(err)
		}
		assertDenied(op.ID, "app", req.Branch, captured)
	}
	evidence, err := control.Evidence(op)
	if err != nil {
		t.Fatal(err)
	}
	evidence.OriginRef = "refs/heads/other"
	badEvidence, _ := json.Marshal(evidence)
	if err := store.AdvanceOperation(ctx, op.ID, "running", "source-ready", false, badEvidence, "test mismatched evidence"); err != nil {
		t.Fatal(err)
	}
	assertDenied(op.ID, "app", req.Branch, captured)
	if err := store.AdvanceOperation(ctx, op.ID, "running", "source-ready", false, op.Evidence, ""); err != nil {
		t.Fatal(err)
	}
	if err := b.CreateCapturedOriginBranch(ctx, op.ID, "app", req.Branch, captured); err != nil {
		t.Fatalf("SQLite-authorized selected WASI branch creation: %v", err)
	}
	if got := gitTest(t, nil, "-C", repo, "rev-parse", "refs/heads/from-origin"); got != captured {
		t.Fatalf("branch tip %s, want captured %s", got, captured)
	}
	if err := b.CreateCapturedOriginBranch(ctx, op.ID, "app", req.Branch, captured); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("existing-ref CAS: %v", err)
	}
	// An established session cannot reuse a pending creation's exception even
	// if an external actor removes its ref.
	if err := store.AdvanceSessionRegistry(ctx, session.UUID, "creating", "established"); err != nil {
		t.Fatal(err)
	}
	gitTest(t, nil, "-C", repo, "update-ref", "-d", "refs/heads/from-origin", captured)
	assertDenied(op.ID, "app", req.Branch, captured)

}

func TestNativeSourceObservationAndAbsentRefCAS(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git unavailable")
	}
	state := t.TempDir()
	if err := os.Mkdir(filepath.Join(state, "repositories"), 0700); err != nil {
		t.Fatal(err)
	}
	b := &Backend{stateDir: state, gitPath: gitPath}
	repo, err := b.repository("app")
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, nil, "init", "--bare", "--initial-branch=main", repo)
	ctx := context.Background()
	unborn := &gitBroker{backend: b, project: "app", source: plugin.GitSourceSelector{Kind: "branch", Value: "refs/heads/main"}}
	if oid, err := unborn.ObserveSource(ctx); err == nil || oid != "" {
		t.Fatalf("unborn main accepted: %s %v", oid, err)
	}
	env := []string{"GIT_AUTHOR_NAME=P Test", "GIT_AUTHOR_EMAIL=p@example.test", "GIT_COMMITTER_NAME=P Test", "GIT_COMMITTER_EMAIL=p@example.test"}
	tree := gitTest(t, nil, "-C", repo, "mktree")
	first := gitTest(t, env, "-C", repo, "commit-tree", tree, "-m", "first")
	second := gitTest(t, env, "-C", repo, "commit-tree", tree, "-p", first, "-m", "second")
	hidden := gitTest(t, env, "-C", repo, "commit-tree", tree, "-m", "hidden")
	gitTest(t, nil, "-C", repo, "update-ref", "refs/hidden/only", hidden)
	gitTest(t, nil, "-C", repo, "update-ref", "refs/heads-private/only", hidden)
	gitTest(t, nil, "-C", repo, "update-ref", "refs/tags/only", hidden)
	gitTest(t, nil, "-C", repo, "update-ref", "refs/heads/main", first)
	branch := &gitBroker{backend: b, project: "app", source: plugin.GitSourceSelector{Kind: "branch", Value: "refs/heads/main"}}
	if oid, err := branch.ObserveSource(ctx); err != nil || oid != first {
		t.Fatalf("first tip: %s %v", oid, err)
	}
	gitTest(t, nil, "-C", repo, "update-ref", "refs/heads/main", second)
	if oid, err := branch.ObserveSource(ctx); err != nil || oid != second {
		t.Fatalf("moved tip: %s %v", oid, err)
	}
	for _, oid := range []string{first, second} {
		selector := &gitBroker{backend: b, project: "app", source: plugin.GitSourceSelector{Kind: "commit", Value: oid}}
		if got, err := selector.ObserveSource(ctx); err != nil || got != oid {
			t.Fatalf("reachable commit %s: %s %v", oid, got, err)
		}
	}
	for name, selector := range map[string]plugin.GitSourceSelector{
		"hidden only":      {Kind: "commit", Value: hidden},
		"missing branch":   {Kind: "branch", Value: "refs/heads/missing"},
		"noncommit":        {Kind: "commit", Value: tree},
		"invalid selector": {Kind: "branch", Value: "refs/hidden/only"},
	} {
		g := &gitBroker{backend: b, project: "app", source: selector}
		if oid, err := g.ObserveSource(ctx); err == nil || oid != "" {
			t.Errorf("%s accepted: %s %v", name, oid, err)
		}
	}
	create := &gitBroker{backend: b, project: "app", branch: "feature/new", commitOID: first}
	if err := create.CreateBranch(ctx); err != nil {
		t.Fatal(err)
	}
	if got := gitTest(t, nil, "-C", repo, "rev-parse", "refs/heads/feature/new"); got != first {
		t.Fatalf("created at %s", got)
	}
	if err := create.CreateBranch(ctx); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("same-tip replay: %v", err)
	}
	create.commitOID = second
	if err := create.CreateBranch(ctx); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("different-tip existing ref: %v", err)
	}
	if got := gitTest(t, nil, "-C", repo, "rev-parse", "refs/heads/feature/new"); got != first {
		t.Fatalf("existing destination moved: %s", got)
	}
	gitTest(t, nil, "-C", repo, "symbolic-ref", "refs/heads/alias", "refs/heads/missing")
	create.branch = "alias"
	if err := create.CreateBranch(ctx); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("existing dangling symbolic destination: %v", err)
	}
	if got := gitTest(t, nil, "-C", repo, "symbolic-ref", "refs/heads/alias"); got != "refs/heads/missing" {
		t.Fatalf("symbolic destination changed: %s", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "refs", "heads", "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symbolic target created: %v", err)
	}
	gitTest(t, nil, "-C", repo, "symbolic-ref", "refs/heads/resolved", "refs/heads/main")
	create.branch = "resolved"
	if err := create.CreateBranch(ctx); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("existing resolved symbolic destination: %v", err)
	}
	if got := gitTest(t, nil, "-C", repo, "symbolic-ref", "refs/heads/resolved"); got != "refs/heads/main" {
		t.Fatalf("resolved symbolic destination changed: %s", got)
	}
	if got := gitTest(t, nil, "-C", repo, "rev-parse", "refs/heads/main"); got != second {
		t.Fatalf("resolved symbolic target changed: %s", got)
	}
	create.branch = "feature/new/child"
	if err := create.CreateBranch(ctx); err == nil {
		t.Fatal("ref-name collision accepted")
	}
	if got := gitTest(t, nil, "-C", repo, "rev-parse", "refs/heads/feature/new"); got != first {
		t.Fatalf("colliding parent ref changed: %s", got)
	}
	create.branch, create.commitOID = "feature/hidden", hidden
	if err := create.CreateBranch(ctx); err == nil {
		t.Fatal("hidden-only commit created branch")
	}
	create.branch, create.commitOID = "feature/tree", tree
	if err := create.CreateBranch(ctx); err == nil {
		t.Fatal("tree object created branch")
	}
	if _, err := os.Stat(filepath.Join(repo, "refs", "heads", "feature", "hidden")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hidden ref created: %v", err)
	}
	// The captured first OID remains usable after main moved because it is
	// still reachable through main; the branch is never silently retargeted.
	if current := gitTest(t, nil, "-C", repo, "rev-parse", "refs/heads/main"); current != second {
		t.Fatalf("source moved unexpectedly: %s", current)
	}
}

func TestInspectBranchRefDistinguishesAbsenceFromSymbolicAndGitFailure(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git unavailable")
	}
	state := t.TempDir()
	b := &Backend{stateDir: state, gitPath: gitPath}
	repo, err := b.repository("app")
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, nil, "init", "--bare", "--initial-branch=main", repo)
	ctx := context.Background()
	check := func(branch, wantOID string, wantExists bool, wantErr error) {
		t.Helper()
		oid, exists, err := b.inspectBranchRef(ctx, repo, "refs/heads/"+branch)
		if oid != wantOID || exists != wantExists || (wantErr == nil && err != nil) || (wantErr != nil && !errors.Is(err, wantErr)) {
			t.Fatalf("%s: oid=%q exists=%v err=%v; want oid=%q exists=%v err=%v", branch, oid, exists, err, wantOID, wantExists, wantErr)
		}
	}
	check("main", "", false, nil) // Unborn HEAD does not occupy the branch ref.
	check("missing", "", false, nil)
	env := []string{"GIT_AUTHOR_NAME=P Test", "GIT_AUTHOR_EMAIL=p@example.test", "GIT_COMMITTER_NAME=P Test", "GIT_COMMITTER_EMAIL=p@example.test"}
	tree := gitTest(t, nil, "-C", repo, "mktree")
	oid := gitTest(t, env, "-C", repo, "commit-tree", tree, "-m", "one")
	gitTest(t, nil, "-C", repo, "update-ref", "refs/heads/main", oid)
	gitTest(t, nil, "-C", repo, "update-ref", "refs/heads/nested/child", oid)
	check("main", oid, true, nil)
	check("nested", "", false, nil) // A descendant is not the exact ref.
	gitTest(t, nil, "-C", repo, "symbolic-ref", "refs/heads/dangling", "refs/heads/missing")
	gitTest(t, nil, "-C", repo, "symbolic-ref", "refs/heads/resolved", "refs/heads/main")
	check("dangling", "", false, control.ErrConflict)
	check("resolved", "", false, control.ErrConflict)
	if got := gitTest(t, nil, "-C", repo, "symbolic-ref", "refs/heads/resolved"); got != "refs/heads/main" {
		t.Fatalf("inspection changed symbolic ref to %q", got)
	}

	// Git 2.55 uses status 128 for a missing ref with --hash. A native
	// failure at the quiet existence probe must still fail closed.
	wrapper := filepath.Join(state, "failing-git")
	script := "#!/bin/sh\nif [ \"$3\" = show-ref ]; then exit 128; fi\nexec '" + gitPath + "' \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	b.gitPath = wrapper
	if oid, exists, err := b.inspectBranchRef(ctx, repo, "refs/heads/missing"); err == nil || oid != "" || exists {
		t.Fatalf("Git failure classified as absence: oid=%q exists=%v err=%v", oid, exists, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if oid, exists, err := b.inspectBranchRef(canceled, repo, "refs/heads/missing"); !errors.Is(err, context.Canceled) || oid != "" || exists {
		t.Fatalf("canceled inspection classified as absence: oid=%q exists=%v err=%v", oid, exists, err)
	}
}

func TestNativeSourceObservationPreservesCancellation(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git unavailable")
	}
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep unavailable")
	}
	state := t.TempDir()
	if err := os.Mkdir(filepath.Join(state, "repositories"), 0700); err != nil {
		t.Fatal(err)
	}
	b := &Backend{stateDir: state, gitPath: gitPath}
	repo, err := b.repository("app")
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, nil, "init", "--bare", "--initial-branch=main", repo)
	wrapper := filepath.Join(state, "slow-git")
	script := "#!/bin/sh\nif [ \"$3\" = show-ref ]; then exec " + sleepPath + " 30; fi\nexec " + gitPath + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	b.gitPath = wrapper
	g := &gitBroker{backend: b, project: "app", source: plugin.GitSourceSelector{Kind: "branch", Value: "refs/heads/main"}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = g.ObserveSource(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost cancellation: %v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("native source operation ignored deadline")
	}
}
