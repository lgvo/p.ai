package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimedriver/memory"
	"github.com/lgvo/p.ai/internal/sshserver"
)

// hookedDriver is the in-memory driver with hooks that let a test act as
// calls reach the backend.
type hookedDriver struct {
	*memory.Driver
	// beforeStop runs when Stop is called; a non-nil error fails the call
	// before it reaches the backend.
	beforeStop func() error
	// beforeSettle runs when Settle is called.
	beforeSettle func()
	// beforeEdit runs when a workspace write or removal is called, with its
	// kind and path.
	beforeEdit func(kind runtimedriver.EffectKind, path string)
}

func (d *hookedDriver) Stop(ctx context.Context, s runtimedriver.Session) (runtimedriver.Observation, error) {
	if d.beforeStop != nil {
		if err := d.beforeStop(); err != nil {
			return runtimedriver.Observation{}, err
		}
	}
	return d.Driver.Stop(ctx, s)
}

func (d *hookedDriver) WriteWorkspace(ctx context.Context, t runtimedriver.Target, path string, f runtimedriver.File, g runtimedriver.Gate) error {
	if d.beforeEdit != nil {
		d.beforeEdit(runtimedriver.EffectWrite, path)
	}
	return d.Driver.WriteWorkspace(ctx, t, path, f, g)
}

func (d *hookedDriver) RemoveWorkspace(ctx context.Context, t runtimedriver.Target, path string, g runtimedriver.Gate) error {
	if d.beforeEdit != nil {
		d.beforeEdit(runtimedriver.EffectRemove, path)
	}
	return d.Driver.RemoveWorkspace(ctx, t, path, g)
}

func (d *hookedDriver) Settle(ctx context.Context, e runtimedriver.Effect) (runtimedriver.Settlement, error) {
	if d.beforeSettle != nil {
		d.beforeSettle()
	}
	return d.Driver.Settle(ctx, e)
}

// sessionFixture is a lifecycle over a real store, P's Git service and the
// in-memory runtime driver, with one session of project app on branch work,
// created from the driver's base image.
type sessionFixture struct {
	t       *testing.T
	ctx     context.Context
	l       *lifecycle
	world   *memory.World
	driver  *hookedDriver
	repo    string
	tip     string
	session control.Session
	created runtimedriver.Session // the spec the runtime was created with
	creator string                // the session's creation
}

// newSessionFixture is the fixture with the session established and its
// runtime running.
func newSessionFixture(t *testing.T) *sessionFixture {
	t.Helper()
	f := newCreatedFixture(t)
	if err := f.l.store.CompleteCreation(f.ctx, f.creator); err != nil {
		t.Fatal(err)
	}
	session, err := f.l.store.GetSession(f.ctx, f.session.UUID)
	if err != nil {
		t.Fatal(err)
	}
	f.session = session
	if _, err = f.driver.Start(f.ctx, f.spec()); err != nil {
		t.Fatal(err)
	}
	return f
}

// newFailedAssemblyFixture is the fixture with the session's creation
// blocked once its runtime was assembled, the runtime stopped.
func newFailedAssemblyFixture(t *testing.T) *sessionFixture {
	t.Helper()
	f := newCreatedFixture(t)
	creator := f.operation(f.creator)
	ev, err := control.Evidence(creator)
	if err != nil {
		t.Fatal(err)
	}
	ev.RuntimeInitState = "attempted"
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.l.store.AdvanceOperation(f.ctx, f.creator, "blocked", "assembly-ready", true, raw, api.ReasonRuntime, "the session host failed"); err != nil {
		t.Fatal(err)
	}
	return f
}

