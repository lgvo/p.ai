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
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

type createReplacePreviewState struct {
	Preview control.CreateReplacePreview
	Expiry  time.Time
}

func (l *lifecycle) createReplaceTip(ctx context.Context, req control.ReserveSessionRequest) (string, error) {
	tip, exists, err := l.git.backend.InspectBranchRef(ctx, req.Project, req.Branch)
	if err != nil || !exists {
		return "", errors.Join(err, control.ErrConflict)
	}
	observed, err := l.git.backend.ObserveSource(ctx, req.Project, plugin.GitSourceSelector{Kind: "branch", Value: "refs/heads/" + req.Branch})
	if err != nil || observed != tip {
		return "", errors.Join(err, control.ErrConflict)
	}
	return tip, nil
}

// createReplaceEffects proves absence of every UUID-scoped effect the selected
// creation path could have reached. It never removes a native resource.
func (l *lifecycle) createReplaceEffects(ctx context.Context, op control.Operation, s control.Session) error {
	if op.Kind != "session.create" || op.Status != "blocked" ||
		(op.Phase != "source-ready" && op.Phase != "branch-assigned") || s.Registry != "creating" {
		return control.ErrConflict
	}
	l.mu.Lock()
	working := l.working[op.ID] || l.hasAttachmentLocked(s.UUID)
	l.mu.Unlock()
	if working {
		return control.ErrConflict
	}
	ev, err := control.Evidence(op)
	if err != nil || !ev.BranchExisted || ev.RefCASIntent || ev.BuilderTreeOID != "" || ev.EnvironmentState != nil ||
		(op.Phase == "source-ready" && op.Committed || op.Phase == "branch-assigned" && !op.Committed) {
		return control.ErrConflict
	}
	if _, found, err := l.store.SessionGitPrincipal(ctx, s.UUID); err != nil || found {
		return errors.Join(err, control.ErrConflict)
	}
	if _, err := os.Lstat(filepath.Join(l.store.StateDir(), "session_keys", s.UUID)); !errors.Is(err, os.ErrNotExist) {
		return errors.Join(err, control.ErrConflict)
	}
	l.endpoints.mu.Lock()
	opened := l.endpoints.opened[s.UUID] != nil
	l.endpoints.mu.Unlock()
	if opened {
		return control.ErrConflict
	}
	if _, err := os.Lstat(filepath.Join(l.cfg.EndpointPrefix, s.UUID)); !errors.Is(err, os.ErrNotExist) {
		return errors.Join(err, control.ErrConflict)
	}
	return l.runtime.ConfirmFailedCreateEffectsAbsent(ctx, runtimeincus.Session{InstanceUUID: l.instanceID,
		SessionUUID: s.UUID, ProjectPath: s.Project, ContractVersion: "1", ImageFingerprint: ev.ImageFingerprint}, op.ID)
}

func (l *lifecycle) createReplaceFacts(ctx context.Context, oldUUID string, req control.ReserveSessionRequest) (control.CreateReplacePreview, error) {
	p := control.CreateReplacePreview{OldUUID: oldUUID, NewRequest: req, UnsafeReasons: []string{},
		Provisional: control.CreateReplaceResources{}, NewImageFingerprint: l.cfg.BaseImageFingerprint,
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
	if req.Project != s.Project || req.Branch != s.Branch || req.Choice != "existing" {
		unsafe("unsupported_replacement_choice")
	}
	if op.Status != "blocked" || op.Kind != "session.create" || s.Registry != "creating" {
		unsafe("old_creation_not_blocked")
	}
	if op.Phase != "source-ready" && op.Phase != "branch-assigned" || !ev.BranchExisted || ev.RefCASIntent || ev.BuilderTreeOID != "" || ev.EnvironmentState != nil {
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
	if tip, e := l.createReplaceTip(ctx, req); e != nil {
		unsafe("new_source_unavailable")
	} else {
		p.NewCapturedOID = tip
	}
	if p.NewCapturedOID != "" && p.NewCapturedOID == p.OldCapturedOID && p.NewPolicySHA256 == p.OldPolicySHA256 {
		unsafe("request_unchanged")
	}
	if e := l.createReplaceEffects(ctx, op, s); e != nil {
		unsafe("provisional_resource_unavailable")
	} else {
		p.Provisional.Runtime = "absent"
		p.Provisional.Builder = "absent"
		p.Provisional.SessionKey = "absent"
		p.Provisional.Endpoint = "absent"
		p.Provisional.Principal = "absent"
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
	release, err := l.lockSession(ctx, oldUUID)
	if err != nil {
		return control.Operation{}, err
	}
	defer release()
	hash := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(hash[:])
	if prior, e := l.store.GetOperationByKey(ctx, key); e == nil {
		var saved control.CreationEvidence
		if prior.Kind != "session.create" || json.Unmarshal(prior.Evidence, &saved) != nil ||
			saved.SupersedesUUID != oldUUID || saved.ReplacementTokenSHA256 != tokenHash {
			return control.Operation{}, control.ErrConflict
		}
		return prior, nil
	} else if !errors.Is(e, control.ErrNotFound) {
		return control.Operation{}, e
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
			if e := l.createReplaceEffects(call, oldOp, s); e != nil {
				return "", e
			}
			tip, e := l.createReplaceTip(call, state.Preview.NewRequest)
			if e != nil || tip != state.Preview.NewCapturedOID {
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
	l.recordCreation(op, s.Branch)
	if e = l.enqueue(op.ID); e != nil {
		return op, e
	}
	return op, nil
}
