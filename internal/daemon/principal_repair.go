package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
)

type principalRepairPreviewState struct {
	Preview control.PrincipalRepairPreview
	Expiry  time.Time
}

func principalKeyDir(stateDir string) (string, error) {
	dir := filepath.Join(stateDir, "session_keys")
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0700 || owner.Uid != uint32(os.Geteuid()) {
		return "", errors.New("session key directory is unsafe")
	}
	return dir, nil
}

func principalKeyStatus(stateDir, uuid, old string) (string, error) {
	if _, err := principalKeyDir(stateDir); err != nil {
		return "unsafe", err
	}
	fingerprint, err := inspectRegisteredSessionKey(stateDir, uuid)
	if errors.Is(err, os.ErrNotExist) {
		return "missing", nil
	}
	if err != nil {
		return "unsafe", err
	}
	if old == "" || fingerprint != old {
		return "mismatch", nil
	}
	return "matching", nil
}

func principalRepairRefFacts(tip string, exists bool) (status, assignedTip, unsafeReason string) {
	if exists {
		return "present", tip, ""
	}
	if tip == "" {
		return "missing", "", "assigned_ref_missing"
	}
	return "", "", "assigned_ref_unavailable"
}

func (l *lifecycle) principalRepairFacts(ctx context.Context, uuid string) (control.PrincipalRepairPreview, bool, error) {
	s, err := l.store.GetSession(ctx, uuid)
	if err != nil || s.Registry != "established" {
		return control.PrincipalRepairPreview{}, false, errors.Join(err, control.ErrConflict)
	}
	native, err := l.runtimeSession(ctx, s)
	if err != nil {
		return control.PrincipalRepairPreview{}, false, err
	}
	p := control.PrincipalRepairPreview{Kind: "session_git_principal", SessionUUID: uuid, Project: s.Project,
		Branch: s.Branch, IncusProject: l.cfg.IncusProject, InstanceName: "p-" + uuid,
		ImageFingerprint: native.ImageFingerprint, PolicySHA256: s.PolicySHA256, UnsafeReasons: []string{}}
	unsafe := func(reason string) { p.UnsafeReasons = append(p.UnsafeReasons, reason) }
	tip, exists, err := l.git.backend.InspectBranchRef(ctx, s.Project, s.Branch)
	if err != nil {
		return p, false, err
	}
	var reason string
	p.AssignedRefStatus, p.AssignedTip, reason = principalRepairRefFacts(tip, exists)
	if reason != "" {
		// A project.create row does not prove the branch is still unborn: its
		// ref may have been committed and subsequently deleted. This repair
		// never interprets an absent P ref as an authority to rotate a key.
		unsafe(reason)
	}
	if exists {
		present, e := l.git.backend.PCommitPresent(ctx, s.Project, tip)
		if e != nil || !present {
			unsafe("assigned_tip_unavailable")
		}
	}
	if l.policyCondition(ctx, s) != "current" {
		unsafe("policy_changed")
	}
	old, registered, err := l.store.SessionGitPrincipal(ctx, uuid)
	if err != nil {
		return p, false, err
	}
	active, err := l.store.IsSessionGitPrincipalActive(ctx, uuid)
	if err != nil {
		return p, false, err
	}
	p.OldFingerprint = old
	switch {
	case !registered:
		p.Registration = "missing"
	case !active:
		p.Registration = "revoked"
	default:
		p.Registration = "active"
	}
	p.KeyStatus, err = principalKeyStatus(l.store.StateDir(), uuid, old)
	if err != nil {
		unsafe("host_key_unsafe")
	}
	observed, err := l.runtime.Inspect(ctx, native)
	if err != nil {
		return p, active, err
	}
	if !observed.Exists || observed.Name != p.InstanceName || observed.Fingerprint != p.ImageFingerprint ||
		observed.IncusUUID == "" || observed.Generation == "" {
		unsafe("runtime_changed")
	} else {
		p.RuntimeStatus, p.IncusUUID, p.Generation = observed.Status, observed.IncusUUID, observed.Generation
		if observed.Status != "Stopped" {
			unsafe("runtime_not_stopped")
		} else {
			guest, e := l.runtime.InspectStoppedSessionIdentity(ctx, native, observed.IncusUUID, observed.Generation)
			if e != nil {
				unsafe("runtime_key_target_unavailable")
			} else if !guest.Exists {
				p.GuestKeyStatus = "missing"
			} else if guest.Fingerprint == "" {
				p.GuestKeyStatus = "invalid"
			} else if guest.Fingerprint == old {
				p.GuestKeyStatus = "matching"
			} else {
				p.GuestKeyStatus = "mismatch"
			}
			p.GuestKeySHA256 = guest.SHA256
		}
	}
	if active && p.KeyStatus == "matching" && p.GuestKeyStatus == "matching" {
		unsafe("principal_intact")
	}
	if err := l.checkNoWorkspaceInspect(ctx, uuid); err != nil {
		unsafe("operation_active")
	}
	l.mu.Lock()
	attached := l.hasAttachmentLocked(uuid)
	l.mu.Unlock()
	if attached {
		unsafe("attachment_active")
	}
	p.Eligible = len(p.UnsafeReasons) == 0
	return p, active, nil
}

