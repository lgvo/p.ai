package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
)

type localReplacementSource interface {
	InspectBranchRef(context.Context, string, string) (string, bool, error)
	CaptureLocalSource(context.Context, control.ReserveSessionRequest) (control.CapturedSource, error)
}

func observeCreateReplacementSources(ctx context.Context, git localReplacementSource, old, req control.ReserveSessionRequest, oldOID string) (api.CreateReplaceBranch, api.CreateReplaceBranch, string, error) {
	oldBranch := api.CreateReplaceBranch{Ref: "refs/heads/" + old.Branch}
	target := api.CreateReplaceBranch{Ref: "refs/heads/" + req.Branch}
	oid, exists, err := git.InspectBranchRef(ctx, old.Project, old.Branch)
	if err != nil {
		return oldBranch, target, "", control.Failed(api.ReasonGit, err)
	}
	oldBranch.Observed, oldBranch.Exists, oldBranch.OID = true, exists, oid
	if old.Branch == req.Branch {
		target = oldBranch
	} else {
		oid, exists, err = git.InspectBranchRef(ctx, req.Project, req.Branch)
		if err != nil {
			return oldBranch, target, "", control.Failed(api.ReasonGit, err)
		}
		target.Observed, target.Exists, target.OID = true, exists, oid
	}
	// An unexpected old created tip requires a broader review; this slice only
	// releases its assignment and never deletes or resets the preserved ref.
	if old.Choice == "new" && oldBranch.Exists && oldBranch.OID != oldOID {
		return oldBranch, target, "", control.ErrConflict
	}
	captured, err := git.CaptureLocalSource(ctx, req)
	if err != nil {
		return oldBranch, target, "", control.Failed(api.ReasonGit, err)
	}
	oid, exists, err = git.InspectBranchRef(ctx, req.Project, req.Branch)
	if err != nil {
		return oldBranch, target, "", control.Failed(api.ReasonGit, err)
	}
	if exists != captured.Existed || exists && oid != captured.OID || exists != target.Exists || oid != target.OID {
		return oldBranch, target, "", control.ErrConflict
	}
	return oldBranch, target, captured.OID, nil
}

// createReplaceEffects reviews only local resources, with full native absence.
func (l *lifecycle) createReplaceEffects(ctx context.Context, op api.Operation, s control.Session) (*api.CreateReplacementCleanup, error) {
	if op.Kind != "session.create" || op.Status != "blocked" || s.Registry != "creating" {
		return nil, control.ErrConflict
	}
	var working bool
	l.withLock(func() { working = l.working[op.ID] || l.hasAttachmentLocked(s.UUID) })
	if working {
		return nil, control.ErrConflict
	}
	ev, err := control.Evidence(op)
	var req control.ReserveSessionRequest
	if err != nil || json.Unmarshal(op.Request, &req) != nil || !control.ReplaceableCreationEvidence(req, ev) || !control.ReplaceableCreationPhase(op, ev) || ev.ReplacementCleanup != nil && !ev.ReplacementCleanup.Completed {
		return nil, control.ErrConflict
	}
	fp, found, err := l.store.SessionGitPrincipal(ctx, s.UUID)
	if err != nil {
		return nil, err
	}
	var cleanup *api.CreateReplacementCleanup
	if op.Phase == "principals-ready" {
		active, e := l.store.IsSessionGitPrincipalActive(ctx, s.UUID)
		if !found || !active || e != nil || l.store.CheckCreateReplacementPrincipal(ctx, s.UUID, s.Project, fp) != nil {
			return nil, control.ErrConflict
		}
		cleanup, err = reviewReplacementLocal(l.store.StateDir(), l.endpoints, s.UUID, op.ID, ev.ImageFingerprint, fp)
		if err != nil {
			return nil, err
		}
	} else {
		if found {
			return nil, control.ErrConflict
		}
		if _, err = os.Lstat(sessionKeyPath(l.store.StateDir(), s.UUID)); !errors.Is(err, os.ErrNotExist) {
			return nil, control.ErrConflict
		}
		opened := l.endpoints.isOpen(s.UUID)
		if opened {
			return nil, control.ErrConflict
		}
		if _, err = os.Lstat(filepath.Join(l.cfg.EndpointPrefix, s.UUID)); !errors.Is(err, os.ErrNotExist) {
			return nil, control.ErrConflict
		}
	}
	err = proveFailedCreationAbsent(ctx, l.driver, l.runtimeSpec(s.UUID, s.Project, ev.ImageFingerprint, nil, ""), op.ID)
	return cleanup, err
}

