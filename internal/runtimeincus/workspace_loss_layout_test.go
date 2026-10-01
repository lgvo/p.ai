package runtimeincus

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func linkedLayout(root string) WorkspaceSnapshot {
	return WorkspaceSnapshot{Entries: []WorkspaceEntry{
		{Path: ".git/worktrees", Type: "directory"},
		{Path: ".git/worktrees/side", Type: "directory"},
		{Path: ".git/worktrees/side/gitdir", Type: "file", Data: []byte(root + "/.git\n")},
		{Path: ".git/worktrees/side/commondir", Type: "file", Data: []byte("../..\n")},
		{Path: ".git/worktrees/side/HEAD", Type: "file", Data: []byte("ref: refs/heads/side\n")},
	}}
}

func TestWorkspaceLinkedTreeInventoryAndBidirectionalPointer(t *testing.T) {
	for _, root := range []string{"/workspace/side", "/home/p/worktrees/side"} {
		links, err := parseWorkspaceLinkedTrees(linkedLayout(root))
		if err != nil || len(links) != 1 || links[0].Root != root || links[0].GitFile != root+"/.git" || links[0].AdminDir != "/workspace/.git/worktrees/side" {
			t.Fatalf("supported linked tree %s: %+v, %v", root, links, err)
		}
		if err := verifyLinkedGitFile(links[0], WorkspaceEntry{Type: "file", Data: []byte("gitdir: /workspace/.git/worktrees/side\n")}); err != nil {
			t.Fatal(err)
		}
		for _, bad := range []WorkspaceEntry{
			{Type: "symlink", Target: "/workspace/.git/worktrees/side"},
			{Type: "file", Data: []byte("gitdir: /etc/p/git\n")},
			{Type: "file", Data: []byte("gitdir: /workspace/.git/worktrees/side\nextra")},
		} {
			if err := verifyLinkedGitFile(links[0], bad); err == nil {
				t.Fatal("unsafe reverse pointer accepted")
			}
		}
	}
}

func TestWorkspaceLinkedTreeInventoryRefusesIncompleteOrProtectedPaths(t *testing.T) {
	for _, root := range []string{
		"/", "/home/p", "/home/p/.codex", "/home/p/worktrees", "/etc/p/git", "/opt/p/endpoints",
		"/workspace/../home/p", "/workspace/sub/side", "/workspace/.git", "/tmp/side", "/mnt/external/side",
	} {
		if _, err := parseWorkspaceLinkedTrees(linkedLayout(root)); err == nil {
			t.Fatalf("protected or unsupported worktree %q accepted", root)
		}
	}
	for _, mutation := range []func(*WorkspaceSnapshot){
		func(s *WorkspaceSnapshot) { s.Entries[2].Data = []byte("/home/p/worktrees/side/.git\nother\n") },
		func(s *WorkspaceSnapshot) { s.Entries[3].Data = []byte("../../../../../etc/p/git\n") },
		func(s *WorkspaceSnapshot) { s.Entries[4].Type = "symlink" },
		func(s *WorkspaceSnapshot) { s.Entries[0].Type = "symlink" },
		func(s *WorkspaceSnapshot) { s.Entries = append(s.Entries, s.Entries[2]) },
		func(s *WorkspaceSnapshot) {
			s.Entries = append(s.Entries, WorkspaceEntry{Path: ".git/worktrees/hidden/gitdir", Type: "file"})
		},
		func(s *WorkspaceSnapshot) {
			s.Entries = append(s.Entries, WorkspaceEntry{Path: ".git/worktrees/side/config.worktree", Type: "file", Data: []byte("[core]\nfsmonitor=/tmp/evil\n")})
		},
		func(s *WorkspaceSnapshot) {
			s.Entries = append(s.Entries, WorkspaceEntry{Path: ".git/worktrees/side/custom", Type: "file", Data: []byte("unexpected")})
		},
	} {
		snapshot := linkedLayout("/home/p/worktrees/side")
		mutation(&snapshot)
		if _, err := parseWorkspaceLinkedTrees(snapshot); err == nil {
			t.Fatal("incomplete or ambiguous Git worktree inventory accepted")
		}
	}
	if _, err := parseWorkspaceLinkedTrees(WorkspaceSnapshot{Entries: []WorkspaceEntry{{Path: ".git/worktrees", Type: "directory"}, {Path: ".git/worktrees/" + strings.Repeat("x", 101), Type: "directory"}}}); err == nil {
		t.Fatal("oversized worktree administration name accepted")
	}
}

func TestWorkspaceLinkedTreeInventoryFromGitWorktreeWithReflog(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("Git is required by the package check inputs")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	side := filepath.Join(t.TempDir(), "side")
	home := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-C", dir}, args...)...)
		cmd.Env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + home, "PATH=" + filepath.Dir(git),
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=0",
			"GIT_ATTR_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.invalid"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run(repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "tracked"), []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", "tracked")
	run(repo, "commit", "-m", "seed")
	run(repo, "worktree", "add", "-b", "side", side)
	if err := os.WriteFile(filepath.Join(side, "tracked"), []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run(side, "commit", "-am", "side change")
	admin := filepath.Join(repo, ".git", "worktrees")
	snapshot := WorkspaceSnapshot{Entries: []WorkspaceEntry{{Path: ".git/worktrees", Type: "directory"}}}
	var walk func(string, string)
	walk = func(dir, rel string) {
		t.Helper()
		children, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, child := range children {
			childRel := filepath.ToSlash(filepath.Join(rel, child.Name()))
			entry := WorkspaceEntry{Path: ".git/worktrees/" + childRel}
			if child.IsDir() {
				entry.Type = "directory"
				snapshot.Entries = append(snapshot.Entries, entry)
				walk(filepath.Join(dir, child.Name()), childRel)
				continue
			}
			if !child.Type().IsRegular() {
				t.Fatal("Git made unsupported administration entry")
			}
			entry.Type = "file"
			entry.Data, err = os.ReadFile(filepath.Join(dir, child.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if child.Name() == "gitdir" {
				entry.Data = []byte("/workspace/side/.git\n")
			}
			snapshot.Entries = append(snapshot.Entries, entry)
		}
	}
	walk(admin, "")
	log, err := os.ReadFile(filepath.Join(admin, "side", "logs", "HEAD"))
	if err != nil || len(log) == 0 {
		t.Fatalf("real linked worktree HEAD reflog absent: %v", err)
	}
	links, err := parseWorkspaceLinkedTrees(snapshot)
	if err != nil || len(links) != 1 || links[0].Root != "/workspace/side" {
		t.Fatalf("real linked worktree unavailable: %+v, %v", links, err)
	}
	gitFile, err := os.ReadFile(filepath.Join(side, ".git"))
	if err != nil || !strings.HasPrefix(string(gitFile), "gitdir: ") {
		t.Fatalf("real linked .git pointer unavailable: %v", err)
	}
	if err := verifyLinkedGitFile(links[0], WorkspaceEntry{Type: "file", Data: []byte("gitdir: /workspace/.git/worktrees/side\n")}); err != nil {
		t.Fatal(err)
	}
}
