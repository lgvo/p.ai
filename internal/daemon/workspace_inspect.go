package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

// InspectWorkspace accepts only one closed read-only operation. The operation
// and helper name are durable before Incus init; it never runs Git in the
// daemon or activates the target session.
func (l *lifecycle) InspectWorkspace(ctx context.Context, req control.WorkspaceInspectRequest) (control.Operation, error) {
	return l.inspectWorkspaceRead(ctx, req, "workspace.inspect")
}

func (l *lifecycle) InspectWorkspaceLoss(ctx context.Context, req control.WorkspaceInspectRequest) (control.Operation, error) {
	return l.inspectWorkspaceRead(ctx, req, "workspace.loss.inspect")
}

func (l *lifecycle) inspectWorkspaceRead(ctx context.Context, req control.WorkspaceInspectRequest, kind string) (control.Operation, error) {
	if req.Key == "" || len(req.Key) > 128 || len(req.SessionUUID) != 36 {
		return control.Operation{}, control.ErrInvalid
	}
	release, err := l.lockSession(ctx, req.SessionUUID)
	if err != nil {
		return control.Operation{}, err
	}
	defer release()
	l.mu.Lock()
	attached := l.hasAttachmentLocked(req.SessionUUID)
	l.mu.Unlock()
	if attached {
		return control.Operation{}, control.ErrConflict
	}
	if prior, err := l.store.GetOperationByKey(ctx, req.Key); err == nil {
		var old control.WorkspaceInspectRequest
		if prior.Kind != kind || json.Unmarshal(prior.Request, &old) != nil || old != req {
			return control.Operation{}, control.ErrConflict
		}
		if prior.Status == "running" {
			_ = l.enqueue(prior.ID)
		}
		return prior, nil
	} else if !errors.Is(err, control.ErrNotFound) {
		return control.Operation{}, err
	}
	s, err := l.store.GetSession(ctx, req.SessionUUID)
	if err != nil {
		return control.Operation{}, err
	}
	if s.Registry != "established" {
		return control.Operation{}, control.ErrConflict
	}
	native, err := l.runtimeSession(ctx, s)
	if err != nil {
		return control.Operation{}, err
	}
	if err := l.runtime.CheckWorkspaceHelperCapacity(ctx); err != nil {
		return control.Operation{}, err
	}
	observed, err := l.runtime.Inspect(ctx, native)
	if err != nil || !observed.Exists || observed.Status != "Running" && observed.Status != "Stopped" {
		return control.Operation{}, errors.Join(err, control.ErrConflict)
	}
	if kind == "workspace.loss.inspect" && (observed.IncusUUID == "" || observed.Generation == "") {
		return control.Operation{}, errors.New("workspace instance generation unavailable")
	}
	ev := control.WorkspaceInspectEvidence{InstanceUUID: l.instanceID, ImageFingerprint: native.ImageFingerprint,
		BaseFingerprint: l.cfg.BaseImageFingerprint, SourceIncusUUID: observed.IncusUUID,
		SourceGeneration: observed.Generation, OriginalStatus: observed.Status}
	var op control.Operation
	if kind == "workspace.loss.inspect" {
		op, err = l.store.BeginWorkspaceLossInspect(ctx, req, ev)
	} else {
		op, err = l.store.BeginWorkspaceInspect(ctx, req, ev)
	}
	if err != nil {
		return control.Operation{}, err
	}
	if err := l.enqueue(op.ID); err != nil {
		return op, err
	}
	return op, nil
}

func (l *lifecycle) checkNoWorkspaceInspect(ctx context.Context, sessionUUID string) error {
	if l.store == nil {
		return nil
	} // unit-only lifecycle seam; production always owns a Store
	_, active, err := l.store.ActiveWorkspaceInspect(ctx, sessionUUID)
	if err != nil {
		return err
	}
	if active {
		return control.ErrConflict
	}
	return nil
}

