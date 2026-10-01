package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/control"
)

func TestRetryRefusesFinalizedWorkspaceInspectionWithoutEnqueueOrMutation(t *testing.T) {
	for _, kind := range []string{"workspace.inspect", "workspace.loss.inspect"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			store, err := control.OpenStore(filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.CreateProject(ctx, "app", []byte(`{}`)); err != nil {
				t.Fatal(err)
			}
			creator, source, err := store.ReserveSession(ctx, control.ReserveSessionRequest{Key: "source", Project: "app", Branch: "main", Choice: "blank"})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.AdvanceSessionRegistry(ctx, source.UUID, "creating", "established"); err != nil {
				t.Fatal(err)
			}
			if err := store.AdvanceOperation(ctx, creator.ID, "completed", "established", true, creator.Evidence, ""); err != nil {
				t.Fatal(err)
			}
			evidence := control.WorkspaceInspectEvidence{InstanceUUID: "11111111-1111-1111-1111-111111111111", ImageFingerprint: strings.Repeat("a", 64), BaseFingerprint: strings.Repeat("b", 64), SourceIncusUUID: "22222222-2222-2222-2222-222222222222", SourceGeneration: "33333333-3333-3333-3333-333333333333", OriginalStatus: "Stopped"}
			req := control.WorkspaceInspectRequest{Key: "inspection", SessionUUID: source.UUID}
			var op control.Operation
			if kind == "workspace.inspect" {
				op, err = store.BeginWorkspaceInspect(ctx, req, evidence)
			} else {
				op, err = store.BeginWorkspaceLossInspect(ctx, req, evidence)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := store.AdvanceOperation(ctx, op.ID, "failed", "cleaned", true, op.Evidence, "bounded scan failed; helper removed"); err != nil {
				t.Fatal(err)
			}
			before, _ := store.GetOperation(ctx, op.ID)
			sourceBefore, _ := store.GetSession(ctx, source.UUID)
			// No runtime is available. A refused request must neither reach
			// native effects nor schedule a worker for the finalized identity.
			l := &lifecycle{ctx: ctx, store: store, working: map[string]bool{}, queueSlots: make(chan struct{})}
			got, err := l.Retry(ctx, op.ID)
			if !errors.Is(err, control.ErrConflict) || !reflect.DeepEqual(got, before) {
				t.Fatalf("finalized inspection was accepted: %+v %v", got, err)
			}
			if len(l.pending) != 0 || len(l.working) != 0 {
				t.Fatal("finalized inspection was enqueued")
			}
			after, _ := store.GetOperation(ctx, op.ID)
			sourceAfter, _ := store.GetSession(ctx, source.UUID)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(sourceBefore, sourceAfter) {
				t.Fatal("refused Retry changed durable helper/source evidence or session")
			}
			if _, active, err := store.ActiveWorkspaceInspect(ctx, source.UUID); err != nil || active {
				t.Fatalf("refused Retry reacquired the released guard: %v %v", active, err)
			}
		})
	}
}

func TestWorkspaceInspectRefusesActiveAttachmentBeforeNativeOrDurableIntent(t *testing.T) {
	uuid := "550e8400-e29b-41d4-a716-446655440000"
	l := &lifecycle{attachments: map[string]*attachment{
		"pending": {session: uuid, expires: time.Now().Add(time.Minute)},
	}}
	// Store and runtime are deliberately absent. A valid attached request must
	// be refused before either can be read or any helper intent can be written.
	_, err := l.InspectWorkspace(context.Background(), control.WorkspaceInspectRequest{Key: "inspect-attached", SessionUUID: uuid})
	if !errors.Is(err, control.ErrConflict) {
		t.Fatalf("attached source was accepted for quiescence: %v", err)
	}
}