// newCreatedFixture is the fixture with the session's creation at its
// local steps and its runtime created, complete and stopped.
func newCreatedFixture(t *testing.T) *sessionFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store, err := control.OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	policy := control.ProjectPolicy{Network: "none", FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/sh"}}
	snapshot, _, err := control.ProjectPolicySnapshot(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateProject(ctx, "app", snapshot); err != nil {
		t.Fatal(err)
	}
	source, repo, tip := gitSource(t, store)
	dispatchGit(t, repo, "update-ref", "refs/heads/work", tip)
	// A socket path must stay short.
	prefix, err := os.MkdirTemp("/tmp", "p-removal-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(prefix) })
	endpoints, err := newEndpointManager(prefix, "127.0.0.1:1", store)
	if err != nil {
		t.Fatal(err)
	}
	world := memory.NewWorld(memory.Config{})
	driver := &hookedDriver{Driver: memory.NewDriver(world)}
	git := testGitCapability()
	git.driver = source
	l := &lifecycle{ctx: ctx, store: store, driver: driver, git: git, endpoints: endpoints, working: map[string]bool{},
		instanceID: "99999999-9999-4999-8999-999999999999",
		cfg: control.RuntimeConfig{EndpointPrefix: prefix, BaseImageFingerprint: string(world.BaseImage()),
			ProjectPolicies: map[string]control.ProjectPolicy{"app": policy}}}
	// The journal releases a worker's hold on the session when it records
	// the operation's end.
	store.ObserveJournal(l)
	op, session, err := store.BeginSessionCreate(ctx, control.ReserveSessionRequest{Key: "session", Project: "app", Branch: "work", Choice: "existing"},
		string(world.BaseImage()), l.selection(), func(context.Context, control.ReserveSessionRequest) (string, bool, error) { return tip, true, nil })
	if err != nil {
		t.Fatal(err)
	}
	// The creation's local steps: the endpoints, the Git key and its principal.
	if _, err = endpoints.Ensure(ctx, session.UUID); err != nil {
		t.Fatal(err)
	}
	key, pub, err := l.sessionKey(ctx, session.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureSessionGitPrincipal(ctx, sshserver.Fingerprint(pub), "app", session.UUID); err != nil {
		t.Fatal(err)
	}
	f := &sessionFixture{t: t, ctx: ctx, l: l, world: world, driver: driver, repo: repo, tip: tip, session: session, creator: op.ID}
	spec := f.spec()
	f.created = spec
	spec.GitKey = key
	if _, err = driver.Create(ctx, spec, "", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *sessionFixture) spec() runtimedriver.Session {
	f.t.Helper()
	spec, err := f.l.sessionSpec(f.ctx, f.session)
	if err != nil {
		f.t.Fatal(err)
	}
	return spec
}

// runtime is the session's runtime as the backend holds it.
func (f *sessionFixture) runtime() runtimedriver.Observation {
	f.t.Helper()
	o, err := memory.NewDriver(f.world).Inspect(f.ctx, f.created)
	if err != nil {
		f.t.Fatal(err)
	}
	return o
}

func (f *sessionFixture) operation(id string) api.Operation {
	f.t.Helper()
	op, err := f.l.store.GetOperation(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return op
}

func (f *sessionFixture) branchExists() bool {
	f.t.Helper()
	_, exists, err := f.l.git.driver.InspectBranch(f.ctx, "app", "work")
	if err != nil {
		f.t.Fatal(err)
	}
	return exists
}

// removal is the session's discard or delete, by action.
func (f *sessionFixture) removal(action string) func(context.Context, string, api.ChangeRequest) (api.ChangeResult[api.RemovalDetails], error) {
	if action == "delete" {
		return f.l.DeleteSession
	}
	return f.l.DiscardSession
}

// confirmRemoval plans the session's discard or delete with a dry run and
// runs it with the plan's change tag.
func (f *sessionFixture) confirmRemoval(action string) api.Operation {
	f.t.Helper()
	remove := f.removal(action)
	dry, err := remove(f.ctx, f.session.UUID, api.ChangeRequest{})
	if err != nil || dry.ChangeTag == "" {
		f.t.Fatalf("%s plan: %+v %v", action, dry.Plan, err)
	}
	run, err := remove(f.ctx, f.session.UUID, api.ChangeRequest{Key: action, IfMatch: dry.ChangeTag})
	if err != nil {
		f.t.Fatal(err)
	}
	return *run.Operation
}

// retry is the user's Retry of a blocked operation, which the test's worker
// then runs.
func (f *sessionFixture) retry(id string) api.Operation {
	f.t.Helper()
	if _, err := f.l.Retry(f.ctx, id); err != nil {
		f.t.Fatal(err)
	}
	return f.operation(id)
}

func TestDiscardStopsARunningSessionAfterRecordingTheStopThenDeletesIt(t *testing.T) {
	f := newSessionFixture(t)
	op := f.confirmRemoval("discard")
	phaseAtStop := ""
	f.driver.beforeStop = func() error {
		phaseAtStop = f.operation(op.ID).Phase
		return nil
	}
	f.l.processDiscard(op)
	if phaseAtStop != "stop-issued" {
		t.Fatalf("the running source was stopped at phase %q, want stop-issued", phaseAtStop)
	}
	if done := f.operation(op.ID); done.Status != "completed" || done.Phase != "discarded" {
		t.Fatalf("discard ended %s at %s: %s", done.Status, done.Phase, done.Diagnostic)
	}
	if f.runtime().State != runtimedriver.StateAbsent || !f.branchExists() {
		t.Fatal("discard did not delete the runtime and keep the branch")
	}
	if f.world.DeletedWhileRunning(f.runtimeRef()) {
		t.Fatal("the runtime was deleted while it ran")
	}
}

func (f *sessionFixture) runtimeRef() runtimedriver.Ref {
	return runtimedriver.Ref{Kind: runtimedriver.KindSession, ID: f.session.UUID}
}

func TestDiscardThatFindsItsStopIssuedStopsAgain(t *testing.T) {
	f := newSessionFixture(t)
	op := f.confirmRemoval("discard")
	f.driver.beforeStop = func() error { return errors.New("the stop's reply was lost") }
	f.l.processDiscard(op)
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != "stop-issued" {
		t.Fatalf("discard with a lost stop: %s at %s", blocked.Status, blocked.Phase)
	}
	f.driver.beforeStop = nil
	f.l.processDiscard(f.retry(op.ID))
	if done := f.operation(op.ID); done.Status != "completed" || done.Phase != "discarded" {
		t.Fatalf("retried discard ended %s at %s: %s", done.Status, done.Phase, done.Diagnostic)
	}
	if f.runtime().State != runtimedriver.StateAbsent || f.world.DeletedWhileRunning(f.runtimeRef()) {
		t.Fatal("the retried discard did not stop and delete the runtime")
	}
}

func TestDiscardWithAnUnknownDeleteOutcomeStaysBlockedAndNeverDeletesAgain(t *testing.T) {
	f := newSessionFixture(t)
	op := f.confirmRemoval("discard")
	late := f.world.Delay(runtimedriver.EffectDelete)
	f.l.processDiscard(op)
	blocked := f.operation(op.ID)
	if blocked.Status != "blocked" || blocked.Phase != "delete-issued" || f.runtime().State != runtimedriver.StateStopped {
		t.Fatalf("discard with an unanswered delete: %s at %s", blocked.Status, blocked.Phase)
	}
	// A retry settles the delete again; had it sent another, the runtime
	// would be gone before the first one lands.
	f.l.processDiscard(f.retry(op.ID))
	if blocked = f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != "delete-issued" {
		t.Fatalf("retried discard with the delete still unanswered: %s at %s", blocked.Status, blocked.Phase)
	}
	if f.runtime().State != runtimedriver.StateStopped {
		t.Fatal("a retry sent the delete again")
	}
	late.Land()
	f.l.processDiscard(f.retry(op.ID))
	if done := f.operation(op.ID); done.Status != "completed" || done.Phase != "discarded" {
		t.Fatalf("discard after the delete landed ended %s at %s: %s", done.Status, done.Phase, done.Diagnostic)
	}
	if f.runtime().State != runtimedriver.StateAbsent || !f.branchExists() {
		t.Fatal("discard did not delete the runtime and keep the branch")
	}
}

func TestDeleteCompletesWhenTheDriverSettlesALostDeleteReplyApplied(t *testing.T) {
	f := newSessionFixture(t)
	op := f.confirmRemoval("delete")
	late := f.world.Delay(runtimedriver.EffectDelete)
	f.driver.beforeSettle = late.Land
	f.l.processDiscard(op)
	if done := f.operation(op.ID); done.Status != "completed" || done.Phase != "deleted" {
		t.Fatalf("delete ended %s at %s: %s", done.Status, done.Phase, done.Diagnostic)
	}
	if f.runtime().State != runtimedriver.StateAbsent || f.branchExists() {
		t.Fatal("delete did not delete the runtime and the branch")
	}
}

func TestRemovalIsRefusedWhileATerminalIsAttached(t *testing.T) {
	f := newSessionFixture(t)
	dry, err := f.l.DiscardSession(f.ctx, f.session.UUID, api.ChangeRequest{})
	if err != nil || dry.ChangeTag == "" {
		t.Fatalf("discard plan: %+v %v", dry.Plan, err)
	}
	f.l.attachments = map[string]*attachment{"token": {session: f.session.UUID, expires: time.Now().Add(time.Minute)}}
	for _, action := range []string{"discard", "delete"} {
		plan, err := f.removal(action)(f.ctx, f.session.UUID, api.ChangeRequest{})
		if err != nil || plan.Plan == nil || plan.Plan.Allowed || plan.ChangeTag != "" || len(plan.Plan.Refusals) != 1 || plan.Plan.Refusals[0].Code != api.ReasonAttachmentActive {
			t.Fatalf("%s plan with a terminal attached: %+v %v", action, plan.Plan, err)
		}
	}
	if _, err := f.l.DiscardSession(f.ctx, f.session.UUID, api.ChangeRequest{Key: "attached", IfMatch: dry.ChangeTag}); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("discard with a terminal attached: %v", err)
	}
	if o := f.runtime(); o.State != runtimedriver.StateRunning {
		t.Fatalf("an attached session's runtime is %s", o.State)
	}
}

func TestProjectDeletionWithAnUnknownDeleteOutcomeStaysBlockedAndNeverDeletesAgain(t *testing.T) {
	f := newSessionFixture(t)
	if _, err := f.driver.Stop(f.ctx, f.spec()); err != nil {
		t.Fatal(err)
	}
	op := f.confirmProjectDelete()
	late := f.world.Delay(runtimedriver.EffectDelete)
	f.l.processProjectDelete(op)
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || f.runtime().State != runtimedriver.StateStopped {
		t.Fatalf("project deletion with an unanswered delete: %s", blocked.Status)
	}
	// A retry settles the delete again; had it sent another, the runtime
	// would be gone before the first one lands.
	f.l.processProjectDelete(f.retry(op.ID))
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || f.runtime().State != runtimedriver.StateStopped {
		t.Fatalf("retried project deletion with the delete still unanswered: %s, runtime %s", blocked.Status, f.runtime().State)
	}
	late.Land()
	f.l.processProjectDelete(f.retry(op.ID))
	if done := f.operation(op.ID); done.Status != "completed" {
		t.Fatalf("project deletion after the delete landed ended %s at %s: %s", done.Status, done.Phase, done.Diagnostic)
	}
	if f.runtime().State != runtimedriver.StateAbsent {
		t.Fatal("project deletion did not delete the runtime")
	}
}

func TestProjectDeletionCompletesWhenTheDriverSettlesALostDeleteReplyApplied(t *testing.T) {
	f := newSessionFixture(t)
	if _, err := f.driver.Stop(f.ctx, f.spec()); err != nil {
		t.Fatal(err)
	}
	op := f.confirmProjectDelete()
	f.driver.beforeSettle = f.world.Delay(runtimedriver.EffectDelete).Land
	f.l.processProjectDelete(op)
	if done := f.operation(op.ID); done.Status != "completed" {
		t.Fatalf("project deletion ended %s at %s: %s", done.Status, done.Phase, done.Diagnostic)
	}
	if f.runtime().State != runtimedriver.StateAbsent {
		t.Fatal("project deletion did not delete the runtime")
	}
}

func TestProjectDeletionSettlesAnIssuedDeleteEvenWhenTheRuntimeIsGone(t *testing.T) {
	f := newSessionFixture(t)
	if _, err := f.driver.Stop(f.ctx, f.spec()); err != nil {
		t.Fatal(err)
	}
	op := f.confirmProjectDelete()
	late := f.world.Delay(runtimedriver.EffectDelete)
	f.l.processProjectDelete(op)
	if blocked := f.operation(op.ID); blocked.Status != "blocked" {
		t.Fatalf("project deletion with an unanswered delete: %s", blocked.Status)
	}
	// The delete lands while the backend's operation still runs: the runtime
	// is gone, and the driver cannot settle the delete yet.
	release := f.world.Hold(f.runtimeRef())
	late.Land()
	f.l.processProjectDelete(f.retry(op.ID))
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || f.runtime().State != runtimedriver.StateAbsent {
		t.Fatalf("project deletion with the runtime gone and its delete unsettled: %s, runtime %s", blocked.Status, f.runtime().State)
	}
	release()
	f.l.processProjectDelete(f.retry(op.ID))
	if done := f.operation(op.ID); done.Status != "completed" {
		t.Fatalf("project deletion once its delete settled ended %s at %s: %s", done.Status, done.Phase, done.Diagnostic)
	}
}

// confirmCleanup plans the cleanup of the session's failed creation with a
// dry run and runs it with the plan's change tag.
func (f *sessionFixture) confirmCleanup() api.Operation {
	f.t.Helper()
	dry, err := f.l.CleanupCreation(f.ctx, f.session.UUID, api.ChangeRequest{})
	if err != nil || dry.ChangeTag == "" {
		f.t.Fatalf("cleanup plan: %+v %v", dry.Plan, err)
	}
	run, err := f.l.CleanupCreation(f.ctx, f.session.UUID, api.ChangeRequest{Key: "cleanup", IfMatch: dry.ChangeTag})
	if err != nil {
		f.t.Fatal(err)
	}
	return *run.Operation
}

// confirmProjectDelete plans the deletion of project app with a dry run and
// runs it with the plan's change tag.
func (f *sessionFixture) confirmProjectDelete() api.Operation {
	f.t.Helper()
	dry, err := f.l.DeleteProject(f.ctx, "app", api.ChangeRequest{})
	if err != nil || dry.ChangeTag == "" {
		f.t.Fatalf("project deletion plan: %+v %v", dry.Plan, err)
	}
	run, err := f.l.DeleteProject(f.ctx, "app", api.ChangeRequest{Key: "delete-app", IfMatch: dry.ChangeTag})
	if err != nil {
		f.t.Fatal(err)
	}
	return *run.Operation
}

func TestAssembledCleanupWithAnUnknownDeleteOutcomeStaysBlockedAndNeverDeletesAgain(t *testing.T) {
	f := newFailedAssemblyFixture(t)
	op := f.confirmCleanup()
	late := f.world.Delay(runtimedriver.EffectDelete)
	f.l.processCreateCleanup(op)
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != "delete-issued" || f.runtime().State != runtimedriver.StateStopped {
		t.Fatalf("cleanup with an unanswered delete: %s at %s", blocked.Status, blocked.Phase)
	}
	// A retry settles the delete again; had it sent another, the runtime
	// would be gone before the first one lands.
	f.l.processCreateCleanup(f.retry(op.ID))
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != "delete-issued" || f.runtime().State != runtimedriver.StateStopped {
		t.Fatalf("retried cleanup with the delete still unanswered: %s at %s", blocked.Status, blocked.Phase)
	}
	late.Land()
	f.l.processCreateCleanup(f.retry(op.ID))
	if done := f.operation(op.ID); done.Status != "completed" {
		t.Fatalf("cleanup after the delete landed ended %s at %s: %s", done.Status, done.Phase, done.Diagnostic)
	}
	if f.runtime().State != runtimedriver.StateAbsent || !f.branchExists() {
		t.Fatal("cleanup did not delete the runtime and keep the branch")
	}
}

func TestAssembledCleanupCompletesWhenTheDriverSettlesALostDeleteReplyApplied(t *testing.T) {
	f := newFailedAssemblyFixture(t)
	op := f.confirmCleanup()
	f.driver.beforeSettle = f.world.Delay(runtimedriver.EffectDelete).Land
	f.l.processCreateCleanup(op)
	if done := f.operation(op.ID); done.Status != "completed" {
		t.Fatalf("cleanup ended %s at %s: %s", done.Status, done.Phase, done.Diagnostic)
	}
	if f.runtime().State != runtimedriver.StateAbsent {
		t.Fatal("cleanup did not delete the runtime")
	}
}
