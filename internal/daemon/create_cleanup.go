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

type createCleanupPreviewState struct {
	Preview control.CreateCleanupPreview
	Expiry  time.Time
}

func (l *lifecycle) reviewCreateCleanupLocal(ctx context.Context, op control.Operation, s control.Session) (*control.CreateReplacementCleanup, error) {
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
	parent, e := replacementLocalIdentity(filepath.Join(state, "session_keys"))
	if e != nil && !errors.Is(e, os.ErrNotExist) || e == nil && parent.Mode != 040700 {
		return control.ErrConflict
	}
	prefix, e := replacementLocalIdentity(endpoints.prefix)
	if e != nil || prefix.Mode != 040700 {
		return control.ErrConflict
	}

	if _, err := os.Lstat(filepath.Join(state, "session_keys", uuid)); !errors.Is(err, os.ErrNotExist) {
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
func (l *lifecycle) createCleanupNativeAbsent(ctx context.Context, p control.CreateCleanupPreview, instance string) error {
	return l.runtime.ConfirmFailedCreateEffectsAbsent(ctx, runtimeincus.Session{InstanceUUID: instance, SessionUUID: p.UUID, ProjectPath: p.OldRequest.Project, ContractVersion: "1", ImageFingerprint: p.ImageFingerprint}, p.OldOperationID)
}
func (l *lifecycle) createCleanupFacts(ctx context.Context, uuid string) (control.CreateCleanupPreview, error) {
	p := control.CreateCleanupPreview{UUID: uuid, UnsafeReasons: []string{}, ExternalMounts: "preserved", SharedImages: "preserved", Provisional: control.CreateReplaceResources{RuntimeLocal: "unavailable"}}
	unsafe := func(reason string) { p.UnsafeReasons = append(p.UnsafeReasons, reason) }
	op, err := l.store.CreationForSession(ctx, uuid)
	if err != nil {
		return p, err
	}
	s, err := l.store.GetSession(ctx, uuid)
	if err != nil {
		return p, err
	}
	ev, err := control.Evidence(op)
	if err != nil || json.Unmarshal(op.Request, &p.OldRequest) != nil {
		return p, control.ErrConflict
	}
	p.OldOperationID, p.OldPhase, p.OldEvidenceSHA256 = op.ID, op.Phase, hexDigest(op.Evidence)
	p.PolicySHA256, p.ImageFingerprint, p.EnvironmentBuilder = s.PolicySHA256, ev.ImageFingerprint, ev.EnvironmentBuilder
	if op.Kind != "session.create" || op.Status != "blocked" || s.Registry != "creating" || p.OldRequest.Project != s.Project || p.OldRequest.Branch != s.Branch || ev.PolicySHA256 != s.PolicySHA256 {
		unsafe("blocked_local_creation_required")
	}
	if !control.SafeCreateCleanupEvidence(p.OldRequest, ev) {
		unsafe("native_or_builder_dispatch_unsettled_or_publication_origin_unsupported")
	}
	if !control.SafeCreateCleanupPhase(op) {
		unsafe("assembled_workspace_requires_dedicated_loss_inspection; preserve_or_retry_exact_request")
	}
	l.mu.Lock()
	working := l.working[op.ID] || l.hasAttachmentLocked(uuid)
	l.mu.Unlock()
	if working {
		unsafe("creation_worker_or_attachment_active")
	}
	oid, exists, e := l.git.backend.InspectBranchRef(ctx, s.Project, s.Branch)
	p.AssignedBranch = control.CreateReplaceBranch{Ref: "refs/heads/" + s.Branch, Observed: e == nil, Exists: exists, OID: oid}
	if e != nil || !exists {
		unsafe("assigned_P_ref_unavailable")
	}
	if l.store.CheckCreateReplacementTarget(ctx, s.Project, s.Branch, uuid) != nil {
		unsafe("assignment_or_lifecycle_guard_unavailable")
	}
	local, e := l.reviewCreateCleanupLocal(ctx, op, s)
	if e != nil {
		unsafe("local_credentials_or_endpoints_unverified")
	} else {
		p.Provisional.Cleanup = local
		p.Provisional.SessionKey = "absent"
		p.Provisional.Endpoint = "absent"
		p.Provisional.Principal = "absent"
		if local != nil {
			p.Provisional.SessionKey = "present"
			p.Provisional.Endpoint = "present"
			p.Provisional.Principal = "present"
		}
	}
	if e = l.createCleanupNativeAbsent(ctx, p, l.instanceID); e != nil {
		unsafe("native_runtime_or_builder_present_ambiguous_or_unreachable")
	} else {
		p.Provisional.Runtime = "absent"
		p.Provisional.Builder = "absent"
	}
	p.Provisional.AssignedRef = "preserved_existing"
	p.Eligible = len(p.UnsafeReasons) == 0
	return p, nil
}
func hexDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func (l *lifecycle) PreviewCreateCleanup(ctx context.Context, uuid string) (control.CreateCleanupPreview, error) {
	release, err := l.lockSession(ctx, uuid)
	if err != nil {
		return control.CreateCleanupPreview{}, err
	}
	defer release()
	p, err := l.createCleanupFacts(ctx, uuid)
	if err != nil || !p.Eligible {
		return p, err
	}
	var bytes [16]byte
	if _, err = rand.Read(bytes[:]); err != nil {
		return p, err
	}
	expiry := time.Now().Add(2 * time.Minute)
	p.ConfirmationToken = hex.EncodeToString(bytes[:])
	p.ExpiresAt = expiry.UTC().Format(time.RFC3339Nano)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.createCleanupPreviews == nil {
		l.createCleanupPreviews = map[string]createCleanupPreviewState{}
	}
	for k, v := range l.createCleanupPreviews {
		if !time.Now().Before(v.Expiry) {
			delete(l.createCleanupPreviews, k)
		}
	}
	if len(l.createCleanupPreviews) >= 128 {
		return control.CreateCleanupPreview{}, control.ErrConflict
	}
	l.createCleanupPreviews[p.ConfirmationToken] = createCleanupPreviewState{p, expiry}
	return p, nil
}
func (l *lifecycle) ConfirmCreateCleanup(ctx context.Context, uuid, key, token string) (control.Operation, error) {
	if len(uuid) != 36 || key == "" || len(key) > 128 || len(token) != 32 {
		return control.Operation{}, control.ErrInvalid
	}
	req := control.DiscardRequest{UUID: uuid, Key: key, TokenSHA256: hexDigest([]byte(token))}
	replay := func() (control.Operation, bool, error) {
		op, e := l.store.GetOperationByKey(ctx, key)
		if errors.Is(e, control.ErrNotFound) {
			return op, false, nil
		}
		var saved control.DiscardRequest
		if e != nil {
			return op, true, e
		}
		if op.Kind != "session.create.cleanup" || json.Unmarshal(op.Request, &saved) != nil || saved != req {
			return control.Operation{}, true, control.ErrConflict
		}
		return op, true, nil
	}
	if op, found, e := replay(); found || e != nil {
		return op, e
	}
	release, err := l.lockSession(ctx, uuid)
	if err != nil {
		return control.Operation{}, err
	}
	defer release()
	if op, found, e := replay(); found || e != nil {
		return op, e
	}
	l.mu.Lock()
	state, found := l.createCleanupPreviews[token]
	l.mu.Unlock()
	if !found || state.Preview.UUID != uuid || !time.Now().Before(state.Expiry) {
		return control.Operation{}, control.ErrConflict
	}
	fresh, err := l.createCleanupFacts(ctx, uuid)
	if err != nil {
		return control.Operation{}, err
	}
	fresh.ConfirmationToken, fresh.ExpiresAt = token, state.Preview.ExpiresAt
	if !fresh.Eligible || !reflect.DeepEqual(fresh, state.Preview) {
		return control.Operation{}, control.ErrConflict
	}
	ev := control.CreateCleanupEvidence{Review: fresh, InstanceUUID: l.instanceID}
	ev.Review.ConfirmationToken = ""
	op, err := l.store.BeginCreateCleanup(ctx, req, ev, func(call context.Context) error {
		actual, e := l.createCleanupFacts(call, uuid)
		actual.ConfirmationToken, actual.ExpiresAt = token, fresh.ExpiresAt
		if e != nil || !actual.Eligible || !reflect.DeepEqual(actual, fresh) {
			return control.ErrConflict
		}
		return nil
	})
	if err != nil {
		return op, err
	}
	l.mu.Lock()
	delete(l.createCleanupPreviews, token)
	l.mu.Unlock()
	l.recordProgress(op, "running")
	return op, l.enqueue(op.ID)
}
func (l *lifecycle) processCreateCleanup(observed control.Operation) {
	ctx := l.ctx
	slot := l.sessionLockSlot(observed.SessionUUID)
	select {
	case <-ctx.Done():
		return
	case <-slot:
	}
	defer func() { slot <- struct{}{} }()
	op, err := l.store.GetOperation(ctx, observed.ID)
	if err != nil || op.Kind != "session.create.cleanup" || op.Status != "running" && op.Status != "blocked" {
		return
	}
	var ev control.CreateCleanupEvidence
	if json.Unmarshal(op.Evidence, &ev) != nil {
		return
	}
	block := func(e error) {
		if ctx.Err() == nil {
			diagnostic := "failed creation cleanup refused: " + e.Error()
			if len(diagnostic) > 1024 {
				diagnostic = diagnostic[:1024]
			}
			_ = l.store.AdvanceOperation(ctx, op.ID, "blocked", op.Phase, true, op.Evidence, diagnostic)
		}
	}
	old, oldErr := l.store.GetOperation(ctx, ev.Review.OldOperationID)
	session, sessionErr := l.store.GetSession(ctx, op.SessionUUID)
	var request control.ReserveSessionRequest
	var original control.CreationEvidence
	// Recheck the durable retired authority before touching any local entry.
	// The original evidence remains inspectable after the atomic handoff.
	if oldErr != nil || sessionErr != nil || old.Kind != "session.create" || old.Status != "superseded" || old.SessionUUID != op.SessionUUID || old.Project != op.Project || session.Registry != "removing" || session.Project != op.Project || session.Branch != ev.Review.OldRequest.Branch || ev.InstanceUUID != l.instanceID || ev.Review.UUID != op.SessionUUID || ev.Review.OldEvidenceSHA256 != hexDigest(old.Evidence) || json.Unmarshal(old.Request, &request) != nil || request != ev.Review.OldRequest || json.Unmarshal(old.Evidence, &original) != nil || !control.SafeCreateCleanupEvidence(request, original) || original.PolicySHA256 != ev.Review.PolicySHA256 || original.ImageFingerprint != ev.Review.ImageFingerprint || ev.Review.AssignedBranch.Ref != "refs/heads/"+session.Branch {
		block(control.ErrConflict)
		return
	}
	if op.Status == "blocked" {
		if err = l.store.AdvanceOperation(ctx, op.ID, "running", op.Phase, true, op.Evidence, ""); err != nil {
			return
		}
		op.Status = "running"
	}
	prove := func(call context.Context) error { return l.createCleanupNativeAbsent(call, ev.Review, ev.InstanceUUID) }
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
		if err = l.store.AdvanceOperation(ctx, op.ID, "running", "local-complete", true, raw, ""); err != nil {
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
			oid, exists, e := l.git.backend.InspectBranchRef(call, op.Project, review.Review.OldRequest.Branch)
			if e != nil || !exists || oid != review.Review.AssignedBranch.OID {
				return control.ErrConflict
			}
			return nil
		})
		if err != nil {
			block(err)
			return
		}
		l.recordProgress(op, "completed")
	default:
		block(errors.New("cleanup checkpoint unavailable"))
	}
}
