package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/driverwire/launch"
	"github.com/lgvo/p.ai/internal/eventhandler"
)

func testDelivery(ctx context.Context, dispatch func(context.Context, eventhandler.Event) error) *eventDelivery {
	worker, cancel := context.WithCancel(ctx)
	d := &eventDelivery{instance: "instance", queue: make(chan eventhandler.Event, 64), cancel: cancel, done: make(chan struct{}), dispatch: dispatch}
	go d.run(worker)
	return d
}

func nextEvent(t *testing.T, ch <-chan eventhandler.Event) eventhandler.Event {
	t.Helper()
	select {
	case e := <-ch:
		if err := e.Validate(); err != nil {
			t.Fatalf("invalid event: %v", err)
		}
		return e
	case <-time.After(time.Second):
		t.Fatal("event not delivered")
		return eventhandler.Event{}
	}
}

func TestEventDeliveryKeepsOrderAfterHandlerFailure(t *testing.T) {
	got := make(chan eventhandler.Event, 3)
	calls := 0
	d := testDelivery(context.Background(), func(_ context.Context, e eventhandler.Event) error {
		got <- e
		calls++
		if calls == 1 {
			return errors.New("private failure details")
		}
		return nil
	})
	defer d.Close()
	d.emit("session.condition_changed", "app", "session", "main", "condition", "ready")
	d.emit("session.policy_changed", "app", "session", "main", "policy_condition", "outdated")
	first, second := nextEvent(t, got), nextEvent(t, got)
	if first.Kind != "session.condition_changed" || second.Kind != "session.policy_changed" || first.ID == second.ID {
		t.Fatalf("unexpected event order or IDs: %+v %+v", first, second)
	}
}

func TestEventDeliveryBoundedDropAndShutdown(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	d := testDelivery(context.Background(), func(_ context.Context, _ eventhandler.Event) error {
		once.Do(func() { close(entered) })
		<-release // A broken handler cannot hold daemon shutdown indefinitely.
		return nil
	})
	d.emit("session.condition_changed", "app", "session", "main", "condition", "ready")
	<-entered
	for i := 0; i < 80; i++ {
		d.emit("session.policy_changed", "app", "session", "main", "policy_condition", "current")
	}
	if got := len(d.queue); got != 64 {
		t.Fatalf("queue length = %d, want 64", got)
	}
	start := time.Now()
	d.Close()
	if time.Since(start) > time.Second {
		t.Fatal("shutdown waited on broken handler")
	}
	close(release)
}

// A handler that misses an event's deadline holds up nothing: the next
// event goes to the handler its supervisor started again, and the missed
// deadline is logged.
func TestAMissedDeadlineDoesNotStopDelivery(t *testing.T) {
	log := captureLog(t)
	got := make(chan eventhandler.Event, 2)
	d := testDelivery(context.Background(), func(ctx context.Context, e eventhandler.Event) error {
		if e.Fields["condition"] == "ready" {
			<-ctx.Done()
			return ctx.Err()
		}
		got <- e
		return nil
	})
	defer d.Close()
	d.deadline = 25 * time.Millisecond
	d.emit("session.condition_changed", "app", "session", "main", "condition", "ready")
	d.emit("session.condition_changed", "app", "session", "main", "condition", "stopped")
	if e := nextEvent(t, got); e.Fields["condition"] != "stopped" {
		t.Fatalf("delivered %+v after the missed deadline", e)
	}
	if !strings.Contains(log(), "event handler deadline exceeded; restarting it") {
		t.Fatalf("the log holds %q", log())
	}
}

// An event the handler is down for is dropped, and the drop is logged at
// once, with no later event needed and even right after another diagnostic.
func TestAnEventTheHandlerIsDownForIsDroppedAndLogged(t *testing.T) {
	log := captureLog(t)
	d := testDelivery(context.Background(), func(_ context.Context, e eventhandler.Event) error {
		if e.Fields["condition"] == "creating" {
			return errors.New("private failure details")
		}
		return fmt.Errorf("the event handler is not running: %w", eventhandler.ErrUnavailable)
	})
	defer d.Close()
	d.emit("session.condition_changed", "app", "session", "main", "condition", "creating")
	d.emit("session.condition_changed", "app", "session", "main", "condition", "starting")
	logged := ""
	for limit := time.Now().Add(time.Second); time.Now().Before(limit); time.Sleep(10 * time.Millisecond) {
		if logged = log(); strings.Contains(logged, "event delivery dropped") {
			break
		}
	}
	if !strings.Contains(logged, "event handler delivery failed") || !strings.Contains(logged, "event delivery dropped") {
		t.Fatalf("the log holds %q, want the failure and the drop", logged)
	}
	if strings.Contains(logged, "private failure details") {
		t.Fatalf("the log holds the handler's error: %q", logged)
	}
}

