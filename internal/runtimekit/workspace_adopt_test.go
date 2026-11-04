package runtimekit

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
)

// imageWorkspaceFixture is P's repository with branch feature at one commit,
// and the clone a commit image holds of it: detached at that commit, with no
// branch and no remote.
func imageWorkspaceFixture(t *testing.T, flake bool) (WorkspaceConfig, string, string) {
	t.Helper()
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	seed := filepath.Join(base, "seed")
	work := filepath.Join(base, "workspace")
	gitTest(t, base, "init", "--bare", remote)
	gitTest(t, base, "init", "-b", "feature", seed)
	files := map[string]string{"tracked": "captured contents\n"}
	if flake {
		files["flake.nix"] = "{ outputs = _: {}; }\n"
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(seed, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	gitTest(t, seed, "add", ".")
	gitTest(t, seed, "-c", "user.name=P", "-c", "user.email=p@example.invalid", "commit", "-m", "captured")
	gitTest(t, seed, "remote", "add", "origin", remote)
	gitTest(t, seed, "push", "origin", "feature")
	oid := gitTest(t, seed, "rev-parse", "HEAD")
	gitTest(t, base, "clone", "--quiet", "--branch", "feature", remote, work)
	gitTest(t, work, "checkout", "--quiet", "--detach", oid)
	gitTest(t, work, "branch", "--quiet", "-D", "feature")
	gitTest(t, work, "remote", "remove", "origin")
	selection := "no-flake"
	if flake {
		selection = "devshell"
	}
	return WorkspaceConfig{Schema: "p.workspace/v2", Repository: "app", Branch: "feature", InitialOID: oid, EnvironmentSelection: selection}, remote, work
}

func assertAdoptedWorkspace(t *testing.T, c WorkspaceConfig, remote, work string) {
	t.Helper()
	if got := gitTest(t, work, "symbolic-ref", "HEAD"); got != "refs/heads/feature" {
		t.Fatalf("HEAD=%s", got)
	}
	if got := gitTest(t, work, "rev-parse", "HEAD"); got != c.InitialOID {
		t.Fatalf("HEAD at %s, want %s", got, c.InitialOID)
	}
	if got := gitTest(t, work, "rev-parse", "--abbrev-ref", "@{upstream}"); got != "origin/feature" {
		t.Fatalf("upstream=%s", got)
	}
	if got := gitTest(t, work, "remote", "get-url", "--all", "origin"); got != remote {
		t.Fatalf("origin=%s", got)
	}
	if got := gitTest(t, work, "status", "--porcelain"); got != "" {
		t.Fatalf("checkout is dirty: %s", got)
	}
	if initialized, err := workspaceInitialized(work, uint32(os.Geteuid())); err != nil || !initialized {
		t.Fatalf("initialization marker: %t %v", initialized, err)
	}
}

func TestAdoptImageWorkspacePutsTheCloneOnTheSessionBranch(t *testing.T) {
	c, remote, work := imageWorkspaceFixture(t, false)
	if err := adoptWorkspaceRemote(c, work, "git", remote); err != nil {
		t.Fatal(err)
	}
	assertAdoptedWorkspace(t, c, remote, work)

	// Once adopted, a later Start leaves the user's Git state alone.
	gitTest(t, work, "checkout", "--quiet", "-b", "local-work")
	gitTest(t, work, "remote", "set-url", "origin", "ssh://elsewhere.example/app")
	before := workspaceSnapshot(t, work)
	if err := adoptWorkspaceRemote(c, work, "git", remote); err != nil {
		t.Fatal(err)
	}
	if after := workspaceSnapshot(t, work); !reflect.DeepEqual(after, before) {
		t.Fatal("adopted workspace changed on a later Start")
	}
	if got := gitTest(t, work, "symbolic-ref", "HEAD"); got != "refs/heads/local-work" {
		t.Fatalf("later Start moved HEAD to %s", got)
	}
}

// The adoption child really dies after a Git command succeeds; the next run
// has only what that command left in the workspace.
func TestAdoptWorkspaceCrashWorker(t *testing.T) {
	if os.Getenv("P_ADOPT_CRASH_WORKER") != "1" {
		return
	}
	var c WorkspaceConfig
	if err := json.Unmarshal([]byte(os.Getenv("P_ADOPT_CONFIG")), &c); err != nil {
		t.Fatal(err)
	}
	if err := adoptWorkspaceRemote(c, os.Getenv("P_ADOPT_WORK"), os.Getenv("P_ADOPT_GIT"), os.Getenv("P_ADOPT_REMOTE")); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash point was not reached")
}

func TestAdoptImageWorkspaceRepeatsAfterEveryInterruption(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	// Each step is a Git command that writes; the run dies right after it.
	for _, step := range []string{"remote add", "fetch --quiet", "update-ref refs/heads/feature", "checkout --quiet", "branch --set-upstream-to"} {
		t.Run(step, func(t *testing.T) {
			c, remote, work := imageWorkspaceFixture(t, false)
			wrapper := filepath.Join(t.TempDir(), "git-wrapper")
			// The first six arguments are the three fixed -c settings.
			script := fmt.Sprintf("#!/bin/sh\n'%s' \"$@\" || exit $?\nif [ \"$7 $8\" = '%s' ]; then kill -KILL \"$PPID\"; fi\n", git, step)
			if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			config, _ := json.Marshal(c)
			cmd := exec.Command(os.Args[0], "-test.run=^TestAdoptWorkspaceCrashWorker$")
			cmd.Env = append(os.Environ(), "P_ADOPT_CRASH_WORKER=1", "P_ADOPT_CONFIG="+string(config), "P_ADOPT_GIT="+wrapper, "P_ADOPT_REMOTE="+remote, "P_ADOPT_WORK="+work)
			out, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || !exitErr.ProcessState.Sys().(syscall.WaitStatus).Signaled() {
				t.Fatalf("worker did not crash after %s: %v: %s", step, err, out)
			}
			if initialized, err := workspaceInitialized(work, uint32(os.Geteuid())); err != nil || initialized {
				t.Fatalf("interrupted adoption marked initialized: %t %v", initialized, err)
			}
			for retry := 0; retry < 2; retry++ {
				if err := adoptWorkspaceRemote(c, work, "git", remote); err != nil {
					t.Fatalf("retry %d: %v", retry, err)
				}
			}
			assertAdoptedWorkspace(t, c, remote, work)
		})
	}
}

func TestAdoptImageWorkspaceRefusesWhatIsNotTheCapturedClone(t *testing.T) {
	commit := func(t *testing.T, dir string) string {
		t.Helper()
		gitTest(t, dir, "-c", "user.name=P", "-c", "user.email=p@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "other")
		return gitTest(t, dir, "rev-parse", "HEAD")
	}
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, c WorkspaceConfig, remote, work string)
	}{
		{"branch moved in P", func(t *testing.T, _ WorkspaceConfig, remote, work string) {
			moved := filepath.Join(t.TempDir(), "moved")
			gitTest(t, work, "clone", "--quiet", "--branch", "feature", remote, moved)
			commit(t, moved)
			gitTest(t, moved, "push", "--quiet", "origin", "feature")
		}},
		{"foreign origin", func(t *testing.T, _ WorkspaceConfig, _, work string) {
			gitTest(t, work, "remote", "add", "origin", "ssh://elsewhere.example/app")
		}},
		{"second origin URL", func(t *testing.T, _ WorkspaceConfig, remote, work string) {
			gitTest(t, work, "remote", "add", "origin", remote)
			gitTest(t, work, "remote", "set-url", "--add", "origin", "ssh://elsewhere.example/app")
		}},
		{"another remote", func(t *testing.T, _ WorkspaceConfig, remote, work string) {
			gitTest(t, work, "remote", "add", "upstream", remote)
		}},
		{"HEAD at another commit", func(t *testing.T, _ WorkspaceConfig, _, work string) {
			commit(t, work)
		}},
		{"HEAD on another branch", func(t *testing.T, _ WorkspaceConfig, _, work string) {
			gitTest(t, work, "checkout", "--quiet", "-b", "other")
		}},
		{"branch at another commit", func(t *testing.T, c WorkspaceConfig, _, work string) {
			other := commit(t, work)
			gitTest(t, work, "branch", "feature", other)
			gitTest(t, work, "checkout", "--quiet", "--detach", c.InitialOID)
		}},
		{"no Git directory", func(t *testing.T, _ WorkspaceConfig, _, work string) {
			if err := os.RemoveAll(filepath.Join(work, ".git")); err != nil {
				t.Fatal(err)
			}
		}},
		{"Git directory is a link", func(t *testing.T, _ WorkspaceConfig, _, work string) {
			git := filepath.Join(work, ".git")
			if err := os.Rename(git, git+"-moved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(git+"-moved", git); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, remote, work := imageWorkspaceFixture(t, false)
			tc.change(t, c, remote, work)
			if err := adoptWorkspaceRemote(c, work, "git", remote); err == nil {
				t.Fatal("adopted")
			}
			if _, err := os.Lstat(filepath.Join(work, ".git", "p-initialized")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refused adoption left a marker: %v", err)
			}
		})
	}
}

func TestAdoptImageWorkspaceRequiresTheSelectionToMatchTheRootFlake(t *testing.T) {
	for _, tc := range []struct {
		flake     bool
		selection string
		adopted   bool
	}{
		{false, "no-flake", true},
		{false, "no-default", false},
		{false, "devshell", false},
		{true, "no-flake", false},
		{true, "no-default", true},
		{true, "devshell", true},
	} {
		t.Run(fmt.Sprintf("flake=%t/%s", tc.flake, tc.selection), func(t *testing.T) {
			c, remote, work := imageWorkspaceFixture(t, tc.flake)
			c.EnvironmentSelection = tc.selection
			err := adoptWorkspaceRemote(c, work, "git", remote)
			if tc.adopted && err != nil || !tc.adopted && err == nil {
				t.Fatalf("adoption error %v, want adopted=%t", err, tc.adopted)
			}
			if initialized, _ := workspaceInitialized(work, uint32(os.Geteuid())); initialized != tc.adopted {
				t.Fatalf("initialized=%t, want %t", initialized, tc.adopted)
			}
		})
	}
}
