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

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
)

type retainedDeletePreviewState struct {
	Preview api.RetainedDeletePreview
	Expiry  time.Time
}

// freshRetainedDeleteReview reviews deleting branch afresh. A branch that is
// gone answers not_found; a failure to review it is returned as it is.
func (l *lifecycle) freshRetainedDeleteReview(ctx context.Context, project, branch string, scope *gitservice.OriginScope) (*api.RemovalBranchLoss, error) {
	loss, err := l.git.backend.BranchRemovalLoss(ctx, project, branch, true)
	if err != nil {
		return nil, gitFailure(err)
	}
	if loss.AssignedTip == "" {
		return nil, control.NotFound(api.ReasonBranch, api.TargetBranch(project, branch))
	}
	return l.makeDeleteReviewInScope(ctx, project, loss, scope)
}

func (l *lifecycle) PreviewRetainedDelete(ctx context.Context, project, branch string) (api.RetainedDeletePreview, error) {
	if project == "" || branch == "" {
		return api.RetainedDeletePreview{}, control.ErrInvalid
	}
	if err := l.checkBranchName(ctx, "branch", branch); err != nil {
		return api.RetainedDeletePreview{}, err
	}
	retained, err := l.store.IsRetainedBranch(ctx, project, branch)
	if errors.Is(err, control.ErrNotFound) {
		return api.RetainedDeletePreview{}, control.NotFound(api.ReasonProject, api.TargetProject(project))
	}
	if err != nil {
		return api.RetainedDeletePreview{}, err
	}
	if !retained {
		return api.RetainedDeletePreview{}, control.Conflict(api.ReasonBranchAssigned, api.TargetBranch(project, branch))
	}
	var review *api.RemovalBranchLoss
	err = withOriginScope(ctx, l.git.backend, project, func(scope *gitservice.OriginScope) error {
		var e error
		review, e = l.freshRetainedDeleteReview(ctx, project, branch, scope)
		return e
	})
	if err != nil {
		return api.RetainedDeletePreview{}, err
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		return api.RetainedDeletePreview{}, err
	}
	now := time.Now().UTC()
	expiry := now.Add(removalPreviewTTL)
	preview := api.RetainedDeletePreview{Project: project, Branch: branch, Ref: review.AssignedRef, Tip: review.AssignedTip, BranchLoss: review,
		ConfirmationToken: hex.EncodeToString(token[:]), ExpiresAt: expiry.Format(time.RFC3339Nano)}
	raw, err := json.Marshal(api.RetainedDeletePreviewResult{Preview: preview})
	if err != nil {
		return api.RetainedDeletePreview{}, control.Internal(api.ReasonEncoding, err)
	}
	if len(raw) > api.MaxFrameBytes-1024 {
		return api.RetainedDeletePreview{}, control.Conflict(api.ReasonBoundExceeded, api.TargetBranch(project, branch))
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
		return api.RetainedDeletePreview{}, control.Retry(api.ReasonTooManyPreviews, "")
	}
	l.retainedDeletePreviews[preview.ConfirmationToken] = retainedDeletePreviewState{Preview: preview, Expiry: expiry}
	return preview, nil
}

func (l *lifecycle) ConfirmRetainedDelete(ctx context.Context, key, project, branch, token string) (api.Operation, error) {
	if len(key) == 0 || len(key) > 128 || project == "" || branch == "" || !validRetainedDeleteToken(token) {
		return api.Operation{}, control.ErrInvalid
	}
	sum := sha256.Sum256([]byte(token))
	req := control.RetainedDeleteRequest{Key: key, Project: project, Branch: branch, TokenSHA256: hex.EncodeToString(sum[:])}
	if prior, replayed, err := replayRequest(l, ctx, key, control.KindRetainedDelete, req); err != nil || replayed {
		return prior, err
	}
	var state retainedDeletePreviewState
	var found bool
	l.withLock(func() { state, found = l.retainedDeletePreviews[token] })
	// A token this daemon does not hold, used or never issued, expired as far
	// as the client can tell; one issued for another branch reviewed other
	// facts. Either needs a new review.
	if !found || !time.Now().Before(state.Expiry) {
		return api.Operation{}, control.Stale(api.ReasonExpired)
	}
	if state.Preview.Project != project || state.Preview.Branch != branch {
		return api.Operation{}, control.Stale(api.ReasonChanged)
	}
	preview := state.Preview
	digest, err := deleteReviewDigest(preview.BranchLoss)
	if err != nil {
		return api.Operation{}, err
	}
	ev := control.RetainedDeleteEvidence{Project: project, Branch: branch, Tip: preview.Tip, ReviewSHA256: digest,
		InstanceUUID: l.instanceID, PreviewExpiresAt: preview.ExpiresAt}
	var op api.Operation
	err = withOriginScope(ctx, l.git.backend, project, func(scope *gitservice.OriginScope) error {
		var e error
		op, e = l.store.BeginRetainedDelete(ctx, req, ev, func(ctx context.Context, evidence control.RetainedDeleteEvidence) error {
			return l.verifyRetainedDeleteReview(ctx, evidence, scope)
		})
		return e
	})
	if err != nil {
		return api.Operation{}, err
	}
	l.withLock(func() { delete(l.retainedDeletePreviews, token) })
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

// verifyRetainedDeleteReview checks that a fresh review of the deletion still
// matches the reviewed one. A changed review answers precondition_failed and
// a failure to review answers failed with reason git; both keep ErrConflict,
// so a deletion whose review fails before its effect fails as stale.
func (l *lifecycle) verifyRetainedDeleteReview(ctx context.Context, e control.RetainedDeleteEvidence, scope *gitservice.OriginScope) error {
	if e.InstanceUUID != l.instanceID {
		return control.ErrConflict
	}
	current, exists, err := l.git.backend.InspectBranchRef(ctx, e.Project, e.Branch)
	if err != nil {
		return control.Failed(api.ReasonGit, err)
	}
	if !exists || current != e.Tip {
		return control.Stale(api.ReasonChanged)
	}
	fresh, err := l.freshRetainedDeleteReview(ctx, e.Project, e.Branch, scope)
	if errors.Is(err, control.ErrNotFound) {
		return control.Stale(api.ReasonChanged)
	}
	if err != nil {
		// The worker fails a review it cannot repeat as stale.
		return errors.Join(err, control.ErrConflict)
	}
	return matchRetainedDeleteReview(fresh, e.ReviewSHA256)
}

func matchRetainedDeleteReview(fresh *api.RemovalBranchLoss, reviewedSHA256 string) error {
	digest, err := deleteReviewDigest(fresh)
	if err != nil {
		return err
	}
	if digest != reviewedSHA256 {
		return errors.Join(errDeleteReviewChanged, control.Stale(api.ReasonChanged))
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

func (l *lifecycle) processRetainedDelete(op api.Operation) {
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
