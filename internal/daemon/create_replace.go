package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

type createReplacePreviewState struct {
	Preview control.CreateReplacePreview
	Expiry  time.Time
}

type localReplacementSource interface {
	InspectBranchRef(context.Context, string, string) (string, bool, error)
	CaptureLocalSource(context.Context, control.ReserveSessionRequest) (control.CapturedSource, error)
}

func observeCreateReplacementSources(ctx context.Context, git localReplacementSource, old, req control.ReserveSessionRequest, oldOID string) (control.CreateReplaceBranch, control.CreateReplaceBranch, string, error) {
	oldBranch := control.CreateReplaceBranch{Ref: "refs/heads/" + old.Branch}
	target := control.CreateReplaceBranch{Ref: "refs/heads/" + req.Branch}
	oid, exists, err := git.InspectBranchRef(ctx, old.Project, old.Branch)
	if err != nil {
		return oldBranch, target, "", err
	}
	oldBranch.Observed, oldBranch.Exists, oldBranch.OID = true, exists, oid
	if old.Branch == req.Branch {
		target = oldBranch
	} else {
		oid, exists, err = git.InspectBranchRef(ctx, req.Project, req.Branch)
		if err != nil {
			return oldBranch, target, "", err
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
		return oldBranch, target, "", err
	}
	oid, exists, err = git.InspectBranchRef(ctx, req.Project, req.Branch)
	if err != nil || exists != captured.Existed || exists && oid != captured.OID || exists != target.Exists || oid != target.OID {
		return oldBranch, target, "", errors.Join(err, control.ErrConflict)
	}
	return oldBranch, target, captured.OID, nil
}

// createReplaceEffects reviews only local resources, with full native absence.
func (l *lifecycle) createReplaceEffects(ctx context.Context, op control.Operation, s control.Session) (*control.CreateReplacementCleanup, error) {
	if op.Kind != "session.create" || op.Status != "blocked" || s.Registry != "creating" {
		return nil, control.ErrConflict
	}
	l.mu.Lock()
	working := l.working[op.ID] || l.hasAttachmentLocked(s.UUID)
	l.mu.Unlock()
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
	var cleanup *control.CreateReplacementCleanup
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
		if _, err = os.Lstat(filepath.Join(l.store.StateDir(), "session_keys", s.UUID)); !errors.Is(err, os.ErrNotExist) {
			return nil, control.ErrConflict
		}
		l.endpoints.mu.Lock()
		opened := l.endpoints.opened[s.UUID] != nil
		l.endpoints.mu.Unlock()
		if opened {
			return nil, control.ErrConflict
		}
		if _, err = os.Lstat(filepath.Join(l.cfg.EndpointPrefix, s.UUID)); !errors.Is(err, os.ErrNotExist) {
			return nil, control.ErrConflict
		}
	}
	err = l.runtime.ConfirmFailedCreateEffectsAbsent(ctx, runtimeincus.Session{InstanceUUID: l.instanceID, SessionUUID: s.UUID, ProjectPath: s.Project, ContractVersion: "1", ImageFingerprint: ev.ImageFingerprint}, op.ID)
	return cleanup, err
}

