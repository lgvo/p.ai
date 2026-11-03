package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// ensureBranchAssigned brings the branch of creation op, whose request is
// req, to capturedOID, the commit it captured. An existing branch must still
// be there. A new one is created there. The creation records that it sent
// the creation right before the source driver gets it, so recovery that
// finds it sent settles it first: a creation that applied goes on, one that
// never applied is sent again, and any other answer blocks. A creation never
// sent finds a head at the captured commit its own, since the creation
// recorded its intent to create it (RefCASIntent) with the operation.
func (l *lifecycle) ensureBranchAssigned(ctx context.Context, op *api.Operation, req control.ReserveSessionRequest, capturedOID string) error {
	if req.Choice != "existing" && req.Choice != "new" {
		return control.ErrInvalid
	}
	ev, err := control.Evidence(*op)
	if err != nil {
		return err
	}
	if req.Choice == "new" && ev.BranchCreateIssued {
		settled, err := l.git.driver.Settle(ctx, creation(req.Project, req.Branch, capturedOID))
		switch {
		case err != nil || settled == sourcedriver.Unknown:
			return control.OutcomeUnknown(errors.Join(errors.New("the new branch's creation is unresolved"), err))
		case settled == sourcedriver.Applied:
			return l.newBranchExact(ctx, req, capturedOID)
		}
		// The creation never applied and never will: it may be sent again.
	} else {
		tip, exists, err := l.inspectBranch(ctx, req.Project, req.Branch)
		if err != nil {
			return err
		}
		if req.Choice == "existing" && (!exists || tip != capturedOID) {
			return refusalBecause(api.KindPreconditionFailed, api.ReasonChanged, "", control.ErrConflict, errors.New("assigned existing branch changed from captured tip"))
		}
		if exists && tip != capturedOID {
			return refusalBecause(api.KindPreconditionFailed, api.ReasonChanged, "", control.ErrConflict, errors.New("new branch differs from captured CAS intent"))
		}
		if exists {
			return nil
		}
	}
	if err = l.checkNewBranchSource(ctx, op.ID, req, capturedOID); err != nil {
		return control.Failed(api.ReasonGit, err)
	}
	sent := func() error {
		if ev.BranchCreateIssued {
			return nil
		}
		ev.BranchCreateIssued = true
		raw, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		if err = l.store.AdvanceOperation(ctx, op.ID, "running", op.Phase, op.Committed, raw, "", ""); err != nil {
			return err
		}
		op.Evidence = raw
		return nil
	}
	if err = l.createBranch(ctx, req.Project, req.Branch, capturedOID, sent); err != nil {
		return err
	}
	return l.newBranchExact(ctx, req, capturedOID)
}

// sentCreationUnsettled reports whether creation op, at source-ready, sent
// its new branch's creation and the source driver cannot yet tell whether
// it happened. Its intent (RefCASIntent) guards the branch until then, so a
// replacement or cleanup of the creation waits; a driver that cannot be
// reached cannot tell either.
func (l *lifecycle) sentCreationUnsettled(ctx context.Context, op api.Operation, req control.ReserveSessionRequest, ev control.CreationEvidence) bool {
	if op.Phase != "source-ready" || !ev.BranchCreateIssued {
		return false
	}
	settled, err := l.git.driver.Settle(ctx, creation(req.Project, req.Branch, ev.CapturedOID))
	if err != nil {
		log.Printf("creation %s: its branch's creation could not be settled: %v", op.ID, err)
	}
	return err != nil || settled == sourcedriver.Unknown
}

// newBranchExact proves creation req's new branch at commit captured.
func (l *lifecycle) newBranchExact(ctx context.Context, req control.ReserveSessionRequest, captured string) error {
	tip, exists, err := l.inspectBranch(ctx, req.Project, req.Branch)
	if err != nil {
		return err
	}
	if !exists || tip != captured {
		return control.Failed(api.ReasonGit, errors.New("new branch CAS postcondition failed"))
	}
	return nil
}

// checkNewBranchSource checks what lets creation operationID, whose request
// is req, create its branch at commit captured. A local source must still be
// reachable from a head, as a session's reads are. An origin source has no
// head yet: the creation recorded it, and only the recorded operation,
// request, evidence and creating session authorize the head, never the
// origin's current state or another object.
func (l *lifecycle) checkNewBranchSource(ctx context.Context, operationID string, req control.ReserveSessionRequest, captured string) error {
	if req.OriginRef == "" {
		commit, err := l.git.driver.InspectCommit(ctx, req.Project, sourcedriver.OID(captured))
		if err != nil {
			return err
		}
		if !commit.Present || !commit.Reachable {
			return sourceMissing("no head reaches the captured commit")
		}
		return nil
	}
	op, err := l.store.GetOperation(ctx, operationID)
	if err != nil {
		return err
	}
	var recorded control.ReserveSessionRequest
	if err := json.Unmarshal(op.Request, &recorded); err != nil {
		return control.ErrInvalid
	}
	ev, err := control.Evidence(op)
	if err != nil {
		return err
	}
	if op.Kind != "session.create" || op.Phase != "source-ready" || op.Committed ||
		(op.Status != "running" && op.Status != "blocked") ||
		!control.ValidSessionCreateRequest(recorded) || recorded.Key != op.Key ||
		op.Project != req.Project || recorded.Project != req.Project || recorded.Branch != req.Branch ||
		recorded.Choice != "new" || recorded.Source != "" || recorded.OriginRef == "" ||
		recorded.OriginRef != ev.OriginRef || recorded.ExpectedCommitOID != captured ||
		ev.OriginURL == "" || ev.CapturedOID != captured ||
		!ev.RefCASIntent || ev.BranchExisted || op.SessionUUID == "" {
		return control.ErrInvalid
	}
	session, err := l.store.GetSession(ctx, op.SessionUUID)
	if err != nil {
		return err
	}
	if session.Project != req.Project || session.Branch != req.Branch || session.Registry != "creating" {
		return control.ErrConflict
	}
	return nil
}
