package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver/memory"
	"github.com/lgvo/p.ai/internal/sourcedriver"
	sourcememory "github.com/lgvo/p.ai/internal/sourcedriver/memory"
)

// originFixture is a lifecycle over a real store, the in-memory runtime
// driver and the in-memory source driver, whose host configuration has a
// policy for projects app and team/new. Project app exists, with its
// repository and its origin at url, whose main is the commit main. Its
// workers never start: a test runs an operation's work itself.
type originFixture struct {
	ctx    context.Context
	l      *lifecycle
	store  *control.Store
	world  *sourcememory.World
	driver *sourcememory.Driver
	url    string
	main   sourcedriver.OID
}

func newOriginFixture(t *testing.T) *originFixture {
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
	world, driver := memorySource(t)
	if _, err = driver.CreateRepository(ctx, "app"); err != nil {
		t.Fatal(err)
	}
	f := &originFixture{ctx: ctx, store: store, world: world, driver: driver, url: world.NewOrigin()}
	f.main = world.OriginCommit(f.url)
	world.SetOriginRef(f.url, "refs/heads/main", f.main)
	change, err := store.BeginOriginChange(ctx, control.OriginChange{Key: "origin", Project: "app", Kind: "set", ProposedURL: f.url})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := driver.ObserveOrigin(ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CompleteOriginChange(ctx, change, refs); err != nil {
		t.Fatal(err)
	}
	runtime := memory.NewWorld(memory.Config{Capacity: 8})
	f.l = &lifecycle{ctx: ctx, store: store, driver: memory.NewDriver(runtime), git: &gitCapability{driver: driver},
		cfg: control.RuntimeConfig{BaseImageFingerprint: string(runtime.BaseImage()),
			ProjectPolicies: map[string]control.ProjectPolicy{"app": policy, "team/new": policy}},
		// No worker starts: a queued operation stays pending.
		working: map[string]bool{}, queueSlots: make(chan struct{}), sessionLocks: map[string]chan struct{}{}}
	return f
}

func (f *originFixture) operation(t *testing.T, id string) api.Operation {
	t.Helper()
	op, err := f.store.GetOperation(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

// A new project with an origin URL the source driver refuses answers invalid
// naming url, before anything is recorded.
func TestOriginProjectCreationRefusesAURLTheSourceDriverRefuses(t *testing.T) {
	f := newOriginFixture(t)
	_, err := f.l.CreateProject(f.ctx, control.BlankProjectRequest{Key: "create", Project: "team/new", URL: "https://host/repo.git"})
	var refusal *control.Refusal
	if !errors.As(err, &refusal) || refusal.Kind != api.KindInvalid || refusal.Field != "url" {
		t.Fatalf("a URL the driver refuses answered %v, want invalid url", err)
	}
	if _, err = f.store.GetOperationByKey(f.ctx, "create"); !errors.Is(err, control.ErrNotFound) {
		t.Fatalf("a refused URL recorded its key: %v", err)
	}
}

// A recorded project creation is answered from SQLite alone: its operation
// comes back while the source driver is down.
func TestOriginProjectCreationReplaysARecordedRequestWithoutTheSourceDriver(t *testing.T) {
	f := newOriginFixture(t)
	req := control.BlankProjectRequest{Key: "create", Project: "team/new", URL: f.url}
	first, err := f.l.CreateProject(f.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	f.driver.Kill()
	if replay, err := f.l.CreateProject(f.ctx, req); err != nil || replay.ID != first.ID {
		t.Fatalf("replay with the source driver down: %+v %v, want %s", replay, err, first.ID)
	}
}

// A project created from an origin that answers gets its repository and the
// origin's observation together; one whose origin cannot be reached blocks
// with reason origin and creates nothing.
func TestOriginProjectCreationObservesTheOriginBeforeCreatingTheRepository(t *testing.T) {
	f := newOriginFixture(t)
	op, err := f.l.CreateProject(f.ctx, control.BlankProjectRequest{Key: "create", Project: "team/new", URL: f.url})
	if err != nil {
		t.Fatal(err)
	}
	f.l.processCreation(op)
	if done := f.operation(t, op.ID); done.Status != "completed" {
		t.Fatalf("creation from a reachable origin ended %s %s (%s)", done.Status, done.Reason, done.Diagnostic)
	}
	state, refs, err := f.store.Origin(f.ctx, "team/new")
	want := []api.OriginRef{{Ref: "refs/heads/main", OID: string(f.main), CommitOID: string(f.main)}}
	if err != nil || state.URL != f.url || state.Status != "fresh" || len(refs) != 1 || refs[0] != want[0] {
		t.Fatalf("origin of the new project: %+v %+v %v, want %+v", state, refs, err, want)
	}
	if repo, err := f.driver.InspectRepository(f.ctx, "team/new"); err != nil || !repo.Exists {
		t.Fatalf("repository of the new project: %+v %v", repo, err)
	}

	unreachable := f.world.NewOrigin()
	f.world.CutOffOrigin(unreachable)
	f.l.cfg.ProjectPolicies["team/other"] = f.l.cfg.ProjectPolicies["app"]
	op, err = f.l.CreateProject(f.ctx, control.BlankProjectRequest{Key: "unreachable", Project: "team/other", URL: unreachable})
	if err != nil {
		t.Fatal(err)
	}
	f.l.processCreation(op)
	if blocked := f.operation(t, op.ID); blocked.Status != "blocked" || blocked.Reason != api.ReasonOrigin {
		t.Fatalf("creation from an unreachable origin ended %s %s, want blocked origin", blocked.Status, blocked.Reason)
	}
	if repo, err := f.driver.InspectRepository(f.ctx, "team/other"); err != nil || repo.Exists {
		t.Fatalf("an unreachable origin's project got a repository: %+v %v", repo, err)
	}
	if active, err := f.store.HasActiveProject(f.ctx, "team/other"); err != nil || active {
		t.Fatalf("an unreachable origin's project is active: %v %v", active, err)
	}
}

// A session created from an origin ref at the commit the client observed
// captures that commit, which the fetch brings into P's repository, for a
// branch and for an annotated tag that peels to it.
func TestSessionCreationFromAnOriginRefCapturesTheFetchedCommit(t *testing.T) {
	f := newOriginFixture(t)
	f.world.OriginAnnotatedTag(f.url, "v1", f.main)
	for _, ref := range []string{"refs/heads/main", "refs/tags/v1"} {
		key := strings.ReplaceAll(ref, "/", "-")
		op, err := f.l.CreateSession(f.ctx, control.ReserveSessionRequest{Key: key, Project: "app", Branch: key, Choice: "new",
			OriginRef: ref, ExpectedCommitOID: string(f.main), ExpectedOriginURL: f.url})
		if err != nil {
			t.Fatalf("creation from %s: %v", ref, err)
		}
		ev, err := control.Evidence(op)
		if err != nil || ev.CapturedOID != string(f.main) || ev.OriginURL != f.url || ev.OriginRef != ref {
			t.Fatalf("creation from %s captured %+v %v, want %s", ref, ev, err, f.main)
		}
	}
	if commit, err := f.driver.InspectCommit(f.ctx, "app", f.main); err != nil || !commit.Present {
		t.Fatalf("the captured commit is not in P's repository: %+v %v", commit, err)
	}
}

// A creation from an origin ref the origin no longer advertises at the
// commit the client observed answers precondition_failed, records nothing,
// and keeps the observation it made.
func TestSessionCreationFromAMovedOriginRefIsRefused(t *testing.T) {
	f := newOriginFixture(t)
	moved := f.world.OriginCommit(f.url, f.main)
	f.world.SetOriginRef(f.url, "refs/heads/main", moved)
	for _, ref := range []string{"refs/heads/main", "refs/heads/gone"} {
		key := strings.ReplaceAll(ref, "/", "-")
		_, err := f.l.CreateSession(f.ctx, control.ReserveSessionRequest{Key: key, Project: "app", Branch: "work", Choice: "new",
			OriginRef: ref, ExpectedCommitOID: string(f.main)})
		wantRefusal(t, "creation from "+ref, err, api.KindPreconditionFailed, api.ReasonChanged, "")
		if _, err = f.store.GetOperationByKey(f.ctx, key); !errors.Is(err, control.ErrNotFound) {
			t.Fatalf("a refused creation from %s recorded its key: %v", ref, err)
		}
	}
	if _, refs, err := f.store.Origin(f.ctx, "app"); err != nil || len(refs) != 1 || refs[0].OID != string(moved) {
		t.Fatalf("the creation's observation was not recorded: %+v %v", refs, err)
	}
}

// A creation whose origin cannot be observed, or whose fetch fails, answers
// failed with reason origin, records nothing, and marks the origin's
// observation unknown.
func TestSessionCreationFromAnOriginThatFailsIsRefused(t *testing.T) {
	for _, test := range []struct {
		name    string
		disturb func(f *originFixture)
	}{
		{"an unreachable origin", func(f *originFixture) { f.world.CutOffOrigin(f.url) }},
		// The ref moves after the observation: the fetch finds it moved.
		{"a failing fetch", func(f *originFixture) {
			f.l.git.driver = &movingOrigin{Driver: f.driver, world: f.world, ref: "refs/heads/main", to: f.world.OriginCommit(f.url, f.main)}
		}},
	} {
		f := newOriginFixture(t)
		test.disturb(f)
		_, err := f.l.CreateSession(f.ctx, control.ReserveSessionRequest{Key: "work", Project: "app", Branch: "work", Choice: "new",
			OriginRef: "refs/heads/main", ExpectedCommitOID: string(f.main)})
		wantRefusal(t, test.name, err, api.KindFailed, api.ReasonOrigin, "")
		if _, err = f.store.GetOperationByKey(f.ctx, "work"); !errors.Is(err, control.ErrNotFound) {
			t.Fatalf("%s: the creation recorded its key: %v", test.name, err)
		}
		if state, _, err := f.store.Origin(f.ctx, "app"); err != nil || state.Status != "unknown" {
			t.Fatalf("%s: the origin's state is %+v %v, want unknown", test.name, state, err)
		}
	}
}

// movingOrigin is the in-memory source driver over an origin whose ref
// moves to commit to right after each observation.
type movingOrigin struct {
	*sourcememory.Driver
	world *sourcememory.World
	ref   string
	to    sourcedriver.OID
}

func (d *movingOrigin) ObserveOrigin(ctx context.Context, url string) ([]sourcedriver.OriginRef, error) {
	refs, err := d.Driver.ObserveOrigin(ctx, url)
	d.world.SetOriginRef(url, d.ref, d.to)
	return refs, err
}

// boundedCalls is the in-memory source driver, recording for each call to an
// origin how long its context let it run, or that it had no deadline.
type boundedCalls struct {
	*sourcememory.Driver
	calls map[string]time.Duration // by call; -1 without a deadline
}

func (d *boundedCalls) record(call string, ctx context.Context) {
	if deadline, ok := ctx.Deadline(); ok {
		d.calls[call] = time.Until(deadline)
		return
	}
	d.calls[call] = -1
}

func (d *boundedCalls) ObserveOrigin(ctx context.Context, url string) ([]sourcedriver.OriginRef, error) {
	d.record("observe", ctx)
	return d.Driver.ObserveOrigin(ctx, url)
}

func (d *boundedCalls) FetchOrigin(ctx context.Context, project, url string, ref sourcedriver.OriginRef) (sourcedriver.OID, error) {
	d.record("fetch", ctx)
	return d.Driver.FetchOrigin(ctx, project, url, ref)
}

// A creation's calls to an origin each end within 35 seconds, so a stalled
// origin cannot hold the creation, its origin lock or the Git authority
// without end: a session's creation from an origin ref, and a project's
// creation from an origin.
func TestOriginCallsOfACreationAreBounded(t *testing.T) {
	for _, test := range []struct {
		name  string
		calls []string
		run   func(t *testing.T, f *originFixture)
	}{
		{"a session from an origin ref", []string{"observe", "fetch"}, func(t *testing.T, f *originFixture) {
			if _, err := f.l.CreateSession(f.ctx, control.ReserveSessionRequest{Key: "work", Project: "app", Branch: "work", Choice: "new",
				OriginRef: "refs/heads/main", ExpectedCommitOID: string(f.main)}); err != nil {
				t.Fatal(err)
			}
		}},
		{"a project from an origin", []string{"observe"}, func(t *testing.T, f *originFixture) {
			op, err := f.l.CreateProject(f.ctx, control.BlankProjectRequest{Key: "create", Project: "team/new", URL: f.url})
			if err != nil {
				t.Fatal(err)
			}
			f.l.processCreation(op)
		}},
	} {
		f := newOriginFixture(t)
		bounded := &boundedCalls{Driver: f.driver, calls: map[string]time.Duration{}}
		f.l.git.driver = bounded
		test.run(t, f)
		for _, call := range test.calls {
			if left, made := bounded.calls[call]; !made || left < 0 || left > 35*time.Second {
				t.Errorf("%s: its %s call could run %v (made %v), want at most 35s", test.name, call, left, made)
			}
		}
	}
}
