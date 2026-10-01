package runtimekit

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func workspaceFixture(t *testing.T, blank bool) (WorkspaceConfig, string, string, string) {
	t.Helper()
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	gitTest(t, base, "init", "--bare", remote)
	c := WorkspaceConfig{Schema: "p.workspace/v1", Repository: "app", Branch: "main"}
	if !blank {
		seed := filepath.Join(base, "seed")
		gitTest(t, base, "init", "-b", "main", seed)
		if err := os.WriteFile(filepath.Join(seed, "tracked"), []byte("captured contents\n"), 0644); err != nil {
			t.Fatal(err)
		}
		// A source symlink must not cause root's durability walk to follow it.
		if err := os.Symlink("tracked", filepath.Join(seed, "link")); err != nil {
			t.Fatal(err)
		}
		gitTest(t, seed, "add", ".")
		gitTest(t, seed, "-c", "user.name=P", "-c", "user.email=p@example.invalid", "commit", "-m", "initial")
		gitTest(t, seed, "remote", "add", "origin", remote)
		gitTest(t, seed, "push", "origin", "main")
		c.InitialOID = gitTest(t, seed, "rev-parse", "HEAD")
	}
	work := filepath.Join(base, "workspace")
	if err := os.Mkdir(work, 0755); err != nil {
		t.Fatal(err)
	}
	return c, remote, work, filepath.Join(base, "scratch")
}

func populateFixture(c WorkspaceConfig, remote string) func(string, *os.File) error {
	return func(stage string, _ *os.File) error {
		return initWorkspaceRemote(c, stage, "git", remote)
	}
}

func runWorkspaceFixture(c WorkspaceConfig, remote, work, scratch string) error {
	return workspaceTransaction(work, scratch, uint32(os.Geteuid()), uint32(os.Getegid()), populateFixture(c, remote), publishWorkspace)
}

func TestPopulateWorkspaceCannotReleaseSupervisorLock(t *testing.T) {
	root := t.TempDir()
	stagePath := filepath.Join(root, "tree")
	if err := os.Mkdir(stagePath, 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	stage, err := os.Open(stagePath)
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	cmd := populateWorkspaceCommand(stage)
	if len(cmd.ExtraFiles) != 1 || cmd.ExtraFiles[0] != stage {
		t.Fatal("unprivileged population received an fd besides its staging directory")
	}
	if cred := cmd.SysProcAttr.Credential; cred == nil || cred.Uid != 1000 || cred.Gid != 1000 || !reflect.DeepEqual(cred.Groups, []uint32{1000}) {
		t.Fatal("population is not confined to the fixed session identity")
	}
	// Execute the configured fd transfer with this test binary. It tries to
	// unlock fd 3; the supervisor lock must remain held through a separate fd.
	cmd.Path = os.Args[0]
	cmd.Args = []string{os.Args[0], "-test.run=^TestPopulateWorkspaceFDWorker$"}
	cmd.Dir = root
	cmd.Env = append(cmd.Env, "P_WORKSPACE_FD_WORKER=1", "P_WORKSPACE_STAGE="+stagePath)
	cmd.SysProcAttr.Credential = nil // ordinary test users cannot set groups
	cmd.Stdout, cmd.Stderr = nil, nil
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("population fd probe: %v: %s", err, out)
	}
	probe, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	if err := syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != syscall.EWOULDBLOCK {
		t.Fatalf("population released supervisor lock: %v", err)
	}
}

