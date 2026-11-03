package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimedriver/memory"
)

const (
	renameOp      = "550e8400-e29b-41d4-a716-446655440001"
	renameBaseOID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	renameConfig0 = "[core]\nrepositoryformatversion = 0\nbare = false\n[remote \"origin\"]\nurl = ssh://secret@private.invalid/app\nfetch = +refs/heads/*:refs/remotes/origin/*\n[branch \"main\"]\nremote = origin\nmerge = refs/heads/main\n"
)

// cloneSession is a stopped runtime of the in-memory driver whose workspace
// holds a Git clone on branch main, at commit renameBaseOID, with a reflog
// and an untracked file.
type cloneSession struct {
	t     *testing.T
	world *memory.World
	clone renameClone
}

func newCloneSession(t *testing.T) *cloneSession {
	t.Helper()
	world := memory.NewWorld(memory.Config{})
	driver := memory.NewDriver(world)
	l := &lifecycle{git: testGitCapability(), cfg: control.RuntimeConfig{EndpointPrefix: "/run/p-endpoints"}}
	spec := l.runtimeSpec("11111111-1111-4111-8111-111111111111", "app", string(world.BaseImage()), nil, "")
	spec.Command, spec.GitHostKey, spec.Workspace.Branch = []string{"/bin/sh"}, l.git.info.HostPublicKey, "main"
	created := spec
	created.GitKey = testSessionKey
	o, err := driver.Create(t.Context(), created, "", func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	c := &cloneSession{t: t, world: world, clone: renameClone{driver: driver, target: runtimedriver.Target{Session: spec, At: o.Incarnation}}}
	for _, dir := range []string{".git/refs", ".git/refs/heads", ".git/logs", ".git/logs/refs", ".git/logs/refs/heads"} {
		c.put(dir, runtimedriver.File{Type: runtimedriver.FileDirectory, UID: renameUID, GID: renameUID, Mode: 0o755})
	}
	c.write(renameRefPath("main"), renameBaseOID+"\n")
	c.write(renameLogPath("main"), "history\n")
	c.write(renameConfigPath, renameConfig0)
	c.write("private-note", "private")
	return c
}

func (c *cloneSession) put(p string, f runtimedriver.File) {
	c.t.Helper()
	if err := c.clone.driver.WriteWorkspace(c.t.Context(), c.clone.target, p, f, func() error { return nil }); err != nil {
		c.t.Fatal(err)
	}
}

// write writes a file of the session user, as the session's Git would.
func (c *cloneSession) write(p, data string) {
	c.put(p, runtimedriver.File{Type: runtimedriver.FileRegular, UID: renameUID, GID: renameUID, Mode: 0o644, Data: []byte(data)})
}

func (c *cloneSession) read(p string) runtimedriver.File {
	c.t.Helper()
	f, err := c.clone.driver.ReadWorkspace(c.t.Context(), c.clone.target, p, 1<<20)
	if err != nil {
		c.t.Fatal(err)
	}
	return f
}

// snapshot is every path of the clone a rename may touch.
func (c *cloneSession) snapshot() map[string]runtimedriver.File {
	c.t.Helper()
	out := map[string]runtimedriver.File{}
	for _, p := range []string{renameHeadPath, renameConfigPath, renameRefPath("main"), renameRefPath("renamed"), renameLogPath("main"),
		renameLogPath("renamed"), renameBackupPath(renameOp), "private-note"} {
		out[p] = c.read(p)
	}
	return out
}

func TestRenameMismatchIsBoundedReadOnlyAndManualCorrectionRechecks(t *testing.T) {
	for _, tc := range []struct{ name, head, config, field, actual string }{
		{"branch", "ref: refs/heads/other\n", renameConfig0, "branch", "refs/heads/other"},
		{"detached", renameBaseOID + "\n", renameConfig0, "branch", "<detached> " + renameBaseOID},
		{"unsafe-head", "ref: refs/heads/secret@private.invalid\n", renameConfig0, "branch", "<unavailable>"},
		{"upstream", "ref: refs/heads/main\n", strings.ReplaceAll(renameConfig0, "merge = refs/heads/main", "merge = refs/heads/other"), "upstream", "origin:refs/heads/other"},
		{"remote", "ref: refs/heads/main\n", strings.ReplaceAll(renameConfig0, "origin", "other"), "upstream", "other:refs/heads/main"},
		{"unset", "ref: refs/heads/main\n", "[core]\nrepositoryformatversion = 0\nbare = false\n", "upstream", "<unset>"},
		{"incomplete", "ref: refs/heads/main\n", strings.ReplaceAll(renameConfig0, "merge = refs/heads/main\n", ""), "upstream", "<incomplete>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCloneSession(t)
			c.write(renameHeadPath, tc.head)
			c.write(renameConfigPath, tc.config)
			before := c.snapshot()
			marked := 0
			_, err := c.clone.prepare(t.Context(), renameOp, "main", "renamed", func() error { marked++; return nil })
			var mismatch *workspaceAssignmentMismatchError
			if !errors.As(err, &mismatch) || mismatch.Field != tc.field || mismatch.Actual != tc.actual {
				t.Fatalf("missing exact mismatch: %+v %v", mismatch, err)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private.invalid") || len(err.Error()) > 512 {
				t.Fatalf("unsafe diagnostic: %v", err)
			}
			if marked != 0 || !reflect.DeepEqual(before, c.snapshot()) {
				t.Fatal("mismatch mutated workspace")
			}
			// An ordinary external Git correction permits the next independent
			// attempt. No repair action or automatic checkout is involved.
			c.write(renameHeadPath, "ref: refs/heads/main\n")
			c.write(renameConfigPath, renameConfig0)
			if _, err := c.clone.prepare(t.Context(), renameOp, "main", "renamed", func() error { marked++; return nil }); err != nil || marked != 1 {
				t.Fatalf("manual correction not rechecked: marker=%d err=%v", marked, err)
			}
		})
	}
}

