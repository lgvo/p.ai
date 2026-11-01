package daemon

import (
	"context"
	"encoding/json"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
)

// lockCreationOperation serializes every creation effect with replacement of
// that session. A replay can enqueue after replacement's last absence proof:
// waiting here keeps it from acting until the handoff has committed. Reloading
// then rejects the superseded request before reading its old session or
// creating endpoints, credentials, builders, or runtimes. Waiting also keeps
// a worker queued during a read-only preview from being silently dropped.
func (l *lifecycle) lockCreationOperation(ctx context.Context, observed api.Operation,
	load func(context.Context, string) (api.Operation, error)) (api.Operation, func(), error) {
	slot := l.sessionLockSlot(observed.SessionUUID)
	select {
	case <-ctx.Done():
		return api.Operation{}, nil, ctx.Err()
	case <-slot:
	}
	release := func() { slot <- struct{}{} }
	fresh, err := load(ctx, observed.ID)
	if err != nil {
		release()
		return api.Operation{}, nil, err
	}
	if fresh.Kind != "session.create" || fresh.SessionUUID != observed.SessionUUID || fresh.Status != "running" && fresh.Status != "blocked" {
		release()
		return api.Operation{}, nil, control.ErrConflict
	}
	if err = l.checkNoWorkspaceInspect(ctx, fresh.SessionUUID); err != nil {
		release()
		return api.Operation{}, nil, err
	}
	return fresh, release, nil
}

// A blocked early local request remains inspectable until an explicit Retry or
// exact Create replay. Restart must not turn a failed planned branch CAS into
// fresh UUID effects while the user is preparing replacement. Base-image
// principals-ready failures also stay dormant with their existing key/endpoint.
// Running intents and later/origin recovery keep automatic reconciliation.
func deferBlockedCreationUntilRetry(op api.Operation) bool {
	if op.Kind != "session.create" || op.Status != "blocked" || op.Phase != "source-ready" && op.Phase != "branch-assigned" && op.Phase != "principals-ready" {
		return false
	}
	var req control.ReserveSessionRequest
	ev, err := control.Evidence(op)
	return err == nil && json.Unmarshal(op.Request, &req) == nil && (control.ReplaceableCreationEvidence(req, ev) && control.ReplaceableCreationPhase(op, ev) || control.SafeCreateCleanupEvidence(req, ev) && control.SafeCreateCleanupPhase(op))
}
