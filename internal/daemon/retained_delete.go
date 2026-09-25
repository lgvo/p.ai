package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
)

type retainedDeletePreviewState struct {
	Preview control.RetainedDeletePreview
	Expiry  time.Time
}

func (l *lifecycle) freshRetainedDeleteReview(ctx context.Context, project, branch string, scope *gitservice.OriginScope) (*control.RemovalBranchLoss, error) {
	loss, err := l.git.backend.BranchRemovalLoss(ctx, project, branch, false)
	if err != nil || loss.AssignedTip == "" {
		return nil, errors.Join(err, control.ErrConflict)
	}
	return l.makeDeleteReviewInScope(ctx, project, loss, scope)
}

func (l *lifecycle) PreviewRetainedDelete(ctx context.Context, project, branch string) (control.RetainedDeletePreview, error) {
	if project == "" || branch == "" {
		return control.RetainedDeletePreview{}, control.ErrInvalid
	}
	if err := l.git.backend.ValidateRenameBranch(ctx, branch); err != nil {
		return control.RetainedDeletePreview{}, err
	}
	retained, err := l.store.IsRetainedBranch(ctx, project, branch)
	if err != nil || !retained {
		return control.RetainedDeletePreview{}, errors.Join(err, control.ErrConflict)
	}
	var review *control.RemovalBranchLoss
	err = l.git.backend.WithOrigin(ctx, project, func(scope *gitservice.OriginScope) error {
		var e error
		review, e = l.freshRetainedDeleteReview(ctx, project, branch, scope)
		return e
	})
	if err != nil {
		return control.RetainedDeletePreview{}, err
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		return control.RetainedDeletePreview{}, err
	}
	now := time.Now().UTC()
	expiry := now.Add(removalPreviewTTL)
	preview := control.RetainedDeletePreview{Project: project, Branch: branch, Ref: review.AssignedRef, Tip: review.AssignedTip, BranchLoss: review,
		ConfirmationToken: hex.EncodeToString(token[:]), ExpiresAt: expiry.Format(time.RFC3339Nano)}
	raw, err := json.Marshal(map[string]any{"v": 1, "preview": preview})
	if err != nil || len(raw) > control.MaxFrameBytes-1024 {
		return control.RetainedDeletePreview{}, errors.New("retained delete preview exceeds bounded control response")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.retainedDeletePreviews == nil {
		l.retainedDeletePreviews = map[string]retainedDeletePreviewState{}
	}
	for k, v := range l.retainedDeletePreviews {
		if !now.Before(v.Expiry) {
			delete(l.retainedDeletePreviews, k)
		}
	}
	if len(l.retainedDeletePreviews) >= 128 {
		return control.RetainedDeletePreview{}, control.ErrConflict
	}
	l.retainedDeletePreviews[preview.ConfirmationToken] = retainedDeletePreviewState{Preview: preview, Expiry: expiry}
	return preview, nil
}

func (l *lifecycle) ConfirmRetainedDelete(ctx context.Context, key, project, branch, token string) (control.Operation, error) {
	if len(key) == 0 || len(key) > 128 || project == "" || branch == "" || !validRetainedDeleteToken(token) {
		return control.Operation{}, control.ErrInvalid
	}
	sum := sha256.Sum256([]byte(token))
	req := control.RetainedDeleteRequest{Key: key, Project: project, Branch: branch, TokenSHA256: hex.EncodeToString(sum[:])}
	if prior, err := l.store.GetOperationByKey(ctx, key); err == nil {
		var saved control.RetainedDeleteRequest
		if prior.Kind != "project.retained.delete" || json.Unmarshal(prior.Request, &saved) != nil || saved != req {
			return control.Operation{}, control.ErrConflict
		}
		if prior.Status == "running" {
			_ = l.enqueue(prior.ID)
		}
		return prior, nil
	} else if !errors.Is(err, control.ErrNotFound) {
		return control.Operation{}, err
	}
	l.mu.Lock()
	state, found := l.retainedDeletePreviews[token]
	l.mu.Unlock()
	if !found || state.Preview.Project != project || state.Preview.Branch != branch || !time.Now().Before(state.Expiry) {
		return control.Operation{}, control.ErrConflict
	}
	preview := state.Preview
	digest, err := deleteReviewDigest(preview.BranchLoss)
	if err != nil {
		return control.Operation{}, err
	}
	ev := control.RetainedDeleteEvidence{Project: project, Branch: branch, Tip: preview.Tip, ReviewSHA256: digest,
		InstanceUUID: l.instanceID, PreviewExpiresAt: preview.ExpiresAt}
	var op control.Operation
	err = l.git.backend.WithOrigin(ctx, project, func(scope *gitservice.OriginScope) error {
		var e error
		op, e = l.store.BeginRetainedDelete(ctx, req, ev, func(ctx context.Context, evidence control.RetainedDeleteEvidence) error {
			return l.verifyRetainedDeleteReview(ctx, evidence, scope)
		})
		return e
	})
	if err != nil {
		return control.Operation{}, err
	}
	l.mu.Lock()
	delete(l.retainedDeletePreviews, token)
	l.mu.Unlock()
	if err = l.enqueue(op.ID); err != nil {
		return op, err
	}
	return op, nil
}

func validRetainedDeleteToken(token string) bool {
	if len(token) != 32 {
		return false
	}
	for _, c := range token {
		if c < '0' || c > '9' && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (l *lifecycle) verifyRetainedDeleteReview(ctx context.Context, e control.RetainedDeleteEvidence, scope *gitservice.OriginScope) error {
	if e.InstanceUUID != l.instanceID {
		return control.ErrConflict
	}
	current, exists, err := l.git.backend.InspectBranchRef(ctx, e.Project, e.Branch)
	if err != nil {
		return err
	}
	if !exists || current != e.Tip {
		return control.ErrConflict
	}
	fresh, err := l.freshRetainedDeleteReview(ctx, e.Project, e.Branch, scope)
	if err != nil {
		return err
	}
	return matchRetainedDeleteReview(fresh, e.ReviewSHA256)
}

func matchRetainedDeleteReview(fresh *control.RemovalBranchLoss, reviewedSHA256 string) error {
	digest, err := deleteReviewDigest(fresh)
	if err != nil {
		return err
	}
	if digest != reviewedSHA256 {
		return errors.Join(errDeleteReviewChanged, control.ErrConflict)
	}
	return nil
}

func (l *lifecycle) blockRetainedDelete(id string, cause error) {
	if l.ctx.Err() != nil {
		return
	}
	op, err := l.store.GetOperation(l.ctx, id)
	if err != nil {
		return
	}
	message := fmt.Sprintf("retained delete blocked: %v", cause)
	if len(message) > 900 {
		message = message[:900]
	}
	_ = l.store.AdvanceOperation(l.ctx, id, "blocked", op.Phase, op.Committed, op.Evidence, message)
}

func (l *lifecycle) processRetainedDelete(op control.Operation) {
	if op.Status != "running" {
		return
	}
	var e control.RetainedDeleteEvidence
	if json.Unmarshal(op.Evidence, &e) != nil || e.Project != op.Project || e.InstanceUUID != l.instanceID {
		l.blockRetainedDelete(op.ID, errors.New("durable retained delete identity unavailable"))
		return
	}
	ctx := l.ctx
	switch op.Phase {
	case "guarded":
		var issued bool
		err := l.git.backend.WithOrigin(ctx, e.Project, func(scope *gitservice.OriginScope) error {
			return l.store.IssueRetainedDelete(ctx, op.ID, func(ctx context.Context, saved control.RetainedDeleteEvidence) error {
				return l.verifyRetainedDeleteReview(ctx, saved, scope)
			}, func() error {
				issued = true
				return l.git.backend.DeleteAssignedBranchExact(ctx, e.Project, e.Branch, e.Tip, func() error { return nil })
			})
		})
		if err != nil {
			if !issued && (errors.Is(err, control.ErrConflict) || errors.Is(err, errDeleteReviewChanged)) {
				if staleErr := l.store.FailRetainedDeleteStale(ctx, op.ID); staleErr == nil {
					return
				} else {
					err = errors.Join(err, staleErr)
				}
			}
			l.blockRetainedDelete(op.ID, err)
			return
		}
		fallthrough
	case "ref-delete-issued":
		current, exists, err := l.git.backend.InspectBranchRef(ctx, e.Project, e.Branch)
		if err != nil || exists || current != "" {
			l.blockRetainedDelete(op.ID, errors.Join(err, errors.New("exact retained P ref absence unavailable")))
			return
		}
		if err = l.store.CompleteRetainedDelete(ctx, op.ID, func(ctx context.Context, saved control.RetainedDeleteEvidence) error {
			current, exists, err := l.git.backend.InspectBranchRef(ctx, saved.Project, saved.Branch)
			if err != nil {
				return err
			}
			if exists || current != "" {
				return control.ErrConflict
			}
			return nil
		}); err != nil {
			l.blockRetainedDelete(op.ID, err)
		}
	default:
		l.blockRetainedDelete(op.ID, errors.New("unsupported durable phase"))
	}
}
