package gitclone_test

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/gitclone"
)

// sessionUser is the session user as the clone's files show it: the test's
// own account maps to it, as a session's idmap maps the guest's user.
const sessionUser = 1000

// workspace is a real Git clone on disk, read and written the way core
// reads and writes a session's clone through the runtime driver.
type workspace struct {
	t    *testing.T
	root string
}

func (w workspace) read(p string, bound int64) sourcedriver.File {
	w.t.Helper()
	full := filepath.Join(w.root, filepath.FromSlash(p))
	info, err := os.Lstat(full)
	if errors.Is(err, fs.ErrNotExist) {
		return sourcedriver.File{Type: sourcedriver.FileAbsent}
	}
	if err != nil {
		w.t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	f := sourcedriver.File{Type: sourcedriver.FileOther, UID: int(stat.Uid), GID: int(stat.Gid), Mode: uint32(info.Mode().Perm())}
	if int(stat.Uid) == os.Getuid() {
		f.UID, f.GID = sessionUser, sessionUser
	}
	switch {
	case info.Mode().IsRegular():
		f.Type = sourcedriver.FileRegular
		if info.Size() > bound {
			w.t.Fatalf("%s holds %d bytes, more than its bound of %d", p, info.Size(), bound)
		}
		if f.Data, err = os.ReadFile(full); err != nil {
			w.t.Fatal(err)
		}
	case info.IsDir():
		f.Type = sourcedriver.FileDirectory
	case info.Mode()&fs.ModeSymlink != 0:
		f.Type = sourcedriver.FileSymlink
	}
	return f
}

func (w workspace) apply(edits []sourcedriver.WorkspaceFile) {
	w.t.Helper()
	for _, e := range edits {
		full := filepath.Join(w.root, filepath.FromSlash(e.Path))
		if e.File.Type == sourcedriver.FileAbsent {
			if err := os.Remove(full); err != nil {
				w.t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(full, e.File.Data, 0o600); err != nil {
			w.t.Fatal(err)
		}
		if err := os.Chmod(full, fs.FileMode(e.File.Mode)); err != nil {
			w.t.Fatal(err)
		}
	}
}

// reach takes the clone to r's goal and proves that it holds: once the
// edits are applied, Rename asks for none.
func (w workspace) reach(r sourcedriver.WorkspaceRename) {
	w.t.Helper()
	for _, apply := range []bool{true, false} {
		var files []sourcedriver.WorkspaceFile
		for {
			answer, err := gitclone.Rename(r, files)
			if err != nil {
				w.t.Fatalf("Rename toward %s: %v", r.Goal, err)
			}
			if len(answer.Read) == 0 {
				if !apply && len(answer.Edits) != 0 {
					w.t.Fatalf("toward %s, %d edits are left once applied", r.Goal, len(answer.Edits))
				}
				w.apply(answer.Edits)
				break
			}
			for p, bound := range answer.Read {
				files = append(files, sourcedriver.WorkspaceFile{Path: p, File: w.read(p, bound)})
			}
		}
	}
}

func TestRenamedCloneIsOnTheNewBranchForGitWithItsUpstreamIdentityAndFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git unavailable")
	}
	repo := t.TempDir()
	git := func(args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	run := func(args ...string) string {
		t.Helper()
		out, err := git(args...)
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return out
	}
	run("init", "--initial-branch=main")
	run("config", "user.name", "P")
	run("config", "user.email", "p@example.invalid")
	run("config", "remote.origin.url", "ssh://git@p/app")
	run("config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	run("config", "branch.main.remote", "origin")
	run("config", "branch.main.merge", "refs/heads/main")
	for _, content := range []string{"base", "ahead"} {
		if err := os.WriteFile(filepath.Join(repo, "tracked"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		run("add", "tracked")
		run("commit", "-qm", content)
	}
	if err := os.WriteFile(filepath.Join(repo, "untracked"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	head := run("rev-parse", "HEAD")
	// A session's Git writes nothing others can write, whatever the test's
	// umask is.
	if err := filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.Chmod(p, info.Mode().Perm()&^0o022)
	}); err != nil {
		t.Fatal(err)
	}
	w := workspace{t: t, root: repo}
	for _, goal := range []sourcedriver.Goal{sourcedriver.GoalPrepared, sourcedriver.GoalRenamed, sourcedriver.GoalDone} {
		w.reach(sourcedriver.WorkspaceRename{ID: "550e8400-e29b-41d4-a716-446655440001", Old: "main", New: "renamed", Goal: goal, User: sessionUser})
	}
	for _, check := range []struct{ args, want string }{
		{"branch --show-current", "renamed"},
		{"rev-parse HEAD", head},
		{"config branch.renamed.remote", "origin"},
		{"config branch.renamed.merge", "refs/heads/renamed"},
		{"config user.email", "p@example.invalid"},
		{"rev-list --count renamed@{1}", "1"},
		{"status --porcelain", "?? untracked"},
	} {
		if got := run(strings.Fields(check.args)...); got != check.want {
			t.Errorf("git %s = %q, want %q", check.args, got, check.want)
		}
	}
	if out, err := git("rev-parse", "--verify", "--quiet", "refs/heads/main"); err == nil {
		t.Errorf("the old branch is still there, at %s", out)
	}
	if out, err := git("config", "branch.main.merge"); err == nil {
		t.Errorf("the old branch's upstream is still there: %s", out)
	}
	if content, err := os.ReadFile(filepath.Join(repo, "untracked")); err != nil || string(content) != "private" {
		t.Errorf("the untracked file holds %q, %v; want it as it was", content, err)
	}
	if backups, _ := filepath.Glob(filepath.Join(repo, ".git", "p-rename-*")); len(backups) != 0 {
		t.Errorf("the backup is still there: %v", backups)
	}
}
