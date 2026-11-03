package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimedriver/memory"
)

// testGitCapability is P's Git service as session specs name it.
func testGitCapability() *gitCapability {
	return &gitCapability{info: &api.GitInfo{HostPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFixtureFixtureFixtureFixtureFixtureFixture p"}}
}

// testSessionKey is the Git key a creation gives the driver.
var testSessionKey = []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nfixture\n-----END OPENSSH PRIVATE KEY-----\n")

// driverFixture is a lifecycle over a real store and the in-memory runtime
// driver, with one session of project app on branch work, created from the
// driver's base image and still creating.
type driverFixture struct {
	t       *testing.T
	ctx     context.Context
	l       *lifecycle
	world   *memory.World
	session control.Session
	opID    string
}

func newDriverFixture(t *testing.T, capabilities runtimedriver.Capabilities) *driverFixture {
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
	world := memory.NewWorld(memory.Config{Capabilities: capabilities})
	selection := control.CreationSelection{HostID: "host", HostSHA256: strings.Repeat("b", 64), SourceID: "source", SourceSHA256: strings.Repeat("c", 64)}
	op, session, err := store.BeginSessionCreate(ctx, control.ReserveSessionRequest{Key: "driver", Project: "app", Branch: "work", Choice: "existing"},
		string(world.BaseImage()), selection, func(context.Context, control.ReserveSessionRequest) (string, bool, error) {
			return strings.Repeat("e", 40), true, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	l := &lifecycle{ctx: ctx, store: store, driver: memory.NewDriver(world), git: testGitCapability(),
		cfg:     control.RuntimeConfig{EndpointPrefix: "/run/p-endpoints", ProjectPolicies: map[string]control.ProjectPolicy{"app": policy}},
		working: map[string]bool{}, queueSlots: make(chan struct{}, 8), sessionLocks: map[string]chan struct{}{}}
	return &driverFixture{t: t, ctx: ctx, l: l, world: world, session: session, opID: op.ID}
}

func (f *driverFixture) ref() runtimedriver.Ref {
	return runtimedriver.Ref{Kind: runtimedriver.KindSession, ID: f.session.UUID}
}

// spec is the session's spec as its creation gives it to the driver.
func (f *driverFixture) spec() runtimedriver.Session {
	f.t.Helper()
	spec, err := f.l.sessionSpec(f.ctx, f.session)
	if err != nil {
		f.t.Fatal(err)
	}
	spec.GitKey = testSessionKey
	return spec
}

func (f *driverFixture) operation() api.Operation {
	f.t.Helper()
	op, err := f.l.store.GetOperation(f.ctx, f.opID)
	if err != nil {
		f.t.Fatal(err)
	}
	return op
}

// atPrincipalsReady moves the creation to principals-ready with no runtime
// init attempted, where processCreation creates the runtime.
func (f *driverFixture) atPrincipalsReady() api.Operation {
	f.t.Helper()
	ev, err := control.Evidence(f.operation())
	if err != nil {
		f.t.Fatal(err)
	}
	ev.RuntimeInitState = "not-attempted"
	raw, err := json.Marshal(ev)
	if err != nil {
		f.t.Fatal(err)
	}
	if err = f.l.store.AdvanceOperation(f.ctx, f.opID, "running", "principals-ready", true, raw, "", ""); err != nil {
		f.t.Fatal(err)
	}
	return f.operation()
}

func (f *driverFixture) initState() string {
	f.t.Helper()
	ev, err := control.Evidence(f.operation())
	if err != nil {
		f.t.Fatal(err)
	}
	return ev.RuntimeInitState
}

// createRuntime creates the session's runtime directly, leaving it stopped.
func (f *driverFixture) createRuntime() {
	f.t.Helper()
	if _, err := memory.NewDriver(f.world).Create(f.ctx, f.spec(), "", func() error { return nil }); err != nil {
		f.t.Fatal(err)
	}
}

// establish ends the session's creation, as processCreation does once its
// host is ready.
func (f *driverFixture) establish() {
	f.t.Helper()
	if err := f.l.store.CompleteCreation(f.ctx, f.opID); err != nil {
		f.t.Fatal(err)
	}
	session, err := f.l.store.GetSession(f.ctx, f.session.UUID)
	if err != nil {
		f.t.Fatal(err)
	}
	f.session = session
}

// interruptCreate leaves the runtime a Create left when its driver died
// right after the init: stopped, with nothing installed.
func (f *driverFixture) interruptCreate() {
	f.t.Helper()
	dying := memory.NewDriver(f.world)
	reached := f.world.DieInCreate(memory.AfterInit)
	go func() { _, _ = dying.Create(f.ctx, f.spec(), "", func() error { return nil }) }()
	<-reached
	dying.Kill()
}

func (f *driverFixture) observe() runtimedriver.Observation {
	f.t.Helper()
	o, err := memory.NewDriver(f.world).Inspect(f.ctx, f.spec())
	if err != nil {
		f.t.Fatal(err)
	}
	return o
}

// gateProbe calls check right after the driver's create gate passed, before
// the driver's first effect.
type gateProbe struct {
	*memory.Driver
	check func()
}

func (d gateProbe) Create(ctx context.Context, s runtimedriver.Session, at runtimedriver.Incarnation, g runtimedriver.Gate) (runtimedriver.Observation, error) {
	if g != nil {
		gate := g
		g = func() error {
			err := gate()
			if err == nil {
				d.check()
			}
			return err
		}
	}
	return d.Driver.Create(ctx, s, at, g)
}

func TestCreationRecordsItsInitAttemptBeforeTheRuntimeIsInitialized(t *testing.T) {
	f := newDriverFixture(t, runtimedriver.Capabilities{})
	op := f.atPrincipalsReady()
	gated := false
	f.l.driver = gateProbe{Driver: memory.NewDriver(f.world), check: func() {
		gated = true
		if state := f.initState(); state != "attempted" || f.world.Initializations(f.ref()) != 0 {
			t.Errorf("at the gate: init state %q, %d initializations", state, f.world.Initializations(f.ref()))
		}
	}}
	if err := f.l.createRuntime(f.ctx, &op, f.spec()); err != nil {
		t.Fatal(err)
	}
	if !gated || f.world.Initializations(f.ref()) != 1 {
		t.Fatalf("gate called %t, %d initializations", gated, f.world.Initializations(f.ref()))
	}
	if stored := f.operation(); op.Phase != "assembly-ready" || stored.Phase != "assembly-ready" {
		t.Fatalf("creation at %s, stored at %s; want assembly-ready", op.Phase, stored.Phase)
	}
	if o := f.observe(); o.State != runtimedriver.StateRunning || !o.Complete || !o.Host.Ready {
		t.Fatalf("created runtime %+v, want running, complete and ready", o)
	}
}

func TestCreationWithAnUnknownInitOutcomeNeverInitializesAgain(t *testing.T) {
	f := newDriverFixture(t, runtimedriver.Capabilities{})
	op := f.atPrincipalsReady()
	late := f.world.Delay(runtimedriver.EffectCreate)
	if err := f.l.createRuntime(f.ctx, &op, f.spec()); !errors.Is(err, runtimedriver.ErrUnknownOutcome) {
		t.Fatalf("unanswered init: %v, want an unknown outcome", err)
	}
	if state, phase := f.initState(), f.operation().Phase; state != "attempted" || phase != "principals-ready" {
		t.Fatalf("after an unknown init: init state %q at %s", state, phase)
	}
	// A retry finds the runtime absent; the recorded attempt refuses another init.
	op = f.operation()
	if err := f.l.createRuntime(f.ctx, &op, f.spec()); err == nil || !strings.Contains(err.Error(), "absent runtime cannot authorize another init") {
		t.Fatalf("retry after an unknown init: %v", err)
	}
	if n := f.world.Initializations(f.ref()); n != 0 {
		t.Fatalf("a retry initialized %d runtimes", n)
	}
	// The init lands late: the retry completes that runtime.
	late.Land()
	op = f.operation()
	if err := f.l.createRuntime(f.ctx, &op, f.spec()); err != nil {
		t.Fatal(err)
	}
	if n := f.world.Initializations(f.ref()); n != 1 {
		t.Fatalf("%d initializations, want the late one only", n)
	}
	if o := f.observe(); o.State != runtimedriver.StateRunning || !o.Host.Ready {
		t.Fatalf("late runtime %+v, want running and ready", o)
	}
}

func TestCreationCompletesAnInterruptedRuntimeAtItsIncarnation(t *testing.T) {
	f := newDriverFixture(t, runtimedriver.Capabilities{})
	op := f.atPrincipalsReady()
	dying := memory.NewDriver(f.world)
	f.l.driver = dying
	reached := f.world.DieInCreate(memory.AfterInit)
	failed := make(chan error, 1)
	go func() { failed <- f.l.createRuntime(f.ctx, &op, f.spec()) }()
	<-reached
	dying.Kill()
	if err := <-failed; !errors.Is(err, runtimedriver.ErrUnknownOutcome) {
		t.Fatalf("driver death in Create: %v", err)
	}
	interrupted := f.observe()
	if interrupted.State != runtimedriver.StateStopped || interrupted.Complete {
		t.Fatalf("interrupted runtime %+v, want stopped and incomplete", interrupted)
	}
	f.l.driver = memory.NewDriver(f.world)
	op = f.operation()
	if err := f.l.createRuntime(f.ctx, &op, f.spec()); err != nil {
		t.Fatal(err)
	}
	o := f.observe()
	if f.world.Initializations(f.ref()) != 1 || o.Incarnation != interrupted.Incarnation || o.State != runtimedriver.StateRunning || !o.Complete {
		t.Fatalf("completed runtime %+v after %d initializations, want incarnation %s running and complete", o, f.world.Initializations(f.ref()), interrupted.Incarnation)
	}
}

func TestCreationHostFailureReturnsTheHostsDiagnostic(t *testing.T) {
	f := newDriverFixture(t, runtimedriver.Capabilities{})
	op := f.atPrincipalsReady()
	f.createRuntime()
	f.world.FailHostOnNextStart(f.ref(), "p-interactive.service: exit status 1")
	err := f.l.createRuntime(f.ctx, &op, f.spec())
	var host *runtimedriver.HostError
	if !errors.As(err, &host) || err.Error() != "p-interactive.service: exit status 1" {
		t.Fatalf("host failure: %v, want the host's diagnostic", err)
	}
	if o := f.observe(); o.State != runtimedriver.StateStopped || op.Phase != "assembly-ready" {
		t.Fatalf("after a host failure: runtime %s, creation at %s", o.State, op.Phase)
	}
}

func TestCreationRefusesARuntimeRunningBeforeItsAssembly(t *testing.T) {
	f := newDriverFixture(t, runtimedriver.Capabilities{})
	op := f.atPrincipalsReady()
	f.createRuntime()
	if _, err := memory.NewDriver(f.world).Start(f.ctx, f.spec()); err != nil {
		t.Fatal(err)
	}
	transitions := f.world.Transitions(f.ref())
	if err := f.l.createRuntime(f.ctx, &op, f.spec()); err == nil || !strings.Contains(err.Error(), "running before verified assembly") {
		t.Fatalf("running runtime before assembly: %v", err)
	}
	if f.world.Transitions(f.ref()) != transitions || f.operation().Phase != "principals-ready" {
		t.Fatal("a runtime running before its assembly was acted on")
	}
}

// unknownHost is a driver whose session hosts cannot be read.
type unknownHost struct{ *memory.Driver }

func (d unknownHost) Inspect(ctx context.Context, s runtimedriver.Session) (runtimedriver.Observation, error) {
	o, err := d.Driver.Inspect(ctx, s)
	o.Host = runtimedriver.HostState{Unknown: true}
	return o, err
}

// endSessionChange records a start or stop, kind, of the session that ended
// with status and diagnostic, as its worker records one.
func (f *driverFixture) endSessionChange(kind, status, diagnostic string) {
	f.t.Helper()
	op, _, err := f.l.store.BeginSessionChange(f.ctx, kind, f.session.UUID)
	if err != nil {
		f.t.Fatal(err)
	}
	reason := ""
	if status == "failed" {
		reason = api.ReasonRuntime
	}
	if err = f.l.store.AdvanceOperation(f.ctx, op.ID, status, op.Phase, false, nil, reason, diagnostic); err != nil {
		f.t.Fatal(err)
	}
}

func TestInspectSessionConditionComesFromTheDriver(t *testing.T) {
	f := newDriverFixture(t, runtimedriver.Capabilities{})
	f.createRuntime()
	f.establish()
	condition := func() api.SessionView {
		t.Helper()
		v, err := f.l.InspectSession(f.ctx, f.session.UUID)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := condition(); v.Condition != "stopped" {
		t.Fatalf("stopped runtime: %+v", v)
	}
	f.endSessionChange(control.KindSessionStart, "failed", "the last start failed")
	if v := condition(); v.Condition != "stopped" || v.Diagnostic != "the last start failed" {
		t.Fatalf("stopped after a failed start: %+v", v)
	}
	if _, err := memory.NewDriver(f.world).Start(f.ctx, f.spec()); err != nil {
		t.Fatal(err)
	}
	if v := condition(); v.Condition != "ready" {
		t.Fatalf("ready host: %+v", v)
	}
	f.world.UnreadyHost(f.ref(), "the host restarts")
	if v := condition(); v.Condition != "starting" || v.Diagnostic != "the last start failed" {
		t.Fatalf("host not ready: %+v, want starting with the failed start's diagnostic", v)
	}
	f.endSessionChange(control.KindSessionStart, "completed", "")
	if v := condition(); v.Condition != "starting" || v.Diagnostic != "the host restarts" {
		t.Fatalf("host not ready: %+v, want starting with the host's diagnostic", v)
	}
	ready := f.l.driver
	f.l.driver = unknownHost{memory.NewDriver(f.world)}
	if v := condition(); v.Condition != "unreachable" {
		t.Fatalf("unreadable host: %+v", v)
	}
	f.l.driver = ready
	f.world.Drift(f.ref(), memory.DriftImage)
	if v := condition(); v.Condition != "unreachable" {
		t.Fatalf("runtime that differs from its spec: %+v", v)
	}
}

func TestInspectSessionOfAMissingOrIncompleteRuntime(t *testing.T) {
	f := newDriverFixture(t, runtimedriver.Capabilities{})
	f.establish()
	if v, err := f.l.InspectSession(f.ctx, f.session.UUID); err != nil || v.Condition != "missing" {
		t.Fatalf("absent runtime: %+v %v", v, err)
	}
	f.interruptCreate()
	if v, err := f.l.InspectSession(f.ctx, f.session.UUID); err != nil || v.Condition != "unreachable" {
		t.Fatalf("runtime missing its devices and guest files: %+v %v", v, err)
	}
}

// startFixture is a session with a registered Git principal and endpoints,
// which Start requires, and no runtime yet.
func startFixture(t *testing.T) *driverFixture {
	t.Helper()
	f := newDriverFixture(t, runtimedriver.Capabilities{})
	prefix, err := os.MkdirTemp("/tmp", "p-drv-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(prefix) })
	endpoints, err := newEndpointManager(prefix, "127.0.0.1:1", f.l.store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(endpoints.Close)
	f.l.endpoints, f.l.cfg.EndpointPrefix = endpoints, prefix
	_, pub, err := f.l.sessionKey(f.ctx, f.session.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.l.store.EnsureSessionGitPrincipal(f.ctx, gitservice.Fingerprint(pub), "app", f.session.UUID); err != nil {
		t.Fatal(err)
	}
	return f
}

// awaitChange waits for the start or stop result recorded to end and returns
// the session then.
func (f *driverFixture) awaitChange(result api.SessionChangeResult) api.SessionView {
	f.t.Helper()
	if result.Operation == nil {
		f.t.Fatalf("no operation recorded: %+v", result)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		op, err := f.l.store.GetOperation(f.ctx, result.Operation.ID)
		if err != nil {
			f.t.Fatal(err)
		}
		if op.Status != "running" {
			break
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("%s did not end", op.Kind)
		}
	}
	v, err := f.l.InspectSession(f.ctx, f.session.UUID)
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}

func TestStartWaitsForTheHostAndStopStopsTheRuntime(t *testing.T) {
	f := startFixture(t)
	f.createRuntime()
	f.establish()
	result, err := f.l.StartSession(f.ctx, f.session.UUID)
	if err != nil || !result.Changed {
		t.Fatalf("Start: %+v %v", result, err)
	}
	if v := f.awaitChange(result); v.Condition != "ready" || f.world.Transitions(f.ref()) != 1 {
		t.Fatalf("after Start: %+v, %d transitions", v, f.world.Transitions(f.ref()))
	}
	if result, err = f.l.StartSession(f.ctx, f.session.UUID); err != nil || result.Changed || result.Session == nil || result.Session.Condition != "ready" || f.world.Transitions(f.ref()) != 1 {
		t.Fatalf("Start of a ready session: %+v %v", result, err)
	}
	if result, err = f.l.StopSession(f.ctx, f.session.UUID); err != nil || !result.Changed {
		t.Fatalf("Stop: %+v %v", result, err)
	}
	if v := f.awaitChange(result); v.Condition != "stopped" {
		t.Fatalf("after Stop: %+v", v)
	}
}

func TestStartKeepsTheHostsDiagnosticWhenTheHostFails(t *testing.T) {
	f := startFixture(t)
	f.createRuntime()
	f.establish()
	f.world.FailHostOnNextStart(f.ref(), "p-interactive.service: exit status 1")
	result, err := f.l.StartSession(f.ctx, f.session.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if v := f.awaitChange(result); v.Condition != "stopped" || v.Diagnostic != "p-interactive.service: exit status 1" {
		t.Fatalf("after a host failure: %+v", v)
	}
	if op, _, err := f.l.store.LatestSessionChange(f.ctx, f.session.UUID); err != nil || op.Status != "failed" || op.Reason != api.ReasonRuntime {
		t.Fatalf("failed start recorded %+v %v, want failed with reason runtime", op, err)
	}
}

func TestStartAndStopRefuseAMissingOrIncompleteRuntime(t *testing.T) {
	f := startFixture(t)
	f.establish()
	_, err := f.l.StartSession(f.ctx, f.session.UUID)
	requireRefusal(t, err, api.KindConflict, api.ReasonRuntimeMissing, api.TargetSession(f.session.UUID))
	f.interruptCreate()
	_, err = f.l.StartSession(f.ctx, f.session.UUID)
	requireRefusal(t, err, api.KindFailed, api.ReasonRuntime, "")
	_, err = f.l.StopSession(f.ctx, f.session.UUID)
	requireRefusal(t, err, api.KindFailed, api.ReasonRuntime, "")
	if _, recorded, e := f.l.store.LatestSessionChange(f.ctx, f.session.UUID); e != nil || recorded || f.world.Transitions(f.ref()) != 0 {
		t.Fatalf("Start or Stop of an incomplete runtime recorded a change (%t) or started it: %v", recorded, e)
	}
}

// A session whose host ended on its own shows the host's last words, read
// apart from the runtime's state.
func TestAStoppedSessionShowsItsHostsLastWords(t *testing.T) {
	f := newDriverFixture(t, runtimedriver.Capabilities{})
	f.createRuntime()
	f.establish()
	f.world.FailHostOnNextStart(f.ref(), "the interactive command exited")
	var failed *runtimedriver.HostError
	if _, err := memory.NewDriver(f.world).Start(f.ctx, f.spec()); !errors.As(err, &failed) {
		t.Fatalf("host failure: %v", err)
	}
	observed := f.observe()
	if observed.State != runtimedriver.StateStopped || observed.Host != (runtimedriver.HostState{}) {
		t.Fatalf("a stopped runtime's observation carries a host: %+v", observed)
	}
	v, err := f.l.InspectSession(f.ctx, f.session.UUID)
	if err != nil || v.Condition != "stopped" || !strings.Contains(v.Diagnostic, "the interactive command exited") {
		t.Fatalf("stopped session: %+v %v", v, err)
	}
}
