package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
)

// TestOperationWorkPanicBlocksTheOperation checks that a panic in an
// operation's work leaves the operation blocked with reason internal, keeps
// its guards, and is not run again by the scheduler, while the daemon goes
// on serving.
func TestOperationWorkPanicBlocksTheOperation(t *testing.T) {
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
	request := control.RetainedRenameRequest{Key: "rename", Project: "app", OldBranch: "saved", NewBranch: "archive", ExpectedOldTip: strings.Repeat("a", 40)}
	op, err := store.BeginRetainedRename(ctx, request, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	var runs atomic.Int32
	kind := operationKinds[control.KindRetainedRename]
	defer func() { operationKinds[control.KindRetainedRename] = kind }()
	panicking := kind
	panicking.process = func(*lifecycle, api.Operation) {
		runs.Add(1)
		var missing map[string]int
		missing["work"]++ // a bug in operation work
	}
	operationKinds[control.KindRetainedRename] = panicking
	l := &lifecycle{ctx: ctx, store: store, working: map[string]bool{}, queueSlots: make(chan struct{}, 8)}
	go l.schedule()
	deadline := time.Now().Add(10 * time.Second)
	var got api.Operation
	for {
		if got, err = store.GetOperation(ctx, op.ID); err != nil {
			t.Fatal(err)
		}
		if got.Status == "blocked" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the panicking operation was not blocked: %+v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got.Reason != api.ReasonInternal || got.Phase != op.Phase || !strings.Contains(got.Diagnostic, "assignment to entry in nil map") {
		t.Fatalf("blocked operation: %+v", got)
	}
	// The guards stay: the same names cannot be renamed by another request.
	again := request
	again.Key = "rename-again"
	_, err = store.BeginRetainedRename(ctx, again, func(context.Context) error { return nil })
	wantRefusal(t, "a rename of the guarded names", err, api.KindConflict, api.ReasonBranchReserved, "project/app:branch/saved")
	// The scheduler polls every second; it does not run the blocked operation.
	time.Sleep(2500 * time.Millisecond)
	if n := runs.Load(); n != 1 {
		t.Fatalf("the work ran %d times", n)
	}
}

// TestRetryClearsTheReason checks that the operation Retry returns no longer
// carries the reason it was blocked with, like the stored one.
func TestRetryClearsTheReason(t *testing.T) {
	ctx := context.Background()
	store, err := control.OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.CreateProject(ctx, "app", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	op, err := store.BeginRetainedRename(ctx, control.RetainedRenameRequest{Key: "rename", Project: "app", OldBranch: "saved", NewBranch: "archive", ExpectedOldTip: strings.Repeat("a", 40)}, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = store.AdvanceOperation(ctx, op.ID, "blocked", op.Phase, op.Committed, op.Evidence, api.ReasonGit, "ref read failed"); err != nil {
		t.Fatal(err)
	}
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	l := &lifecycle{ctx: stopped, store: store, working: map[string]bool{}, queueSlots: make(chan struct{}, 8)}
	retried, err := l.Retry(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Status != "running" || retried.Reason != "" || retried.Diagnostic != "" || stored.Reason != "" {
		t.Fatalf("Retry returned %+v; stored reason %q", retried, stored.Reason)
	}
}
