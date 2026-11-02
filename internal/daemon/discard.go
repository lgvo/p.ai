package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
)

func (l *lifecycle) DiscardSession(ctx context.Context, req control.DiscardConfirmRequest) (api.Operation, error) {
	return l.confirmRemoval(ctx, req, "discard")
}

func (l *lifecycle) DeleteSession(ctx context.Context, req control.DiscardConfirmRequest) (api.Operation, error) {
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

func (l *lifecycle) confirmRemoval(ctx context.Context, req control.DiscardConfirmRequest, action string) (api.Operation, error) {
	if action != "discard" && action != "delete" {
		return api.Operation{}, control.ErrInvalid
	}
	if len(req.Key) < 1 || len(req.Key) > 128 || len(req.UUID) != 36 || len(req.ConfirmationToken) != 32 {
		return api.Operation{}, control.ErrInvalid
	}
	release, err := l.lockSession(ctx, req.UUID)
	if err != nil {
		return api.Operation{}, err
	}
	defer release()
	sum := sha256.Sum256([]byte(req.ConfirmationToken))
	pinned := control.DiscardRequest{Key: req.Key, UUID: req.UUID, TokenSHA256: hex.EncodeToString(sum[:])}
	if prior, replayed, err := replayRequest(l, ctx, req.Key, "session."+action, pinned); err != nil || replayed {
		return prior, err
	}
	var attached, found bool
	var state removalPreviewState
	l.withLock(func() {
		attached = l.hasAttachmentLocked(req.UUID)
		state, found = l.removalPreviews[req.ConfirmationToken]
	})
	if attached || !found || state.Preview.Kind != action || state.Preview.SessionUUID != req.UUID || !time.Now().Before(state.Expiry) {
		return api.Operation{}, control.ErrConflict
	}
	preview := state.Preview
	created, err := l.store.CreationForSession(ctx, req.UUID)
	if err != nil {
		return api.Operation{}, err
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
			return api.Operation{}, err
		}
	}
	if ev.MissingRuntime {
		session, err := l.store.GetSession(ctx, req.UUID)
		if err != nil {
			return api.Operation{}, err
		}
		native, err := l.runtimeSession(ctx, session)
		if err != nil {
			return api.Operation{}, err
		}
		if err := l.runtime.ConfirmSessionRuntimeAbsent(ctx, native); err != nil {
			return api.Operation{}, errors.Join(err, control.ErrConflict)
		}
	}
	var op api.Operation
	if action == "delete" {
		op, err = l.store.BeginDelete(ctx, pinned, ev)
	} else {
		op, err = l.store.BeginDiscard(ctx, pinned, ev)
	}
	if err != nil {
		return api.Operation{}, err
	}
	l.withLock(func() { delete(l.removalPreviews, req.ConfirmationToken) })
	if err = l.enqueue(op.ID); err != nil {
		return op, err
	}
	return op, nil
}
