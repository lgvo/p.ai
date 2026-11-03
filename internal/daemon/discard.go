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

// staleIfConflict answers a confirmation whose recheck found that the
// reviewed state no longer holds: a refusal of kind conflict means the facts
// changed since the review, so the confirmation is stale. Any other error,
// such as a runtime that could not be observed, keeps its answer.
func staleIfConflict(err error) error {
	var refusal *control.Refusal
	if errors.As(err, &refusal) && refusal.Kind == api.KindConflict {
		return &control.Refusal{Kind: api.KindPreconditionFailed, Reason: api.ReasonChanged, Cause: err}
	}
	return err
}

func deleteReviewDigest(review *api.RemovalBranchLoss) (string, error) {
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
	switch {
	case attached:
		return api.Operation{}, control.Conflict(api.ReasonAttachmentActive, api.TargetSession(req.UUID))
	case !found || !time.Now().Before(state.Expiry):
		return api.Operation{}, control.Stale(api.ReasonExpired)
	case state.Preview.Kind != action || state.Preview.SessionUUID != req.UUID:
		// The token reviews another removal than this request.
		return api.Operation{}, control.Stale(api.ReasonChanged)
	}
	preview := state.Preview
	ev := control.DiscardEvidence{Action: action, Project: preview.Project, Branch: preview.Branch, AssignedTip: preview.AssignedTip,
		PolicySHA256: preview.PolicySHA256, IncusProject: preview.Runtime.IncusProject, InstanceName: preview.Runtime.InstanceName,
		InstanceUUID: l.instanceID, ImageFingerprint: preview.Runtime.ImageFingerprint, BaseFingerprint: l.cfg.BaseImageFingerprint,
		IncusUUID: preview.Runtime.IncusUUID, Generation: preview.Runtime.Generation,
		MissingRuntime: preview.Runtime.Condition == "missing", PreviewExpiresAt: preview.ExpiresAt}
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
			return api.Operation{}, staleIfConflict(runtimeFailure(err, api.TargetSession(req.UUID)))
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