func (l *lifecycle) createReplaceFacts(ctx context.Context, oldUUID string, req control.ReserveSessionRequest) (control.CreateReplacePreview, error) {
	p := control.CreateReplacePreview{OldUUID: oldUUID, NewRequest: req, UnsafeReasons: []string{},
		Provisional: control.CreateReplaceResources{RuntimeLocal: "unavailable"}, NewImageFingerprint: l.cfg.BaseImageFingerprint,
		NewSelection: l.selection(), NewEnvironment: l.environmentIntent()}
	unsafe := func(reason string) { p.UnsafeReasons = append(p.UnsafeReasons, reason) }
	if !control.ValidSessionCreateRequest(req) {
		return p, control.ErrInvalid
	}
	op, err := l.store.CreationForSession(ctx, oldUUID)
	if err != nil {
		return p, err
	}
	s, err := l.store.GetSession(ctx, oldUUID)
	if err != nil {
		return p, err
	}
	var oldReq control.ReserveSessionRequest
	ev, evErr := control.Evidence(op)
	if json.Unmarshal(op.Request, &oldReq) != nil || evErr != nil {
		return p, control.ErrConflict
	}
	p.OldOperationID, p.OldStatus, p.OldPhase = op.ID, op.Status, op.Phase
	p.OldRequest, p.OldCapturedOID, p.OldPolicySHA256 = oldReq, ev.CapturedOID, s.PolicySHA256
	sum := sha256.Sum256(op.Evidence)
	p.OldEvidenceSHA256 = hex.EncodeToString(sum[:])
	p.OldImageFingerprint = ev.ImageFingerprint
	p.Provisional.AssignedRef = "preserved_existing"
	if req.Project != s.Project || oldReq.Project != s.Project || oldReq.Branch != s.Branch || req.OriginRef != "" ||
		(oldReq.Choice == "existing" && (req.Branch != s.Branch || req.Choice != "existing")) ||
		(oldReq.Choice == "new" && req.Choice == "existing" && req.Branch != s.Branch) {
		unsafe("unsupported_replacement_choice")
	}
	if op.Status != "blocked" || op.Kind != "session.create" || s.Registry != "creating" {
		unsafe("old_creation_not_blocked")
	}
	if !control.ReplaceableCreationPhase(op, ev) || !control.ReplaceableCreationEvidence(oldReq, ev) {
		unsafe("old_effect_not_proven_absent")
	}
	policy, hash, err := l.store.ProjectPolicyRecord(ctx, s.Project)
	if err != nil || len(policy) == 0 {
		unsafe("current_policy_unavailable")
	} else {
		p.NewPolicySHA256 = hash
	}
	if _, configured := l.cfg.PolicyForProject(s.Project); !configured || l.currentProjectPolicy(ctx, s.Project) != nil {
		unsafe("current_policy_unavailable")
	}
	oldBranch, target, tip, sourceErr := observeCreateReplacementSources(ctx, l.git.backend, oldReq, req, ev.CapturedOID)
	p.OldBranch, p.NewBranch = oldBranch, target
	if !oldBranch.Observed {
		p.Provisional.AssignedRef = "unavailable"
	} else if oldBranch.Exists {
		if oldReq.Choice == "existing" {
			p.Provisional.AssignedRef = "preserved_existing"
		} else if oldBranch.OID == ev.CapturedOID {
			p.Provisional.AssignedRef = "preserved_created"
		} else {
			p.Provisional.AssignedRef = "preserved_present"
		}
	} else {
		p.Provisional.AssignedRef = "absent"
	}
	if sourceErr != nil {
		unsafe("new_source_or_branch_unavailable")
	} else {
		p.NewCapturedOID = tip
	}
	if l.store.CheckCreateReplacementTarget(ctx, req.Project, req.Branch, oldUUID) != nil || l.store.CheckCreateReplacementTarget(ctx, s.Project, s.Branch, oldUUID) != nil {
		unsafe("target_assignment_unavailable")
	}
	if p.NewCapturedOID != "" && p.NewCapturedOID == p.OldCapturedOID && p.NewPolicySHA256 == p.OldPolicySHA256 && control.SameCreateSelection(oldReq, req) {
		unsafe("request_unchanged")
	}
	if cleanup, e := l.createReplaceEffects(ctx, op, s); e != nil {
		unsafe("provisional_resource_unavailable")
	} else {
		p.Provisional.Runtime = "absent"
		p.Provisional.Builder = "absent"
		p.Provisional.SessionKey = "absent"
		p.Provisional.Endpoint = "absent"
		p.Provisional.Principal = "absent"
		if cleanup != nil {
			p.Provisional.SessionKey = "present"
			p.Provisional.Endpoint = "present"
			p.Provisional.Principal = "present"
			p.Provisional.Cleanup = cleanup
		}
	}
	p.Eligible = len(p.UnsafeReasons) == 0
	return p, nil
}

func (l *lifecycle) PreviewCreateReplace(ctx context.Context, oldUUID string, req control.ReserveSessionRequest) (control.CreateReplacePreview, error) {
	release, err := l.lockSession(ctx, oldUUID)
	if err != nil {
		return control.CreateReplacePreview{}, err
	}
	defer release()
	p, err := l.createReplaceFacts(ctx, oldUUID, req)
	if err != nil || !p.Eligible {
		return p, err
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		return control.CreateReplacePreview{}, err
	}
	expiry := time.Now().Add(2 * time.Minute)
	p.ConfirmationToken = hex.EncodeToString(token[:])
	p.ExpiresAt = expiry.UTC().Format(time.RFC3339Nano)
	encoded, err := json.Marshal(map[string]any{"v": 1, "preview": p})
	if err != nil || len(encoded) > control.MaxFrameBytes-1024 {
		return control.CreateReplacePreview{}, control.ErrConflict
	}
	l.mu.Lock()
	if l.createReplacePreviews == nil {
		l.createReplacePreviews = map[string]createReplacePreviewState{}
	}
	for key, state := range l.createReplacePreviews {
		if !time.Now().Before(state.Expiry) {
			delete(l.createReplacePreviews, key)
		}
	}
	if len(l.createReplacePreviews) >= 128 {
		l.mu.Unlock()
		return control.CreateReplacePreview{}, control.ErrConflict
	}
	l.createReplacePreviews[p.ConfirmationToken] = createReplacePreviewState{Preview: p, Expiry: expiry}
	l.mu.Unlock()
	return p, nil
}