func (l *lifecycle) processWorkspaceInspect(op control.Operation) {
	if op.Status != "running" {
		return
	}
	var ev control.WorkspaceInspectEvidence
	if json.Unmarshal(op.Evidence, &ev) != nil || ev.InstanceUUID != l.instanceID || ev.BaseFingerprint != l.cfg.BaseImageFingerprint ||
		ev.OriginalStatus != "Stopped" && ev.OriginalStatus != "Running" || len(op.SessionUUID) != 36 {
		l.blockWorkspaceInspect(op, errors.New("workspace operation identity unavailable"))
		return
	}
	if op.Kind == "workspace.loss.inspect" && (ev.SourceIncusUUID == "" || ev.SourceGeneration == "") {
		l.blockWorkspaceInspect(op, errors.New("workspace instance generation unavailable"))
		return
	}
	ctx := l.ctx
	entryPhase := op.Phase
	s, err := l.store.GetSession(ctx, op.SessionUUID)
	if err != nil || s.Registry != "established" || s.Project != op.Project {
		l.blockWorkspaceInspect(op, errors.Join(err, errors.New("workspace session changed")))
		return
	}
	source, err := l.runtimeSession(ctx, s)
	if err != nil || source.ImageFingerprint != ev.ImageFingerprint {
		l.blockWorkspaceInspect(op, errors.Join(err, errors.New("workspace source image changed")))
		return
	}
	helper := runtimeincus.WorkspaceHelper(ev.InstanceUUID, op.ID, op.SessionUUID, op.Project, ev.BaseFingerprint)
	lossSourceMatches := func(observed runtimeincus.Observation) bool {
		return op.Kind != "workspace.loss.inspect" || observed.IncusUUID == ev.SourceIncusUUID && observed.Generation == ev.SourceGeneration
	}
	advance := func(phase string) error {
		raw, err := json.Marshal(ev)
		if err != nil || len(raw) > 16384 {
			return errors.New("workspace evidence exceeded bound")
		}
		if err := l.store.AdvanceOperation(ctx, op.ID, "running", phase, true, raw, ""); err != nil {
			return err
		}
		op.Phase, op.Evidence, op.Committed = phase, raw, true
		return nil
	}
	fail := func(cause error) { l.failWorkspaceInspect(op, ev, source, helper, cause) }
	switch op.Phase {
	case "helper-intent":
		observed, e := l.runtime.InspectWorkspaceHelper(ctx, helper)
		if e != nil {
			fail(e)
			return
		}
		if observed.Exists {
			fail(errors.New("workspace helper appeared before durable init marker"))
			return
		}
		if _, e = l.runtime.CreateWorkspaceHelper(ctx, helper, func() error { return advance("init-issued") }); e != nil {
			fail(e)
			return
		}
		fallthrough
	case "init-issued":
		var observed runtimeincus.Observation
		var e error
		if entryPhase == "init-issued" {
			observed, e = l.runtime.ReconcileWorkspaceHelperInit(ctx, helper)
		} else {
			observed, e = l.runtime.InspectWorkspaceHelper(ctx, helper)
		}
		if e != nil {
			fail(e)
			return
		}
		if !observed.Exists {
			fail(errors.New("workspace helper init outcome unresolved"))
			return
		}
		if observed.Status == "Stopped" {
			if e = l.runtime.StartWorkspaceHelper(ctx, helper); e != nil {
				fail(e)
				return
			}
		} else if observed.Status == "Running" {
			if e = l.runtime.VerifyWorkspaceHelperBoot(ctx, helper); e != nil {
				fail(e)
				return
			}
		} else {
			fail(errors.New("workspace helper state unavailable"))
			return
		}
		if e = advance("helper-ready"); e != nil {
			fail(e)
			return
		}
		fallthrough
	case "helper-ready":
		observed, e := l.runtime.Inspect(ctx, source)
		if e != nil || !observed.Exists || !lossSourceMatches(observed) {
			fail(errors.Join(e, errors.New("workspace source unavailable")))
			return
		}
		if ev.OriginalStatus == "Running" {
			if observed.Status != "Running" {
				fail(errors.New("workspace source status changed"))
				return
			}
			if op.Kind == "workspace.loss.inspect" {
				e = l.runtime.FreezeWorkspaceSourceExact(ctx, source, ev.SourceIncusUUID, ev.SourceGeneration, func() error { return advance("freeze-intent") })
			} else {
				e = l.runtime.FreezeWorkspaceSource(ctx, source, func() error { return advance("freeze-intent") })
			}
			if e != nil {
				fail(e)
				return
			}
		} else if observed.Status != "Stopped" {
			fail(errors.New("stopped workspace source changed"))
			return
		}
		if e = advance("quiescent"); e != nil {
			fail(e)
			return
		}
		// A restart from quiescent cannot trust a possibly partial helper copy.
		// The only same-turn path proceeds to fresh extraction below.
		fallthrough
	case "freeze-intent":
		if entryPhase == "freeze-intent" {
			var e error
			if op.Kind == "workspace.loss.inspect" {
				e = l.runtime.ReconcileWorkspaceFreezeExact(ctx, source, ev.SourceIncusUUID, ev.SourceGeneration)
			} else {
				e = l.runtime.ReconcileWorkspaceFreeze(ctx, source)
			}
			if e != nil {
				fail(e)
				return
			}
			if e := advance("quiescent"); e != nil {
				fail(e)
				return
			}
		}
		fallthrough
	case "quiescent":
		if entryPhase == "quiescent" {
			fail(errors.New("workspace export interrupted before accepted analysis"))
			return
		}
		var e error
		if op.Kind == "workspace.loss.inspect" {
			ev.Result, e = l.inspectWorkspaceLossResult(ctx, s, source, helper, ev)
		} else {
			var snapshot runtimeincus.WorkspaceSnapshot
			snapshot, e = l.runtime.ExportWorkspace(ctx, source)
			if e == nil {
				e = l.runtime.InstallWorkspaceHelperCopy(ctx, helper, snapshot)
			}
			if e == nil {
				var result runtimeincus.WorkspaceAnalysis
				result, e = l.runtime.AnalyzeWorkspaceHelper(ctx, helper)
				if e == nil {
					ev.Result, e = json.Marshal(result)
				}
			}
		}
		if e != nil {
			fail(e)
			return
		}
		if len(ev.Result) > 12<<10 {
			fail(errors.New("workspace result exceeded bound"))
			return
		}
		if e = advance("analyzed"); e != nil {
			fail(e)
			return
		}
		fallthrough
	case "analyzed":
		if len(ev.Result) == 0 {
			fail(errors.New("workspace analysis missing"))
			return
		}
		if e := l.runtime.DeleteWorkspaceHelper(ctx, helper); e != nil {
			fail(e)
			return
		}
		if e := advance("helper-absent"); e != nil {
			fail(e)
			return
		}
		fallthrough
	case "helper-absent":
		if ev.OriginalStatus == "Running" {
			var e error
			if op.Kind == "workspace.loss.inspect" {
				e = l.runtime.ThawWorkspaceSourceExact(ctx, source, ev.SourceIncusUUID, ev.SourceGeneration)
			} else {
				e = l.runtime.ThawWorkspaceSource(ctx, source)
			}
			if e != nil {
				fail(e)
				return
			}
		} else {
			observed, e := l.runtime.Inspect(ctx, source)
			if e != nil || !observed.Exists || observed.Status != "Stopped" || !lossSourceMatches(observed) {
				fail(errors.Join(e, errors.New("stopped source changed")))
				return
			}
		}
		if e := advance("source-restored"); e != nil {
			fail(e)
			return
		}
		fallthrough
	case "source-restored":
		if observed, e := l.runtime.InspectWorkspaceHelper(ctx, helper); e != nil || observed.Exists {
			fail(errors.Join(e, errors.New("workspace helper still present")))
			return
		}
		if observed, e := l.runtime.Inspect(ctx, source); e != nil || !observed.Exists || observed.Status != ev.OriginalStatus || !lossSourceMatches(observed) {
			fail(errors.Join(e, errors.New("workspace source not restored")))
			return
		}
		if e := l.store.AdvanceOperation(ctx, op.ID, "completed", "inspected", true, op.Evidence, ""); e != nil {
			fail(e)
		}
	default:
		l.blockWorkspaceInspect(op, errors.New("workspace phase unavailable"))
	}
}

