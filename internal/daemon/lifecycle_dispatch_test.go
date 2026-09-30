package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/plugin"
)

// Exercise real journal, guards, selected source plugin and Git ref effects.
// Creation workers wait on real session locks to occupy every worker slot;
// their completion must drain pending renames without periodic polling.
func TestLifecycleDispatchSaturation(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart=%t", restart), func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "state")
			store, err := control.OpenStore(state)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			defer func() { _ = store.Close() }()
			if err = store.CreateProject(ctx, "app", []byte(`{}`)); err != nil {
				t.Fatal(err)
			}
			backend, tip := dispatchGitFixture(t, store)
			repo, err := backend.RepositoryPath("app")
			if err != nil {
				t.Fatal(err)
			}
			var ops []control.Operation
			for i := 0; i < 21; i++ {
				old := fmt.Sprintf("old-%d", i)
				dispatchGit(t, repo, "update-ref", "refs/heads/"+old, tip)
				op, err := store.BeginRetainedRename(ctx, control.RetainedRenameRequest{Key: old, Project: "app", OldBranch: old, NewBranch: fmt.Sprintf("new-%d", i), ExpectedOldTip: tip}, func(context.Context) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				ops = append(ops, op)
			}
			l := &lifecycle{ctx: ctx, store: store, git: &gitCapability{backend: backend}, working: map[string]bool{}, queueSlots: make(chan struct{}, 8)}
			blockers := dispatchBlockers(t, ctx, store)
			releases := dispatchHold(t, l, blockers)
			for _, op := range blockers {
				if err = l.enqueue(op.ID); err != nil {
					t.Fatal(err)
				}
			}
			for _, op := range ops {
				if err = l.enqueue(op.ID); err != nil {
					t.Fatal(err)
				}
				if err = l.enqueue(op.ID); err != nil {
					t.Fatal(err)
				}
			}
			l.mu.Lock()
			if len(l.pending) != len(ops) || len(l.working) != len(ops)+len(blockers) {
				t.Fatalf("accepted work dropped or duplicated: pending=%d working=%d", len(l.pending), len(l.working))
			}
			l.mu.Unlock()
			if restart {
				cancel()
				dispatchWait(t, l, false)
				if err = store.Close(); err != nil {
					t.Fatal(err)
				}
				store, err = control.OpenStore(state)
				if err != nil {
					t.Fatal(err)
				}
				pkg, err := plugin.Conformance(filepath.Join(state, "source-package"))
				if err != nil {
					t.Fatal(err)
				}
				backend, err = gitservice.New(store, state, []plugin.Active{{Package: pkg, Grants: []string{"git.project"}}})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel = context.WithCancel(context.Background())
				defer cancel()
				l = &lifecycle{ctx: ctx, store: store, git: &gitCapability{backend: backend}, working: map[string]bool{}, queueSlots: make(chan struct{}, 8)}
				releases = dispatchHold(t, l, blockers)
				if err = l.Recover(); err != nil {
					t.Fatal(err)
				}
				l.mu.Lock()
				if len(l.pending) != len(ops) {
					t.Errorf("restart overflow lost: pending=%d", len(l.pending))
				}
				l.mu.Unlock()
			}
			// Real worker completion must drain all accepted work. There is no
			// scheduler running in the admission case.
			for _, release := range releases {
				release()
			}
			deadline := time.Now().Add(60 * time.Second)
			for {
				completed := 0
				for _, op := range ops {
					got, err := store.GetOperation(ctx, op.ID)
					if err != nil {
						t.Fatal(err)
					}
					if got.Status == "completed" {
						completed++
					} else if got.Status != "running" {
						t.Fatalf("rename failed: %+v", got)
					}
				}
				if completed == len(ops) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("accepted operations stranded: completed %d/%d", completed, len(ops))
				}
				time.Sleep(10 * time.Millisecond)
			}
			for i := range ops {
				old, newName := fmt.Sprintf("old-%d", i), fmt.Sprintf("new-%d", i)
				if _, exists, err := backend.InspectBranchRef(ctx, "app", old); err != nil || exists {
					t.Fatalf("old ref survived: %s %v", old, err)
				}
				if got, exists, err := backend.InspectBranchRef(ctx, "app", newName); err != nil || !exists || got != tip {
					t.Fatalf("new ref incorrect: %s %s %v", newName, got, err)
				}
				// Released guards permit a new reservation for the renamed branch.
				if _, err := store.BeginRetainedRename(ctx, control.RetainedRenameRequest{Key: fmt.Sprintf("after-%d", i), Project: "app", OldBranch: newName, NewBranch: fmt.Sprintf("later-%d", i), ExpectedOldTip: tip}, func(context.Context) error { return nil }); err != nil {
					t.Fatalf("completed rename retained guards: %v", err)
				}
			}
			dispatchWait(t, l, true)
		})
	}
}

