package gitservice

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
)

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
