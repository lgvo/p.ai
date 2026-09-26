package daemon

import (
	"context"

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