func (l *lifecycle) failWorkspaceInspect(op control.Operation, ev control.WorkspaceInspectEvidence, source, helper runtimeincus.Session, cause error) {
	if l.ctx.Err() != nil {
		return
	}
	// The helper init may still be in flight if its CLI failed or timed out.
	// In that phase a single miss cannot release the durable guard.
	if op.Phase == "init-issued" || op.Phase == "freeze-intent" {
		l.blockWorkspaceInspect(op, fmt.Errorf("workspace native effect outcome unresolved; exact positive state or administrative resolution required: %w", cause))
		return
	}
	if op.Kind == "workspace.loss.inspect" {
		observed, err := l.runtime.Inspect(l.ctx, source)
		if err != nil || !observed.Exists || observed.IncusUUID != ev.SourceIncusUUID || observed.Generation != ev.SourceGeneration {
			l.blockWorkspaceInspect(op, errors.Join(cause, err, errors.New("workspace source generation changed before cleanup")))
			return
		}
	}
	if op.Phase == "helper-ready" && ev.OriginalStatus == "Running" {
		observed, err := l.runtime.Inspect(l.ctx, source)
		if err != nil || !observed.Exists || observed.Status != "Running" ||
			op.Kind == "workspace.loss.inspect" && (observed.IncusUUID != ev.SourceIncusUUID || observed.Generation != ev.SourceGeneration) {
			l.blockWorkspaceInspect(op, errors.Join(cause, err, errors.New("workspace source changed before owned freeze")))
			return
		}
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := l.runtime.DeleteWorkspaceHelper(cleanupCtx, helper); err != nil {
		l.blockWorkspaceInspect(op, errors.Join(cause, fmt.Errorf("exact helper cleanup unresolved: %w", err)))
		return
	}
	ownedFreeze := op.Phase == "quiescent" || op.Phase == "analyzed" || op.Phase == "helper-absent" || op.Phase == "source-restored"
	if ev.OriginalStatus == "Running" && ownedFreeze {
		var err error
		if op.Kind == "workspace.loss.inspect" {
			err = l.runtime.ThawWorkspaceSourceExact(cleanupCtx, source, ev.SourceIncusUUID, ev.SourceGeneration)
		} else {
			err = l.runtime.ThawWorkspaceSource(cleanupCtx, source)
		}
		if err != nil {
			l.blockWorkspaceInspect(op, errors.Join(cause, fmt.Errorf("source thaw unresolved: %w", err)))
			return
		}
	} else {
		observed, err := l.runtime.Inspect(cleanupCtx, source)
		if err != nil || !observed.Exists || observed.Status != ev.OriginalStatus ||
			op.Kind == "workspace.loss.inspect" && (observed.IncusUUID != ev.SourceIncusUUID || observed.Generation != ev.SourceGeneration) {
			l.blockWorkspaceInspect(op, errors.Join(cause, err, errors.New("source state changed before owned freeze")))
			return
		}
	}
	if op.Kind == "workspace.loss.inspect" {
		observed, err := l.runtime.Inspect(cleanupCtx, source)
		if err != nil || !observed.Exists || observed.Status != ev.OriginalStatus || observed.IncusUUID != ev.SourceIncusUUID || observed.Generation != ev.SourceGeneration {
			l.blockWorkspaceInspect(op, errors.Join(cause, err, errors.New("workspace source generation changed before cleanup completion")))
			return
		}
	}
	message := fmt.Sprintf("workspace inspection unavailable after exact cleanup: %v", cause)
	if len(message) > 900 {
		message = message[:900]
	}
	_ = l.store.AdvanceOperation(cleanupCtx, op.ID, "failed", "cleaned", true, op.Evidence, message)
}

func (l *lifecycle) blockWorkspaceInspect(op control.Operation, cause error) {
	if l.ctx.Err() != nil {
		return
	}
	message := fmt.Sprintf("workspace inspection blocked: %v", cause)
	if len(message) > 900 {
		message = message[:900]
	}
	_ = l.store.AdvanceOperation(l.ctx, op.ID, "blocked", op.Phase, true, op.Evidence, message)
}
