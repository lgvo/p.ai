package runtimeincus

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type delayedRenameFiles struct {
	memoryImageFiles
	pending     guestFile
	pendingPath string
}

func (f *delayedRenameFiles) lstatOptional(_ context.Context, name string) (guestFile, bool, error) {
	v, ok := f.files[name]
	return v, ok, nil
}

func (f *delayedRenameFiles) put(_ context.Context, name string, file guestFile) error {
	f.pendingPath, f.pending = name, file
	return context.DeadlineExceeded
}

func TestRenameBackupUncertainWriteRetainsEffectMarkerForRecovery(t *testing.T) {
	ctx := context.Background()
	dir := guestFile{typ: "directory", uid: 1000, gid: 1000, mode: 0755}
	file := func(data string) guestFile {
		return guestFile{typ: "file", uid: 1000, gid: 1000, mode: 0644, data: []byte(data)}
	}
	opID := "550e8400-e29b-41d4-a716-446655440001"
	f := &delayedRenameFiles{memoryImageFiles: memoryImageFiles{files: map[string]guestFile{
		"/workspace": dir, "/workspace/.git": dir, "/workspace/.git/refs": dir,
		"/workspace/.git/refs/heads":      dir,
		"/workspace/.git/HEAD":            file("ref: refs/heads/main\n"),
		"/workspace/.git/refs/heads/main": file(strings.Repeat("a", 40) + "\n"),
		"/workspace/.git/config":          file("[core]\n repositoryformatversion = 0\n bare = false\n"),
	}}}
	marked := 0
	_, err := prepareWorkspaceRenameFiles(ctx, f, opID, "main", "renamed", func() error { marked++; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || marked != 1 || f.pendingPath != renameBackupPath(opID) {
		t.Fatalf("uncertain write lacks exact durable marker: marker=%d path=%s err=%v", marked, f.pendingPath, err)
	}
	if _, exists := f.files[f.pendingPath]; exists {
		t.Fatal("timed-out write already visible")
	}
	// A delayed Incus admission can materialize the exact backup after the
	// original request returned. Recovery may move forward only on this proof.
	f.files[f.pendingPath] = f.pending
	plan, err := verifyPreparedWorkspaceRenameFiles(ctx, f, opID, "main", "renamed")
	if err != nil || plan.HeadOID != strings.Repeat("a", 40) {
		t.Fatalf("delayed exact backup not recoverable: %+v %v", plan, err)
	}
}

func TestRenameConfigPreservesIdentityAndMovesOnlyAssignedUpstream(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "--initial-branch=main")
	run("config", "user.name", "P")
	run("config", "user.email", "p@example.invalid")
	run("config", "remote.origin.url", "ssh://git@p/app")
	run("config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	run("config", "branch.main.remote", "origin")
	run("config", "branch.main.merge", "refs/heads/main")
	if err := os.WriteFile(filepath.Join(repo, "tracked"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "tracked")
	run("commit", "-qm", "base")
	if err := os.WriteFile(filepath.Join(repo, "tracked"), []byte("ahead"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "tracked")
	run("commit", "-qm", "ahead")
	if err := os.WriteFile(filepath.Join(repo, "untracked"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	head := run("rev-parse", "HEAD")
	configPath := filepath.Join(repo, ".git", "config")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	after, err := renameConfig(before, "main", "renamed")
	if err != nil || bytes.Equal(after, before) {
		t.Fatalf("config rewrite unavailable: %v", err)
	}
	backup := workspaceRenameBackup{Schema: "p.workspace-rename/v1", Old: "main", New: "renamed", Head: []byte("ref: refs/heads/main\n"), Ref: []byte(head + "\n"), Config: before}
	plan, checked, err := renameBackupDetails(backup, "main", "renamed")
	if err != nil || plan.HeadOID != head || !bytes.Equal(checked, after) {
		t.Fatalf("backup plan: %+v %v", plan, err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "refs", "heads", "renamed"), backup.Ref, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/renamed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, after, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, ".git", "refs", "heads", "main")); err != nil {
		t.Fatal(err)
	}
	if got := run("branch", "--show-current"); got != "renamed" {
		t.Fatalf("branch = %q", got)
	}
	if got := run("rev-parse", "HEAD"); got != head {
		t.Fatalf("local-ahead commit changed: %q", got)
	}
	if got := run("config", "branch.renamed.merge"); got != "refs/heads/renamed" {
		t.Fatalf("upstream remained old: %q", got)
	}
	if got := run("config", "user.email"); got != "p@example.invalid" {
		t.Fatalf("identity changed: %q", got)
	}
	if content, err := os.ReadFile(filepath.Join(repo, "untracked")); err != nil || string(content) != "private" {
		t.Fatalf("untracked file changed: %q %v", content, err)
	}
}

func TestRenameConfigRefusesExecutableOrAmbiguousGitSettings(t *testing.T) {
	for _, raw := range []string{
		"[core]\nrepositoryformatversion = 0\nbare = false\n[include]\npath = /tmp/evil\n",
		"[core]\nrepositoryformatversion = 0\nbare = false\n[branch \"main\"]\nremote = origin\nmerge = refs/heads/other\n[remote \"origin\"]\nfetch = +refs/heads/*:refs/remotes/origin/*\n",
		"[core]\nrepositoryformatversion = 0\nbare = false\n[branch \"renamed\"]\nremote = origin\nmerge = refs/heads/renamed\n[remote \"origin\"]\nfetch = +refs/heads/*:refs/remotes/origin/*\n",
	} {
		if _, err := renameConfig([]byte(raw), "main", "renamed"); err == nil {
			t.Fatalf("unsafe Git config accepted: %q", raw)
		}
	}
	for _, name := range []string{"../escape", "a//b", ".private", "a.lock", strings.Repeat("x", 101)} {
		if validWorkspaceRenameName(name) {
			t.Fatalf("unsafe branch accepted: %q", name)
		}
	}
}
