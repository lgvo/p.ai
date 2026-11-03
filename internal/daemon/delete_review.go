package daemon

import (
	"context"
	"errors"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
)

var errDeleteReviewChanged = errors.New("reviewed Delete branch loss or origin changed")

// freshDeleteReview recomputes a session Delete's branch loss. A missing
// assigned ref leaves nothing of the branch to lose in P's repository.
func (l *lifecycle) freshDeleteReview(ctx context.Context, ev control.DiscardEvidence) (*control.RemovalBranchLoss, error) {
	branchLoss, err := l.git.backend.BranchRemovalLoss(ctx, ev.Project, ev.Branch, true)
	if err != nil {
		return nil, err
	}
	return deleteReview(branchLoss), nil
}

// deleteReview lists the commits a deletion removes from P's repository.
// Session and project deletion do not read the origin: whether the origin
// keeps those commits is the user's concern.
func deleteReview(branchLoss gitservice.BranchRemovalLoss) *control.RemovalBranchLoss {
	branch := &control.RemovalBranchLoss{AssignedRef: branchLoss.AssignedRef, AssignedTip: branchLoss.AssignedTip,
		CommitsLosingPReachability: branchLoss.CommitsLosingPReachability}
	branch.PRefs = make([]struct {
		Name string `json:"name"`
		OID  string `json:"oid"`
	}, 0, len(branchLoss.PRefs))
	for _, ref := range branchLoss.PRefs {
		branch.PRefs = append(branch.PRefs, struct {
			Name string `json:"name"`
			OID  string `json:"oid"`
		}{ref.Ref, ref.OID})
	}
	return branch
}

// makeDeleteReviewInScope adds a fresh origin comparison to a retained-branch
// deletion's review. The caller holds the origin scope through its final
// effect, which avoids a nested origin lock.
func (l *lifecycle) makeDeleteReviewInScope(ctx context.Context, project string, branchLoss gitservice.BranchRemovalLoss, scope *gitservice.OriginScope) (*control.RemovalBranchLoss, error) {
	branch := deleteReview(branchLoss)
	branch.Origin = &control.RemovalOrigin{Status: "local_only", ContainingBranches: []string{}, UnresolvedRefs: []string{}}
	origin, _, err := l.store.Origin(ctx, project)
	if err != nil {
		return nil, err
	}
	if origin.URL == "" {
		return branch, nil
	}
	if scope == nil {
		return nil, control.ErrConflict
	}
	branch.Origin.URL, branch.Origin.Status, branch.Origin.Reason = origin.URL, "unknown", "origin_refresh_unavailable"
	advertised, e := scope.Observe(ctx, origin.URL)
	if e != nil {
		return branch, nil
	}
	comparison, e := l.git.backend.CompareOriginRemoval(ctx, project, advertised, branch.CommitsLosingPReachability)
	if e != nil {
		return branch, nil
	}
	branch.Origin.Status, branch.Origin.ContainingBranches = comparison.Status, comparison.ContainingBranches
	branch.Origin.Reason = comparison.Reason
	branch.Origin.ObservedRefsDigest, branch.Origin.UnresolvedRefs = comparison.ObservedRefsDigest, comparison.UnresolvedRefs
	return branch, nil
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
