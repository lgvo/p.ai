package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/sourcedriver"
	sourcememory "github.com/lgvo/p.ai/internal/sourcedriver/memory"
)

// originDriver is the in-memory source driver, counting its contacts with
// origins and the pushes it starts. While failure is set, every origin fails
// to answer an observation with it; while prestart is set, a push passes its
// gate and then cannot start. While stall is set, every origin stops
// answering observations, and while stallPush is set, pushes once they
// started: the call waits until its context ends, as the bundled driver's Git
// does, or until unstall is closed.
type originDriver struct {
	*sourcememory.Driver
	world            *sourcememory.World
	mu               sync.Mutex // guards observes, which concurrent calls count
	observes, pushes int
	failure          error
	prestart         error
	stall, stallPush bool
	unstall          chan struct{}
	// observeHook runs as each observation starts; gateHook runs after a
	// push's gate passed, right before the push.
	observeHook, gateHook func()
}

func newOriginDriver() *originDriver {
	w := sourcememory.NewWorld()
	return &originDriver{Driver: sourcememory.NewDriver(w), world: w, unstall: make(chan struct{})}
}

func (d *originDriver) ObserveOrigin(ctx context.Context, url string) ([]sourcedriver.OriginRef, error) {
	d.mu.Lock()
	d.observes++
	d.mu.Unlock()
	if d.observeHook != nil {
		d.observeHook()
	}
	if d.stall {
		return nil, d.stalled(ctx)
	}
	if d.failure != nil {
		return nil, d.failure
	}
	return d.Driver.ObserveOrigin(ctx, url)
}

// stalled waits as an origin that stopped answering, and returns the
// error the call ends with.
func (d *originDriver) stalled(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-d.unstall:
		return errors.New("the origin stopped answering")
	}
}

func (d *originDriver) Publish(ctx context.Context, project string, p sourcedriver.Publication, g sourcedriver.Gate) (sourcedriver.PublishStatus, error) {
	if d.stallPush {
		if err := g(); err != nil {
			return "", err
		}
		d.pushes++
		// A push cut off after it started may have landed.
		return "", fmt.Errorf("%w: %w", d.stalled(ctx), sourcedriver.ErrUnknownOutcome)
	}
	return d.Driver.Publish(ctx, project, p, func() error {
		if err := g(); err != nil {
			return err
		}
		if d.gateHook != nil {
			d.gateHook()
		}
		if d.prestart != nil {
			return d.prestart
		}
		d.pushes++
		return nil
	})
}

// newOrigin makes an origin whose main branch is a commit, and returns its
// URL.
func (d *originDriver) newOrigin() string {
	url := d.world.NewOrigin()
	d.world.SetOriginRef(url, "refs/heads/main", d.world.OriginCommit(url))
	return url
}

// originAPI is the origin API over store s and the in-memory source
// driver.
func originAPI(s *Store) (*OriginAPI, *originDriver) {
	d := newOriginDriver()
	return &OriginAPI{Store: s, Driver: d}, d
}

// runQueued makes a's Queue run each queued origin operation's work at once,
// to its end, as a daemon worker would, through a's Store as it is then.
func runQueued(a *OriginAPI) {
	a.Queue = func(id string) error {
		ctx := context.Background()
		op, err := a.Store.GetOperation(ctx, id)
		if err != nil {
			return err
		}
		switch op.Kind {
		case KindOriginChange:
			a.RunOriginChange(ctx, op)
		case KindOriginPublish:
			a.RunPublication(ctx, op)
		default:
			return fmt.Errorf("queued operation %s of kind %s", id, op.Kind)
		}
		return nil
	}
}

