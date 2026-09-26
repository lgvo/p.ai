package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/control"
)

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
