package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
)

func (l *lifecycle) reviewCreateCleanupLocal(ctx context.Context, op api.Operation, s control.Session) (*api.CreateReplacementCleanup, error) {
	fp, found, err := l.store.SessionGitPrincipal(ctx, s.UUID)
	if err != nil {
		return nil, err
	}
	if found {
		if err = l.store.CheckCreateReplacementPrincipal(ctx, s.UUID, s.Project, fp); err != nil {
			return nil, err
		}
		ev, err := control.Evidence(op)
		if err != nil {
			return nil, err
		}
		return reviewReplacementLocal(l.store.StateDir(), l.endpoints, s.UUID, op.ID, ev.ImageFingerprint, fp)
	}
	if err = l.createCleanupLocalAbsent(s.UUID); err != nil {
		return nil, err
	}
	return nil, nil
}
func (l *lifecycle) createCleanupLocalAbsent(uuid string) error {
	return checkCreateCleanupLocalAbsent(l.store.StateDir(), l.endpoints, uuid)
}
func checkCreateCleanupLocalAbsent(state string, endpoints *endpointManager, uuid string) error {
	parent, e := replacementLocalIdentity(sessionKeysDir(state))
	if e != nil && !errors.Is(e, os.ErrNotExist) || e == nil && parent.Mode != 040700 {
		return control.ErrConflict
	}
	prefix, e := replacementLocalIdentity(endpoints.prefix)
	if e != nil || prefix.Mode != 040700 {
		return control.ErrConflict
	}

	if _, err := os.Lstat(sessionKeyPath(state, uuid)); !errors.Is(err, os.ErrNotExist) {
		return control.ErrConflict
	}
	endpoints.mu.Lock()
	defer endpoints.mu.Unlock()
	if endpoints.opened[uuid] != nil {
		return control.ErrConflict
	}
	if _, err := os.Lstat(filepath.Join(endpoints.prefix, uuid)); !errors.Is(err, os.ErrNotExist) {
		return control.ErrConflict
	}
	return nil
}

// failedCreationAbsent proves that the failed creation of session uuid that
// a cleanup planned as d left no runtime and no builder of its latest build.
func (l *lifecycle) failedCreationAbsent(ctx context.Context, uuid string, d api.CreateCleanupDetails) error {
	spec := l.runtimeSpec(uuid, d.OldRequest.Project, d.ImageFingerprint, nil, "")
	return proveFailedCreationAbsent(ctx, l.driver, spec, lastBuild(d.OldOperationID, d.EnvironmentBuilder))
}

// lastBuild names the creation's latest build, the only one whose builder
// may remain: each earlier cycle ended with its builder absent. A creation
// that never built is named by its operation.
func lastBuild(operationID string, builder *api.CreationBuilderState) string {
	if builder == nil || builder.Cycle == 0 {
		return operationID
	}
	return control.BuildRef(operationID, builder.Cycle)
}

// CleanupCreation answers a cleanup of session uuid's failed creation: its
// plan, or the operation of a run.
func (l *lifecycle) CleanupCreation(ctx context.Context, uuid string, req api.ChangeRequest) (api.ChangeResult[api.CreateCleanupDetails], error) {
	recorded := control.DiscardRequest{Key: req.Key, UUID: uuid, IfMatch: req.IfMatch}
	change := control.Change[api.CreateCleanupDetails]{
		Action: api.SessionCreateCleanup.Name, Target: api.TargetSession(uuid), Instance: l.instanceID,
		Hold: l.holdingSession(uuid),
		Plan: func(ctx context.Context, p *api.Plan[api.CreateCleanupDetails]) error {
			return l.planCreateCleanup(ctx, uuid, p)
		},
		Shown: createCleanupShown,
		Replay: func(ctx context.Context) (api.Operation, bool, error) {
			return replayRequest(l, ctx, req.Key, control.KindCreateCleanup, recorded)
		},
		Begin: func(ctx context.Context, run control.Run[api.CreateCleanupDetails]) (api.Operation, error) {
			ev := control.CreateCleanupEvidence{Review: run.Plan.Details, InstanceUUID: l.instanceID}
			begin := l.store.BeginCreateCleanup
			if ev.Review.Runtime != nil {
				begin = l.store.BeginAssembledCreateCleanup
			}
			op, err := begin(ctx, recorded, ev, run.Hash, run.Recheck)
			if err != nil {
				return api.Operation{}, err
			}
			return op, l.enqueue(op.ID)
		},
	}
	return change.Do(ctx, req)
}

