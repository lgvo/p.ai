package daemon

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
)

// Retry accepts only kinds listed in operationKinds.
type operationKind struct {
	process func(l *lifecycle, op api.Operation)
	// refuseRetry refuses an explicit Retry the kind cannot resume; nil
	// accepts every unfinished operation.
	refuseRetry func(l *lifecycle, ctx context.Context, op api.Operation) error
	// admit refuses to schedule an operation that must not run now; nil admits
	// every one.
	admit func(l *lifecycle, ctx context.Context, op api.Operation) error
	// removal is the condition of a session this kind is removing; empty when
	// the kind removes no session.
	removal string
}

// operationKinds is filled by init: its workers schedule work through enqueue,
// which reads the table.
var operationKinds map[string]operationKind

func init() {
	operationKinds = map[string]operationKind{
		control.KindProjectCreate: {process: (*lifecycle).processCreation},
		control.KindSessionCreate: {process: (*lifecycle).processCreation,
			refuseRetry: creationAwaitsCleanup, admit: creationAwaitsCleanup},
		control.KindProjectDelete:  {process: (*lifecycle).processProjectDelete, removal: "deleting"},
		control.KindSessionDiscard: {process: (*lifecycle).processDiscard, removal: "discarding"},
		control.KindSessionDelete:  {process: (*lifecycle).processDiscard, removal: "deleting"},
		control.KindCreateCleanup:  {process: (*lifecycle).processCreateCleanup, refuseRetry: cleanupFoundStale, removal: "discarding"},
		control.KindSessionRename:  {process: (*lifecycle).processRename},
		control.KindRetainedRename: {process: (*lifecycle).processRetainedRename},
		control.KindRetainedDelete: {process: (*lifecycle).processRetainedDelete},
	}
}

// creationAwaitsCleanup keeps an unfinished creation from running while an
// assembled cleanup holds its session.
func creationAwaitsCleanup(l *lifecycle, ctx context.Context, op api.Operation) error {
	if op.Status == "completed" || op.Status == "superseded" {
		return nil
	}
	return l.admitSessionWork(ctx, op.SessionUUID, op.ID)
}

func cleanupFoundStale(_ *lifecycle, _ context.Context, op api.Operation) error {
	if op.Status == "failed" && op.Phase == "stale" {
		// A cleanup that found its review stale ends there; a new review
		// starts another.
		return control.Conflict(api.ReasonOperationNotRetryable, api.TargetOperation(op.ID))
	}
	return nil
}

// replayRequest returns the operation an earlier identical request recorded
// under key, scheduling it again while it runs. replayed is false when key is
// unused; a key recorded for another kind or another request is reused.
func replayRequest[R comparable](l *lifecycle, ctx context.Context, key, kind string, req R) (op api.Operation, replayed bool, err error) {
	prior, err := l.store.GetOperationByKey(ctx, key)
	if errors.Is(err, control.ErrNotFound) {
		return api.Operation{}, false, nil
	}
	if err != nil {
		return api.Operation{}, false, err
	}
	var saved R
	if prior.Kind != kind || json.Unmarshal(prior.Request, &saved) != nil || saved != req {
		return api.Operation{}, false, control.KeyReused()
	}
	if prior.Status == "running" {
		_ = l.enqueue(prior.ID)
	}
	return prior, true, nil
}
