package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

// These tests state what the journal's one write path gives the daemon:
// operation.wait, the rule that an operation ends only after its worker lets
// go of the session, the progress events, and the admission check.

// observedLifecycle is a lifecycle observing f's journal, with its worker
// slots closed so no work runs.
func observedLifecycle(t *testing.T, f *journalFixture) (*lifecycle, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	l := closedLifecycle(ctx, f.store)
	f.store.ObserveJournal(l)
	return l, ctx
}

func TestWaitAnswersFinishedOperationsAtOnce(t *testing.T) {
	f := newJournalFixture(t)
	l, ctx := observedLifecycle(t, f)
	for _, status := range []string{"blocked", "failed", "completed", "superseded"} {
		id := f.operation(t, "session.rename", status, "", sessionUUID(1), false)
		start := time.Now()
		op, err := l.WaitOperation(ctx, id, 20*time.Second)
		if err != nil || op.Status != status || time.Since(start) > time.Second {
			t.Fatalf("%s: %s %v after %s", status, op.Status, err, time.Since(start))
		}
	}
	if _, err := l.WaitOperation(ctx, "00000000-0000-4000-8000-999999999999", time.Second); !errors.Is(err, control.ErrNotFound) {
		t.Fatalf("unknown operation answered %v", err)
	}
}

