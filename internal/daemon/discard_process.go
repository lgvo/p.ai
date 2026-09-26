package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

var errDiscardRefsChanged = errors.New("P refs changed after fresh discard inspection")

func discardPRefsDigest(refs []workspaceLossRef) (string, error) {
	data, err := json.Marshal(refs)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func discardPluginRefsDigest(refs []plugin.GitRef) (string, error) {
	closed := make([]workspaceLossRef, 0, len(refs))
	for _, ref := range refs {
		closed = append(closed, workspaceLossRef{Name: ref.Ref, OID: ref.OID})
	}
	return discardPRefsDigest(closed)
}

func (l *lifecycle) verifyDiscardRefs(ctx context.Context, ev control.DiscardEvidence) error {
	if err := l.discardAssignedTip(ctx, ev); err != nil {
		if errors.Is(err, control.ErrConflict) {
			return errDiscardRefsChanged
		}
		return err
	}
	current, err := l.git.backend.AssignedBranchSnapshot(ctx, ev.Project, ev.Branch, ev.AllowUnborn)
	if err != nil {
		return err
	}
	digest, err := discardPluginRefsDigest(current.PRefs)
	if err != nil {
		return err
	}
	if current.AssignedTip != ev.AssignedTip || digest != ev.PRefsDigest {
		return errDiscardRefsChanged
	}
	return nil
}

func (l *lifecycle) advanceDiscard(op *control.Operation, ev control.DiscardEvidence, phase string) error {
	raw, err := json.Marshal(ev)
	if err != nil || len(raw) > 4096 {
		return errors.New("discard evidence exceeds bound")
	}
	if err = l.store.AdvanceOperation(l.ctx, op.ID, "running", phase, op.Committed, raw, ""); err != nil {
		return err
	}
	op.Phase, op.Evidence, op.Status = phase, raw, "running"
	return nil
}

func (l *lifecycle) blockDiscard(op control.Operation, cause error) {
	if l.ctx.Err() != nil {
		return
	}
	diagnostic := "discard authority unavailable"
	if cause != nil {
		diagnostic = cause.Error()
	}
	if len(diagnostic) > 512 {
		diagnostic = diagnostic[:512]
	}
	_ = l.store.AdvanceOperation(l.ctx, op.ID, "blocked", op.Phase, op.Committed, op.Evidence, diagnostic)
	logDiscardProgress(l, op, "blocked")
}

func logDiscardProgress(l *lifecycle, op control.Operation, status string) {
	l.recordProgress(op, status)
}

func (l *lifecycle) discardSourceMatches(o runtimeincus.Observation, ev control.DiscardEvidence, want string) bool {
	return o.Exists && o.Name == ev.InstanceName && o.Status == want && o.IncusUUID == ev.IncusUUID &&
		o.Generation == ev.Generation && o.Fingerprint == ev.ImageFingerprint
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

func (l *lifecycle) rollbackStaleDiscard(op *control.Operation, ev control.DiscardEvidence, source, helper runtimeincus.Session, reason error) {
	ctx := l.ctx
	if op.Phase != "stale-cleanup" {
		// Only this operation's persisted freeze intent can require source
		// restoration. A source independently stopped or replaced while we
		// still held only the P-ref guard is stale, not ours to thaw.
		ev.StaleNeedsThaw = discardStaleNeedsThaw(ev, op.Phase)
		if err := l.advanceDiscard(op, ev, "stale-cleanup"); err != nil {
			l.blockDiscard(*op, err)
			return
		}
	}
	if !ev.MissingRuntime {
		if err := l.runtime.DeleteWorkspaceHelper(ctx, helper); err != nil {
			l.blockDiscard(*op, err)
			return
		}
		if ev.StaleNeedsThaw {
			observed, err := l.runtime.Inspect(ctx, source)
			if err != nil || !observed.Exists || observed.IncusUUID != ev.IncusUUID || observed.Generation != ev.Generation {
				l.blockDiscard(*op, errors.Join(err, errors.New("stale discard source identity unavailable")))
				return
			}
			if observed.Status == "Frozen" {
				if err = l.runtime.ThawWorkspaceSourceExact(ctx, source, ev.IncusUUID, ev.Generation); err != nil {
					l.blockDiscard(*op, err)
					return
				}
			}
			if observed, err = l.runtime.Inspect(ctx, source); err != nil || !l.discardSourceMatches(observed, ev, "Running") {
				l.blockDiscard(*op, errors.Join(err, errors.New("stale discard source not restored")))
				return
			}
		}
	}
	diagnostic := ev.Action + " confirmation stale; review fresh loss evidence"
	if reason != nil {
		diagnostic = fmt.Sprintf("%s confirmation stale: %v", ev.Action, reason)
	}
	var finishErr error
	if ev.Action == "delete" {
		finishErr = l.store.FailStaleDelete(ctx, op.ID, diagnostic)
	} else {
		finishErr = l.store.FailStaleDiscard(ctx, op.ID, diagnostic)
	}
	if finishErr != nil {
		l.blockDiscard(*op, finishErr)
		return
	}
	l.recordProgress(*op, "failed")
}

func discardStaleNeedsThaw(ev control.DiscardEvidence, phase string) bool {
	if ev.OriginalStatus != "Running" || ev.MissingRuntime {
		return false
	}
	switch phase {
	case "freeze-intent", "quiescent", "analyzed", "validated":
		return true
	default:
		return false
	}
}

func (l *lifecycle) processDiscard(op control.Operation) {
	if op.Status != "running" {
		return
	}
	var ev control.DiscardEvidence
	if json.Unmarshal(op.Evidence, &ev) != nil {
		l.blockDiscard(op, errors.New("removal durable evidence unreadable"))
		return
	}
	if ev.Action == "" && op.Kind == "session.discard" {
		ev.Action = "discard"
	}
	if (ev.Action != "discard" && ev.Action != "delete") || op.Kind != "session."+ev.Action || ev.Project != op.Project || ev.InstanceUUID != l.instanceID || ev.BaseFingerprint != l.cfg.BaseImageFingerprint ||
		ev.IncusProject != l.cfg.IncusProject || ev.InstanceName != "p-"+op.SessionUUID || ev.Branch == "" {
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
	source, err := l.runtimeSession(ctx, session)
	if err != nil || source.ImageFingerprint != ev.ImageFingerprint && !ev.MissingRuntime {
		l.blockDiscard(op, errors.Join(err, errors.New("discard runtime selection changed")))
		return
	}
	helper := runtimeincus.WorkspaceHelper(ev.InstanceUUID, op.ID, op.SessionUUID, op.Project, ev.BaseFingerprint)
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
			l.rollbackStaleDiscard(&op, ev, source, helper, err)
			return
		}
		observed, e := l.runtime.Inspect(ctx, source)
		if e != nil {
			l.blockDiscard(op, e)
			return
		}
		if ev.MissingRuntime {
			if observed.Exists {
				l.rollbackStaleDiscard(&op, ev, source, helper, errors.New("missing runtime reappeared"))
				return
			}
			if e := l.runtime.ConfirmSessionRuntimeAbsent(ctx, source); e != nil {
				l.blockDiscard(op, e)
				return
			}
			refs, e := l.git.backend.AssignedBranchSnapshot(ctx, ev.Project, ev.Branch, ev.AllowUnborn)
			if e != nil {
				l.blockDiscard(op, e)
				return
			}
			ev.PRefsDigest, e = discardPluginRefsDigest(refs.PRefs)
			if e != nil {
				l.blockDiscard(op, e)
				return
			}
			if e = l.verifyDeleteReview(ctx, ev); e != nil {
				if errors.Is(e, control.ErrConflict) || errors.Is(e, errDeleteReviewChanged) {
					l.rollbackStaleDiscard(&op, ev, source, helper, e)
				} else {
					l.blockDiscard(op, e)
				}
				return
			}
			if err = l.advanceDiscard(&op, ev, "analyzed"); err != nil {
				l.blockDiscard(op, err)
				return
			}
			break
		}
		if !l.discardSourceMatches(observed, ev, ev.OriginalStatus) {
			l.rollbackStaleDiscard(&op, ev, source, helper, errors.New("runtime generation or status changed"))
			return
		}
		if err = l.runtime.CheckWorkspaceHelperCapacity(ctx); err != nil {
			l.rollbackStaleDiscard(&op, ev, source, helper, err)
			return
		}
		if err = l.advanceDiscard(&op, ev, "helper-intent"); err != nil {
			l.blockDiscard(op, err)
			return
		}
		fallthrough
	case "helper-intent":
		observed, e := l.runtime.InspectWorkspaceHelper(ctx, helper)
		if e != nil || observed.Exists {
			l.blockDiscard(op, errors.Join(e, errors.New("discard helper unexpectedly present before init")))
			return
		}
		if _, e = l.runtime.CreateWorkspaceHelper(ctx, helper, func() error { return l.advanceDiscard(&op, ev, "init-issued") }); e != nil {
			l.blockDiscard(op, e)
			return
		}
		fallthrough
	case "init-issued":
		var observed runtimeincus.Observation
		var e error
		if entry == "init-issued" {
			observed, e = l.runtime.ReconcileWorkspaceHelperInit(ctx, helper)
		} else {
			observed, e = l.runtime.InspectWorkspaceHelper(ctx, helper)
		}
		if e != nil || !observed.Exists {
			l.blockDiscard(op, errors.Join(e, errors.New("discard helper init unresolved")))
			return
		}
		if observed.Status == "Stopped" {
			e = l.runtime.StartWorkspaceHelper(ctx, helper)
		} else if observed.Status == "Running" {
			e = l.runtime.VerifyWorkspaceHelperBoot(ctx, helper)
		} else {
			e = errors.New("discard helper state unavailable")
		}
		if e != nil {
			l.blockDiscard(op, e)
			return
		}
		if err = l.advanceDiscard(&op, ev, "helper-ready"); err != nil {
			l.blockDiscard(op, err)
			return
		}
		fallthrough
	case "helper-ready":
		observed, e := l.runtime.Inspect(ctx, source)
		if e != nil {
			l.blockDiscard(op, e)
			return
		}
		if !l.discardSourceMatches(observed, ev, ev.OriginalStatus) {
			l.rollbackStaleDiscard(&op, ev, source, helper, errors.New("discard source changed before quiescence"))
			return
		}
		if ev.OriginalStatus == "Running" {
			e = l.runtime.FreezeWorkspaceSourceExact(ctx, source, ev.IncusUUID, ev.Generation, func() error { return l.advanceDiscard(&op, ev, "freeze-intent") })
			if e != nil {
				l.blockDiscard(op, e)
				return
			}
		} else if observed.Status != "Stopped" {
			l.blockDiscard(op, errors.New("discard stopped source changed"))
			return
		}
		if err = l.advanceDiscard(&op, ev, "quiescent"); err != nil {
			l.blockDiscard(op, err)
			return
		}
		fallthrough
	case "freeze-intent":
		if entry == "freeze-intent" {
			if err = l.runtime.ReconcileWorkspaceFreezeExact(ctx, source, ev.IncusUUID, ev.Generation); err != nil {
				l.blockDiscard(op, err)
				return
			}
			if err = l.advanceDiscard(&op, ev, "quiescent"); err != nil {
				l.blockDiscard(op, err)
				return
			}
		}
		fallthrough
	case "quiescent":
		if entry == "quiescent" {
			observed, e := l.runtime.Inspect(ctx, source)
			want := "Stopped"
			if ev.OriginalStatus == "Running" {
				want = "Frozen"
			}
			if e != nil || !l.discardSourceMatches(observed, ev, want) {
				l.blockDiscard(op, errors.Join(e, errors.New("discard quiescent source changed")))
				return
			}
			if e = l.runtime.VerifyWorkspaceHelperBoot(ctx, helper); e != nil {
				l.blockDiscard(op, e)
				return
			}
		}
		workspaceEv := control.WorkspaceInspectEvidence{InstanceUUID: ev.InstanceUUID, ImageFingerprint: ev.ImageFingerprint,
			BaseFingerprint: ev.BaseFingerprint, SourceIncusUUID: ev.IncusUUID, SourceGeneration: ev.Generation, OriginalStatus: ev.OriginalStatus}
		fresh, e := l.inspectWorkspaceLossResult(ctx, session, source, helper, workspaceEv)
		if e != nil {
			l.blockDiscard(op, e)
			return
		}
		var loss workspaceLossResult
		if json.Unmarshal(fresh, &loss) != nil || loss.Fingerprint == "" {
			l.blockDiscard(op, errors.New("fresh discard loss malformed"))
			return
		}
		if loss.Fingerprint != ev.LossFingerprint || l.discardAssignedTip(ctx, ev) != nil {
			l.rollbackStaleDiscard(&op, ev, source, helper, errors.New("workspace or assigned P ref changed"))
			return
		}
		ev.PRefsDigest, e = discardPRefsDigest(loss.PRefs)
		if e != nil {
			l.blockDiscard(op, e)
			return
		}
		if e = l.verifyDeleteReview(ctx, ev); e != nil {
			if errors.Is(e, control.ErrConflict) || errors.Is(e, errDeleteReviewChanged) {
				l.rollbackStaleDiscard(&op, ev, source, helper, e)
			} else {
				l.blockDiscard(op, e)
			}
			return
		}
		if err = l.advanceDiscard(&op, ev, "analyzed"); err != nil {
			l.blockDiscard(op, err)
			return
		}
		fallthrough
	case "analyzed":
		if !ev.MissingRuntime {
			observed, e := l.runtime.Inspect(ctx, source)
			want := "Stopped"
			if ev.OriginalStatus == "Running" {
				want = "Frozen"
			}
			if e != nil || !l.discardSourceMatches(observed, ev, want) {
				l.blockDiscard(op, errors.Join(e, errors.New("discard source changed after analysis")))
				return
			}
			if err = l.runtime.DeleteWorkspaceHelper(ctx, helper); err != nil {
				l.blockDiscard(op, err)
				return
			}
		}
		if err = l.discardAssignedTip(ctx, ev); err != nil {
			l.rollbackStaleDiscard(&op, ev, source, helper, err)
			return
		}
		if err = l.advanceDiscard(&op, ev, "validated"); err != nil {
			l.blockDiscard(op, err)
			return
		}
		fallthrough
	case "validated":
		if !ev.MissingRuntime {
			observed, e := l.runtime.Inspect(ctx, source)
			want := "Stopped"
			if ev.OriginalStatus == "Running" {
				want = "Frozen"
			}
			if e != nil || !l.discardSourceMatches(observed, ev, want) {
				l.blockDiscard(op, errors.Join(e, errors.New("validated discard source changed")))
				return
			}
		}
		if err = l.discardAssignedTip(ctx, ev); err != nil {
			l.rollbackStaleDiscard(&op, ev, source, helper, err)
			return
		}
		if err = l.verifyDeleteReview(ctx, ev); err != nil {
			if errors.Is(err, control.ErrConflict) || errors.Is(err, errDeleteReviewChanged) {
				l.rollbackStaleDiscard(&op, ev, source, helper, err)
			} else {
				l.blockDiscard(op, err)
			}
			return
		}
		verify := func(c context.Context, facts control.DiscardEvidence) error {
			if facts.MissingRuntime {
				if e := l.runtime.ConfirmSessionRuntimeAbsent(c, source); e != nil {
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
				l.rollbackStaleDiscard(&op, ev, source, helper, err)
				return
			}
			l.blockDiscard(op, err)
			return
		}
		op.Phase, op.Committed = "removal-committed", true
		fallthrough
	case "removal-committed":
		if ev.MissingRuntime {
			if e := l.runtime.ConfirmSessionRuntimeAbsent(ctx, source); e != nil {
				l.blockDiscard(op, errors.Join(e, errors.New("missing discard runtime reappeared")))
				return
			}
			if err = l.advanceDiscard(&op, ev, "runtime-absent"); err != nil {
				l.blockDiscard(op, err)
				return
			}
		} else if ev.OriginalStatus == "Running" {
			if err = l.runtime.StopForDiscardExact(ctx, source, ev.IncusUUID, ev.Generation, func() error { return l.advanceDiscard(&op, ev, "stop-issued") }); err != nil {
				l.blockDiscard(op, err)
				return
			}
			if err = l.advanceDiscard(&op, ev, "source-stopped"); err != nil {
				l.blockDiscard(op, err)
				return
			}
		} else {
			if err = l.advanceDiscard(&op, ev, "source-stopped"); err != nil {
				l.blockDiscard(op, err)
				return
			}
		}
		fallthrough
	case "stop-issued":
		if entry == "stop-issued" {
			l.blockDiscard(op, errors.New("Incus stop outcome unresolved; exact administrative reconciliation required"))
			return
		}
		fallthrough
	case "source-stopped":
		if !ev.MissingRuntime {
			if err = l.runtime.DeleteForDiscardExact(ctx, source, ev.IncusUUID, ev.Generation, func() error { return l.advanceDiscard(&op, ev, "delete-issued") }); err != nil {
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
			l.blockDiscard(op, errors.New("Incus delete outcome unresolved; exact administrative reconciliation required"))
			return
		}
		fallthrough
	case "runtime-absent":
		if e := l.runtime.ConfirmSessionRuntimeAbsent(ctx, source); e != nil {
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
			if e := l.runtime.ConfirmSessionRuntimeAbsent(ctx, source); e != nil {
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
			if e := l.runtime.ConfirmSessionRuntimeAbsent(c, source); e != nil {
				return e
			}
			return l.discardAssignedTip(c, ev)
		}); err != nil {
			l.blockDiscard(op, err)
			return
		}
		l.recordProgress(op, "completed")
		return
	case "branch-delete-issued":
		l.blockDiscard(op, errors.New("P ref delete outcome unresolved; targeted repair required"))
		return
	case "branch-absent":
		if err = l.store.CompleteDelete(ctx, op.ID, func(c context.Context, project, branch, tip string) error {
			if e := l.runtime.ConfirmSessionRuntimeAbsent(c, source); e != nil {
				return e
			}
			return l.deleteAssignedAbsent(c, ev)
		}); err != nil {
			l.blockDiscard(op, err)
			return
		}
		l.recordProgress(op, "completed")
		return
	case "stale-cleanup":
		l.rollbackStaleDiscard(&op, ev, source, helper, errors.New("prior confirmation stale"))
		return
	default:
		l.blockDiscard(op, errors.New("discard phase unavailable"))
		return
	}
	// The missing-runtime branch reaches analyzed without a helper. Continue
	// from its durably recorded phase in the same turn.
	if op.Phase == "analyzed" || op.Phase == "branch-absent" {
		l.processDiscard(op)
	}
}
