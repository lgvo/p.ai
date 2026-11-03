package daemon

import (
	"context"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
)

// lockCreationOperation serializes every creation effect with replacement of
// that session. A replay can enqueue after replacement's last absence proof:
// waiting here keeps it from acting until the handoff has committed. Reloading
// then rejects the superseded request before reading its old session or
// creating endpoints, credentials, builders, or runtimes. Waiting also keeps
// a worker queued during a read-only dry run from being silently dropped.
// The hold belongs to the operation (holdSession).
func (l *lifecycle) lockCreationOperation(ctx context.Context, observed api.Operation,
	load func(context.Context, string) (api.Operation, error)) (api.Operation, error) {
	if err := l.holdSession(ctx, observed.ID, observed.SessionUUID); err != nil {
		return api.Operation{}, err
	}
	fresh, err := load(ctx, observed.ID)
	if err != nil {
		return api.Operation{}, err
	}
	if fresh.Kind != "session.create" || fresh.SessionUUID != observed.SessionUUID || fresh.Status != "running" && fresh.Status != "blocked" {
		return api.Operation{}, control.ErrConflict
	}
	if err = l.admitSessionWork(ctx, fresh.SessionUUID, fresh.ID); err != nil {
		return api.Operation{}, err
	}
	return fresh, nil
}
