package daemon

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
)

var errDeleteReviewChanged = errors.New("planned branch loss changed")

// freshDeleteReview recomputes a session Delete's branch loss. A missing
// assigned ref leaves nothing of the branch to lose in P's repository.
func (l *lifecycle) freshDeleteReview(ctx context.Context, ev control.DiscardEvidence) (*api.RemovalBranchLoss, error) {
	branchLoss, err := l.git.backend.BranchRemovalLoss(ctx, ev.Project, ev.Branch, true)
	if err != nil {
		return nil, err
	}
	return deleteReview(branchLoss), nil
}

// deleteReview lists the commits a deletion removes from P's repository.
// Session and project deletion do not read the origin: whether the origin
// keeps those commits is the user's concern.
func deleteReview(branchLoss gitservice.BranchRemovalLoss) *api.RemovalBranchLoss {
	branch := &api.RemovalBranchLoss{AssignedRef: branchLoss.AssignedRef, AssignedTip: branchLoss.AssignedTip,
		CommitsLosingPReachability: branchLoss.CommitsLosingPReachability}
	branch.PRefs = make([]api.PRef, 0, len(branchLoss.PRefs))
	for _, ref := range branchLoss.PRefs {
		branch.PRefs = append(branch.PRefs, api.PRef{Name: ref.Ref, OID: ref.OID})
	}
	return branch
}

// deleteReviewDigest is the digest of a deletion's branch loss, which its
// worker compares with a fresh one before its effect.
func deleteReviewDigest(review *api.RemovalBranchLoss) (string, error) {
	if review == nil {
		return "", control.ErrInvalid
	}
	raw, err := json.Marshal(review)
	if err != nil {
		return "", err
	}
	return hexDigest(raw), nil
}

func (l *lifecycle) verifyDeleteReview(ctx context.Context, ev control.DiscardEvidence) error {
	if ev.Action != "delete" {
		return nil
	}
	if err := l.discardAssignedTip(ctx, ev); err != nil {
		return err
	}
	fresh, err := l.freshDeleteReview(ctx, ev)
	if err != nil {
		return err
	}
	digest, err := deleteReviewDigest(fresh)
	if err != nil {
		return err
	}
	if digest != ev.DeleteReviewSHA256 {
		return errDeleteReviewChanged
	}
	return nil
}