func (l *lifecycle) PreviewPrincipalRepair(ctx context.Context, uuid string) (control.PrincipalRepairPreview, error) {
	if len(uuid) != 36 {
		return control.PrincipalRepairPreview{}, control.ErrInvalid
	}
	release, err := l.lockSession(ctx, uuid)
	if err != nil {
		return control.PrincipalRepairPreview{}, err
	}
	defer release()
	p, _, err := l.principalRepairFacts(ctx, uuid)
	if err != nil || !p.Eligible {
		return p, err
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return control.PrincipalRepairPreview{}, err
	}
	expiry := time.Now().Add(repairPreviewTTL)
	p.ConfirmationToken, p.ExpiresAt = hex.EncodeToString(token[:]), expiry.UTC().Format(time.RFC3339Nano)
	encoded, err := json.Marshal(map[string]any{"v": 1, "preview": p})
	if err != nil || len(encoded) > control.MaxFrameBytes-1024 {
		return control.PrincipalRepairPreview{}, errors.New("principal repair preview exceeds bounded response")
	}
	l.mu.Lock()
	if l.principalRepairPreviews == nil {
		l.principalRepairPreviews = map[string]principalRepairPreviewState{}
	}
	for key, state := range l.principalRepairPreviews {
		if !time.Now().Before(state.Expiry) {
			delete(l.principalRepairPreviews, key)
		}
	}
	if len(l.principalRepairPreviews) >= 128 {
		l.mu.Unlock()
		return control.PrincipalRepairPreview{}, errors.New("principal repair preview capacity unavailable")
	}
	l.principalRepairPreviews[p.ConfirmationToken] = principalRepairPreviewState{Preview: p, Expiry: expiry}
	l.mu.Unlock()
	return p, nil
}

