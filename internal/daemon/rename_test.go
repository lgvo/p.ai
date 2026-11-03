package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimedriver/memory"
	"github.com/lgvo/p.ai/internal/sourcedriver"
	sourcememory "github.com/lgvo/p.ai/internal/sourcedriver/memory"
)

func TestRenameRequiresAStoppedSession(t *testing.T) {
	ev := control.RenameEvidence{Incarnation: "incarnation-1"}
	stopped := runtimedriver.Observation{State: runtimedriver.StateStopped, Incarnation: runtimedriver.Incarnation(ev.Incarnation)}
	if !renameSourceStopped(stopped) {
		t.Fatal("a stopped session was refused")
	}
	for _, state := range []runtimedriver.State{runtimedriver.StateRunning, runtimedriver.StateOther, runtimedriver.StateAbsent} {
		o := stopped
		o.State = state
		if renameSourceStopped(o) {
			t.Fatalf("a %s session was accepted", state)
		}
	}
	if !renameSourceExact(stopped, ev) {
		t.Fatal("the recorded stopped runtime was refused")
	}
	started := stopped
	started.State = runtimedriver.StateRunning
	replaced := stopped
	replaced.Incarnation = "incarnation-2"
	for name, o := range map[string]runtimedriver.Observation{"started": started, "replaced": replaced} {
		if renameSourceExact(o, ev) {
			t.Fatalf("a %s runtime was taken for the recorded one", name)
		}
	}
}

// wantRefusal fails unless err is a refusal of kind with reason and target.
func TestRenameOfASessionThatIsNotStoppedIsAConflict(t *testing.T) {
	const session = "11111111-1111-4111-8111-111111111111"
	stopped := runtimedriver.Observation{State: runtimedriver.StateStopped, Incarnation: "incarnation-1"}
	if err := renameSourceRefusal(stopped, session); err != nil {
		t.Fatalf("a stopped session was refused: %v", err)
	}
	for _, state := range []runtimedriver.State{runtimedriver.StateRunning, runtimedriver.StateOther} {
		o := stopped
		o.State = state
		wantRefusal(t, string(state), renameSourceRefusal(o, session), api.KindConflict, api.ReasonSessionNotStopped, "session/"+session)
	}
	wantRefusal(t, "missing", renameSourceRefusal(runtimedriver.Observation{State: runtimedriver.StateAbsent}, session), api.KindConflict, api.ReasonRuntimeMissing, "session/"+session)
}

