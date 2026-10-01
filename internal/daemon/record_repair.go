package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

type recordRepairPreviewState struct {
	Preview control.RecordRepairPreview
	Expiry  time.Time
}

func (l *lifecycle) recordRepairNative(s control.Session) runtimeincus.Session {
	// Only the deterministic name and labels are needed for an absence proof.
	// No image selection or policy-dependent runtime mutation is involved.
	return runtimeincus.Session{InstanceUUID: l.instanceID, SessionUUID: s.UUID,
		ProjectPath: s.Project, ContractVersion: "1", ImageFingerprint: l.cfg.BaseImageFingerprint}
}

func (l *lifecycle) recordRepairAbsence(ctx context.Context, s control.Session) error {
	tip, exists, err := l.git.backend.InspectBranchRef(ctx, s.Project, s.Branch)
	if err != nil || exists || tip != "" {
		return errors.Join(err, control.ErrConflict)
	}
	return l.runtime.ConfirmSessionRuntimeAbsent(ctx, l.recordRepairNative(s))
}

func (l *lifecycle) recordRepairFacts(ctx context.Context, uuid string) (control.RecordRepairPreview, error) {
	s, err := l.store.GetSession(ctx, uuid)
	if err != nil || s.Registry != "established" {
		return control.RecordRepairPreview{}, errors.Join(err, control.ErrConflict)
	}
	p := control.RecordRepairPreview{Kind: "unrecoverable_session_record", SessionUUID: uuid,
		Project: s.Project, Branch: s.Branch, IncusProject: l.cfg.IncusProject,
		InstanceName: "p-" + uuid, PolicySHA256: s.PolicySHA256,
		ExternalAuthority: "none_registered", UnsafeReasons: []string{}}
	unsafe := func(reason string) { p.UnsafeReasons = append(p.UnsafeReasons, reason) }
	principal, registered, err := l.store.SessionGitPrincipal(ctx, uuid)
	if err != nil {
		return p, err
	}
	if registered {
		p.Principal = principal
		p.PrincipalActive, err = l.store.IsSessionGitPrincipalActive(ctx, uuid)
		if err != nil {
			return p, err
		}
	}
	tip, exists, err := l.git.backend.InspectBranchRef(ctx, s.Project, s.Branch)
	if err != nil || tip != "" && !exists {
		unsafe("assigned_ref_unavailable")
	} else if exists {
		p.AssignedRefStatus = "present"
		unsafe("assigned_ref_present")
	} else {
		p.AssignedRefStatus = "missing"
	}
	if err := l.runtime.ConfirmSessionRuntimeAbsent(ctx, l.recordRepairNative(s)); err != nil {
		unsafe("runtime_absence_unavailable")
	} else {
		p.RuntimeStatus = "missing"
	}
	if active, e := l.store.HasActiveSessionOperation(ctx, uuid); e != nil || active {
		unsafe("operation_active")
	}
	l.mu.Lock()
	attached := l.hasAttachmentLocked(uuid)
	l.mu.Unlock()
	if attached {
		unsafe("attachment_active")
	}
	// Current selected MVP modules hold no P-owned external credential; the
	// only P-issued session authority is the SQLite Git principal. Unknown
	// module contracts never reach this lifecycle: newLifecycle rejects them.
	p.Eligible = len(p.UnsafeReasons) == 0
	return p, nil
}

func (l *lifecycle) PreviewRecordRepair(ctx context.Context, uuid string) (control.RecordRepairPreview, error) {
	if len(uuid) != 36 {
		return control.RecordRepairPreview{}, control.ErrInvalid
	}
	release, err := l.lockSession(ctx, uuid)
	if err != nil {
		return control.RecordRepairPreview{}, err
	}
	defer release()
	p, err := l.recordRepairFacts(ctx, uuid)
	if err != nil || !p.Eligible {
		return p, err
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		return control.RecordRepairPreview{}, err
	}
	expiry := time.Now().Add(repairPreviewTTL)
	p.ConfirmationToken, p.ExpiresAt = hex.EncodeToString(token[:]), expiry.UTC().Format(time.RFC3339Nano)
	encoded, err := json.Marshal(map[string]any{"v": 1, "preview": p})
	if err != nil || len(encoded) > control.MaxFrameBytes-1024 {
		return control.RecordRepairPreview{}, errors.New("record repair preview exceeds bounded response")
	}
	l.mu.Lock()
	if l.recordRepairPreviews == nil {
		l.recordRepairPreviews = map[string]recordRepairPreviewState{}
	}
	for key, state := range l.recordRepairPreviews {
		if !time.Now().Before(state.Expiry) {
			delete(l.recordRepairPreviews, key)
		}
	}
	if len(l.recordRepairPreviews) >= 128 {
		l.mu.Unlock()
		return control.RecordRepairPreview{}, errors.New("record repair preview capacity unavailable")
	}
	l.recordRepairPreviews[p.ConfirmationToken] = recordRepairPreviewState{Preview: p, Expiry: expiry}
	l.mu.Unlock()
	return p, nil
}