func (l *lifecycle) ConfirmCreateReplace(ctx context.Context, oldUUID, key, token string) (control.Operation, error) {
	if len(oldUUID) != 36 || key == "" || len(key) > 128 || len(token) != 32 {
		return control.Operation{}, control.ErrInvalid
	}
	hash := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(hash[:])
	// Accepted replay reads durable intent only. Cleanup may own the retired
	// UUID lock for its entire recovery; replay must not need that mutation lock.
	if prior, found, e := l.store.ReplayCreateReplacement(ctx, key, oldUUID, tokenHash); e != nil || found {
		return prior, e
	}
	release, err := l.lockSession(ctx, oldUUID)
	if err != nil {
		return control.Operation{}, err
	}
	defer release()
	// Another confirmation may have completed between the read and admission.
	if prior, found, e := l.store.ReplayCreateReplacement(ctx, key, oldUUID, tokenHash); e != nil || found {
		return prior, e
	}
	l.mu.Lock()
	state, ok := l.createReplacePreviews[token]
	l.mu.Unlock()
	if !ok || state.Preview.OldUUID != oldUUID || state.Preview.NewRequest.Key != key {
		return control.Operation{}, control.ErrConflict
	}
	intent := control.CreateReplaceIntent{OldOperationID: state.Preview.OldOperationID, OldUUID: oldUUID,
		OldEvidenceSHA256: state.Preview.OldEvidenceSHA256, OldPolicySHA256: state.Preview.OldPolicySHA256,
		NewPolicySHA256: state.Preview.NewPolicySHA256,
		Cleanup:         state.Preview.Provisional.Cleanup,
		TokenSHA256:     tokenHash, ExpiresAt: state.Preview.ExpiresAt, New: state.Preview.NewRequest}
	if !time.Now().Before(state.Expiry) {
		return control.Operation{}, control.ErrConflict
	}
	p, e := l.createReplaceFacts(ctx, oldUUID, state.Preview.NewRequest)
	if e != nil || !p.Eligible {
		return control.Operation{}, errors.Join(e, control.ErrConflict)
	}
	p.ConfirmationToken, p.ExpiresAt = token, state.Preview.ExpiresAt
	if !reflect.DeepEqual(p, state.Preview) {
		return control.Operation{}, control.ErrConflict
	}
	oldOp, e := l.store.GetOperation(ctx, p.OldOperationID)
	if e != nil {
		return control.Operation{}, e
	}
	s, e := l.store.GetSession(ctx, oldUUID)
	if e != nil {
		return control.Operation{}, e
	}
	op, e := l.store.ReplaceBlockedCreate(ctx, intent, l.cfg.BaseImageFingerprint, l.selection(), l.environmentIntent(), l.observeSessionCapacity,
		func(call context.Context) (string, error) {
			if cleanup, e := l.createReplaceEffects(call, oldOp, s); e != nil || !reflect.DeepEqual(cleanup, state.Preview.Provisional.Cleanup) {
				return "", errors.Join(e, control.ErrConflict)
			}
			oldBranch, target, tip, e := observeCreateReplacementSources(call, l.git.backend, state.Preview.OldRequest, state.Preview.NewRequest, state.Preview.OldCapturedOID)
			if e != nil || tip != state.Preview.NewCapturedOID || oldBranch != state.Preview.OldBranch || target != state.Preview.NewBranch {
				return "", errors.Join(e, control.ErrConflict)
			}
			return tip, nil
		})
	if e != nil {
		return control.Operation{}, e
	}
	l.mu.Lock()
	delete(l.createReplacePreviews, token)
	l.mu.Unlock()
	l.recordCreation(op, state.Preview.NewRequest.Branch)
	if e = l.enqueue(op.ID); e != nil {
		return op, e
	}
	return op, nil
}
