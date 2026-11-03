package gitservice

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func bareGit(t *testing.T, repo string, input []byte, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=P Test", "GIT_AUTHOR_EMAIL=p@example.test", "GIT_COMMITTER_NAME=P Test", "GIT_COMMITTER_EMAIL=p@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// bareFixture is P's bare repository for project "app", without a selected
// source-Git package, which only P's Git service needs.
func bareFixture(t *testing.T) (*Backend, string) {
	t.Helper()
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
	bareGit(t, state, nil, "init", "--bare", "--initial-branch=main", repo)
	return b, repo
}

func bareBlob(t *testing.T, repo, content string) string {
	t.Helper()
	return bareGit(t, repo, []byte(content), "hash-object", "-w", "--stdin")
}

func bareTree(t *testing.T, repo string, entries ...string) string {
	t.Helper()
	return bareGit(t, repo, []byte(strings.Join(entries, "\x00")+"\x00"), "mktree", "-z")
}

func bareCommit(t *testing.T, repo, tree, parent string) string {
	t.Helper()
	args := []string{"commit-tree", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	return bareGit(t, repo, []byte("commit\n"), args...)
}