// createCleanupShown leaves the driver's name for the runtime out of a failed
// creation's cleanup's change hash: it is for display only, and the cleanup
// acts on the runtime's incarnation.
func createCleanupShown(d api.CreateCleanupDetails) api.CreateCleanupDetails {
	if d.Runtime != nil {
		runtime := *d.Runtime
		runtime.Name = ""
		d.Runtime = &runtime
	}
	return d
}

// planCreateCleanup plans the cleanup of session uuid's failed creation into
// p. A creator blocked before its runtime was initialized takes the early
// path; any later creator takes the assembled path, which removes its exact
// stopped container.
func (l *lifecycle) planCreateCleanup(ctx context.Context, uuid string, p *api.Plan[api.CreateCleanupDetails]) error {
	d := &p.Details
	*d = api.CreateCleanupDetails{ExternalMounts: "preserved", SharedImages: "preserved", Provisional: api.CreateReplaceResources{RuntimeLocal: "unavailable"}}
	refuse := func(code string) { p.Refusals = append(p.Refusals, api.Refusal{Code: code}) }
	s, err := l.store.GetSession(ctx, uuid)
	if err != nil {
		return err
	}
	op, err := l.store.CreationForSession(ctx, uuid)
	if err != nil {
		return err
	}
	ev, err := control.Evidence(op)
	if err != nil || json.Unmarshal(op.Request, &d.OldRequest) != nil {
		return control.ErrConflict
	}
	d.OldOperationID, d.OldPhase, d.OldEvidenceSHA256 = op.ID, op.Phase, hexDigest(op.Evidence)
	d.PolicySHA256, d.ImageFingerprint, d.EnvironmentBuilder = s.PolicySHA256, ev.ImageFingerprint, (*api.CreationBuilderState)(ev.EnvironmentBuilder)
	if op.Kind != "session.create" || op.Status != "blocked" || s.Registry != "creating" || d.OldRequest.Project != s.Project || d.OldRequest.Branch != s.Branch || ev.PolicySHA256 != s.PolicySHA256 {
		refuse(api.ReasonBlockedLocalCreationRequired)
	}
	assembled := !control.SafeCreateCleanupPhase(op)
	if assembled {
		d.OldRequestSHA256 = hexDigest(op.Request)
		if control.AssembledCreationForCleanup(s, op, l.cfg.BaseImageFingerprint) != nil || ev.Selection != l.selection() {
			refuse(api.ReasonAssemblyReadyCreatorRequired)
		}
		if l.admitSessionWork(ctx, uuid, op.ID) != nil {
			refuse(api.ReasonActiveWorkspaceOrCleanupGuard)
		}
	} else if !control.SafeCreateCleanupEvidence(d.OldRequest, ev) {
		refuse(api.ReasonDispatchUnsettled)
	}
	if l.sentCreationUnsettled(ctx, op, d.OldRequest, ev) {
		refuse(api.ReasonOutcomeUnknown)
	}
	var working bool
	l.withLock(func() { working = l.working[op.ID] || l.hasAttachmentLocked(uuid) })
	if working {
		refuse(api.ReasonCreationWorkerOrAttachmentActive)
	}
	oid, exists, e := l.inspectBranch(ctx, s.Project, s.Branch)
	d.AssignedBranch = api.CreateReplaceBranch{Ref: "refs/heads/" + s.Branch, Observed: e == nil, Exists: exists, OID: oid}
	if e != nil || !exists {
		refuse(api.ReasonAssignedRefUnavailable)
	}
	if l.store.CheckCreateReplacementTarget(ctx, s.Project, s.Branch, uuid) != nil {
		refuse(api.ReasonAssignmentOrGuardUnavailable)
	}
	local, e := l.reviewCreateCleanupLocal(ctx, op, s)
	if e != nil {
		refuse(api.ReasonLocalCredentialsUnverified)
	} else {
		d.Provisional.Cleanup = local
		d.Provisional.SessionKey = "absent"
		d.Provisional.Endpoint = "absent"
		d.Provisional.Principal = "absent"
		if local != nil {
			d.Provisional.SessionKey = "present"
			d.Provisional.Endpoint = "present"
			d.Provisional.Principal = "present"
		}
	}
	if assembled {
		d.Runtime, e = l.reviewAssembledCleanupRuntime(ctx, s, op)
		if e != nil {
			log.Printf("cleanup of failed creation %s: creator runtime unavailable: %v", uuid, e)
			refuse(api.ReasonExactStoppedRuntimeUnavailable)
		} else {
			d.Provisional.Runtime, d.Provisional.Builder, d.Provisional.RuntimeLocal = "present", "absent", "will_be_removed"
			p.Loss = append(p.Loss, containerLoss(uuid))
		}
	} else if e = l.failedCreationAbsent(ctx, uuid, *d); e != nil {
		refuse(api.ReasonNativeEffectsUnsettled)
	} else {
		d.Provisional.Runtime = "absent"
		d.Provisional.Builder = "absent"
	}
	d.Provisional.AssignedRef = "preserved_existing"
	return nil
}

func hexDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func (l *lifecycle) processCreateCleanup(observed api.Operation) {
	ctx := l.ctx
	if l.holdSession(ctx, observed.ID, observed.SessionUUID) != nil {
		return
	}
	op, err := l.store.GetOperation(ctx, observed.ID)
	if err != nil || op.Kind != "session.create.cleanup" || op.Status != "running" && op.Status != "blocked" {
		return
	}
	var ev control.CreateCleanupEvidence
	if json.Unmarshal(op.Evidence, &ev) != nil {
		return
	}
	if ev.Review.Runtime != nil {
		l.processAssembledCreateCleanup(op, ev)
		return
	}
	block := func(e error) {
		if ctx.Err() == nil {
			diagnostic := "failed creation cleanup refused: " + e.Error()
			_ = l.store.AdvanceOperation(ctx, op.ID, "blocked", op.Phase, true, op.Evidence, control.OperationReason(e), diagnostic)
		}
	}
	old, oldErr := l.store.GetOperation(ctx, ev.Review.OldOperationID)
	session, sessionErr := l.store.GetSession(ctx, op.SessionUUID)
	var request control.ReserveSessionRequest
	var original control.CreationEvidence
	// Recheck the durable retired authority before touching any local entry.
	// The original evidence remains inspectable after the atomic handoff.
	if oldErr != nil || sessionErr != nil || old.Kind != "session.create" || old.Status != "superseded" || old.SessionUUID != op.SessionUUID || old.Project != op.Project || session.Registry != "removing" || session.Project != op.Project || session.Branch != ev.Review.OldRequest.Branch || ev.InstanceUUID != l.instanceID || ev.Review.OldEvidenceSHA256 != hexDigest(old.Evidence) || json.Unmarshal(old.Request, &request) != nil || request != ev.Review.OldRequest || json.Unmarshal(old.Evidence, &original) != nil || !control.SafeCreateCleanupEvidence(request, original) || original.PolicySHA256 != ev.Review.PolicySHA256 || original.ImageFingerprint != ev.Review.ImageFingerprint || ev.Review.AssignedBranch.Ref != "refs/heads/"+session.Branch {
		block(control.ErrConflict)
		return
	}
	if op.Status == "blocked" {
		if err = l.store.AdvanceOperation(ctx, op.ID, "running", op.Phase, true, op.Evidence, "", ""); err != nil {
			return
		}
		op.Status = "running"
	}
	prove := func(call context.Context) error { return l.failedCreationAbsent(call, op.SessionUUID, ev.Review) }
	if err = prove(ctx); err != nil {
		block(err)
		return
	}
	switch op.Phase {
	case "local-cleanup":
		if ev.Review.Provisional.Cleanup != nil {
			err = cleanupReplacementLocal(ctx, l.store.StateDir(), l.endpoints, ev.Review.Provisional.Cleanup, prove)
		} else {
			err = l.createCleanupLocalAbsent(op.SessionUUID)
		}
		if err != nil {
			block(err)
			return
		}
		ev.LocalComplete = true
		raw, _ := json.Marshal(ev)
		if err = l.store.AdvanceOperation(ctx, op.ID, "running", "local-complete", true, raw, "", ""); err != nil {
			block(err)
			return
		}
		op.Phase, op.Evidence = "local-complete", raw
		fallthrough
	case "local-complete":
		err = l.store.CompleteCreateCleanup(ctx, op.ID, func(call context.Context, review control.CreateCleanupEvidence) error {
			if e := prove(call); e != nil {
				return e
			}
			if e := l.createCleanupLocalAbsent(op.SessionUUID); e != nil {
				return e
			}
			oid, exists, e := l.inspectBranch(call, op.Project, review.Review.OldRequest.Branch)
			if e != nil || !exists || oid != review.Review.AssignedBranch.OID {
				return control.ErrConflict
			}
			return nil
		})
		if err != nil {
			block(err)
		}
	default:
		block(errors.New("cleanup checkpoint unavailable"))
	}
}