// captureLog sends the standard logger's output to a buffer until the test
// ends and returns a function that reads it.
func captureLog(t *testing.T) func() string {
	t.Helper()
	var mu sync.Mutex
	var buf strings.Builder
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestEventReductionAndInvalidValues(t *testing.T) {
	got := make(chan eventhandler.Event, 6)
	d := testDelivery(context.Background(), func(_ context.Context, e eventhandler.Event) error { got <- e; return nil })
	defer d.Close()
	l := &lifecycle{events: d}
	l.observeView(api.SessionView{UUID: "session", Project: "app", Branch: "main", Condition: "ready", PolicyCondition: "current", Diagnostic: "secret diagnostic"})
	l.observeView(api.SessionView{UUID: "session", Project: "app", Branch: "main", Condition: "stopped", PolicyCondition: "outdated", Diagnostic: "secret diagnostic"})
	for _, kind := range []string{"session.attachment_changed", "session.unattended_changed", "operation.progress"} {
		field, value := "count", "1"
		switch kind {
		case "session.unattended_changed":
			field, value = "unattended_condition", "none"
		case "operation.progress":
			field, value = "phase", "completed"
		}
		d.emit(kind, "app", "session", "main", field, value)
	}
	d.emit("session.condition_changed", "app", "session", "main", "condition", "secret diagnostic")
	for i := 0; i < 5; i++ {
		e := nextEvent(t, got)
		if len(e.Fields) != 1 || e.Fields["condition"] == "secret diagnostic" {
			t.Fatalf("unreduced event: %+v", e)
		}
	}
	select {
	case e := <-got:
		t.Fatalf("invalid event delivered: %+v", e)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestProjectCreationEventWaitsForSessionCommit(t *testing.T) {
	got := make(chan eventhandler.Event, 2)
	d := testDelivery(context.Background(), func(_ context.Context, e eventhandler.Event) error { got <- e; return nil })
	defer d.Close()
	l := &lifecycle{events: d}
	op := api.Operation{ID: "op", Kind: "project.create", Project: "app", SessionUUID: "session", Status: "running"}
	// The journal reports the accepted creation; it names no branch yet.
	l.Changed(op, "")
	if e := nextEvent(t, got); e.Kind != "operation.progress" || e.Fields["phase"] != "accepted" || e.Branch != "" {
		t.Fatalf("premature session event: %+v", e)
	}
	l.recordCreation(op, "")
	select {
	case e := <-got:
		t.Fatalf("premature session event: %+v", e)
	case <-time.After(20 * time.Millisecond):
	}
	l.recordCreatingSession(control.Session{UUID: "session", Project: "app", Branch: "main"})
	if e := nextEvent(t, got); e.Kind != "session.condition_changed" || e.Fields["condition"] != "creating" {
		t.Fatalf("missing committed session event: %+v", e)
	}
}

func TestAttachmentCallbackEmitsConfirmedCountAndClear(t *testing.T) {
	ctx := context.Background()
	session := control.Session{UUID: "session", Project: "app", Branch: "main"}
	got := make(chan eventhandler.Event, 2)
	d := testDelivery(ctx, func(_ context.Context, e eventhandler.Event) error { got <- e; return nil })
	defer d.Close()
	l := &lifecycle{events: d}
	l.seedRecoveredSession(session, "current")
	l.attachmentChanged(ctx, session.UUID, 1, true)
	first, second := nextEvent(t, got), nextEvent(t, got)
	if first.Kind != "session.attachment_changed" || first.Fields["count"] != "1" ||
		second.Kind != "session.unattended_changed" || second.Fields["unattended_condition"] != "none" ||
		first.Project != "app" || first.Session != session.UUID {
		t.Fatalf("incorrect confirmed transition: %+v %+v", first, second)
	}
}

// The daemon refuses to start with an event handler whose package differs
// from the digest host configuration pins.
func TestEventDeliveryRefusesAPackageOfAnotherDigest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "handler")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema":"p.plugin/v2","id":"example.handler","version":"1.0.0","description":"test","capability":"event-handler","entry":"handler"}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "handler"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &control.EventsConfig{Path: dir, SHA256: strings.Repeat("0", 64), Config: json.RawMessage(`{}`)}
	d, err := newEventDelivery(context.Background(), cfg, "instance")
	if err == nil {
		d.Close()
		t.Fatal("an event handler of another digest started")
	}
	if !errors.Is(err, launch.ErrLaunchCheck) || !strings.Contains(err.Error(), "not the pinned") {
		t.Fatalf("newEventDelivery = %v, want the launch checks' refusal", err)
	}
}

func TestStoppingIsAReportedCondition(t *testing.T) {
	got := make(chan eventhandler.Event, 2)
	d := testDelivery(context.Background(), func(_ context.Context, e eventhandler.Event) error { got <- e; return nil })
	defer d.Close()
	l := &lifecycle{events: d}
	l.observeView(api.SessionView{UUID: "session", Project: "app", Branch: "main", Condition: "ready", PolicyCondition: "current"})
	l.observeView(api.SessionView{UUID: "session", Project: "app", Branch: "main", Condition: "stopping", PolicyCondition: "current"})
	if e := nextEvent(t, got); e.Kind != "session.condition_changed" || e.Fields["condition"] != "stopping" {
		t.Fatalf("event %+v", e)
	}
}