func dispatchGitFixture(t *testing.T, store *control.Store) (*gitservice.Backend, string) {
	t.Helper()
	state := store.StateDir()
	dir := filepath.Join(state, "source-package")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plugin.json", "p-git-ssh"} {
		data, err := os.ReadFile(filepath.Join("../../plugins/bundled/source-git", name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	build := exec.Command("go", "build", "-o", filepath.Join(dir, "git.wasm"), "../../plugins/bundled/source-git")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOTOOLCHAIN=local")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Git module: %v %s", err, out)
	}
	pkg, err := plugin.Conformance(dir)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := gitservice.New(store, state, []plugin.Active{{Package: pkg, Grants: []string{"git.project"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = backend.EnsureBare(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	repo, err := backend.RepositoryPath("app")
	if err != nil {
		t.Fatal(err)
	}
	tree := dispatchGit(t, repo, "mktree")
	tip := dispatchGit(t, repo, "-c", "user.name=P fixture", "-c", "user.email=p@example.test", "commit-tree", tree, "-m", "fixture")
	return backend, tip
}
func dispatchGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"--git-dir=" + repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func dispatchBlockers(t *testing.T, ctx context.Context, store *control.Store) []control.Operation {
	t.Helper()
	selection := control.CreationSelection{RuntimeID: "fixture", RuntimeSHA256: strings.Repeat("a", 64), HostID: "fixture", HostSHA256: strings.Repeat("b", 64), SourceID: "fixture", SourceSHA256: strings.Repeat("c", 64)}
	var ops []control.Operation
	for i := 0; i < 8; i++ {
		op, _, err := store.BeginSessionCreate(ctx, control.ReserveSessionRequest{Key: fmt.Sprintf("blocker-%d", i), Project: "app", Branch: fmt.Sprintf("blocker-%d", i), Choice: "existing"}, strings.Repeat("d", 64), selection, func(context.Context, control.ReserveSessionRequest) (string, bool, error) {
			return strings.Repeat("e", 40), true, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		ops = append(ops, op)
	}
	return ops
}
func dispatchHold(t *testing.T, l *lifecycle, ops []control.Operation) []func() {
	t.Helper()
	var releases []func()
	for _, op := range ops {
		release, err := l.lockSession(l.ctx, op.SessionUUID)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	return releases
}
func dispatchWait(t *testing.T, l *lifecycle, all bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		l.mu.Lock()
		n := len(l.queueSlots)
		if all {
			n = len(l.working)
		}
		l.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("workers did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLifecyclePollsDurableRetainedRename(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := control.OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.CreateProject(ctx, "app", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	var ops []control.Operation
	for _, name := range []string{"running", "blocked"} {
		op, err := store.BeginRetainedRename(ctx, control.RetainedRenameRequest{Key: name, Project: "app", OldBranch: name, NewBranch: name + "-next", ExpectedOldTip: strings.Repeat("a", 40)}, func(context.Context) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		// An unsupported phase yields a bounded diagnostic when processed. This
		// proves dispatch after a durable admission with no in-memory enqueue,
		// without needing a runtime or performing unreviewed ref effects.
		if err = store.AdvanceOperation(ctx, op.ID, name, "unsupported", false, op.Evidence, "original"); err != nil {
			t.Fatal(err)
		}
		ops = append(ops, op)
	}
	l := &lifecycle{ctx: ctx, store: store, working: map[string]bool{}, queueSlots: make(chan struct{}, 8)}
	go l.schedule()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := store.GetOperation(ctx, ops[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "blocked" && strings.Contains(got.Diagnostic, "unsupported durable phase") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("durable retained rename was not polled: %+v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, err := store.GetOperation(ctx, ops[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Diagnostic != "original" {
		t.Fatalf("polling retried dormant operation: %+v", got)
	}
	dispatchWait(t, l, true)
}