func (l *lifecycle) ConfirmPrincipalRepair(ctx context.Context, key, uuid, token string) (control.Operation, error) {
	if key == "" || len(key) > 128 || len(uuid) != 36 || len(token) != 32 {
		return control.Operation{}, control.ErrInvalid
	}
	release, err := l.lockSession(ctx, uuid)
	if err != nil {
		return control.Operation{}, err
	}
	defer release()
	hash := sha256.Sum256([]byte(token))
	request := control.PrincipalRepairRequest{Key: key, UUID: uuid, TokenSHA256: hex.EncodeToString(hash[:])}
	if prior, e := l.store.GetOperationByKey(ctx, key); e == nil {
		var saved control.PrincipalRepairRequest
		if prior.Kind != "session.principal.repair" || json.Unmarshal(prior.Request, &saved) != nil || saved != request {
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
	state, found := l.principalRepairPreviews[token]
	l.mu.Unlock()
	if !found || !time.Now().Before(state.Expiry) || !state.Preview.Eligible || state.Preview.SessionUUID != uuid {
		return control.Operation{}, control.ErrConflict
	}
	p, oldActive, err := l.principalRepairFacts(ctx, uuid)
	if err != nil || !p.Eligible {
		return control.Operation{}, errors.Join(err, control.ErrConflict)
	}
	p.ConfirmationToken, p.ExpiresAt = state.Preview.ConfirmationToken, state.Preview.ExpiresAt
	if !reflect.DeepEqual(p, state.Preview) {
		return control.Operation{}, control.ErrConflict
	}
	ev := control.PrincipalRepairEvidence{Project: p.Project, Branch: p.Branch, AssignedTip: p.AssignedTip,
		RefPresent: p.AssignedRefStatus == "present", PolicySHA256: p.PolicySHA256,
		OldFingerprint: p.OldFingerprint, OldActive: oldActive, GuestKeyStatus: p.GuestKeyStatus,
		GuestKeySHA256: p.GuestKeySHA256, InstanceUUID: l.instanceID,
		IncusProject: p.IncusProject, InstanceName: p.InstanceName, IncusUUID: p.IncusUUID,
		Generation: p.Generation, ImageFingerprint: p.ImageFingerprint, ExpiresAt: state.Expiry.UTC().Format(time.RFC3339Nano)}
	op, err := l.store.BeginPrincipalRepair(ctx, request, ev, func(call context.Context) error {
		return l.principalRepairRef(call, ev)
	})
	if err != nil {
		return control.Operation{}, err
	}
	l.mu.Lock()
	delete(l.principalRepairPreviews, token)
	l.mu.Unlock()
	if err := l.enqueue(op.ID); err != nil {
		return op, err
	}
	return op, nil
}

func (l *lifecycle) advancePrincipalRepair(op *control.Operation, ev control.PrincipalRepairEvidence, phase string, committed bool) error {
	raw, err := json.Marshal(ev)
	if err != nil || len(raw) > 2048 {
		return control.ErrInvalid
	}
	if err := l.store.AdvanceOperation(l.ctx, op.ID, "running", phase, committed, raw, ""); err != nil {
		return err
	}
	op.Phase, op.Evidence, op.Committed = phase, raw, committed
	return nil
}

func (l *lifecycle) principalRepairRef(ctx context.Context, ev control.PrincipalRepairEvidence) error {
	tip, exists, err := l.git.backend.InspectBranchRef(ctx, ev.Project, ev.Branch)
	if err != nil {
		return err
	}
	if exists != ev.RefPresent || tip != ev.AssignedTip {
		return control.ErrConflict
	}
	return nil
}

func (l *lifecycle) blockPrincipalRepair(op control.Operation, cause error) {
	if l.ctx.Err() != nil {
		return
	}
	message := fmt.Sprintf("principal repair blocked: %v", cause)
	if len(message) > 900 {
		message = message[:900]
	}
	_ = l.store.AdvanceOperation(l.ctx, op.ID, "blocked", op.Phase, op.Committed, op.Evidence, message)
}

func principalRepairStage(stateDir, opID string) string {
	return filepath.Join(stateDir, "session_keys", ".repair-"+opID)
}

func principalRepairKey(path, expected string, create bool) ([]byte, error) {
	if !create {
		if _, err := os.Lstat(path); err != nil {
			return nil, err
		}
	}
	key, pub, err := loadOrCreateKey(path)
	if err != nil || expected != "" && gitservice.Fingerprint(pub) != expected {
		return nil, errors.Join(err, errors.New("principal repair staged key identity changed"))
	}
	return key, nil
}

func placePrincipalRepairKey(stateDir, uuid, opID, fingerprint string) error {
	dir, err := principalKeyDir(stateDir)
	if err != nil {
		return err
	}
	final := filepath.Join(dir, uuid)
	if got, err := inspectRegisteredSessionKey(stateDir, uuid); err == nil {
		if got == fingerprint {
			stage := principalRepairStage(stateDir, opID)
			if _, e := os.Lstat(stage); e == nil {
				if _, e = principalRepairKey(stage, fingerprint, false); e != nil {
					return e
				}
				if e = os.Remove(stage); e != nil {
					return e
				}
			} else if !errors.Is(e, os.ErrNotExist) {
				return e
			}
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	stage := principalRepairStage(stateDir, opID)
	if _, err := principalRepairKey(stage, fingerprint, false); err != nil {
		return err
	}
	if err := os.Rename(stage, final); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		d.Close()
		return err
	}
	if err := d.Close(); err != nil {
		return err
	}
	got, err := inspectRegisteredSessionKey(stateDir, uuid)
	if err != nil || got != fingerprint {
		return errors.Join(err, errors.New("principal repair host key postcondition failed"))
	}
	return nil
}

func (l *lifecycle) processPrincipalRepair(op control.Operation) {
	if op.Status != "running" {
		return
	}
	var ev control.PrincipalRepairEvidence
	if json.Unmarshal(op.Evidence, &ev) != nil || ev.InstanceUUID != l.instanceID || ev.IncusProject != l.cfg.IncusProject ||
		ev.InstanceName != "p-"+op.SessionUUID || ev.Project != op.Project {
		l.blockPrincipalRepair(op, errors.New("durable principal repair identity unavailable"))
		return
	}
	ctx := l.ctx
	s, err := l.store.GetSession(ctx, op.SessionUUID)
	if err != nil || s.Registry != "established" || s.Project != ev.Project || s.Branch != ev.Branch || s.PolicySHA256 != ev.PolicySHA256 ||
		l.policyCondition(ctx, s) != "current" {
		l.blockPrincipalRepair(op, errors.Join(err, errors.New("principal repair assignment changed")))
		return
	}
	native, err := l.runtimeSession(ctx, s)
	if err != nil || native.ImageFingerprint != ev.ImageFingerprint {
		l.blockPrincipalRepair(op, errors.Join(err, errors.New("principal repair runtime selection changed")))
		return
	}
	check := func() error {
		if err := l.principalRepairRef(ctx, ev); err != nil {
			return err
		}
		observed, e := l.runtime.Inspect(ctx, native)
		if e != nil || !observed.Exists || observed.Name != ev.InstanceName || observed.Status != "Stopped" ||
			observed.IncusUUID != ev.IncusUUID || observed.Generation != ev.Generation || observed.Fingerprint != ev.ImageFingerprint {
			return errors.Join(e, errors.New("exact stopped principal repair runtime changed"))
		}
		return l.runtime.CheckStoppedSessionIdentity(ctx, native, ev.IncusUUID, ev.Generation)
	}
	for step := 0; step < 8; step++ {
		switch op.Phase {
		case "guarded":
			if err := check(); err != nil {
				l.blockPrincipalRepair(op, err)
				return
			}
			old, registered, e := l.store.SessionGitPrincipal(ctx, op.SessionUUID)
			active, aerr := l.store.IsSessionGitPrincipalActive(ctx, op.SessionUUID)
			status, kerr := principalKeyStatus(l.store.StateDir(), op.SessionUUID, ev.OldFingerprint)
			guest, gerr := l.runtime.InspectStoppedSessionIdentity(ctx, native, ev.IncusUUID, ev.Generation)
			if e != nil || aerr != nil || kerr != nil || gerr != nil || old != ev.OldFingerprint || registered != (ev.OldFingerprint != "") || active != ev.OldActive ||
				guest.SHA256 != ev.GuestKeySHA256 || active && status == "matching" && ev.GuestKeyStatus == "matching" {
				l.blockPrincipalRepair(op, errors.Join(e, aerr, kerr, gerr, control.ErrConflict))
				return
			}
			if err := l.advancePrincipalRepair(&op, ev, "key-intent", false); err != nil {
				l.blockPrincipalRepair(op, err)
				return
			}
		case "key-intent":
			if err := check(); err != nil {
				l.blockPrincipalRepair(op, err)
				return
			}
			if _, err := principalKeyDir(l.store.StateDir()); err != nil {
				l.blockPrincipalRepair(op, err)
				return
			}
			_, pub, err := loadOrCreateKey(principalRepairStage(l.store.StateDir(), op.ID))
			if err != nil {
				l.blockPrincipalRepair(op, err)
				return
			}
			ev.NewFingerprint = gitservice.Fingerprint(pub)
			if ev.NewFingerprint == ev.OldFingerprint {
				l.blockPrincipalRepair(op, errors.New("replacement key did not change identity"))
				return
			}
			if err := l.advancePrincipalRepair(&op, ev, "key-ready", false); err != nil {
				l.blockPrincipalRepair(op, err)
				return
			}
		case "key-ready":
			if err := check(); err != nil {
				l.blockPrincipalRepair(op, err)
				return
			}
			if _, err := principalRepairKey(principalRepairStage(l.store.StateDir(), op.ID), ev.NewFingerprint, false); err != nil {
				l.blockPrincipalRepair(op, err)
				return
			}
			if err := l.store.RotatePrincipalRepair(ctx, op.ID); err != nil {
				l.blockPrincipalRepair(op, err)
				return
			}
			op.Phase, op.Committed = "authority-rotated", true
		case "authority-rotated":
			if err := check(); err != nil {
				l.blockPrincipalRepair(op, err)
				return
			}
			if err := placePrincipalRepairKey(l.store.StateDir(), op.SessionUUID, op.ID, ev.NewFingerprint); err != nil {
				l.blockPrincipalRepair(op, err)
				return
			}
			if err := l.advancePrincipalRepair(&op, ev, "host-key-ready", true); err != nil {
				l.blockPrincipalRepair(op, err)
				return
			}
		case "host-key-ready":
			if err := check(); err != nil {
				l.blockPrincipalRepair(op, err)
				return
			}
			key, err := principalRepairKey(filepath.Join(l.store.StateDir(), "session_keys", op.SessionUUID), ev.NewFingerprint, false)
			if err != nil {
				l.blockPrincipalRepair(op, errors.Join(err, control.ErrConflict))
				return
			}
			err = l.runtime.InstallStoppedSessionIdentity(ctx, native, ev.IncusUUID, ev.Generation, key, func() error {
				return l.advancePrincipalRepair(&op, ev, "guest-write-issued", true)
			})
			if err != nil {
				l.blockPrincipalRepair(op, err)
				return
			}
			if err := l.advancePrincipalRepair(&op, ev, "guest-key-ready", true); err != nil {
				l.blockPrincipalRepair(op, err)
				return
			}
		case "guest-write-issued":
			key, err := principalRepairKey(filepath.Join(l.store.StateDir(), "session_keys", op.SessionUUID), ev.NewFingerprint, false)
			if err != nil || l.runtime.VerifyStoppedSessionIdentity(ctx, native, ev.IncusUUID, ev.Generation, key) != nil {
				l.blockPrincipalRepair(op, errors.Join(err, errors.New("issued guest key write has no exact positive result")))
				return
			}
			if err := l.advancePrincipalRepair(&op, ev, "guest-key-ready", true); err != nil {
				l.blockPrincipalRepair(op, err)
				return
			}
		case "guest-key-ready":
			if err := l.store.CompletePrincipalRepair(ctx, op.ID, func(call context.Context, got control.PrincipalRepairEvidence) error {
				if err := l.principalRepairRef(call, got); err != nil {
					return err
				}
				key, e := principalRepairKey(filepath.Join(l.store.StateDir(), "session_keys", op.SessionUUID), got.NewFingerprint, false)
				if e != nil {
					return e
				}
				return l.runtime.VerifyStoppedSessionIdentity(call, native, got.IncusUUID, got.Generation, key)
			}); err != nil {
				l.blockPrincipalRepair(op, err)
			}
			return
		default:
			l.blockPrincipalRepair(op, control.ErrConflict)
			return
		}
	}
	l.blockPrincipalRepair(op, errors.New("principal repair phase bound exceeded"))
}