func TestWaitReturnsOnTheCommitThatLeavesRunning(t *testing.T) {
	for _, status := range []string{"blocked", "failed", "completed"} {
		t.Run(status, func(t *testing.T) {
			f := newJournalFixture(t)
			l, ctx := observedLifecycle(t, f)
			id := f.operation(t, "session.rename", "running", "", sessionUUID(1), false)
			done := make(chan api.Operation, 1)
			go func() {
				op, err := l.WaitOperation(ctx, id, 20*time.Second)
				if err != nil {
					t.Error(err)
				}
				done <- op
			}()
			waitForWaiter(t, l, id)
			// A phase change keeps it running, and the wait waiting.
			if err := f.store.AdvanceOperation(ctx, id, "running", "next", false, nil, "", ""); err != nil {
				t.Fatal(err)
			}
			select {
			case op := <-done:
				t.Fatalf("wait returned on a phase change: %+v", op)
			case <-time.After(100 * time.Millisecond):
			}
			if err := f.store.AdvanceOperation(ctx, id, status, "next", false, nil, testReason(status), ""); err != nil {
				t.Fatal(err)
			}
			select {
			case op := <-done:
				if op.Status != status {
					t.Fatalf("wait returned %s", op.Status)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("wait did not return on the commit")
			}
		})
	}
}

func TestWaitAnswersRunningAtItsTimeoutAndEndsOnCancel(t *testing.T) {
	f := newJournalFixture(t)
	l, ctx := observedLifecycle(t, f)
	id := f.operation(t, "session.rename", "running", "", sessionUUID(1), false)
	start := time.Now()
	op, err := l.WaitOperation(ctx, id, 200*time.Millisecond)
	if err != nil || op.Status != "running" || time.Since(start) < 200*time.Millisecond {
		t.Fatalf("timeout answered %+v %v after %s", op, err, time.Since(start))
	}
	cancelled, cancel := context.WithCancel(ctx)
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if _, err = l.WaitOperation(cancelled, id, 20*time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait answered %v", err)
	}
	l.waitMu.Lock()
	defer l.waitMu.Unlock()
	if len(l.waiters) != 0 {
		t.Fatalf("waiters left behind: %v", l.waiters)
	}
}

func waitForWaiter(t *testing.T, l *lifecycle, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		l.waitMu.Lock()
		n := len(l.waiters[id])
		l.waitMu.Unlock()
		if n != 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the wait never started")
}

// pausingObserver passes the journal's reports to the lifecycle, pausing
// after the release of an ending operation's hold (before its commit) or
// after its committed end.
type pausingObserver struct {
	*lifecycle
	beforeCommit bool
	paused       chan struct{}
	resume       chan struct{}
}

func (o pausingObserver) Ending(id string) {
	o.lifecycle.Ending(id)
	if o.beforeCommit {
		close(o.paused)
		<-o.resume
	}
}

func (o pausingObserver) Changed(op api.Operation, from string) {
	o.lifecycle.Changed(op, from)
	if !o.beforeCommit && from == "running" {
		close(o.paused)
		<-o.resume
	}
}

// TestOperationEndsOnlyAfterItsWorkerLetsGo pauses a worker right after its
// end is committed: a request on the session then meets neither the lock nor
// the journal. Paused between the release and the commit, the lock is free
// but the journal's admission check does not answer until the end is
// committed: the store's one connection serializes it after the commit, so
// no request acts on the session while the operation still reads running.
func TestOperationEndsOnlyAfterItsWorkerLetsGo(t *testing.T) {
	for _, beforeCommit := range []bool{false, true} {
		name := "after the commit"
		if beforeCommit {
			name = "before the commit"
		}
		t.Run(name, func(t *testing.T) {
			f := newJournalFixture(t)
			session := sessionUUID(1)
			l, ctx := observedLifecycle(t, f)
			observer := pausingObserver{lifecycle: l, beforeCommit: beforeCommit, paused: make(chan struct{}), resume: make(chan struct{})}
			f.store.ObserveJournal(observer)
			id := f.operation(t, "session.rename", "running", "", session, false)
			// The worker holds the session, then records its end.
			if err := l.holdSession(ctx, id, session); err != nil {
				t.Fatal(err)
			}
			if _, err := l.lockSession(ctx, session); !refusedNaming(err, api.KindRetry, id) {
				t.Fatalf("a request during the work answered %v", err)
			}
			if err := l.admitSessionWork(ctx, session, ""); !refusedNaming(err, api.KindRetry, id) {
				t.Fatalf("admission during the work answered %v", err)
			}
			ended := make(chan error, 1)
			go func() { ended <- f.store.AdvanceOperation(ctx, id, "completed", "completed", false, nil, "", "") }()
			<-observer.paused
			release, err := l.lockSession(ctx, session)
			if err != nil {
				t.Fatalf("the worker still held the session: %v", err)
			}
			defer release()
			admitted := make(chan error, 1)
			go func() { admitted <- l.admitSessionWork(ctx, session, "") }()
			if beforeCommit {
				select {
				case err = <-admitted:
					t.Fatalf("admission answered %v before the end was committed", err)
				case <-time.After(100 * time.Millisecond):
				}
			}
			close(observer.resume)
			if err = <-admitted; err != nil {
				t.Fatalf("admission after the end answered %v", err)
			}
			if err = <-ended; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func refusedNaming(err error, kind api.ErrorKind, id string) bool {
	var refusal *control.Refusal
	return errors.As(err, &refusal) && refusal.Kind == kind && refusal.Target == api.TargetOperation(id)
}

// TestWorkerHoldsAreReleasedWhenItsWorkEnds checks the dispatcher's side of
// the hold: work that returns without recording an end lets go.
func TestWorkerHoldsAreReleasedWhenItsWorkEnds(t *testing.T) {
	f := newJournalFixture(t)
	session := sessionUUID(1)
	l, ctx := observedLifecycle(t, f)
	id := f.operation(t, "project.retained.rename", "running", "", "", false)
	kind := operationKinds[control.KindRetainedRename]
	defer func() { operationKinds[control.KindRetainedRename] = kind }()
	holding := kind
	holding.process = func(l *lifecycle, op api.Operation) {
		if err := l.holdSession(ctx, op.ID, session); err != nil {
			t.Error(err)
		}
	}
	operationKinds[control.KindRetainedRename] = holding
	l.process(id, false)
	release, err := l.lockSession(ctx, session)
	if err != nil {
		t.Fatalf("the returned worker still held the session: %v", err)
	}
	release()
}

func TestAdmissionRefusesWorkCompetingWithAnUnfinishedOperation(t *testing.T) {
	f := newJournalFixture(t)
	l, ctx := observedLifecycle(t, f)
	running, blocked, free, shared := sessionUUID(1), sessionUUID(2), sessionUUID(3), sessionUUID(4)
	runningOp := f.operation(t, "session.create", "running", "", running, false)
	blockedOp := f.operation(t, "session.discard", "blocked", "", blocked, false)
	f.operation(t, "session.rename", "completed", "", free, false)
	// A blocked creator shares its session with an assembled cleanup: the
	// running cleanup is named.
	creator := f.operation(t, "session.create", "blocked", "", shared, false)
	cleanup := f.assembledCleanup(t, shared)
	for _, test := range []struct {
		name, session, except string
		kind                  api.ErrorKind
		target                string
	}{
		{"running", running, "", api.KindRetry, runningOp},
		{"blocked", blocked, "", api.KindConflict, blockedOp},
		{"finished", free, "", "", ""},
		{"the operation itself", running, runningOp, "", ""},
		{"a running cleanup before its blocked creator", shared, "", api.KindRetry, cleanup},
		{"the creator's own work", shared, creator, api.KindRetry, cleanup},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := l.admitSessionWork(ctx, test.session, test.except)
			if test.kind == "" && err != nil || test.kind != "" && !refusedNaming(err, test.kind, test.target) {
				t.Fatalf("answered %v, want %s naming %s", err, test.kind, test.target)
			}
			if test.kind == api.KindConflict {
				var refusal *control.Refusal
				if errors.As(err, &refusal); refusal.Reason != api.ReasonOperationBlocked {
					t.Fatalf("reason %s", refusal.Reason)
				}
			}
		})
	}
}

func TestEveryStatusChangeEmitsOneProgressEvent(t *testing.T) {
	got := make(chan plugin.Event, 16)
	d := testDelivery(context.Background(), func(_ context.Context, e plugin.Event) error { got <- e; return nil })
	defer d.Close()
	f := newJournalFixture(t)
	l, ctx := observedLifecycle(t, f)
	l.events = d
	expect := func(phases ...string) {
		t.Helper()
		for _, phase := range phases {
			if e := nextEvent(t, got); e.Kind != "operation.progress" || e.Fields["phase"] != phase {
				t.Fatalf("event %+v, want phase %s", e, phase)
			}
		}
		select {
		case e := <-got:
			t.Fatalf("unexpected event %+v", e)
		case <-time.After(50 * time.Millisecond):
		}
	}
	n := 0
	for _, kind := range dispatchKinds {
		t.Run(kind, func(t *testing.T) {
			n++
			id := f.operation(t, kind, "running", "", sessionUUID(n), false)
			op, err := f.store.GetOperation(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			// The fixture writes its row past the journal; report it as the
			// journal reports an accepted operation.
			l.Changed(op, "")
			expect("accepted")
			advance := func(status string) {
				t.Helper()
				op, err = f.store.GetOperation(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				if err = f.store.AdvanceOperation(ctx, id, status, op.Phase, op.Committed, op.Evidence, testReason(status), ""); err != nil {
					t.Fatal(err)
				}
			}
			advance("running") // a phase change inside the kind emits nothing
			expect()
			advance("blocked")
			expect("blocked")
			if _, err = l.Retry(ctx, id); err != nil {
				t.Fatal(err)
			}
			expect("running")
			if kind == "project.delete" || kind == "session.create.cleanup" {
				return // the store ends these kinds only through their own steps
			}
			advance("failed")
			expect("failed")
		})
	}
	for from, want := range map[string]string{"": "accepted", "running": "completed", "blocked": "running"} {
		to := map[string]string{"": "running", "running": "completed", "blocked": "running"}[from]
		if got := progressPhase(from, to); got != want {
			t.Errorf("%q to %q: %q, want %q", from, to, got, want)
		}
	}
	if got := progressPhase("blocked", "superseded"); got != "cancelled" {
		t.Errorf("superseded: %q", got)
	}
	if got := progressPhase("running", "running"); got != "" {
		t.Errorf("phase change: %q", got)
	}
}

// TestEndingNeedsNoLifecycleLock keeps the journal from deadlocking: Ending
// runs while the journal's transaction holds the store's one connection, and
// requests hold l.mu around store calls (ConfirmAttachment does).
func TestEndingNeedsNoLifecycleLock(t *testing.T) {
	f := newJournalFixture(t)
	l, ctx := observedLifecycle(t, f)
	id := f.operation(t, "session.rename", "running", "", sessionUUID(1), false)
	if err := l.holdSession(ctx, id, sessionUUID(1)); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	ended := make(chan struct{})
	go func() { l.Ending(id); close(ended) }()
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("Ending waited for l.mu")
	}
}

// TestResumedWorkRunsOnlyWhileRunning: resumption lists running work, but a
// worker that blocked it may end before the resumed worker starts; that one
// then does nothing, and the blocked operation waits for Retry.
func TestResumedWorkRunsOnlyWhileRunning(t *testing.T) {
	f := newJournalFixture(t)
	l, _ := observedLifecycle(t, f)
	id := f.operation(t, "project.retained.rename", "blocked", "", "", false)
	kind := operationKinds[control.KindRetainedRename]
	defer func() { operationKinds[control.KindRetainedRename] = kind }()
	runs := 0
	counting := kind
	counting.process = func(*lifecycle, api.Operation) { runs++ }
	operationKinds[control.KindRetainedRename] = counting
	l.process(id, true)
	if runs != 0 {
		t.Fatal("resumed work ran on a blocked operation")
	}
	l.process(id, false)
	if runs != 1 {
		t.Fatal("a request's own enqueue did not run")
	}
}

// recordingObserver records the journal's reports, pausing the first one
// until released.
type recordingObserver struct {
	reports chan string
	pause   chan struct{}
}

func (o *recordingObserver) Ending(string) {}
func (o *recordingObserver) Changed(op api.Operation, from string) {
	o.reports <- from + ">" + op.Status
	if o.pause != nil {
		<-o.pause
		o.pause = nil
	}
}

// TestJournalReportsEachChangeAsItsTransactionLeftIt: a later change that
// commits while the observer still hears of an earlier one is reported after
// it, each with the status its own transaction recorded.
func TestJournalReportsEachChangeAsItsTransactionLeftIt(t *testing.T) {
	f := newJournalFixture(t)
	ctx := context.Background()
	id := f.operation(t, "session.rename", "running", "", sessionUUID(1), false)
	observer := &recordingObserver{reports: make(chan string, 4), pause: make(chan struct{})}
	f.store.ObserveJournal(observer)
	blocked := make(chan error, 1)
	go func() {
		blocked <- f.store.AdvanceOperation(ctx, id, "blocked", "forged", false, nil, api.ReasonInternal, "")
	}()
	if got := <-observer.reports; got != "running>blocked" {
		t.Fatalf("first report %s", got)
	}
	retried := make(chan error, 1)
	go func() { retried <- f.store.AdvanceOperation(ctx, id, "running", "forged", false, nil, "", "") }()
	select {
	case got := <-observer.reports:
		t.Fatalf("a later change was reported before the earlier report ended: %s", got)
	case <-time.After(100 * time.Millisecond):
	}
	close(observer.pause)
	if err := <-blocked; err != nil {
		t.Fatal(err)
	}
	if got := <-observer.reports; got != "blocked>running" {
		t.Fatalf("second report %s", got)
	}
	if err := <-retried; err != nil {
		t.Fatal(err)
	}
}