func (l *lifecycle) ConfirmRecordRepair(ctx context.Context, key, uuid, token string) (control.Operation, error) {
	if key == "" || len(key) > 128 || len(uuid) != 36 || len(token) != 32 {
		return control.Operation{}, control.ErrInvalid
	}
	release, err := l.lockSession(ctx, uuid)
	if err != nil {
		return control.Operation{}, err
	}
	defer release()
	hash := sha256.Sum256([]byte(token))
	req := control.RecordRepairRequest{Key: key, UUID: uuid, TokenSHA256: hex.EncodeToString(hash[:])}
	if prior, e := l.store.GetOperationByKey(ctx, key); e == nil {
		var saved control.RecordRepairRequest
		if prior.Kind != "session.record.repair" || json.Unmarshal(prior.Request, &saved) != nil || saved != req {
			return control.Operation{}, control.ErrConflict
		}
		if prior.Status == "running" {
			_ = l.enqueue(prior.ID)
		}
		return prior, nil
	} else if !errors.Is(e, control.ErrNotFound) {
		return control.Operation{}, e
	}
	l.mu.Lock()
	state, found := l.recordRepairPreviews[token]
	l.mu.Unlock()
	if !found || !time.Now().Before(state.Expiry) || !state.Preview.Eligible || state.Preview.SessionUUID != uuid {
		return control.Operation{}, control.ErrConflict
	}
	p, err := l.recordRepairFacts(ctx, uuid)
	if err != nil || !p.Eligible {
		return control.Operation{}, errors.Join(err, control.ErrConflict)
	}
	p.ConfirmationToken, p.ExpiresAt = state.Preview.ConfirmationToken, state.Preview.ExpiresAt
	if !reflect.DeepEqual(p, state.Preview) {
		return control.Operation{}, control.ErrConflict
	}
	ev := control.RecordRepairEvidence{Project: p.Project, Branch: p.Branch, Principal: p.Principal,
		PrincipalActive: p.PrincipalActive, ExternalAuthority: p.ExternalAuthority,
		PolicySHA256: p.PolicySHA256, InstanceUUID: l.instanceID,
		IncusProject: p.IncusProject, InstanceName: p.InstanceName,
		ExpiresAt: state.Expiry.UTC().Format(time.RFC3339Nano)}
	s, err := l.store.GetSession(ctx, uuid)
	if err != nil {
		return control.Operation{}, err
	}
	op, err := l.store.BeginRecordRepair(ctx, req, ev, func(call context.Context) error { return l.recordRepairAbsence(call, s) })
	if err != nil {
		return control.Operation{}, err
	}
	l.mu.Lock()
	delete(l.recordRepairPreviews, token)
	l.mu.Unlock()
	if err = l.enqueue(op.ID); err != nil {
		return op, err
	}
	return op, nil
}

func (l *lifecycle) blockRecordRepair(op control.Operation, cause error) {
	if l.ctx.Err() != nil {
		return
	}
	message := fmt.Sprintf("record repair blocked: %v", cause)
	if len(message) > 900 {
		message = message[:900]
	}
	_ = l.store.AdvanceOperation(l.ctx, op.ID, "blocked", op.Phase, op.Committed, op.Evidence, message)
}

func (l *lifecycle) processRecordRepair(op control.Operation) {
	if op.Status != "running" {
		return
	}
	var ev control.RecordRepairEvidence
	if json.Unmarshal(op.Evidence, &ev) != nil || ev.InstanceUUID != l.instanceID || ev.IncusProject != l.cfg.IncusProject ||
		ev.InstanceName != "p-"+op.SessionUUID || ev.Project != op.Project {
		l.blockRecordRepair(op, errors.New("durable record repair identity unavailable"))
		return
	}
	ctx := l.ctx
	s, err := l.store.GetSession(ctx, op.SessionUUID)
	if err != nil || s.Project != ev.Project || s.Branch != ev.Branch || s.PolicySHA256 != ev.PolicySHA256 ||
		s.Registry != "established" && s.Registry != "removing" {
		l.blockRecordRepair(op, errors.Join(err, errors.New("record repair assignment changed")))
		return
	}
	observe := func(call context.Context) error { return l.recordRepairAbsence(call, s) }
	switch op.Phase {
	case "guarded":
		if err := l.store.CommitRecordRepair(ctx, op.ID, observe); err != nil {
			l.blockRecordRepair(op, err)
			return
		}
		op.Phase, op.Committed = "authority-disabled", true
		fallthrough
	case "authority-disabled":
		if err := observe(ctx); err != nil {
			l.blockRecordRepair(op, err)
			return
		}
		if err := l.endpoints.RemoveSession(op.SessionUUID); err != nil {
			l.blockRecordRepair(op, err)
			return
		}
		if err := removeSessionKeyAt(l.store.StateDir(), op.SessionUUID); err != nil {
			l.blockRecordRepair(op, err)
			return
		}
		if err := l.store.AdvanceOperation(ctx, op.ID, "running", "secrets-absent", true, op.Evidence, ""); err != nil {
			l.blockRecordRepair(op, err)
			return
		}
		op.Phase = "secrets-absent"
		fallthrough
	case "secrets-absent":
		if err := l.store.CompleteRecordRepair(ctx, op.ID, observe); err != nil {
			l.blockRecordRepair(op, err)
		}
	default:
		l.blockRecordRepair(op, control.ErrConflict)
	}
}
