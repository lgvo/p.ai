package daemon

import (
	"context"
	"encoding/json"

	"github.com/lgvo/p.ai/internal/control"
)

// lockCreationOperation serializes every creation effect with replacement of
// that session. A replay can enqueue after replacement's last absence proof:
// waiting here keeps it from acting until the handoff has committed. Reloading
// then rejects the superseded request before reading its old session or
// creating endpoints, credentials, builders, or runtimes. Waiting also keeps
// a worker queued during a read-only preview from being silently dropped.
func (l *lifecycle) lockCreationOperation(ctx context.Context, observed control.Operation,
	load func(context.Context, string) (control.Operation, error)) (control.Operation, func(), error) {
	slot := l.sessionLockSlot(observed.SessionUUID)
	select {
	case <-ctx.Done():
		return control.Operation{}, nil, ctx.Err()
	case <-slot:
	}
	release := func() { slot <- struct{}{} }
	fresh, err := load(ctx, observed.ID)
	if err != nil {
		release()
		return control.Operation{}, nil, err
	}
	if fresh.Kind != "session.create" || fresh.SessionUUID != observed.SessionUUID || fresh.Status != "running" && fresh.Status != "blocked" {
		release()
		return control.Operation{}, nil, control.ErrConflict
	}
	return fresh, release, nil
}

// A blocked early local request remains inspectable until an explicit Retry or
// exact Create replay. Restart must not turn a failed planned branch CAS into
// fresh UUID effects while the user is preparing replacement. Running intents
// and later/origin recovery retain their existing automatic reconciliation.
func deferBlockedCreationUntilRetry(op control.Operation) bool {
	if op.Kind != "session.create" || op.Status != "blocked" || op.Phase != "source-ready" && op.Phase != "branch-assigned" {
		return false
	}
	var req control.ReserveSessionRequest
	ev, err := control.Evidence(op)
	return err == nil && json.Unmarshal(op.Request, &req) == nil && control.ReplaceableCreationEvidence(req, ev)
}