func TestRenameRefusesUnsupportedGitLayoutsBeforeAnyWrite(t *testing.T) {
	for name, change := range map[string]func(*cloneSession){
		"packed refs": func(c *cloneSession) { c.write(".git/packed-refs", "# pack-refs\n") },
		"index lock":  func(c *cloneSession) { c.write(".git/index.lock", "") },
		"worktrees": func(c *cloneSession) {
			c.put(".git/worktrees", runtimedriver.File{Type: runtimedriver.FileDirectory, UID: renameUID, GID: renameUID, Mode: 0o755})
		},
		"destination ref": func(c *cloneSession) { c.write(renameRefPath("renamed"), renameBaseOID+"\n") },
		"foreign HEAD": func(c *cloneSession) {
			c.put(renameHeadPath, runtimedriver.File{Type: runtimedriver.FileRegular, UID: 0, GID: 0, Mode: 0o644, Data: []byte("ref: refs/heads/main\n")})
		},
		"writable config": func(c *cloneSession) {
			c.put(renameConfigPath, runtimedriver.File{Type: runtimedriver.FileRegular, UID: renameUID, GID: renameUID, Mode: 0o666, Data: []byte(renameConfig0)})
		},
		"symlinked refs": func(c *cloneSession) {
			c.world.Symlink(c.clone.target.Session.Ref, ".git/refs/heads/main", "/etc/passwd")
		},
		"writable workspace": func(c *cloneSession) { c.world.SetRoot(c.clone.target.Session.Ref, renameUID, renameUID, 0o777) },
		"foreign workspace":  func(c *cloneSession) { c.world.SetRoot(c.clone.target.Session.Ref, 0, 0, 0o755) },
	} {
		t.Run(name, func(t *testing.T) {
			c := newCloneSession(t)
			change(c)
			marked := 0
			if _, err := c.clone.prepare(t.Context(), renameOp, "main", "renamed", func() error { marked++; return nil }); err == nil || marked != 0 {
				t.Fatalf("an unsupported clone was prepared: marker=%d err=%v", marked, err)
			}
			if f := c.read(renameBackupPath(renameOp)); f.Type != runtimedriver.FileAbsent {
				t.Fatal("a backup was written")
			}
		})
	}
}

