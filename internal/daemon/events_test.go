package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

func testDelivery(ctx context.Context, dispatch func(context.Context, plugin.Event) error) *eventDelivery {
	worker, cancel := context.WithCancel(ctx)
	d := &eventDelivery{instance: "instance", queue: make(chan plugin.Event, 64), cancel: cancel, done: make(chan struct{}), dispatch: dispatch}
	go d.run(worker)
	return d
}

func nextEvent(t *testing.T, ch <-chan plugin.Event) plugin.Event {
	t.Helper()
	select {
	case e := <-ch:
		if err := e.Validate(); err != nil {
			t.Fatalf("invalid event: %v", err)
		}
		return e
	case <-time.After(time.Second):
		t.Fatal("event not delivered")
		return plugin.Event{}
	}
}

func TestEventDeliveryKeepsOrderAfterHandlerFailure(t *testing.T) {
	got := make(chan plugin.Event, 3)
	calls := 0
	d := testDelivery(context.Background(), func(_ context.Context, e plugin.Event) error {
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
	d := testDelivery(context.Background(), func(_ context.Context, _ plugin.Event) error {
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

func TestTimedOutHandlerRetiresWorkerWithoutSecondInvocation(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	d := testDelivery(context.Background(), func(_ context.Context, _ plugin.Event) error {
		entered <- struct{}{}
		<-release
		return nil
	})
	d.deadline = 25 * time.Millisecond
	d.emit("session.condition_changed", "app", "session", "main", "condition", "ready")
	d.emit("session.condition_changed", "app", "session", "main", "condition", "stopped")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first invocation absent")
	}
	select {
	case <-d.done:
	case <-time.After(time.Second):
		t.Fatal("timed-out worker remained active")
	}
	select {
	case <-entered:
		t.Fatal("second invocation started after timeout")
	default:
	}
	if len(d.queue) != 1 {
		t.Fatalf("queued events after retirement = %d, want 1", len(d.queue))
	}
	d.Close()
	close(release)
}

func TestEventReductionAndInvalidValues(t *testing.T) {
	got := make(chan plugin.Event, 6)
	d := testDelivery(context.Background(), func(_ context.Context, e plugin.Event) error { got <- e; return nil })
	defer d.Close()
	l := &lifecycle{events: d}
	l.observeView(control.SessionView{UUID: "session", Project: "app", Branch: "main", Condition: "ready", PolicyCondition: "current", Diagnostic: "secret diagnostic"})
	l.observeView(control.SessionView{UUID: "session", Project: "app", Branch: "main", Condition: "stopped", PolicyCondition: "outdated", Diagnostic: "secret diagnostic"})
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
	got := make(chan plugin.Event, 2)
	d := testDelivery(context.Background(), func(_ context.Context, e plugin.Event) error { got <- e; return nil })
	defer d.Close()
	l := &lifecycle{events: d}
	op := control.Operation{ID: "op", Kind: "project.create", Project: "app", SessionUUID: "session"}
	l.recordCreation(op, "")
	if e := nextEvent(t, got); e.Kind != "operation.progress" || e.Branch != "" {
		t.Fatalf("premature session event: %+v", e)
	}
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
	got := make(chan plugin.Event, 2)
	d := testDelivery(ctx, func(_ context.Context, e plugin.Event) error { got <- e; return nil })
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

func TestEventActivationRejectsWrongDigest(t *testing.T) {
	packagePath, err := filepath.Abs("../../plugins/bundled/file-log")
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := plugin.Conformance(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	activationPath := filepath.Join(dir, "activation.json")
	config, _ := json.Marshal(plugin.FileLogConfig{Path: filepath.Join(dir, "events.ndjson"), MaxBytes: 1024})
	selection := plugin.Activation{Schema: plugin.ActivationSchema, Plugins: []plugin.SelectedPackage{{ID: pkg.Manifest.ID, Path: pkg.Path, SHA256: pkg.SHA256, Grants: []string{"event.file.append"}, Config: config}}}
	write := func() {
		data, err := json.Marshal(selection)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(activationPath, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	selected, err := loadEventSelection(control.EventsConfig{ActivationPath: activationPath, PluginID: pkg.Manifest.ID})
	if err != nil {
		t.Fatal(err)
	}
	if selected.Package.SHA256 != pkg.SHA256 {
		t.Fatalf("selected digest = %s, want %s", selected.Package.SHA256, pkg.SHA256)
	}
	selection.Plugins[0].SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	write()
	if _, err = loadEventSelection(control.EventsConfig{ActivationPath: activationPath, PluginID: pkg.Manifest.ID}); err == nil {
		t.Fatal("unapproved digest activated")
	}
}
