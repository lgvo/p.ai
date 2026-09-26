package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/control"
)

// Signal when a worker evaluates its context while entering the lock select.
// This makes the contested handoff deterministic without a production hook.
type creationWaitingContext struct {
	context.Context
	reached chan struct{}
	once    sync.Once
}

func (c *creationWaitingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.reached) })
	return c.Context.Done()
}

func TestCreateReplayQueuedAfterReplacementProofCannotProduceOldEffects(t *testing.T) {
	l := &lifecycle{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// This is the dangerous request: assignment succeeded, the branch still has
	// its captured tip, and only policy changes. An unguarded old worker would
	// pass its branch check and create endpoint/key resources for the old UUID.
	old := control.Operation{ID: "old-operation", Kind: "session.create", SessionUUID: "old-session", Status: "blocked", Phase: "branch-assigned", Committed: true}
	replacing, err := l.lockSession(ctx, old.SessionUUID)
	if err != nil {
		t.Fatal(err)
	}
	// Replacement has finished its last absence proof. Exact Create replay now
	// schedules a worker using the old operation it read before the handoff.
	waiting := &creationWaitingContext{Context: ctx, reached: make(chan struct{})}
	loaded, done := make(chan struct{}), make(chan error, 1)
	fresh := old
	go func() {
		_, release, e := l.lockCreationOperation(waiting, old, func(context.Context, string) (control.Operation, error) {
			close(loaded)
			return fresh, nil
		})
		if release != nil {
			release()
			done <- errors.New("old worker reached effects after supersession")
			return
		}
		done <- e
	}()
	select {
	case <-waiting.reached:
	case <-ctx.Done():
		t.Fatal("worker never reached the contested session lock")
	}
	// The reload itself must wait until replacement has committed and released.
	select {
	case <-loaded:
		t.Fatal("worker loaded old state while replacement held its lock")
	default:
	}
	fresh.Status, fresh.Phase = "superseded", "superseded"
	replacing()
	select {
	case e := <-done:
		if !errors.Is(e, control.ErrConflict) {
			t.Fatalf("superseded worker: %v", e)
		}
	case <-ctx.Done():
		t.Fatal("queued worker did not finish")
	}
	if release, e := l.lockSession(ctx, old.SessionUUID); e != nil {
		t.Fatalf("rejected worker leaked session lock: %v", e)
	} else {
		release()
	}
}

func TestCreationWorkerFirstExcludesReplacementAndReloadsBeforeEffects(t *testing.T) {
	l := &lifecycle{}
	ctx := context.Background()
	old := control.Operation{ID: "old-operation", Kind: "session.create", SessionUUID: "old-session", Status: "blocked", Phase: "source-ready"}
	fresh := old
	fresh.Status = "running"
	fresh.Phase = "branch-assigned"
	fresh.Committed = true
	got, release, e := l.lockCreationOperation(ctx, old, func(context.Context, string) (control.Operation, error) { return fresh, nil })
	if e != nil {
		t.Fatal(e)
	}
	if got.Phase != fresh.Phase || !got.Committed {
		release()
		t.Fatal("worker retained state read before session lock")
	}
	if replacing, e := l.lockSession(ctx, old.SessionUUID); !errors.Is(e, control.ErrConflict) {
		if replacing != nil {
			replacing()
		}
		release()
		t.Fatalf("replacement entered during creation effects: %v", e)
	}
	release()
	if replacing, e := l.lockSession(ctx, old.SessionUUID); e != nil {
		t.Fatal(e)
	} else {
		replacing()
	}
}

func TestCreationWorkerWaitDuringPreviewRetainsWorkAndHonorsCancellation(t *testing.T) {
	l := &lifecycle{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	old := control.Operation{ID: "old-operation", Kind: "session.create", SessionUUID: "old-session", Status: "blocked", Phase: "branch-assigned", Committed: true}
	preview, e := l.lockSession(ctx, old.SessionUUID)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, release, e := l.lockCreationOperation(ctx, old, func(context.Context, string) (control.Operation, error) { return old, nil })
		if release != nil {
			release()
		}
		done <- e
	}()
	preview()
	select {
	case e := <-done:
		if e != nil {
			t.Fatalf("preview contention dropped queued work: %v", e)
		}
	case <-ctx.Done():
		t.Fatal("worker stranded after preview")
	}
	preview, e = l.lockSession(ctx, old.SessionUUID)
	if e != nil {
		t.Fatal(e)
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if _, release, e := l.lockCreationOperation(cancelled, old, func(context.Context, string) (control.Operation, error) {
		t.Fatal("cancelled worker loaded state")
		return old, nil
	}); !errors.Is(e, context.Canceled) || release != nil {
		t.Fatalf("cancelled worker: %v", e)
	}
	preview()
}