func TestPopulateWorkspaceFDWorker(t *testing.T) {
	if os.Getenv("P_WORKSPACE_FD_WORKER") != "1" {
		return
	}
	stage := os.NewFile(3, "workspace-stage")
	defer stage.Close()
	st, err := stage.Stat()
	stagePathStat, pathErr := os.Stat(os.Getenv("P_WORKSPACE_STAGE"))
	if err != nil || pathErr != nil || !os.SameFile(st, stagePathStat) {
		t.Fatalf("staging fd unavailable: %v, %v", err, pathErr)
	}
	if err := syscall.Flock(3, syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
}

// This subprocess really dies without defers after successful Git effects or
// at publication boundaries. The next invocation has only filesystem evidence.
func TestWorkspaceCrashWorker(t *testing.T) {
	if os.Getenv("P_WORKSPACE_CRASH_WORKER") != "1" {
		return
	}
	var c WorkspaceConfig
	if err := json.Unmarshal([]byte(os.Getenv("P_WORKSPACE_CONFIG")), &c); err != nil {
		t.Fatal(err)
	}
	crash := func() {
		if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		select {}
	}
	phase := os.Getenv("P_WORKSPACE_PHASE")
	populate := func(stage string, _ *os.File) error {
		err := initWorkspaceRemote(c, stage, os.Getenv("P_WORKSPACE_GIT"), os.Getenv("P_WORKSPACE_REMOTE"))
		if err == nil && phase == "marker" {
			crash()
		}
		return err
	}
	publish := func(from, to string) error {
		if phase == "before-publish" {
			crash()
		}
		if err := publishWorkspace(from, to); err != nil {
			return err
		}
		if phase == "after-publish" {
			crash()
		}
		return nil
	}
	if err := workspaceTransaction(os.Getenv("P_WORKSPACE_WORK"), os.Getenv("P_WORKSPACE_SCRATCH"), uint32(os.Geteuid()), uint32(os.Getegid()), populate, publish); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash point was not reached")
}

func TestWorkspaceRetryAfterEveryInitializationCrash(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	for _, blank := range []bool{false, true} {
		phases := []string{"init", "remote", "marker", "before-publish", "after-publish"}
		if blank {
			phases = append(phases, "ls-remote")
		} else {
			phases = append(phases, "fetch", "rev-parse", "ls-tree", "update-ref", "checkout", "branch")
		}
		for _, phase := range phases {
			t.Run(fmt.Sprintf("blank=%t/%s", blank, phase), func(t *testing.T) {
				c, remote, work, scratch := workspaceFixture(t, blank)
				wrapper := filepath.Join(filepath.Dir(work), "git-wrapper")
				// The first six argv elements are the three fixed -c settings.
				script := fmt.Sprintf("#!/bin/sh\n'%s' \"$@\" || exit $?\nif [ \"$7\" = '%s' ]; then kill -KILL \"$PPID\"; fi\n", git, phase)
				if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				config, _ := json.Marshal(c)
				cmd := exec.Command(os.Args[0], "-test.run=^TestWorkspaceCrashWorker$")
				cmd.Env = append(os.Environ(), "P_WORKSPACE_CRASH_WORKER=1", "P_WORKSPACE_CONFIG="+string(config), "P_WORKSPACE_PHASE="+phase,
					"P_WORKSPACE_GIT="+wrapper, "P_WORKSPACE_REMOTE="+remote, "P_WORKSPACE_WORK="+work, "P_WORKSPACE_SCRATCH="+scratch)
				out, err := cmd.CombinedOutput()
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || !exitErr.ProcessState.Sys().(syscall.WaitStatus).Signaled() || exitErr.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
					t.Fatalf("worker did not crash at %s: %v: %s", phase, err, out)
				}
				if phase != "after-publish" {
					if err := emptyWorkspace(work); err != nil {
						t.Fatalf("unpublished checkout leaked into /workspace: %v", err)
					}
				}
				for retry := 0; retry < 2; retry++ {
					if err := runWorkspaceFixture(c, remote, work, scratch); err != nil {
						t.Fatalf("retry %d: %v", retry, err)
					}
				}
				if got := gitTest(t, work, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
					t.Fatalf("HEAD=%s", got)
				}
				if !blank {
					if got := gitTest(t, work, "rev-parse", "HEAD"); got != c.InitialOID {
						t.Fatalf("OID=%s", got)
					}
					if got := gitTest(t, work, "status", "--porcelain"); got != "" {
						t.Fatalf("checkout is dirty: %s", got)
					}
					if got := gitTest(t, work, "rev-parse", "--abbrev-ref", "@{upstream}"); got != "origin/main" {
						t.Fatalf("upstream=%s", got)
					}
				} else {
					cmd := exec.Command("git", "rev-parse", "--verify", "HEAD")
					cmd.Dir = work
					if err := cmd.Run(); err == nil {
						t.Fatal("bootstrap HEAD is not unborn")
					}
				}
				entries, err := os.ReadDir(scratch)
				if err != nil || len(entries) != 0 {
					t.Fatalf("retry accumulated scratch: %v %v", entries, err)
				}
			})
		}
	}
}

func workspaceSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data := ""
		if entry.Type()&os.ModeSymlink != 0 {
			data, err = os.Readlink(path)
		} else if info.Mode().IsRegular() {
			var contents []byte
			contents, err = os.ReadFile(path)
			data = string(contents)
		}
		result[strings.TrimPrefix(path, dir)] = fmt.Sprintf("%v:%s", info.Mode(), data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestWorkspaceTransactionPreservesEstablishedState(t *testing.T) {
	c, remote, work, scratch := workspaceFixture(t, false)
	if err := runWorkspaceFixture(c, remote, work, scratch); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "flake.nix"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", "flake.nix")
	gitTest(t, work, "-c", "user.name=P", "-c", "user.email=p@example.invalid", "commit", "-m", "new user flake")
	gitTest(t, work, "checkout", "--detach")
	gitTest(t, work, "remote", "set-url", "origin", "ssh://different.example/changed")
	for _, name := range []string{"tracked", "untracked"} {
		if err := os.WriteFile(filepath.Join(work, name), []byte("user work"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := workspaceSnapshot(t, work)
	if err := runWorkspaceFixture(c, remote, work, scratch); err != nil {
		t.Fatal(err)
	}
	if after := workspaceSnapshot(t, work); !reflect.DeepEqual(before, after) {
		t.Fatal("established workspace changed")
	}
}

func TestWorkspaceTransactionPreservesAmbiguousWorkspace(t *testing.T) {
	for _, kind := range []string{"file", "partial-git", "marker-symlink", "git-symlink"} {
		t.Run(kind, func(t *testing.T) {
			c, remote, work, scratch := workspaceFixture(t, true)
			if kind != "git-symlink" {
				gitTest(t, work, "init", "-b", "main")
			}
			switch kind {
			case "file":
				if err := os.WriteFile(filepath.Join(work, "user-file"), []byte("precious"), 0600); err != nil {
					t.Fatal(err)
				}
			case "marker-symlink":
				if err := os.Symlink(remote, filepath.Join(work, ".git", "p-initialized")); err != nil {
					t.Fatal(err)
				}
			case "git-symlink":
				if err := os.Symlink(remote, filepath.Join(work, ".git")); err != nil {
					t.Fatal(err)
				}
			}
			before, beforeRemote := workspaceSnapshot(t, work), workspaceSnapshot(t, remote)
			if err := runWorkspaceFixture(c, remote, work, scratch); err == nil {
				t.Fatal("ambiguous workspace accepted")
			}
			if !reflect.DeepEqual(before, workspaceSnapshot(t, work)) || !reflect.DeepEqual(beforeRemote, workspaceSnapshot(t, remote)) {
				t.Fatal("ambiguous workspace or link target changed")
			}
		})
	}
}

func TestWorkspaceTransactionPreservesUnexpectedScratch(t *testing.T) {
	for _, kind := range []string{"writable-parent", "parent-symlink", "tree-symlink", "unknown-entry"} {
		t.Run(kind, func(t *testing.T) {
			c, remote, work, scratch := workspaceFixture(t, true)
			if kind == "parent-symlink" {
				if err := os.Symlink(remote, scratch); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(scratch, 0700); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "writable-parent":
					if err := os.Chmod(scratch, 0777); err != nil {
						t.Fatal(err)
					}
				case "tree-symlink":
					if err := os.Symlink(remote, filepath.Join(scratch, "tree")); err != nil {
						t.Fatal(err)
					}
				case "unknown-entry":
					if err := os.WriteFile(filepath.Join(scratch, "unexpected"), []byte("retain"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, beforeRemote := workspaceSnapshot(t, scratch), workspaceSnapshot(t, remote)
			if err := runWorkspaceFixture(c, remote, work, scratch); err == nil {
				t.Fatal("unsafe scratch accepted")
			}
			if !reflect.DeepEqual(before, workspaceSnapshot(t, scratch)) || !reflect.DeepEqual(beforeRemote, workspaceSnapshot(t, remote)) {
				t.Fatal("unsafe scratch or link target changed")
			}
		})
	}
}

func TestWorkspacePublishRefusesNewDestinationContents(t *testing.T) {
	c, remote, work, scratch := workspaceFixture(t, true)
	publish := func(from, to string) error {
		if err := os.WriteFile(filepath.Join(work, "unexpected"), []byte("user work"), 0600); err != nil {
			return err
		}
		return publishWorkspace(from, to)
	}
	if err := workspaceTransaction(work, scratch, uint32(os.Geteuid()), uint32(os.Getegid()), populateFixture(c, remote), publish); err == nil {
		t.Fatal("nonempty destination replaced")
	}
	if data, err := os.ReadFile(filepath.Join(work, "unexpected")); err != nil || string(data) != "user work" {
		t.Fatal("unexpected work lost")
	}
	before := workspaceSnapshot(t, scratch)
	if err := runWorkspaceFixture(c, remote, work, scratch); err == nil {
		t.Fatal("retry accepted ambiguous destination")
	}
	if !reflect.DeepEqual(before, workspaceSnapshot(t, scratch)) {
		t.Fatal("retry discarded evidence despite ambiguous destination")
	}
}

func TestWorkspaceRetryDiscardsInterruptedGitFilesOnlyInPrivateScratch(t *testing.T) {
	c, remote, work, scratch := workspaceFixture(t, false)
	interrupted := errors.New("simulated interrupted Git write")
	populate := func(stage string, _ *os.File) error {
		gitTest(t, stage, "init", "-b", c.Branch)
		for _, name := range []string{".git/index.lock", ".git/objects/incomplete-object", ".git/p-initialized", "tracked"} {
			if err := os.WriteFile(filepath.Join(stage, name), []byte("partial write"), 0600); err != nil {
				return err
			}
		}
		return interrupted
	}
	if err := workspaceTransaction(work, scratch, uint32(os.Geteuid()), uint32(os.Getegid()), populate, publishWorkspace); !errors.Is(err, interrupted) {
		t.Fatalf("expected interruption: %v", err)
	}
	if err := emptyWorkspace(work); err != nil {
		t.Fatal(err)
	}
	if err := runWorkspaceFixture(c, remote, work, scratch); err != nil {
		t.Fatal(err)
	}
	if got := gitTest(t, work, "rev-parse", "HEAD"); got != c.InitialOID {
		t.Fatalf("retry lost captured OID: %s", got)
	}
	if got := gitTest(t, work, "status", "--porcelain"); got != "" {
		t.Fatalf("retry retained partial bytes: %s", got)
	}
}
