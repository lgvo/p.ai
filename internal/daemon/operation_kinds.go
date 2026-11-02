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
	// resumeAtStart reports whether daemon start resumes an unfinished
	// operation without an explicit Retry; nil resumes every one.
	resumeAtStart func(l *lifecycle, ctx context.Context, op api.Operation) (bool, error)
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
			refuseRetry: creationAwaitsInspection, admit: creationAwaitsInspection, resumeAtStart: resumeCreationAtStart},
		control.KindProjectDelete:        {process: (*lifecycle).processProjectDelete, removal: "deleting"},
		control.KindWorkspaceLossInspect: {process: (*lifecycle).processWorkspaceInspect, refuseRetry: inspectionNeedsFreshRequest},
		control.KindSessionDiscard:       {process: (*lifecycle).processDiscard, removal: "discarding"},
		control.KindSessionDelete:        {process: (*lifecycle).processDiscard, removal: "deleting"},
		control.KindCreateCleanup:        {process: (*lifecycle).processCreateCleanup, refuseRetry: cleanupFoundStale, removal: "discarding"},
		control.KindSessionRename:        {process: (*lifecycle).processRename},
		control.KindRetainedRename:       {process: (*lifecycle).processRetainedRename},
		control.KindRetainedDelete:       {process: (*lifecycle).processRetainedDelete},
		control.KindSessionRepair:        {process: (*lifecycle).processRepair},
		control.KindRepairPrepare:        {process: (*lifecycle).processRepairPrepare},
		control.KindRefRepair:            {process: (*lifecycle).processRefRepair},
		control.KindPrincipalRepair:      {process: (*lifecycle).processPrincipalRepair},
		control.KindRecordRepair:         {process: (*lifecycle).processRecordRepair, removal: "deleting"},
	}
}

// creationAwaitsInspection keeps an unfinished creation from running while a
// workspace inspection holds its session.
func creationAwaitsInspection(l *lifecycle, ctx context.Context, op api.Operation) error {
	if op.Status == "completed" || op.Status == "superseded" {
		return nil
	}
	return l.checkNoWorkspaceInspect(ctx, op.SessionUUID)
}

// resumeCreationAtStart leaves creations held by a workspace inspection, and
// blocked early local creations, for an explicit Retry.
func resumeCreationAtStart(l *lifecycle, ctx context.Context, op api.Operation) (bool, error) {
	if err := l.checkNoWorkspaceInspect(ctx, op.SessionUUID); errors.Is(err, control.ErrConflict) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return !deferBlockedCreationUntilRetry(op), nil
}

func inspectionNeedsFreshRequest(_ *lifecycle, _ context.Context, op api.Operation) error {
	if api.WorkspaceInspectionRequiresFreshRequest(op) {
		return control.ErrConflict
	}
	return nil
}

func cleanupFoundStale(_ *lifecycle, _ context.Context, op api.Operation) error {
	if op.Status == "failed" && op.Phase == "stale" {
		return control.ErrConflict
	}
	return nil
}

// replayRequest returns the operation an earlier identical request recorded
// under key, scheduling it again while it runs. replayed is false when key is
// unused; a key recorded for another kind or another request is a conflict.
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
		return api.Operation{}, false, control.ErrConflict
	}
	if prior.Status == "running" {
		_ = l.enqueue(prior.ID)
	}
	return prior, true, nil
}