// ReplaceCreation answers a replacement of the blocked creation of session
// oldUUID with a new creation of choices: its plan, or the new creation's
// operation for a run, whose key is also the new creation's.
func (l *lifecycle) ReplaceCreation(ctx context.Context, oldUUID string, choices api.CreationChoices, req api.ChangeRequest) (api.ChangeResult[api.CreateReplaceDetails], error) {
	create := choices.Create(req.Key)
	change := control.Change[api.CreateReplaceDetails]{
		Action: api.SessionCreateReplace.Name, Target: api.TargetSession(oldUUID), Instance: l.instanceID, Inputs: choices,
		Hold: l.holdingSession(oldUUID),
		Plan: func(ctx context.Context, p *api.Plan[api.CreateReplaceDetails]) error {
			return l.planCreateReplace(ctx, oldUUID, choices, p)
		},
		// A replay reads only the recorded creation: cleanup may hold the
		// retired session's lock for its whole recovery.
		Replay: func(ctx context.Context) (api.Operation, bool, error) {
			return l.store.ReplayCreateReplacement(ctx, oldUUID, req.IfMatch, create)
		},
		Begin: func(ctx context.Context, run control.Run[api.CreateReplaceDetails]) (api.Operation, error) {
			d := run.Plan.Details
			intent := control.CreateReplaceIntent{OldOperationID: d.OldOperationID, OldUUID: oldUUID,
				OldEvidenceSHA256: d.OldEvidenceSHA256, OldPolicySHA256: d.OldPolicySHA256, NewPolicySHA256: d.NewPolicySHA256,
				Cleanup: d.Provisional.Cleanup, IfMatch: req.IfMatch, ChangeHash: run.Hash, New: create}
			op, err := l.store.ReplaceBlockedCreate(ctx, intent, l.cfg.BaseImageFingerprint, l.selection(), l.environmentIntent(), l.observeSessionCapacity,
				func(call context.Context) (string, error) {
					// The same plan has the same new source tip.
					return d.NewCapturedOID, run.Recheck(call)
				})
			if err != nil {
				return api.Operation{}, err
			}
			l.recordCreation(op, create.Branch)
			return op, l.enqueue(op.ID)
		},
	}
	return change.Do(ctx, req)
}

