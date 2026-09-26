package runtimeincus

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceLossCommitInventoryIncludesDanglingObject(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("Git is required by package checks")
	}
	repo, home := t.TempDir(), t.TempDir()
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-C", repo}, args...)...)
		cmd.Env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + home, "PATH=" + filepath.Dir(git),
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=0",
			"GIT_NO_REPLACE_OBJECTS=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return out
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "tracked"), []byte("seed"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "tracked")
	run("commit", "-m", "seed")
	head := strings.TrimSpace(string(run("rev-parse", "HEAD")))
	tree := strings.TrimSpace(string(run("rev-parse", "HEAD^{tree}")))
	dangling := strings.TrimSpace(string(run("commit-tree", tree, "-p", head, "-m", "dangling")))
	raw := run("cat-file", "--batch-all-objects", "--batch-check=%(objectname) %(objecttype)")
	commits, err := parseAllCommitObjects(raw)
	if err != nil || len(commits) != 2 || !containsString(commits, head) || !containsString(commits, dangling) {
		t.Fatalf("dangling commit omitted: %v %+v", err, commits)
	}
	if _, err := parseAllCommitObjects(bytes.Repeat([]byte("a"), 256<<10+1)); err == nil {
		t.Fatal("over-bound object stream accepted")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestWorkspaceLossIgnoredSummaryRequiresEverySourceFile(t *testing.T) {
	snapshot := WorkspaceSnapshot{Entries: []WorkspaceEntry{{Path: "", Type: "directory"}, {Path: "ignored", Type: "file", Data: []byte("abc")}}}
	got, err := summarizeWorkspaceIgnored([]byte("ignored\x00"), snapshot)
	if err != nil || got.Count != 1 || got.LogicalBytes != 3 {
		t.Fatalf("ignored summary: %+v %v", got, err)
	}
	for _, raw := range [][]byte{[]byte("missing\x00"), []byte("ignored\x00ignored\x00"), []byte("../escape\x00"), []byte("ignored")} {
		if _, err := summarizeWorkspaceIgnored(raw, snapshot); err == nil {
			t.Fatalf("unsafe ignored set accepted: %q", raw)
		}
	}
}

func TestWorkspaceLossHeadDistinguishesRealUnbornAndDetachedGit(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("Git is required by package checks")
	}
	repo, home := t.TempDir(), t.TempDir()
	run := func(args ...string) ([]byte, error) {
		cmd := exec.Command(git, append([]string{"-C", repo}, args...)...)
		cmd.Env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + home, "PATH=" + filepath.Dir(git),
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1",
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid"}
		return cmd.Output()
	}
	if _, err := run("init", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	branch, head, err := readLossHead(run)
	if err != nil || branch != "main" || head != "" {
		t.Fatalf("unborn branch: %q %q %v", branch, head, err)
	}
	if err := os.WriteFile(filepath.Join(repo, "tracked"), []byte("seed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := run("add", "tracked"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("commit", "-m", "seed"); err != nil {
		t.Fatal(err)
	}
	branch, head, err = readLossHead(run)
	if err != nil || branch != "main" || !validBuilderOID(head) {
		t.Fatalf("ordinary branch: %q %q %v", branch, head, err)
	}
	if _, err := run("checkout", "--detach", "HEAD"); err != nil {
		t.Fatal(err)
	}
	detachedBranch, detachedHead, err := readLossHead(run)
	if err != nil || detachedBranch != "" || detachedHead != head {
		t.Fatalf("detached branch: %q %q %v", detachedBranch, detachedHead, err)
	}
}
