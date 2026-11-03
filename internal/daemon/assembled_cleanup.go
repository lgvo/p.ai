package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
)

// reviewAssembledCleanupRuntime binds the creator's exact stopped container.
// P does not look inside it: the plan states that it and everything in it is
// lost.
func (l *lifecycle) reviewAssembledCleanupRuntime(ctx context.Context, session control.Session, creator api.Operation) (*api.RemovalRuntime, error) {
	source, err := l.nativeSession(ctx, session)
	if err != nil {
		return nil, err
	}
	observed, err := l.runtime.Inspect(ctx, source)
	if err != nil || !observed.Exists || observed.Status != "Stopped" || observed.IncusUUID == "" || observed.Generation == "" {
		return nil, errors.Join(err, errors.New("failed creation stopped runtime unavailable"))
	}
	if err = l.runtime.CheckAssembledCreationSource(ctx, source, creator.ID, observed.IncusUUID, observed.Generation); err != nil {
		return nil, err
	}
	return &api.RemovalRuntime{Condition: "present", IncusProject: l.incus.Project, InstanceName: "p-" + session.UUID,
		IncusUUID: observed.IncusUUID, Generation: observed.Generation, ImageFingerprint: source.ImageFingerprint, OriginalStatus: "Stopped"}, nil
}

// The caller owns the session lock and reloads the durable operation before
// entering. It never starts the source.
func (l *lifecycle) processAssembledCreateCleanup(op api.Operation, ev control.CreateCleanupEvidence) {
	ctx := l.ctx
	r := ev.Review.Runtime
	block := func(cause error) {
		if ctx.Err() != nil {
			return
		}
		message := fmt.Sprintf("assembled failed creation cleanup blocked: %v", cause)
		_ = l.store.AdvanceOperation(ctx, op.ID, "blocked", op.Phase, op.Committed, op.Evidence, control.OperationReason(cause), message)
	}
	if r == nil || ev.InstanceUUID != l.instanceID || r.IncusProject != l.incus.Project || ev.Review.ImageFingerprint != l.cfg.BaseImageFingerprint || r.OriginalStatus != "Stopped" ||
		l.store.ValidateAssembledCleanupAuthority(ctx, op) != nil {
		block(errors.New("durable creator/session/ref authority changed"))
		return
	}
	session, err := l.store.GetSession(ctx, op.SessionUUID)
	if err != nil {
		block(err)
		return
	}
	creator, err := l.store.GetOperation(ctx, ev.Review.OldOperationID)
	created, evidenceErr := control.Evidence(creator)
	if err != nil || evidenceErr != nil || created.Selection != l.selection() {
		block(errors.New("pinned creator module selection unavailable"))
		return
	}
	source, err := l.nativeSession(ctx, session)
	if err != nil {
		block(err)
		return
	}
	advance := func(phase string) error {
		raw, e := json.Marshal(ev)
		if e != nil || len(raw) > 16384 {
			return errors.New("assembled cleanup evidence exceeded bound")
		}
		if e = l.store.AdvanceOperation(ctx, op.ID, "running", phase, op.Committed, raw, "", ""); e != nil {
			return e
		}
		op.Phase, op.Evidence, op.Status = phase, raw, "running"
		return nil
	}
	proveSource := func(call context.Context) error {
		return l.runtime.CheckAssembledCreationSource(call, source, ev.Review.OldOperationID, r.IncusUUID, r.Generation)
	}
	proveAbsent := func(call context.Context) error {
		return l.createCleanupNativeAbsent(call, op.SessionUUID, ev.Review, ev.InstanceUUID)
	}
	verifyRefs := func(call context.Context) error {
		tip, exists, e := l.git.backend.InspectBranchRef(call, op.Project, session.Branch)
		if e != nil || !exists || tip != ev.Review.AssignedBranch.OID {
			return errors.Join(e, control.ErrConflict)
		}
		return nil
	}
	verifyLocal := func() error {
		e := l.endpoints.withLock(func() error {
			return checkReplacementEndpointsLocked(l.endpoints, ev.Review.Provisional.Cleanup, false)
		})
		if e != nil {
			return e
		}
		return checkReplacementKey(l.store.StateDir(), ev.Review.Provisional.Cleanup, false)
	}
	stale := func(reason error) {
		if op.Committed {
			block(reason)
			return
		}
		if op.Phase != "stale-cleanup" {
			if e := advance("stale-cleanup"); e != nil {
				block(e)
				return
			}
		}
		if e := l.store.FailStaleAssembledCleanup(ctx, op, "assembled cleanup confirmation stale; source preserved: "+reason.Error()); e != nil {
			block(e)
			return
		}
	}
	if op.Status == "blocked" {
		if err = advance(op.Phase); err != nil {
			return
		}
	}
	switch op.Phase {
	case "validated":
		if err = proveSource(ctx); err != nil {
			stale(err)
			return
		}
		if err = verifyRefs(ctx); err != nil {
			stale(err)
			return
		}
		if err = verifyLocal(); err != nil {
			stale(err)
			return
		}
		err = l.runtime.DeleteForDiscardExact(ctx, source, r.IncusUUID, r.Generation, func() error {
			e := l.store.CommitAssembledCleanupDelete(ctx, op, func(call context.Context) error {
				if e := proveSource(call); e != nil {
					return e
				}
				if e := verifyRefs(call); e != nil {
					return e
				}
				return verifyLocal()
			})
			if e == nil {
				op.Committed, op.Phase = true, "delete-issued"
			}
			return e
		})
		if err != nil {
			if !op.Committed {
				stale(err)
			} else {
				block(err)
			}
			return
		}
		ev.RuntimeAbsent = true
		if err = advance("runtime-absent"); err != nil {
			block(err)
			return
		}
		fallthrough
	case "runtime-absent":
		if err = proveAbsent(ctx); err != nil {
			block(err)
			return
		}
		if err = cleanupReplacementLocal(ctx, l.store.StateDir(), l.endpoints, ev.Review.Provisional.Cleanup, proveAbsent); err != nil {
			block(err)
			return
		}
		ev.LocalComplete = true
		if err = advance("local-complete"); err != nil {
			block(err)
			return
		}
		fallthrough
	case "local-complete":
		err = l.store.CompleteCreateCleanup(ctx, op.ID, func(call context.Context, accepted control.CreateCleanupEvidence) error {
			if !reflect.DeepEqual(accepted, ev) {
				return control.ErrConflict
			}
			if e := proveAbsent(call); e != nil {
				return e
			}
			if e := l.createCleanupLocalAbsent(op.SessionUUID); e != nil {
				return e
			}
			tip, exists, e := l.git.backend.InspectBranchRef(call, op.Project, session.Branch)
			if e != nil || !exists || tip != ev.Review.AssignedBranch.OID {
				return errors.Join(e, control.ErrConflict)
			}
			return nil
		})
		if err != nil {
			block(err)
			return
		}
	case "delete-issued":
		block(control.OutcomeUnknown(fmt.Errorf("native delete outcome unresolved for %s UUID %s generation %s", r.InstanceName, r.IncusUUID, r.Generation)))
	case "stale-cleanup":
		stale(errors.New("prior pre-deletion check stale"))
	default:
		block(errors.New("assembled cleanup phase unavailable"))
	}
}