func TestRenameAppliesTheBackupsPlanToTheStoppedClone(t *testing.T) {
	c := newCloneSession(t)
	ctx := t.Context()
	issued := 0
	plan, err := c.clone.prepare(ctx, renameOp, "main", "renamed", func() error { issued++; return nil })
	if err != nil || plan.HeadOID != renameBaseOID {
		t.Fatalf("prepare: %+v %v", plan, err)
	}
	if _, err := c.clone.apply(ctx, renameOp, "main", "renamed", func() error { issued++; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := c.clone.verifyRenamed(ctx, renameOp, "main", "renamed", plan); err != nil {
		t.Fatal(err)
	}
	if err := c.clone.removeBackup(ctx, renameOp, func() error { issued++; return nil }); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{
		renameHeadPath: "ref: refs/heads/renamed\n", renameRefPath("renamed"): renameBaseOID + "\n",
		renameLogPath("renamed"): "history\n", "private-note": "private",
		renameConfigPath: strings.ReplaceAll(strings.ReplaceAll(renameConfig0, "[branch \"main\"]", "[branch \"renamed\"]"), "merge = refs/heads/main", "merge = refs/heads/renamed"),
	} {
		if got := c.read(p); got.Type != runtimedriver.FileRegular || string(got.Data) != want {
			t.Fatalf("%s holds %q, want %q", p, got.Data, want)
		}
	}
	for _, p := range []string{renameRefPath("main"), renameLogPath("main"), renameBackupPath(renameOp)} {
		if got := c.read(p); got.Type != runtimedriver.FileAbsent {
			t.Fatalf("%s is still there", p)
		}
	}
	// The backup's write, the clone's edits (four writes and two removals),
	// then the backup's removal, each after its gate.
	if issued != 8 {
		t.Fatalf("%d gates passed, want 8", issued)
	}
}

func TestRenameBackupUncertainWriteRetainsEffectMarkerForRecovery(t *testing.T) {
	c := newCloneSession(t)
	ctx := t.Context()
	late := c.world.Delay(runtimedriver.EffectWrite)
	marked := 0
	_, err := c.clone.prepare(ctx, renameOp, "main", "renamed", func() error { marked++; return nil })
	if !errors.Is(err, runtimedriver.ErrUnknownOutcome) || marked != 1 {
		t.Fatalf("uncertain write lacks exact durable marker: marker=%d err=%v", marked, err)
	}
	if f := c.read(renameBackupPath(renameOp)); f.Type != runtimedriver.FileAbsent {
		t.Fatal("unanswered write already visible")
	}
	if _, err := c.clone.verifyPrepared(ctx, renameOp, "main", "renamed"); err == nil {
		t.Fatal("a missing backup was taken for a prepared rename")
	}
	// A delayed admission can materialize the exact backup after the
	// original request returned. Recovery may move forward only on this proof.
	late.Land()
	plan, err := c.clone.verifyPrepared(ctx, renameOp, "main", "renamed")
	if err != nil || plan.HeadOID != renameBaseOID {
		t.Fatalf("delayed exact backup not recoverable: %+v %v", plan, err)
	}
}

// landingSettle is a driver whose backend admits a delayed effect right
// before the next Settle observes it: the effect happened, its reply was
// lost.
type landingSettle struct {
	runtimedriver.Driver
	late *memory.Admission
}

func (d landingSettle) Settle(ctx context.Context, e runtimedriver.Effect) (runtimedriver.Settlement, error) {
	d.late.Land()
	return d.Driver.Settle(ctx, e)
}

func TestRenameGoesOnWhenTheDriverSettlesALostWriteApplied(t *testing.T) {
	c := newCloneSession(t)
	ctx := t.Context()
	plan, err := c.clone.prepare(ctx, renameOp, "main", "renamed", func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	clone := c.clone
	clone.driver = landingSettle{clone.driver, c.world.Delay(runtimedriver.EffectWrite)}
	if _, err := clone.apply(ctx, renameOp, "main", "renamed", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := c.clone.verifyRenamed(ctx, renameOp, "main", "renamed", plan); err != nil {
		t.Fatal(err)
	}
}

func TestRenameWithAnUnsettledWriteNeverSendsItAgain(t *testing.T) {
	c := newCloneSession(t)
	ctx := t.Context()
	if _, err := c.clone.prepare(ctx, renameOp, "main", "renamed", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	late := c.world.Delay(runtimedriver.EffectWrite)
	issued := 0
	if _, err := c.clone.apply(ctx, renameOp, "main", "renamed", func() error { issued++; return nil }); !errors.Is(err, runtimedriver.ErrUnknownOutcome) || issued != 1 {
		t.Fatalf("apply with an unanswered write: %d gates, %v", issued, err)
	}
	// Recovery only proves; the unanswered write is not sent again.
	if _, err := c.clone.apply(ctx, renameOp, "main", "renamed", nil); err == nil {
		t.Fatal("recovery took an unanswered write for a renamed clone")
	}
	if f := c.read(renameRefPath("renamed")); f.Type != runtimedriver.FileAbsent {
		t.Fatal("recovery sent the write again")
	}
	late.Land()
	if f := c.read(renameRefPath("renamed")); f.Type != runtimedriver.FileRegular || string(f.Data) != renameBaseOID+"\n" {
		t.Fatalf("the late write left %+v", f)
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

func TestRenameDoesNotAttributeUnrelatedConfigFailureToAssignedUpstream(t *testing.T) {
	base := "[core]\nrepositoryformatversion = 0\nbare = false\n[remote \"origin\"]\nfetch = +refs/heads/*:refs/remotes/origin/*\n[branch \"main\"]\nremote = origin\nmerge = refs/heads/main\n"
	for _, extra := range []string{
		"[branch \"other\"]\nremote = origin\n",
		"[branch \"other\"]\nremote = missing\nmerge = refs/heads/other\n",
	} {
		_, err := renameConfig([]byte(base+extra), "main", "renamed")
		var mismatch *workspaceAssignmentMismatchError
		if err == nil || errors.As(err, &mismatch) {
			t.Fatalf("unrelated config falsely attributed to main: %v", err)
		}
	}
}

func TestRenameGitConfigRejectsExecutableControlsAndOmitsIdentity(t *testing.T) {
	base := "[core]\n repositoryformatversion = 0\n bare = false\n[remote \"origin\"]\n url = ssh://example.invalid/repo\n fetch = +refs/heads/*:refs/remotes/origin/*\n[user]\n name = Alice\n email = a@example.invalid\n[branch \"main\"]\n remote = origin\n merge = refs/heads/main\n"
	out, err := safeGitConfig([]byte(base))
	if err != nil || strings.Contains(string(out), "ssh://") || strings.Contains(string(out), "Alice") || !strings.Contains(string(out), "merge = refs/heads/main") {
		t.Fatalf("safe config: %q %v", out, err)
	}
	for _, malicious := range []string{
		base + "[include]\n path = /tmp/evil\n", base + "[filter \"evil\"]\n clean = /tmp/evil\n",
		base + "[core]\n fsmonitor = /tmp/evil\n", base + "[diff]\n external = /tmp/evil\n",
		base + "[core]\n worktree = /tmp/evil\n",
	} {
		if _, err := safeGitConfig([]byte(malicious)); err == nil {
			t.Fatalf("executable config accepted: %q", malicious)
		}
	}
}
