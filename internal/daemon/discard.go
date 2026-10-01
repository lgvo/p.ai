package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lgvo/p.ai/internal/control"
)

func (l *lifecycle) DiscardSession(ctx context.Context, req control.DiscardConfirmRequest) (control.Operation, error) {
	return l.confirmRemoval(ctx, req, "discard")
}

func (l *lifecycle) DeleteSession(ctx context.Context, req control.DiscardConfirmRequest) (control.Operation, error) {
	return l.confirmRemoval(ctx, req, "delete")
}

func deleteReviewDigest(review *control.RemovalBranchLoss) (string, error) {
	if review == nil {
		return "", control.ErrInvalid
	}
	raw, err := json.Marshal(review)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (l *lifecycle) confirmRemoval(ctx context.Context, req control.DiscardConfirmRequest, action string) (control.Operation, error) {
	if action != "discard" && action != "delete" {
		return control.Operation{}, control.ErrInvalid
	}
	if len(req.Key) < 1 || len(req.Key) > 128 || len(req.UUID) != 36 || len(req.ConfirmationToken) != 32 {
		return control.Operation{}, control.ErrInvalid
	}
	release, err := l.lockSession(ctx, req.UUID)
	if err != nil {
		return control.Operation{}, err
	}
	defer release()
	sum := sha256.Sum256([]byte(req.ConfirmationToken))
	pinned := control.DiscardRequest{Key: req.Key, UUID: req.UUID, TokenSHA256: hex.EncodeToString(sum[:])}
	if prior, e := l.store.GetOperationByKey(ctx, req.Key); e == nil {
		var old control.DiscardRequest
		if prior.Kind != "session."+action || json.Unmarshal(prior.Request, &old) != nil || old != pinned {
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
	attached := l.hasAttachmentLocked(req.UUID)
	state, found := l.removalPreviews[req.ConfirmationToken]
	l.mu.Unlock()
	if attached || !found || state.Preview.Kind != action || state.Preview.SessionUUID != req.UUID || !time.Now().Before(state.Expiry) {
		return control.Operation{}, control.ErrConflict
	}
	preview := state.Preview
	created, err := l.store.CreationForSession(ctx, req.UUID)
	if err != nil {
		return control.Operation{}, err
	}
	ev := control.DiscardEvidence{Action: action, Project: preview.Project, Branch: preview.Branch, AssignedTip: preview.AssignedTip,
		PolicySHA256: preview.PolicySHA256, IncusProject: preview.Runtime.IncusProject, InstanceName: preview.Runtime.InstanceName,
		InstanceUUID: l.instanceID, ImageFingerprint: preview.Runtime.ImageFingerprint, BaseFingerprint: l.cfg.BaseImageFingerprint,
		IncusUUID: preview.Runtime.IncusUUID, Generation: preview.Runtime.Generation, OriginalStatus: preview.Runtime.OriginalStatus,
		LossOperationID: preview.Runtime.LossOperationID, LossFingerprint: preview.Runtime.Fingerprint,
		MissingRuntime: preview.Runtime.Condition == "missing", AllowUnborn: created.Kind == "project.create",
		PreviewExpiresAt: preview.ExpiresAt}
	if action == "delete" {
		ev.DeleteReviewSHA256, err = deleteReviewDigest(preview.BranchLoss)
		if err != nil {
			return control.Operation{}, err
		}
	}
	if ev.MissingRuntime {
		session, err := l.store.GetSession(ctx, req.UUID)
		if err != nil {
			return control.Operation{}, err
		}
		native, err := l.runtimeSession(ctx, session)
		if err != nil {
			return control.Operation{}, err
		}
		if err := l.runtime.ConfirmSessionRuntimeAbsent(ctx, native); err != nil {
			return control.Operation{}, errors.Join(err, control.ErrConflict)
		}
	}
	var op control.Operation
	if action == "delete" {
		op, err = l.store.BeginDelete(ctx, pinned, ev)
	} else {
		op, err = l.store.BeginDiscard(ctx, pinned, ev)
	}
	if err != nil {
		return control.Operation{}, err
	}
	l.mu.Lock()
	delete(l.removalPreviews, req.ConfirmationToken)
	l.mu.Unlock()
	if err = l.enqueue(op.ID); err != nil {
		return op, err
	}
	return op, nil
}

func removeSessionKeyAt(stateDir, uuid string) error {
	if len(uuid) != 36 {
		return errors.New("invalid session key identity")
	}
	dir := filepath.Join(stateDir, "session_keys")
	parent, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	owner, ok := parent.Sys().(*syscall.Stat_t)
	if !ok || !parent.IsDir() || parent.Mode().Perm() != 0700 || owner.Uid != uint32(os.Geteuid()) {
		return errors.New("session key directory identity changed")
	}
	file := filepath.Join(dir, uuid)
	info, err := os.Lstat(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		return errors.New("session key file identity changed")
	}
	if err = os.Remove(file); err != nil {
		return err
	}
	opened, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := opened.Sync()
	closeErr := opened.Close()
	return errors.Join(syncErr, closeErr)
}