// retainedFixture is a lifecycle over a real store and the bundled source
// driver with project app, whose branches saved and taken point at tip. Its
// context is done, so an accepted operation is recorded but never
// processed.
func retainedFixture(t *testing.T) (l *lifecycle, store *control.Store, repo, tip string) {
	t.Helper()
	store, err := control.OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err = store.CreateProject(context.Background(), "app", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	source, repo, tip := gitSource(t, store)
	for _, branch := range []string{"saved", "taken", "held"} {
		dispatchGit(t, repo, "update-ref", "refs/heads/"+branch, tip)
	}
	done, cancel := context.WithCancel(context.Background())
	cancel()
	return &lifecycle{ctx: done, store: store, git: &gitCapability{driver: source}}, store, repo, tip
}

// holdBranch records a creation that holds branch of project app.
func holdBranch(t *testing.T, store *control.Store, branch string) {
	t.Helper()
	selection := control.CreationSelection{HostID: "fixture", HostSHA256: strings.Repeat("b", 64)}
	_, _, err := store.BeginSessionCreate(context.Background(), control.ReserveSessionRequest{Key: "hold-" + branch, Project: "app", Branch: branch, Choice: "existing"}, strings.Repeat("d", 64), selection,
		func(context.Context, control.ReserveSessionRequest) (string, bool, error) {
			return strings.Repeat("e", 40), true, nil
		})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetainedRenameRefusalsNameTheirKind(t *testing.T) {
	l, store, repo, tip := retainedFixture(t)
	ctx := context.Background()
	holdBranch(t, store, "held")
	dispatchGit(t, repo, "symbolic-ref", "refs/heads/linked", "refs/heads/saved")
	base := control.RetainedRenameRequest{Key: "rename", Project: "app", OldBranch: "saved", NewBranch: "archive", ExpectedOldTip: tip}
	for _, test := range []struct {
		name   string
		change func(*control.RetainedRenameRequest)
		kind   api.ErrorKind
		reason string
		target string
	}{
		{"missing old branch", func(r *control.RetainedRenameRequest) { r.OldBranch = "absent" }, api.KindNotFound, api.ReasonBranch, "project/app:branch/absent"},
		{"changed tip", func(r *control.RetainedRenameRequest) { r.ExpectedOldTip = strings.Repeat("b", 40) }, api.KindPreconditionFailed, api.ReasonChanged, ""},
		{"taken new name", func(r *control.RetainedRenameRequest) { r.NewBranch = "taken" }, api.KindConflict, api.ReasonBranchExists, "project/app:branch/taken"},
		{"new name a symbolic ref holds", func(r *control.RetainedRenameRequest) { r.NewBranch = "linked" }, api.KindConflict, api.ReasonBranchExists, "project/app:branch/linked"},
		{"branch a session holds", func(r *control.RetainedRenameRequest) { r.OldBranch = "held" }, api.KindConflict, api.ReasonBranchAssigned, "project/app:branch/held"},
	} {
		r := base
		r.Key = test.name
		test.change(&r)
		_, err := l.RenameRetainedBranch(ctx, r)
		wantRefusal(t, test.name, err, test.kind, test.reason, test.target)
	}
	op, err := l.RenameRetainedBranch(ctx, base)
	if err != nil || op.Kind != control.KindRetainedRename {
		t.Fatalf("rename: %+v %v", op, err)
	}
	reused := base
	reused.NewBranch = "elsewhere"
	_, err = l.RenameRetainedBranch(ctx, reused)
	wantRefusal(t, "reused key", err, api.KindInvalid, api.ReasonKeyReused, "")
	_, err = l.RenameRetainedBranch(ctx, control.RetainedRenameRequest{Key: "guarded", Project: "app", OldBranch: "taken", NewBranch: "archive", ExpectedOldTip: tip})
	wantRefusal(t, "reserved new name", err, api.KindConflict, api.ReasonBranchReserved, "project/app:branch/archive")
}

// TestRetainedDeleteDryRunAndRun checks a retained branch's deletion through
// its two calls on real Git: the dry run plans it, lists the commits that
// leave P as certain loss and changes nothing; the run with its tag records
// the deletion once; a changed branch, an expired tag or a missing tag
// refuse the run; refusals are in the plan, and unknown records are errors.
func TestRetainedDeleteDryRunAndRun(t *testing.T) {
	l, store, repo, tip := retainedFixture(t)
	l.instanceID = "11111111-1111-4111-8111-111111111111"
	ctx := context.Background()
	holdBranch(t, store, "held")
	tree := dispatchGit(t, repo, "mktree")
	own := dispatchGit(t, repo, "-c", "user.name=P fixture", "-c", "user.email=p@example.test", "commit-tree", tree, "-p", tip, "-m", "only on saved")
	dispatchGit(t, repo, "update-ref", "refs/heads/saved", own)
	dryRun := func(branch string) (api.ChangeResult[api.RetainedDeleteDetails], error) {
		return l.DeleteRetainedBranch(ctx, "app", branch, api.ChangeRequest{})
	}

	plan, err := dryRun("saved")
	p := plan.Plan
	if err != nil || p == nil || !p.Allowed || plan.ChangeTag == "" || p.Action != "project.retained.delete" || p.Target != "project/app:branch/saved" ||
		p.Details.Tip != own || p.Details.Ref != "refs/heads/saved" || p.Details.BranchLoss == nil ||
		!reflect.DeepEqual(p.Loss, []api.Loss{{Kind: api.LossPCommits, Target: "project/app:branch/saved", Count: 1, Sample: []string{own}, Certain: true}}) || len(p.Gone) != 0 {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	if raw, _ := json.Marshal(p.Details); strings.Contains(string(raw), "origin") {
		t.Fatalf("the plan reads the origin: %s", raw)
	}
	if again, err := dryRun("saved"); err != nil || !reflect.DeepEqual(again.Plan, p) {
		t.Fatalf("a second dry run of the same state: %+v %v", again, err)
	}
	if got := dispatchGit(t, repo, "rev-parse", "refs/heads/saved"); got != own {
		t.Fatalf("the dry run moved the branch to %s", got)
	}
	if _, err = store.GetOperationByKey(ctx, "delete"); !errors.Is(err, control.ErrNotFound) {
		t.Fatalf("the dry run recorded an operation: %v", err)
	}

	held, err := dryRun("held")
	if err != nil || held.Plan.Allowed || held.ChangeTag != "" || !reflect.DeepEqual(held.Plan.Refusals, []api.Refusal{{Code: api.ReasonBranchAssigned}}) {
		t.Fatalf("an assigned branch's plan: %+v %v", held, err)
	}
	_, err = dryRun("absent")
	wantRefusal(t, "missing branch", err, api.KindNotFound, api.ReasonBranch, "project/app:branch/absent")
	_, err = l.DeleteRetainedBranch(ctx, "gone", "saved", api.ChangeRequest{})
	wantRefusal(t, "missing project", err, api.KindNotFound, api.ReasonProject, "project/gone")

	_, err = l.DeleteRetainedBranch(ctx, "app", "saved", api.ChangeRequest{Key: "delete"})
	if !reflect.DeepEqual(err, api.PreconditionRequired()) {
		t.Fatalf("a run without its tag answered %v", err)
	}
	_, err = l.DeleteRetainedBranch(ctx, "app", "saved", api.ChangeRequest{Key: "delete", IfMatch: futureTag(-time.Second)})
	wantRefusal(t, "an expired tag", err, api.KindPreconditionFailed, api.ReasonExpired, "")
	_, err = l.DeleteRetainedBranch(ctx, "app", "taken", api.ChangeRequest{Key: "delete", IfMatch: plan.ChangeTag})
	wantRefusal(t, "the tag of another branch", err, api.KindPreconditionFailed, api.ReasonChanged, "")
	dispatchGit(t, repo, "update-ref", "refs/heads/saved", tip)
	_, err = l.DeleteRetainedBranch(ctx, "app", "saved", api.ChangeRequest{Key: "delete", IfMatch: plan.ChangeTag})
	wantRefusal(t, "a moved branch", err, api.KindPreconditionFailed, api.ReasonChanged, "")
	if _, err = store.GetOperationByKey(ctx, "delete"); !errors.Is(err, control.ErrNotFound) {
		t.Fatalf("a refused run recorded an operation: %v", err)
	}

	dispatchGit(t, repo, "update-ref", "refs/heads/saved", own)
	run := api.ChangeRequest{Key: "delete", IfMatch: plan.ChangeTag}
	result, err := l.DeleteRetainedBranch(ctx, "app", "saved", run)
	if err != nil || result.Operation == nil || result.Plan != nil || result.Operation.Kind != control.KindRetainedDelete || result.Operation.Phase != "guarded" {
		t.Fatalf("run: %+v %v", result, err)
	}
	var recorded control.RetainedDeleteRequest
	if err = json.Unmarshal(result.Operation.Request, &recorded); err != nil || recorded != (control.RetainedDeleteRequest{Key: "delete", Project: "app", Branch: "saved", IfMatch: plan.ChangeTag}) {
		t.Fatalf("recorded request %s: %v", result.Operation.Request, err)
	}
	if replay, err := l.DeleteRetainedBranch(ctx, "app", "saved", run); err != nil || replay.Operation.ID != result.Operation.ID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	other, _ := dryRun("taken")
	_, err = l.DeleteRetainedBranch(ctx, "app", "taken", api.ChangeRequest{Key: "delete", IfMatch: other.ChangeTag})
	wantRefusal(t, "the key of another run", err, api.KindInvalid, api.ReasonKeyReused, "")
	// The recorded deletion guards its branch until it ends: another plan of
	// it is refused.
	guarded, err := dryRun("saved")
	if err != nil || guarded.Plan.Allowed || guarded.ChangeTag != "" || !reflect.DeepEqual(guarded.Plan.Refusals, []api.Refusal{{Code: api.ReasonBranchReserved}}) {
		t.Fatalf("a guarded branch's plan: %+v %v", guarded, err)
	}
}

// renameFixture is the session fixture over an in-memory source driver whose
// project app holds the session's branch work at oldTip.
type renameFixture struct {
	*sessionFixture
	heads  *sourcememory.World
	source *sourcememory.Driver
	oldTip string
	target runtimedriver.Target // the stopped session's workspace
	// calls are the clone edits the rename asked the runtime driver for, in
	// order.
	calls []renameCall
	// beforeEdit, when set, runs as the rename asks for each clone edit.
	beforeEdit func(kind runtimedriver.EffectKind, path string)
}

type renameCall struct {
	kind runtimedriver.EffectKind
	path string
}

func newRenameFixture(t *testing.T) *renameFixture {
	t.Helper()
	f := &renameFixture{sessionFixture: newSessionFixture(t)}
	f.heads, f.source = memorySource(t)
	if _, err := f.source.CreateRepository(f.ctx, "app"); err != nil {
		t.Fatal(err)
	}
	tip := f.heads.Commit("app")
	f.heads.SetHead("app", "work", tip)
	f.oldTip = string(tip)
	f.l.git.driver = f.source
	return f
}

// stopWithClone stops the session and gives its workspace a Git clone of its
// branch work at the branch's tip, without a reflog.
func (f *renameFixture) stopWithClone() {
	f.t.Helper()
	o, err := f.driver.Stop(f.ctx, f.spec())
	if err != nil {
		f.t.Fatal(err)
	}
	f.target = runtimedriver.Target{Session: f.spec(), At: o.Incarnation}
	config := "[core]\nrepositoryformatversion = 0\nbare = false\n[remote \"origin\"]\nfetch = +refs/heads/*:refs/remotes/origin/*\n[branch \"work\"]\nremote = origin\nmerge = refs/heads/work\n"
	f.put(".git/refs", runtimedriver.File{Type: runtimedriver.FileDirectory, UID: renameUID, GID: renameUID, Mode: 0o755})
	f.put(".git/refs/heads", runtimedriver.File{Type: runtimedriver.FileDirectory, UID: renameUID, GID: renameUID, Mode: 0o755})
	f.write(".git/refs/heads/work", f.oldTip+"\n")
	f.write(".git/config", config)
	f.driver.beforeEdit = func(kind runtimedriver.EffectKind, path string) {
		f.calls = append(f.calls, renameCall{kind, path})
		if f.beforeEdit != nil {
			f.beforeEdit(kind, path)
		}
	}
}

// put writes the clone's file at p, as the session's Git would: not as an
// edit of the rename.
func (f *renameFixture) put(p string, file runtimedriver.File) {
	f.t.Helper()
	hook := f.driver.beforeEdit
	f.driver.beforeEdit = nil
	defer func() { f.driver.beforeEdit = hook }()
	if err := f.driver.WriteWorkspace(f.ctx, f.target, p, file, func() error { return nil }); err != nil {
		f.t.Fatal(err)
	}
}

// write writes a file of the session user, as the session's Git would.
func (f *renameFixture) write(p, data string) {
	f.put(p, runtimedriver.File{Type: runtimedriver.FileRegular, UID: renameUID, GID: renameUID, Mode: 0o644, Data: []byte(data)})
}

// read is the clone's file at p.
func (f *renameFixture) read(p string) runtimedriver.File {
	f.t.Helper()
	file, err := f.driver.ReadWorkspace(f.ctx, f.target, p, 1<<20)
	if err != nil {
		f.t.Fatal(err)
	}
	return file
}

// backup is the path of the rename's backup: the clone file it writes
// first, before P's new head is created.
func (f *renameFixture) backup() string {
	f.t.Helper()
	for _, c := range f.calls {
		if c.kind == runtimedriver.EffectWrite {
			return c.path
		}
	}
	f.t.Fatal("the rename wrote no backup")
	return ""
}

// backupPresent reports whether the clone still holds the rename's backup.
func (f *renameFixture) backupPresent() bool {
	return f.read(f.backup()).Type != runtimedriver.FileAbsent
}

// sentOnce fails unless the rename asked for each clone edit once.
func (f *renameFixture) sentOnce() {
	f.t.Helper()
	seen := map[renameCall]bool{}
	for _, c := range f.calls {
		if seen[c] {
			f.t.Fatalf("the rename asked twice to %s %s", c.kind, c.path)
		}
		seen[c] = true
	}
}

func (f *renameFixture) renameRequest() control.RenameRequest {
	return control.RenameRequest{Key: "rename", UUID: f.session.UUID, NewBranch: "renamed", ExpectedOldTip: f.oldTip}
}

// head is the tip of P's head name of project app, or "" when it is absent.
func (f *renameFixture) head(name string) string {
	f.t.Helper()
	tip, _, err := f.source.InspectBranch(f.ctx, "app", name)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(tip)
}

// renamed checks that the rename completed: the session, its P head and its
// clone are on branch renamed, and the runtime stayed stopped.
func (f *renameFixture) renamed(opID string) {
	f.t.Helper()
	if done := f.operation(opID); done.Status != "completed" {
		f.t.Fatalf("rename ended %s at %s: %s", done.Status, done.Phase, done.Diagnostic)
	}
	session, err := f.l.store.GetSession(f.ctx, f.session.UUID)
	if err != nil || session.Branch != "renamed" {
		f.t.Fatalf("session on branch %q: %v", session.Branch, err)
	}
	if renamed, work := f.head("renamed"), f.head("work"); renamed != f.oldTip || work != "" {
		f.t.Fatalf("P heads after the rename: renamed at %q, work at %q", renamed, work)
	}
	if head := f.read(".git/HEAD"); string(head.Data) != "ref: refs/heads/renamed\n" {
		f.t.Fatalf("clone HEAD %q", head.Data)
	}
	if o := f.runtime(); o.State != runtimedriver.StateStopped {
		f.t.Fatalf("the renamed session's runtime is %s", o.State)
	}
}

// failedUnchanged checks that the rename failed before its commit point
// with reason, its backup gone and P's heads as they were.
func (f *renameFixture) failedUnchanged(opID, reason string) {
	f.t.Helper()
	if done := f.operation(opID); done.Status != "failed" || done.Reason != reason {
		f.t.Fatalf("rename ended %s at %s with reason %q, want failed with %q: %s", done.Status, done.Phase, done.Reason, reason, done.Diagnostic)
	}
	if renamed, work := f.head("renamed"), f.head("work"); renamed != "" || work != f.oldTip {
		f.t.Fatalf("P heads after the failed rename: renamed at %q, work at %q", renamed, work)
	}
	if len(f.calls) != 0 && f.backupPresent() {
		f.t.Fatal("the failed rename left its backup")
	}
}

func TestRenameOfARunningSessionIsRefused(t *testing.T) {
	f := newRenameFixture(t)
	if _, err := f.l.RenameSession(f.ctx, f.renameRequest()); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("rename of a running session: %v", err)
	}
	if _, err := f.l.store.GetOperationByKey(f.ctx, "rename"); !errors.Is(err, control.ErrNotFound) {
		t.Fatalf("a refused rename recorded an operation: %v", err)
	}
	if o := f.runtime(); o.State != runtimedriver.StateRunning || f.head("work") != f.oldTip {
		t.Fatal("a refused rename changed the session")
	}
}

func TestRenameRefusesANewBranchTheSourceDriverRefusesBeforeRecordingIt(t *testing.T) {
	f := newRenameFixture(t)
	f.stopWithClone()
	req := f.renameRequest()
	req.NewBranch = "-renamed"
	_, err := f.l.RenameSession(f.ctx, req)
	var refusal *control.Refusal
	if !errors.As(err, &refusal) || refusal.Kind != api.KindInvalid || refusal.Field != "new_branch" {
		t.Fatalf("rename to a branch Git refuses answered %v, want invalid new_branch", err)
	}
	if _, err := f.l.store.GetOperationByKey(f.ctx, "rename"); !errors.Is(err, control.ErrNotFound) {
		t.Fatalf("a refused rename recorded an operation: %v", err)
	}
}

func TestRenameReplaysARecordedRequestWhileTheSourceDriverIsDown(t *testing.T) {
	f := newRenameFixture(t)
	f.stopWithClone()
	op, err := f.l.RenameSession(f.ctx, f.renameRequest())
	if err != nil {
		t.Fatal(err)
	}
	f.source.Kill()
	if replay, err := f.l.RenameSession(f.ctx, f.renameRequest()); err != nil || replay.ID != op.ID {
		t.Fatalf("replay with the source driver down: %+v %v", replay, err)
	}
	another := f.renameRequest()
	another.Key = "another"
	_, err = f.l.RenameSession(f.ctx, another)
	wantRefusal(t, "a new request with the source driver down", err, api.KindFailed, api.ReasonGit, "")
}

func TestRenameOfAStoppedSessionRenamesItsBranchAndClone(t *testing.T) {
	f := newRenameFixture(t)
	f.stopWithClone()
	op, err := f.l.RenameSession(f.ctx, f.renameRequest())
	if err != nil {
		t.Fatal(err)
	}
	f.l.processRename(op)
	f.renamed(op.ID)
	f.sentOnce()
	if f.backupPresent() {
		t.Fatal("the rename left its backup")
	}
}

func TestRenameOfACloneOnAnotherBranchFailsAsAWorkspaceMismatch(t *testing.T) {
	f := newRenameFixture(t)
	f.stopWithClone()
	f.write(".git/HEAD", "ref: refs/heads/other\n")
	op, err := f.l.RenameSession(f.ctx, f.renameRequest())
	if err != nil {
		t.Fatal(err)
	}
	f.l.processRename(op)
	f.failedUnchanged(op.ID, api.ReasonWorkspaceMismatch)
	if len(f.calls) != 0 {
		t.Fatalf("the refused rename edited the clone: %v", f.calls)
	}
	if done := f.operation(op.ID); !strings.Contains(done.Diagnostic, "refs/heads/other") {
		t.Fatalf("the mismatch's diagnostic %q does not name the clone's branch", done.Diagnostic)
	}
}

// replanningSource is a source driver that answers with another plan than
// the rename recorded whenever replan says so.
type replanningSource struct {
	sourcedriver.Driver
	replan func(sourcedriver.WorkspaceRename) bool
}

func (s replanningSource) RenameWorkspace(ctx context.Context, r sourcedriver.WorkspaceRename, files []sourcedriver.WorkspaceFile) (sourcedriver.WorkspaceEdits, error) {
	answer, err := s.Driver.RenameWorkspace(ctx, r, files)
	if err == nil && len(answer.Read) == 0 && s.replan(r) {
		answer.Plan = "another plan"
	}
	return answer, err
}

func TestRenameWhosePreparedCloneHasAnotherPlanFailsBeforeCreatingTheNewHead(t *testing.T) {
	f := newRenameFixture(t)
	f.stopWithClone()
	op, err := f.l.RenameSession(f.ctx, f.renameRequest())
	if err != nil {
		t.Fatal(err)
	}
	f.l.git.driver = replanningSource{f.source, func(r sourcedriver.WorkspaceRename) bool {
		return r.Goal == sourcedriver.GoalPrepared && f.operation(op.ID).Phase == "workspace-prepared"
	}}
	f.l.processRename(op)
	f.failedUnchanged(op.ID, api.ReasonChanged)
}

func TestRenameWhoseCloneHasAnotherPlanAfterTheNewHeadEditsNothing(t *testing.T) {
	f := newRenameFixture(t)
	f.stopWithClone()
	op, err := f.l.RenameSession(f.ctx, f.renameRequest())
	if err != nil {
		t.Fatal(err)
	}
	f.l.git.driver = replanningSource{f.source, func(r sourcedriver.WorkspaceRename) bool { return r.Goal == sourcedriver.GoalRenamed }}
	f.l.processRename(op)
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != "new-ref-created" {
		t.Fatalf("rename whose renamed clone has another plan: %s at %s", blocked.Status, blocked.Phase)
	}
	if len(f.calls) != 1 || string(f.read(".git/HEAD").Data) != "ref: refs/heads/work\n" {
		t.Fatalf("the rename edited the clone beyond its backup: %v", f.calls)
	}
}

func TestRenameWithAnUnknownWriteOutcomeStaysBlockedUntilTheWriteIsFound(t *testing.T) {
	f := newRenameFixture(t)
	f.stopWithClone()
	op, err := f.l.RenameSession(f.ctx, f.renameRequest())
	if err != nil {
		t.Fatal(err)
	}
	late := f.world.Delay(runtimedriver.EffectWrite)
	f.l.processRename(op)
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != "prepare-issued" {
		t.Fatalf("rename with an unanswered backup write: %s at %s", blocked.Status, blocked.Phase)
	}
	f.l.processRename(f.retry(op.ID))
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != "prepare-issued" || f.head("work") != f.oldTip {
		t.Fatalf("retried rename with the write still unanswered: %s at %s", blocked.Status, blocked.Phase)
	}
	late.Land()
	f.l.processRename(f.retry(op.ID))
	f.renamed(op.ID)
	f.sentOnce()
}

func TestRenameRecoveringItsBackupWriteGoesOnOnlyOnceTheDriverSettlesIt(t *testing.T) {
	f := newRenameFixture(t)
	f.stopWithClone()
	op, err := f.l.RenameSession(f.ctx, f.renameRequest())
	if err != nil {
		t.Fatal(err)
	}
	late := f.world.Delay(runtimedriver.EffectWrite)
	f.l.processRename(op)
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != "prepare-issued" {
		t.Fatalf("rename with an unanswered backup write: %s at %s", blocked.Status, blocked.Phase)
	}
	// The backup lands exactly while the backend's operation still runs.
	release := f.world.Hold(f.runtimeRef())
	late.Land()
	f.l.processRename(f.retry(op.ID))
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != "prepare-issued" || f.head("work") != f.oldTip {
		t.Fatalf("retried rename with its backup in place but unsettled: %s at %s", blocked.Status, blocked.Phase)
	}
	release()
	f.l.processRename(f.retry(op.ID))
	f.renamed(op.ID)
	f.sentOnce()
}

func TestRenameRecoveringItsCloneEditsGoesOnOnlyOnceTheDriverSettlesThem(t *testing.T) {
	f := newRenameFixture(t)
	f.stopWithClone()
	op, err := f.l.RenameSession(f.ctx, f.renameRequest())
	if err != nil {
		t.Fatal(err)
	}
	// The clone keeps no reflog: removing the old local ref is the rename's
	// last edit.
	late := f.world.Delay(runtimedriver.EffectRemove)
	f.l.processRename(op)
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != "workspace-rename-issued" {
		t.Fatalf("rename with an unanswered removal: %s at %s", blocked.Status, blocked.Phase)
	}
	// The removal lands while the backend's operation still runs: every edit
	// is in place, unsettled.
	release := f.world.Hold(f.runtimeRef())
	late.Land()
	f.l.processRename(f.retry(op.ID))
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != "workspace-rename-issued" || f.head("work") != f.oldTip {
		t.Fatalf("retried rename with its edits in place but unsettled: %s at %s", blocked.Status, blocked.Phase)
	}
	release()
	f.l.processRename(f.retry(op.ID))
	f.renamed(op.ID)
	f.sentOnce()
}

// delayFirstCloneEdit makes the backend admit the rename's first edit
// toward the renamed clone, the write of its new local ref, late, and
// returns that admission once it was asked for.
func (f *renameFixture) delayFirstCloneEdit() func() *memory.Admission {
	var late *memory.Admission
	f.beforeEdit = func(kind runtimedriver.EffectKind, path string) {
		if kind == runtimedriver.EffectWrite && path != f.backup() && late == nil {
			late = f.world.Delay(runtimedriver.EffectWrite)
		}
	}
	return func() *memory.Admission { return late }
}

func TestRenameWithALostCloneEditAndEditsLeftSendsNoEditAgain(t *testing.T) {
	f := newRenameFixture(t)
	f.stopWithClone()
	op, err := f.l.RenameSession(f.ctx, f.renameRequest())
	if err != nil {
		t.Fatal(err)
	}
	late := f.delayFirstCloneEdit()
	f.l.processRename(op)
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != "workspace-rename-issued" || late() == nil {
		t.Fatalf("rename with an unanswered clone edit: %s at %s", blocked.Status, blocked.Phase)
	}
	asked := len(f.calls)
	// The lost write lands and nothing runs on the runtime any more: the
	// runtime driver would settle it applied, but the edits after it were
	// never sent, and none is sent now.
	late().Land()
	f.l.processRename(f.retry(op.ID))
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != "workspace-rename-issued" {
		t.Fatalf("retried rename with edits left: %s at %s", blocked.Status, blocked.Phase)
	}
	if len(f.calls) != asked || string(f.read(".git/HEAD").Data) != "ref: refs/heads/work\n" {
		t.Fatalf("the retried rename asked for %v after its lost edit", f.calls[asked:])
	}
}

func TestRenameGoesOnWhenTheRuntimeDriverSettlesALostCloneEditApplied(t *testing.T) {
	f := newRenameFixture(t)
	f.stopWithClone()
	op, err := f.l.RenameSession(f.ctx, f.renameRequest())
	if err != nil {
		t.Fatal(err)
	}
	late := f.delayFirstCloneEdit()
	// The write lands by the time the runtime driver settles it: it happened,
	// its reply was lost.
	f.driver.beforeSettle = func() {
		if late() != nil {
			late().Land()
		}
	}
	f.l.processRename(op)
	f.renamed(op.ID)
	f.sentOnce()
}

// delayBackupRemoval makes the backend admit the first removal of the
// rename's backup late, and returns that admission once it was asked for.
func (f *renameFixture) delayBackupRemoval() func() *memory.Admission {
	var late *memory.Admission
	f.beforeEdit = func(kind runtimedriver.EffectKind, path string) {
		if kind == runtimedriver.EffectRemove && path == f.backup() && late == nil {
			late = f.world.Delay(runtimedriver.EffectRemove)
		}
	}
	return func() *memory.Admission { return late }
}

func TestRenameNeverRemovesItsBackupTwice(t *testing.T) {
	f := newRenameFixture(t)
	f.stopWithClone()
	op, err := f.l.RenameSession(f.ctx, f.renameRequest())
	if err != nil {
		t.Fatal(err)
	}
	late := f.delayBackupRemoval()
	f.l.processRename(op)
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != "old-ref-deleted" || late() == nil {
		t.Fatalf("rename with an unanswered backup removal: %s at %s", blocked.Status, blocked.Phase)
	}
	f.l.processRename(f.retry(op.ID))
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != "old-ref-deleted" || !f.backupPresent() {
		t.Fatalf("retried rename with its backup removal unanswered: %s at %s, backup kept %t", blocked.Status, blocked.Phase, f.backupPresent())
	}
	late().Land()
	f.l.processRename(f.retry(op.ID))
	f.renamed(op.ID)
	f.sentOnce()
}

func TestRenameRollingBackSettlesItsBackupRemovalBeforeAnythingElse(t *testing.T) {
	f := newRenameFixture(t)
	f.stopWithClone()
	op, err := f.l.RenameSession(f.ctx, f.renameRequest())
	if err != nil {
		t.Fatal(err)
	}
	late := f.delayBackupRemoval()
	delayRemoval := f.beforeEdit
	// The old P head goes away once the backup is written: the rename rolls
	// back and removes its backup.
	f.beforeEdit = func(kind runtimedriver.EffectKind, path string) {
		if kind == runtimedriver.EffectWrite && path == f.backup() {
			f.heads.SetHead("app", "work", "")
		}
		delayRemoval(kind, path)
	}
	f.l.processRename(op)
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != "workspace-prepared" || late() == nil {
		t.Fatalf("rollback with an unanswered backup removal: %s at %s", blocked.Status, blocked.Phase)
	}
	// With the old P head back and the backup still there, only the pending
	// removal keeps the rename from going on.
	f.heads.SetHead("app", "work", sourcedriver.OID(f.oldTip))
	f.l.processRename(f.retry(op.ID))
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != "workspace-prepared" || !f.backupPresent() {
		t.Fatalf("retried rollback with its backup removal unanswered: %s at %s", blocked.Status, blocked.Phase)
	}
	late().Land()
	f.l.processRename(f.retry(op.ID))
	f.failedUnchanged(op.ID, api.ReasonChanged)
	f.sentOnce()
}
