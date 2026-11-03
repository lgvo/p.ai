package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

var errDiscardRefsChanged = errors.New("P refs changed after the removal was validated")

func discardPRefsDigest(refs []plugin.GitRef) (string, error) {
	data, err := json.Marshal(refs)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (l *lifecycle) verifyDiscardRefs(ctx context.Context, ev control.DiscardEvidence) error {
	if err := l.discardAssignedTip(ctx, ev); err != nil {
		if errors.Is(err, control.ErrConflict) {
			return errDiscardRefsChanged
		}
		return err
	}
	current, err := l.git.backend.AssignedBranchSnapshot(ctx, ev.Project, ev.Branch, true)
	if err != nil {
		return err
	}
	digest, err := discardPRefsDigest(current.PRefs)
	if err != nil {
		return err
	}
	if current.AssignedTip != ev.AssignedTip || digest != ev.PRefsDigest {
		return errDiscardRefsChanged
	}
	return nil
}

func (l *lifecycle) advanceDiscard(op *api.Operation, ev control.DiscardEvidence, phase string) error {
	raw, err := json.Marshal(ev)
	if err != nil || len(raw) > 4096 {
		return errors.New("discard evidence exceeds bound")
	}
	if err = l.store.AdvanceOperation(l.ctx, op.ID, "running", phase, op.Committed, raw, "", ""); err != nil {
		return err
	}
	op.Phase, op.Evidence, op.Status = phase, raw, "running"
	return nil
}

func (l *lifecycle) blockDiscard(op api.Operation, cause error) {
	if l.ctx.Err() != nil {
		return
	}
	diagnostic := "discard authority unavailable"
	if cause != nil {
		diagnostic = cause.Error()
	}
	_ = l.store.AdvanceOperation(l.ctx, op.ID, "blocked", op.Phase, op.Committed, op.Evidence, control.OperationReason(cause), diagnostic)
}

// discardSourceMatches reports whether o is still the reviewed runtime, the
// one at incarnation at. Its state may have changed since its plan: P
// stops a running one itself.
func discardSourceMatches(o runtimedriver.Observation, at runtimedriver.Incarnation) bool {
	return at != "" && o.Incarnation == at && removableState(o.State)
}

// stopRemovalSource stops the reviewed runtime of a removal when it runs,
// recording stop-issued first. Stop is idempotent by reference, so a removal
// that finds its stop issued sends it again.
func (l *lifecycle) stopRemovalSource(ctx context.Context, op *api.Operation, ev control.DiscardEvidence, spec runtimedriver.Session) error {
	at := recordedIncarnation(ev.IncusUUID, ev.Generation)
	observed, err := l.driver.Inspect(ctx, spec)
	if err != nil || !discardSourceMatches(observed, at) {
		return errors.Join(err, errors.New("discard source generation or state changed before stop"))
	}
	if observed.State == runtimedriver.StateStopped {
		return nil
	}
	if op.Phase != "stop-issued" {
		if err = l.advanceDiscard(op, ev, "stop-issued"); err != nil {
			return err
		}
	}
	stopped, err := l.driver.Stop(ctx, spec)
	if err != nil || stopped.State != runtimedriver.StateStopped || stopped.Incarnation != at {
		return errors.Join(err, errors.New("exact discard stop postcondition unavailable"))
	}
	return nil
}

func (l *lifecycle) discardAssignedTip(ctx context.Context, ev control.DiscardEvidence) error {
	tip, exists, err := l.git.backend.InspectBranchRef(ctx, ev.Project, ev.Branch)
	if err != nil {
		return err
	}
	if ev.AssignedTip == "" {
		if exists || tip != "" {
			return control.ErrConflict
		}
		return nil
	}
	if !exists || tip != ev.AssignedTip {
		return control.ErrConflict
	}
	return nil
}

func (l *lifecycle) deleteAssignedAbsent(ctx context.Context, ev control.DiscardEvidence) error {
	tip, exists, err := l.git.backend.InspectBranchRef(ctx, ev.Project, ev.Branch)
	if err != nil {
		return err
	}
	if exists || tip != "" {
		return control.ErrConflict
	}
	return nil
}

// rollbackStaleDiscard fails a removal whose reviewed facts changed before its
// commit and releases its guard; nothing was removed yet.
func (l *lifecycle) rollbackStaleDiscard(op api.Operation, ev control.DiscardEvidence, reason error) {
	diagnostic := fmt.Sprintf("%s confirmation stale: %v", ev.Action, reason)
	var err error
	if ev.Action == "delete" {
		err = l.store.FailStaleDelete(l.ctx, op.ID, diagnostic)
	} else {
		err = l.store.FailStaleDiscard(l.ctx, op.ID, diagnostic)
	}
	if err != nil {
		l.blockDiscard(op, err)
	}
}

func (l *lifecycle) processDiscard(op api.Operation) {
	if op.Status != "running" {
		return
	}
	var ev control.DiscardEvidence
	if json.Unmarshal(op.Evidence, &ev) != nil {
		l.blockDiscard(op, errors.New("removal durable evidence unreadable"))
		return
	}
	if (ev.Action != "discard" && ev.Action != "delete") || op.Kind != "session."+ev.Action || ev.Project != op.Project || ev.InstanceUUID != l.instanceID || ev.BaseFingerprint != l.cfg.BaseImageFingerprint ||
		ev.IncusProject != l.incus.Project || ev.InstanceName != "p-"+op.SessionUUID || ev.Branch == "" {
		l.blockDiscard(op, errors.New("discard durable identity unavailable"))
		return
	}
	ctx := l.ctx
	session, err := l.store.GetSession(ctx, op.SessionUUID)
	if err != nil || session.Project != op.Project || session.Branch != ev.Branch || session.PolicySHA256 != ev.PolicySHA256 ||
		session.Registry != "established" && session.Registry != "removing" {
		l.blockDiscard(op, errors.Join(err, errors.New("discard session identity changed")))
		return
	}
	spec, err := l.sessionSpec(ctx, session)
	if err != nil || string(spec.Image) != ev.ImageFingerprint && !ev.MissingRuntime {
		l.blockDiscard(op, errors.Join(err, errors.New("discard runtime selection changed")))
		return
	}
	at := recordedIncarnation(ev.IncusUUID, ev.Generation)
	entry := op.Phase
	switch op.Phase {
	case "guard-pending":
		if err = l.store.SetGitRefGuard(ctx, ev.Project, ev.Branch, op.ID, true); err != nil {
			l.blockDiscard(op, err)
			return
		}
		if err = l.advanceDiscard(&op, ev, "guarded"); err != nil {
			l.blockDiscard(op, err)
			return
		}
		fallthrough
	case "guarded":
		if err = l.discardAssignedTip(ctx, ev); err != nil {
			l.rollbackStaleDiscard(op, ev, err)
			return
		}
		observed, e := l.driver.Inspect(ctx, spec)
		if e != nil {
			l.blockDiscard(op, e)
			return
		}
		if ev.MissingRuntime {
			if observed.State != runtimedriver.StateAbsent {
				l.rollbackStaleDiscard(op, ev, errors.New("missing runtime reappeared"))
				return
			}
			if e = proveRuntimeAbsent(ctx, l.driver, spec); e != nil {
				l.blockDiscard(op, e)
				return
			}
		} else if !discardSourceMatches(observed, at) {
			l.rollbackStaleDiscard(op, ev, errors.New("runtime generation or state changed"))
			return
		}
		refs, e := l.git.backend.AssignedBranchSnapshot(ctx, ev.Project, ev.Branch, true)
		if e != nil {
			l.blockDiscard(op, e)
			return
		}
		ev.PRefsDigest, e = discardPRefsDigest(refs.PRefs)
		if e != nil {
			l.blockDiscard(op, e)
			return
		}
		if err = l.advanceDiscard(&op, ev, "validated"); err != nil {
			l.blockDiscard(op, err)
			return
		}
		fallthrough
	case "validated":
		observed, e := l.driver.Inspect(ctx, spec)
		if e != nil {
			l.blockDiscard(op, e)
			return
		}
		if ev.MissingRuntime && observed.State != runtimedriver.StateAbsent {
			l.rollbackStaleDiscard(op, ev, errors.New("missing runtime reappeared"))
			return
		}
		if !ev.MissingRuntime && !discardSourceMatches(observed, at) {
			l.rollbackStaleDiscard(op, ev, errors.New("validated discard source changed"))
			return
		}
		if err = l.discardAssignedTip(ctx, ev); err != nil {
			l.rollbackStaleDiscard(op, ev, err)
			return
		}
		if err = l.verifyDeleteReview(ctx, ev); err != nil {
			if errors.Is(err, control.ErrConflict) || errors.Is(err, errDeleteReviewChanged) {
				l.rollbackStaleDiscard(op, ev, err)
			} else {
				l.blockDiscard(op, err)
			}
			return
		}
		verify := func(c context.Context, facts control.DiscardEvidence) error {
			if facts.MissingRuntime {
				if e := proveRuntimeAbsent(c, l.driver, spec); e != nil {
					return e
				}
			}
			return l.verifyDiscardRefs(c, facts)
		}
		if ev.Action == "delete" {
			err = l.store.CommitDelete(ctx, op.ID, verify)
		} else {
			err = l.store.CommitDiscard(ctx, op.ID, verify)
		}
		if err != nil {
			if errors.Is(err, errDiscardRefsChanged) {
				l.rollbackStaleDiscard(op, ev, err)
				return
			}
			l.blockDiscard(op, err)
			return
		}
		op.Phase, op.Committed = "removal-committed", true
		fallthrough
	case "removal-committed":
		if ev.MissingRuntime {
			if e := proveRuntimeAbsent(ctx, l.driver, spec); e != nil {
				l.blockDiscard(op, errors.Join(e, errors.New("missing discard runtime reappeared")))
				return
			}
			if err = l.advanceDiscard(&op, ev, "runtime-absent"); err != nil {
				l.blockDiscard(op, err)
				return
			}
		} else {
			if err = l.stopRemovalSource(ctx, &op, ev, spec); err != nil {
				l.blockDiscard(op, err)
				return
			}
			if err = l.advanceDiscard(&op, ev, "source-stopped"); err != nil {
				l.blockDiscard(op, err)
				return
			}
		}
		fallthrough
	case "stop-issued":
		if entry == "stop-issued" {
			if err = l.stopRemovalSource(ctx, &op, ev, spec); err != nil {
				l.blockDiscard(op, err)
				return
			}
			if err = l.advanceDiscard(&op, ev, "source-stopped"); err != nil {
				l.blockDiscard(op, err)
				return
			}
		}
		fallthrough
	case "source-stopped":
		if !ev.MissingRuntime {
			if err = l.deleteRuntime(ctx, spec, at, func() error { return l.advanceDiscard(&op, ev, "delete-issued") }); err != nil {
				l.blockDiscard(op, err)
				return
			}
			if err = l.advanceDiscard(&op, ev, "runtime-absent"); err != nil {
				l.blockDiscard(op, err)
				return
			}
		}
		fallthrough
	case "delete-issued":
		if entry == "delete-issued" {
			if err = l.settleRuntimeDelete(ctx, spec, at, errDeleteUnresolved); err != nil {
				l.blockDiscard(op, err)
				return
			}
			if err = l.advanceDiscard(&op, ev, "runtime-absent"); err != nil {
				l.blockDiscard(op, err)
				return
			}
		}
		fallthrough
	case "runtime-absent":
		if e := proveRuntimeAbsent(ctx, l.driver, spec); e != nil {
			l.blockDiscard(op, errors.Join(e, errors.New("discard runtime absence unavailable")))
			return
		}
		if err = l.endpoints.RemoveSession(op.SessionUUID); err != nil {
			l.blockDiscard(op, err)
			return
		}
		if err = removeSessionKeyAt(l.store.StateDir(), op.SessionUUID); err != nil {
			l.blockDiscard(op, err)
			return
		}
		if err = l.advanceDiscard(&op, ev, "secrets-absent"); err != nil {
			l.blockDiscard(op, err)
			return
		}
		fallthrough
	case "secrets-absent":
		if ev.Action == "delete" {
			// This phase can be resumed directly after restart, bypassing the
			// preceding runtime-absent check. Never delete the P ref while a
			// renamed or reappearing session runtime makes absence unverified.
			if e := proveRuntimeAbsent(ctx, l.driver, spec); e != nil {
				l.blockDiscard(op, e)
				return
			}
			if err = l.git.backend.DeleteAssignedBranchExact(ctx, ev.Project, ev.Branch, ev.AssignedTip, func() error { return l.advanceDiscard(&op, ev, "branch-delete-issued") }); err != nil {
				l.blockDiscard(op, err)
				return
			}
			if err = l.advanceDiscard(&op, ev, "branch-absent"); err != nil {
				l.blockDiscard(op, err)
				return
			}
			break
		}
		if err = l.store.CompleteDiscard(ctx, op.ID, func(c context.Context, project, branch, tip string) error {
			if e := proveRuntimeAbsent(c, l.driver, spec); e != nil {
				return e
			}
			return l.discardAssignedTip(c, ev)
		}); err != nil {
			l.blockDiscard(op, err)
		}
		return
	case "branch-delete-issued":
		l.blockDiscard(op, control.OutcomeUnknown(errors.New("P ref delete outcome unresolved")))
		return
	case "branch-absent":
		if err = l.store.CompleteDelete(ctx, op.ID, func(c context.Context, project, branch, tip string) error {
			if e := proveRuntimeAbsent(c, l.driver, spec); e != nil {
				return e
			}
			return l.deleteAssignedAbsent(c, ev)
		}); err != nil {
			l.blockDiscard(op, err)
		}
		return
	default:
		l.blockDiscard(op, errors.New("discard phase unavailable"))
		return
	}
	// Delete records branch-absent before completing. Continue from that
	// durably recorded phase in the same turn.
	if op.Phase == "branch-absent" {
		l.processDiscard(op)
	}
}
