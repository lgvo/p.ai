package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
)

func TestFailedCreateCleanupAbsentLocalResourcesRefusesUnexpectedIdentity(t *testing.T) {
	state, m, c := replacementLocalFixture(t)
	// Reuse the real credential/socket fixture and reviewed cleanup. The sibling
	// remains, while the old UUID reaches the legitimate idempotent absence path.
	if e := cleanupReplacementLocal(context.Background(), state, m, c, func(context.Context) error { return nil }); e != nil {
		t.Fatal(e)
	}
	// The absent-path checker needs only the authority root, so use the same
	// helper beneath it without fabricating a principal or dropping a registry.
	if e := checkCreateCleanupLocalAbsent(state, m, c.OldUUID); e != nil {
		t.Fatal(e)
	}
	key := filepath.Join(state, "session_keys", c.OldUUID)
	if e := os.Symlink(filepath.Join(state, "session_keys", replacementOtherUUID), key); e != nil {
		t.Fatal(e)
	}
	if e := checkCreateCleanupLocalAbsent(state, m, c.OldUUID); e == nil {
		t.Fatal("unexpected key symlink accepted")
	}
	if e := os.Remove(key); e != nil {
		t.Fatal(e)
	}
	endpoint := filepath.Join(m.prefix, c.OldUUID)
	if e := os.Mkdir(endpoint, 0755); e != nil {
		t.Fatal(e)
	}
	if e := checkCreateCleanupLocalAbsent(state, m, c.OldUUID); e == nil {
		t.Fatal("unexpected endpoint directory accepted")
	}
	if e := os.Remove(endpoint); e != nil {
		t.Fatal(e)
	}
}

func TestFailedCreateCleanupCancelledWhileCreationOwnsUUIDDoesNotAct(t *testing.T) {
	l := &lifecycle{}
	ctx, cancel := context.WithCancel(context.Background())
	l.ctx = ctx
	held, e := l.lockSession(context.Background(), replacementOldUUID)
	if e != nil {
		t.Fatal(e)
	}
	// Nil Store/runtime deliberately fail if the cancelled worker crosses the
	// session gate. Its queued cleanup must not steal the old worker's lock.
	cancel()
	done := make(chan struct{})
	go func() { l.processCreateCleanup(api.Operation{SessionUUID: replacementOldUUID}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled cleanup worker waited indefinitely")
	}
	if _, e = l.lockSession(context.Background(), replacementOldUUID); e == nil {
		t.Fatal("cleanup released another worker's lock")
	}
	held()
	if release, e := l.lockSession(context.Background(), replacementOldUUID); e != nil {
		t.Fatal(e)
	} else {
		release()
	}
}

func TestFailedCreateCleanupProvesTheLastBuildsBuilderAbsent(t *testing.T) {
	op := "11111111-1111-4111-8111-111111111111"
	for _, tc := range []struct {
		builder *api.CreationBuilderState
		want    string
	}{
		{nil, op},
		{&api.CreationBuilderState{State: "not-attempted"}, op},
		{&api.CreationBuilderState{Cycle: 3, State: "absent"}, control.BuildRef(op, 3)},
	} {
		if got := lastBuild(op, tc.builder); got != tc.want {
			t.Fatalf("builder %+v: proved %s absent, want %s", tc.builder, got, tc.want)
		}
	}
}