// planCreateReplace plans replacing the blocked creation of session oldUUID
// with a new creation of choices into p.
func (l *lifecycle) planCreateReplace(ctx context.Context, oldUUID string, choices api.CreationChoices, p *api.Plan[api.CreateReplaceDetails]) error {
	selection := l.selection()
	// The new request's key is a run's, and no fact of its plan.
	req := choices.Create("replacement")
	d := &p.Details
	*d = api.CreateReplaceDetails{NewRequest: choices, Provisional: api.CreateReplaceResources{RuntimeLocal: "unavailable"},
		NewImageFingerprint: l.cfg.BaseImageFingerprint, NewSelection: api.CreationSelection(selection),
		NewEnvironment: (*api.EnvironmentIntent)(l.environmentIntent())}
	refuse := func(code string) { p.Refusals = append(p.Refusals, api.Refusal{Code: code}) }
	if !control.ValidSessionCreateRequest(req) {
		return control.ErrInvalid
	}
	s, err := l.store.GetSession(ctx, oldUUID)
	if err != nil {
		return err
	}
	op, err := l.store.CreationForSession(ctx, oldUUID)
	if err != nil {
		return err
	}
	var oldReq control.ReserveSessionRequest
	ev, evErr := control.Evidence(op)
	if json.Unmarshal(op.Request, &oldReq) != nil || evErr != nil {
		return control.ErrConflict
	}
	d.OldOperationID, d.OldStatus, d.OldPhase = op.ID, op.Status, op.Phase
	d.OldRequest, d.OldCapturedOID, d.OldPolicySHA256 = oldReq, ev.CapturedOID, s.PolicySHA256
	d.OldEvidenceSHA256 = hexDigest(op.Evidence)
	d.OldImageFingerprint = ev.ImageFingerprint
	d.Provisional.AssignedRef = "preserved_existing"
	if req.Project != s.Project || oldReq.Project != s.Project || oldReq.Branch != s.Branch || req.OriginRef != "" ||
		(oldReq.Choice == "existing" && (req.Branch != s.Branch || req.Choice != "existing")) ||
		(oldReq.Choice == "new" && req.Choice == "existing" && req.Branch != s.Branch) {
		refuse(api.ReasonUnsupportedReplacementChoice)
	}
	if op.Status != "blocked" || op.Kind != "session.create" || s.Registry != "creating" {
		refuse(api.ReasonOldCreationNotBlocked)
	}
	if !control.ReplaceableCreationPhase(op, ev) || !control.ReplaceableCreationEvidence(oldReq, ev) {
		refuse(api.ReasonOldEffectNotProvenAbsent)
	}
	policy, hash, err := l.store.ProjectPolicyRecord(ctx, s.Project)
	if err != nil || len(policy) == 0 {
		refuse(api.ReasonCurrentPolicyUnavailable)
	} else {
		d.NewPolicySHA256 = hash
	}
	if _, configured := l.cfg.PolicyForProject(s.Project); !configured || l.currentProjectPolicy(ctx, s.Project) != nil {
		refuse(api.ReasonCurrentPolicyUnavailable)
	}
	oldBranch, target, tip, sourceErr := observeCreateReplacementSources(ctx, l.git.backend, oldReq, req, ev.CapturedOID)
	d.OldBranch, d.NewBranch = oldBranch, target
	if !oldBranch.Observed {
		d.Provisional.AssignedRef = "unavailable"
	} else if oldBranch.Exists {
		if oldReq.Choice == "existing" {
			d.Provisional.AssignedRef = "preserved_existing"
		} else if oldBranch.OID == ev.CapturedOID {
			d.Provisional.AssignedRef = "preserved_created"
		} else {
			d.Provisional.AssignedRef = "preserved_present"
		}
	} else {
		d.Provisional.AssignedRef = "absent"
	}
	if sourceErr != nil {
		refuse(api.ReasonNewSourceOrBranchUnavailable)
	} else {
		d.NewCapturedOID = tip
	}
	if l.store.CheckCreateReplacementTarget(ctx, req.Project, req.Branch, oldUUID) != nil || l.store.CheckCreateReplacementTarget(ctx, s.Project, s.Branch, oldUUID) != nil {
		refuse(api.ReasonTargetAssignmentUnavailable)
	}
	if d.NewCapturedOID != "" && d.NewCapturedOID == d.OldCapturedOID && d.NewPolicySHA256 == d.OldPolicySHA256 && control.SameCreateSelection(oldReq, req) {
		refuse(api.ReasonRequestUnchanged)
	}
	if cleanup, e := l.createReplaceEffects(ctx, op, s); e != nil {
		refuse(api.ReasonProvisionalResourceUnavailable)
	} else {
		d.Provisional.Runtime = "absent"
		d.Provisional.Builder = "absent"
		d.Provisional.SessionKey = "absent"
		d.Provisional.Endpoint = "absent"
		d.Provisional.Principal = "absent"
		if cleanup != nil {
			d.Provisional.SessionKey = "present"
			d.Provisional.Endpoint = "present"
			d.Provisional.Principal = "present"
			d.Provisional.Cleanup = cleanup
		}
	}
	return nil
}