// readBack returns a function that reads back the operation a request of a
// returned, as a's journal reads it now; it fails the test on the request's
// error.
func readBack(t *testing.T, a *OriginAPI) func(api.Operation, error) api.Operation {
	return func(op api.Operation, err error) api.Operation {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		got, err := a.Store.GetOperation(context.Background(), op.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
}

// wantEnded fails unless op ended with status and reason.
func wantEnded(t *testing.T, what string, op api.Operation, status, reason string) {
	t.Helper()
	if op.Status != status || op.Reason != reason {
		t.Fatalf("%s ended %s %q (%s), want %s %q", what, op.Status, op.Reason, op.Diagnostic, status, reason)
	}
}

func TestOriginAssociationReplayFailureRefreshAndRestart(t *testing.T) {
	s, dir := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{"network":"none"}`)); err != nil {
		t.Fatal(err)
	}
	a, f := originAPI(s)
	runQueued(a)
	read := readBack(t, a)
	first := OriginChange{Key: "first", Project: "app", Kind: "set", ProposedURL: f.newOrigin()}
	firstOp := read(a.Change(ctx, first))
	wantEnded(t, "first", firstOp, "completed", "")
	state, _, err := s.Origin(ctx, "app")
	if err != nil || state.Status != "fresh" || state.RefCount != 1 || f.observes != 1 {
		t.Fatalf("first: %+v %v %d", state, err, f.observes)
	}
	f.failure = errors.New("offline")
	second := OriginChange{Key: "second", Project: "app", Kind: "set", ExpectedURL: first.ProposedURL, ProposedURL: f.newOrigin()}
	secondOp := read(a.Change(ctx, second))
	wantEnded(t, "failed contact", secondOp, "failed", api.ReasonOrigin)
	if secondOp.Diagnostic != "offline" {
		t.Fatalf("failed contact's diagnostic %q, want the contact's error", secondOp.Diagnostic)
	}
	state, refs, err := s.Origin(ctx, "app")
	if err != nil || state.URL != first.ProposedURL || state.Status != "fresh" || len(refs) != 1 {
		t.Fatalf("failed set: %+v %+v %v", state, refs, err)
	}
	if _, err = a.Change(ctx, OriginChange{Key: "first", Project: "app", Kind: "remove"}); !errors.Is(err, ErrConflict) || f.observes != 2 {
		t.Fatalf("conflicting key: %v observes=%d", err, f.observes)
	}
	if _, err = a.Refresh(ctx, "app"); err != nil {
		t.Fatal(err)
	}
	state, refs, err = s.Origin(ctx, "app")
	if err != nil || state.Status != "unknown" || len(refs) != 1 || state.ObservedAt == "" {
		t.Fatalf("failed refresh: %+v %+v %v", state, refs, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a.Store = s
	replay := read(a.Change(ctx, first))
	if replay.ID != firstOp.ID || replay.Status != "completed" || f.observes != 3 {
		t.Fatalf("completed replay: %+v observes=%d", replay, f.observes)
	}
	f.failure = nil
	// A failed change ends its key: the same key returns the failed
	// operation and contacts nothing; a new key changes the origin.
	replay = read(a.Change(ctx, second))
	if replay.ID != secondOp.ID || replay.Status != "failed" || f.observes != 3 {
		t.Fatalf("failed replay: %+v observes=%d", replay, f.observes)
	}
	second.Key = "second-again"
	wantEnded(t, "new key", read(a.Change(ctx, second)), "completed", "")
	state, _, err = s.Origin(ctx, "app")
	if err != nil || state.URL != second.ProposedURL || state.Status != "fresh" {
		t.Fatalf("retry: %+v %v", state, err)
	}
	if err = s.StoreOriginRefresh(ctx, "app", first.ProposedURL, nil, "late failure"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale refresh identity: %v", err)
	}
	state, _, err = s.Origin(ctx, "app")
	if err != nil || state.Status != "fresh" || state.URL != second.ProposedURL {
		t.Fatalf("stale refresh changed new origin: %+v %v", state, err)
	}
	if _, err = a.Change(ctx, OriginChange{Key: "stale", Project: "app", Kind: "set", ExpectedURL: first.ProposedURL, ProposedURL: "ssh://host/c"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("CAS: %v", err)
	}
	wantEnded(t, "remove", read(a.Change(ctx, OriginChange{Key: "remove", Project: "app", Kind: "remove", ExpectedURL: second.ProposedURL})), "completed", "")
	state, refs, err = s.Origin(ctx, "app")
	if err != nil || state.Status != "local-only" || len(refs) != 0 {
		t.Fatalf("removed: %+v %+v %v", state, refs, err)
	}
}

// TestOriginChangeReplayReturnsItsOperation checks that a key's identical
// request returns the operation the key recorded, whatever its status, and
// contacts the origin no more; only a running one is queued again.
func TestOriginChangeReplayReturnsItsOperation(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	a, f := originAPI(s)
	var queued []string
	a.Queue = func(id string) error { queued = append(queued, id); return nil }
	read := readBack(t, a)
	set := OriginChange{Key: "set", Project: "app", Kind: "set", ProposedURL: f.newOrigin()}
	running, err := a.Change(ctx, set)
	if err != nil || running.Status != "running" || len(queued) != 1 || queued[0] != running.ID || f.observes != 0 {
		t.Fatalf("accepted: %+v %v queued=%v observes=%d", running, err, queued, f.observes)
	}
	again, err := a.Change(ctx, set)
	if err != nil || again.ID != running.ID || len(queued) != 2 || queued[1] != running.ID || f.observes != 0 {
		t.Fatalf("running replay: %+v %v queued=%v observes=%d", again, err, queued, f.observes)
	}
	_, _, before, err := s.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a.RunOriginChange(ctx, running)
	wantEnded(t, "set", read(running, nil), "completed", "")
	f.failure = errors.New("offline")
	failing := OriginChange{Key: "failing", Project: "app", Kind: "set", ExpectedURL: set.ProposedURL, ProposedURL: f.newOrigin()}
	failed, err := a.Change(ctx, failing)
	if err != nil {
		t.Fatal(err)
	}
	a.RunOriginChange(ctx, failed)
	wantEnded(t, "unreachable set", read(failed, nil), "failed", api.ReasonOrigin)
	f.failure = nil
	observes, queuedBefore := f.observes, len(queued)
	for _, replay := range []struct {
		change OriginChange
		want   api.Operation
	}{{set, running}, {failing, failed}} {
		got, err := a.Change(ctx, replay.change)
		if err != nil || got.ID != replay.want.ID || got.Status == "running" {
			t.Fatalf("replay of %s: %+v %v", replay.change.Key, got, err)
		}
	}
	if f.observes != observes || len(queued) != queuedBefore {
		t.Fatalf("replays contacted the origin (observes %d, was %d) or queued work %v", f.observes, observes, queued)
	}
	if _, _, after, err := s.Count(ctx); err != nil || after != before+1 {
		t.Fatalf("replays recorded operations: %d, want %d (%v)", after, before+1, err)
	}
	state, _, err := s.Origin(ctx, "app")
	if err != nil || state.URL != set.ProposedURL {
		t.Fatalf("replayed failure changed the origin: %+v %v", state, err)
	}
}

// TestOriginChangeRefusesAURLTheSourceDriverRefuses checks that setting an
// origin URL the source driver refuses answers invalid naming url, before
// anything is recorded or contacted.
func TestOriginChangeRefusesAURLTheSourceDriverRefuses(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	a, f := originAPI(s)
	runQueued(a)
	_, err := a.Change(ctx, OriginChange{Key: "set", Project: "app", Kind: "set", ProposedURL: "https://host/repo.git"})
	if answer := answerError(ctx, err); !reflect.DeepEqual(answer, api.InvalidParam("url")) {
		t.Fatalf("a URL the driver refuses answered %v, want %v", answer, api.InvalidParam("url"))
	}
	if _, err = s.GetOperationByKey(ctx, "set"); !errors.Is(err, ErrNotFound) || f.observes != 0 {
		t.Fatalf("a refused URL recorded its key (%v) or contacted %d origins", err, f.observes)
	}
}

// TestOriginChangeReplaysARecordedRequestWithoutTheSourceDriver checks that
// a recorded change is answered from SQLite alone: its operation comes back
// while the source driver is down.
func TestOriginChangeReplaysARecordedRequestWithoutTheSourceDriver(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	a, f := originAPI(s)
	runQueued(a)
	read := readBack(t, a)
	set := OriginChange{Key: "set", Project: "app", Kind: "set", ProposedURL: f.newOrigin()}
	first := read(a.Change(ctx, set))
	f.Kill()
	if replay := read(a.Change(ctx, set)); replay.ID != first.ID || replay.Status != "completed" {
		t.Fatalf("replay with the source driver down: %+v, want %+v", replay, first)
	}
}

// TestOriginWorkOfAProjectIsSerialized checks that a project's origin work
// waits while other origin work of the project contacts the origin, and
// that another project's does not.
func TestOriginWorkOfAProjectIsSerialized(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	a, f := originAPI(s)
	runQueued(a)
	for _, project := range []string{"app", "other"} {
		if err := s.CreateProject(ctx, project, json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Change(ctx, OriginChange{Key: project, Project: project, Kind: "set", ProposedURL: f.newOrigin()}); err != nil {
			t.Fatal(err)
		}
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	f.observeHook = func() { close(entered); <-release }
	go func() { _, err := a.Refresh(ctx, "app"); done <- err }()
	<-entered
	f.observeHook = nil
	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	_, err := a.Refresh(waitCtx, "app")
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a second refresh of the project answered %v, want it to wait", err)
	}
	if _, err = a.Refresh(ctx, "other"); err != nil {
		t.Errorf("another project's refresh answered %v while the first observed", err)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

// TestOriginCallsRunAtMostTwoAtOnce checks that at most two calls to
// origins run at once, across projects: a third waits until one ends.
func TestOriginCallsRunAtMostTwoAtOnce(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	a, f := originAPI(s)
	runQueued(a)
	projects := []string{"one", "two", "three"}
	for _, project := range projects {
		if err := s.CreateProject(ctx, project, json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Change(ctx, OriginChange{Key: project, Project: project, Kind: "set", ProposedURL: f.newOrigin()}); err != nil {
			t.Fatal(err)
		}
	}
	entered, release, done := make(chan struct{}, len(projects)), make(chan struct{}), make(chan error, len(projects))
	f.observeHook = func() { entered <- struct{}{}; <-release }
	refresh := func(project string) { _, err := a.Refresh(ctx, project); done <- err }
	go refresh("one")
	go refresh("two")
	<-entered
	<-entered
	go refresh("three")
	select {
	case <-entered:
		t.Error("a third call to an origin ran while two were running")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for range projects {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

// endsWithin fails unless work returns soon after its origin calls'
// deadline: past it, the stalled origin f answers again, so work ends
// either way.
func endsWithin(t *testing.T, f *originDriver, what string, work func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { work(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		close(f.unstall)
		<-done
		t.Fatalf("%s still waited on a stalled origin past its deadline", what)
	}
}

// TestOriginWorkEndsAtItsDeadlineWhenTheOriginStalls checks that work whose
// origin stops answering ends once its call's deadline passes: an origin
// change and a publication end failed with reason origin, and a refresh
// marks the observation unknown.
func TestOriginWorkEndsAtItsDeadlineWhenTheOriginStalls(t *testing.T) {
	s, f, a, r := publicationFixture(t)
	ctx := context.Background()
	s.originCallTimeout = 50 * time.Millisecond
	read := readBack(t, a)
	a.Queue = func(string) error { return nil }
	plan, err := planPublication(a, r)
	if err != nil {
		t.Fatal(err)
	}
	publication := read(runPublication(a, r, plan.ChangeTag))
	change := read(a.Change(ctx, OriginChange{Key: "set", Project: "app", Kind: "set", ExpectedURL: r.ExpectedOriginURL, ProposedURL: f.newOrigin()}))
	f.stall = true
	endsWithin(t, f, "an origin change", func() { a.RunOriginChange(ctx, change) })
	wantEnded(t, "an origin change of a stalled origin", read(change, nil), "failed", api.ReasonOrigin)
	endsWithin(t, f, "a publication", func() { a.RunPublication(ctx, publication) })
	wantEnded(t, "a publication to a stalled origin", read(publication, nil), "failed", api.ReasonOrigin)
	var state api.OriginState
	endsWithin(t, f, "a refresh", func() { state, err = a.Refresh(ctx, "app") })
	if err != nil || state.Status != "unknown" {
		t.Fatalf("a refresh of a stalled origin answered %+v %v, want the observation unknown", state, err)
	}
}

// TestOriginChangeStaleExpectationRecordsNothing checks that an expected URL
// that no longer names the origin when the request arrives is refused there:
// no operation records the key and nothing contacts the proposed origin.
func TestOriginChangeStaleExpectationRecordsNothing(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	a, f := originAPI(s)
	runQueued(a)
	read := readBack(t, a)
	url := f.newOrigin()
	wantEnded(t, "set", read(a.Change(ctx, OriginChange{Key: "set", Project: "app", Kind: "set", ProposedURL: url})), "completed", "")
	_, _, before, err := s.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observes := f.observes
	for _, stale := range []OriginChange{
		{Key: "stale", Project: "app", Kind: "set", ExpectedURL: "ssh://host/old", ProposedURL: "ssh://host/b"},
		{Key: "stale", Project: "app", Kind: "set", ProposedURL: "ssh://host/b"},
		{Key: "stale", Project: "app", Kind: "remove", ExpectedURL: "ssh://host/old"},
	} {
		_, err = a.Change(ctx, stale)
		wantAnswer(t, "stale expectation", err, api.KindPreconditionFailed, api.ReasonChanged, "")
	}
	if _, err = s.GetOperationByKey(ctx, "stale"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale request recorded its key: %v", err)
	}
	if _, _, after, err := s.Count(ctx); err != nil || after != before || f.observes != observes {
		t.Fatalf("stale requests recorded %d operations or contacted the origin %d times (%v)", after-before, f.observes-observes, err)
	}
	// The key stays free for a request that expects the current origin.
	wantEnded(t, "current expectation", read(a.Change(ctx, OriginChange{Key: "stale", Project: "app", Kind: "remove", ExpectedURL: url})), "completed", "")
}

// TestOriginChangeEndsFailedWhenTheOriginChangedMeanwhile checks that an
// origin changed between the request and its work ends the operation failed
// with reason changed, leaving the newer origin in place.
func TestOriginChangeEndsFailedWhenTheOriginChangedMeanwhile(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	a, f := originAPI(s)
	a.Queue = func(string) error { return nil }
	read := readBack(t, a)
	op, err := a.Change(ctx, OriginChange{Key: "set", Project: "app", Kind: "set", ProposedURL: f.newOrigin()})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, `INSERT INTO project_origins(project_path,url,current_status,observed_at,refs_json) VALUES('app','ssh://host/meanwhile','fresh','now','[]')`)
	a.RunOriginChange(ctx, op)
	wantEnded(t, "change of a changed origin", read(op, nil), "failed", api.ReasonChanged)
	if state, _, err := s.Origin(ctx, "app"); err != nil || state.URL != "ssh://host/meanwhile" {
		t.Fatalf("failed change replaced the newer origin: %+v %v", state, err)
	}
}

// TestOriginOperationsNameTheirIntent checks the intent each origin operation
// records: the URL a change sets, and the destination, branch and session a
// publication publishes.
func TestOriginOperationsNameTheirIntent(t *testing.T) {
	s, f, a, r := publicationFixture(t)
	ctx := context.Background()
	a.Queue = func(string) error { return nil }
	set, err := a.Change(ctx, OriginChange{Key: "set", Project: "app", Kind: "set", ExpectedURL: r.ExpectedOriginURL, ProposedURL: "ssh://host/next"})
	if err != nil {
		t.Fatal(err)
	}
	checkIntent(t, s, set, api.OperationIntent{Project: "app", URL: "ssh://host/next"})
	remove, err := a.Change(ctx, OriginChange{Key: "remove", Project: "app", Kind: "remove", ExpectedURL: r.ExpectedOriginURL})
	if err != nil {
		t.Fatal(err)
	}
	checkIntent(t, s, remove, api.OperationIntent{Project: "app"})
	session, _, err := publish(a, r)
	if err != nil {
		t.Fatal(err)
	}
	checkIntent(t, s, session, api.OperationIntent{Project: "app", Session: r.Source, Branch: "work", Destination: r.DestinationRef})
	if session.SessionUUID != r.Source {
		t.Fatalf("session publication records session %q, want %q", session.SessionUUID, r.Source)
	}
	retained := r
	retained.Key, retained.Kind, retained.Source, retained.DestinationRef = "retained", "retained", "kept", "refs/heads/release"
	f.world.SetHead("app", "kept", sourcedriver.OID(r.SourceOID))
	kept, _, err := publish(a, retained)
	if err != nil {
		t.Fatal(err)
	}
	checkIntent(t, s, kept, api.OperationIntent{Project: "app", Branch: "kept", Destination: "refs/heads/release"})
	// Each dry run and run reads the origin; nothing runs the workers.
	if kept.SessionUUID != "" || f.observes != 4 || f.pushes != 0 {
		t.Fatalf("retained publication %+v, or accepting pushed (observes %d, pushes %d)", kept, f.observes, f.pushes)
	}
}

func TestOriginSourcePagesAndStrictRPC(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	origins, f := originAPI(s)
	url := f.world.NewOrigin()
	commit := f.world.OriginCommit(url)
	for i := range 1024 {
		f.world.SetOriginRef(url, fmt.Sprintf("refs/heads/%04d-%s", i, strings.Repeat("x", 450)), commit)
	}
	runQueued(origins)
	handler := NewHandler(HandlerConfig{Store: s, Origins: origins})
	_, e := handler(ctx, "origin.change", json.RawMessage(`{"key":"a","project":"app","kind":"set","url":"`+url+`","extra":1}`))
	if e == nil || e.Kind() != "invalid" {
		t.Fatalf("unknown field: %+v", e)
	}
	_, e = handler(ctx, "origin.change", json.RawMessage(`{"key":"a","key":"a","project":"app","kind":"set","url":"`+url+`"}`))
	if e == nil || e.Kind() != "invalid" {
		t.Fatalf("duplicate field: %+v", e)
	}
	for _, bad := range []string{
		`{"V":2,"key":"a","project":"app","kind":"set","url":"` + url + `"}`,
		`{"Key":"a","project":"app","kind":"set","url":"` + url + `"}`,
		`{"key":"a","Project":"app","kind":"set","url":"` + url + `"}`,
		`{"key":"a","project":"app","kind":"set","URL":"` + url + `"}`,
	} {
		if _, e = handler(ctx, "origin.change", json.RawMessage(bad)); e == nil || e.Kind() != "invalid" {
			t.Fatalf("inexact field accepted: %s %+v", bad, e)
		}
	}
	if _, e = handler(ctx, "origin.change", json.RawMessage(`{"key":"a","project":"app","kind":"set","url":"`+url+`"}`)); e != nil {
		t.Fatal(e)
	}
	after := ""
	seen := 0
	for {
		page, e := callMethod(t, ctx, handler, api.OriginSources, api.OriginSourcesRequest{Project: "app", After: after, Limit: 16})
		if e != nil {
			t.Fatal(e)
		}
		encoded, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage(`"` + strings.Repeat(`\u0000`, 128) + `"`), Result: page})
		if len(encoded)+1 > api.MaxFrameBytes {
			t.Fatalf("page exceeds frame: %d", len(encoded))
		}
		seen += len(page.Refs)
		after = page.Next
		if after == "" {
			break
		}
	}
	if seen != 1024 {
		t.Fatalf("seen=%d", seen)
	}
}

func TestOriginStoreRejectsInvalidUTF8Ref(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	change := OriginChange{Key: "bad-utf8", Project: "app", Kind: "set", ProposedURL: "git@host:a.git"}
	op, err := s.BeginOriginChange(ctx, change)
	if err != nil {
		t.Fatal(err)
	}
	bad := sourcedriver.OriginRef{Ref: "refs/heads/" + string([]byte{0xff}), OID: sourcedriver.OID(strings.Repeat("a", 40)), Target: sourcedriver.OID(strings.Repeat("a", 40))}
	if err := s.CompleteOriginChange(ctx, op, []sourcedriver.OriginRef{bad}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid UTF-8 persisted: %v", err)
	}
	state, refs, err := s.Origin(ctx, "app")
	if err != nil || state.Status != "local-only" || len(refs) != 0 {
		t.Fatalf("failed commit changed state: %+v %+v %v", state, refs, err)
	}
	if read, err := s.GetOperation(ctx, op.ID); err != nil || read.Status != "running" {
		t.Fatalf("failed commit ended the operation: %+v %v", read, err)
	}
	valid := sourcedriver.OriginRef{Ref: "refs/heads/café", OID: sourcedriver.OID(strings.Repeat("a", 40)), Target: sourcedriver.OID(strings.Repeat("a", 40))}
	if err := s.CompleteOriginChange(ctx, op, []sourcedriver.OriginRef{valid}); err != nil {
		t.Fatalf("valid UTF-8 ref refused: %v", err)
	}
	if read, err := s.GetOperation(ctx, op.ID); err != nil || read.Status != "completed" {
		t.Fatalf("commit left the operation %+v %v", read, err)
	}
}

// TestOriginErrorsAnswerTheirKind checks each origin refusal's answer through
// the handler; no answer carries the cause's text. An unreachable origin is
// no refusal: the change's operation ends failed with reason origin.
func TestOriginErrorsAnswerTheirKind(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	origins, f := originAPI(s)
	runQueued(origins)
	handler := NewHandler(HandlerConfig{Store: s, Origins: origins})
	url := f.newOrigin()
	if _, e := callMethod(t, ctx, handler, api.OriginChange, api.OriginChangeRequest{Key: "a", Project: "app", Kind: "set", URL: url}); e != nil {
		t.Fatal(e)
	}
	keyReused := &api.Error{Code: -32602, Message: "invalid", Data: &api.ErrorData{Reason: api.ReasonKeyReused, Field: "key"}}
	for _, test := range []struct {
		name    string
		req     api.OriginChangeRequest
		failure error
		want    *api.Error
	}{
		{"key recorded for another change", api.OriginChangeRequest{Key: "a", Project: "app", Kind: "set", URL: "ssh://host/b"}, nil, keyReused},
		{"expected URL no longer holds", api.OriginChangeRequest{Key: "stale", Project: "app", Kind: "set", URL: "ssh://host/c"}, nil, wantError(api.KindPreconditionFailed, api.ReasonChanged, "")},
		{"unknown project", api.OriginChangeRequest{Key: "missing", Project: "missing", Kind: "set", URL: "ssh://host/a"}, nil, wantError(api.KindNotFound, api.ReasonProject, "project/missing")},
		{"URL the source driver refuses", api.OriginChangeRequest{Key: "syntax", Project: "app", Kind: "set", URL: "https://host/a"}, nil, api.InvalidParam("url")},
	} {
		t.Run(test.name, func(t *testing.T) {
			f.failure = test.failure
			_, e := callMethod(t, ctx, handler, api.OriginChange, test.req)
			if !reflect.DeepEqual(e, test.want) {
				t.Fatalf("answer %v, want %v", e, test.want)
			}
			if encoded, _ := json.Marshal(e); strings.Contains(string(encoded), "secret") {
				t.Fatalf("answer %s carries the cause's text", encoded)
			}
		})
	}
	f.failure = errors.New("offline secret")
	result, e := callMethod(t, ctx, handler, api.OriginChange, api.OriginChangeRequest{Key: "offline", Project: "app", Kind: "set", ExpectedURL: url, URL: "ssh://host/b"})
	if e != nil || result.Operation.Kind != KindOriginChange || result.Operation.Key != "offline" {
		t.Fatalf("unreachable origin answered %+v %v, want its operation", result, e)
	}
	op, err := s.GetOperation(ctx, result.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantEnded(t, "unreachable origin", op, "failed", api.ReasonOrigin)
	f.failure = nil
	for _, m := range []string{api.OriginInspect.Name, api.OriginRefresh.Name} {
		_, e := handler(ctx, m, json.RawMessage(`{"project":"missing"}`))
		if want := wantError(api.KindNotFound, api.ReasonProject, "project/missing"); !reflect.DeepEqual(e, want) {
			t.Fatalf("%s of a missing project answered %v, want %v", m, e, want)
		}
	}
}
