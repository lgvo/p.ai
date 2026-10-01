package daemon

import (
	"context"
	"errors"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
)

var errDeleteReviewChanged = errors.New("reviewed Delete branch loss or origin changed")

// Fresh Delete review is recomputed while the exact runtime is quiescent.
// An origin read that cannot be completed remains an explicit unknown with
// its captured URL, exactly as the public preview presented it.
func (l *lifecycle) freshDeleteReview(ctx context.Context, ev control.DiscardEvidence) (*control.RemovalBranchLoss, error) {
	branchLoss, err := l.git.backend.BranchRemovalLoss(ctx, ev.Project, ev.Branch, ev.AllowUnborn)
	if err != nil {
		return nil, err
	}
	return l.makeDeleteReview(ctx, ev.Project, branchLoss)
}

func (l *lifecycle) makeDeleteReview(ctx context.Context, project string, branchLoss gitservice.BranchRemovalLoss) (*control.RemovalBranchLoss, error) {
	origin, _, err := l.store.Origin(ctx, project)
	if err != nil {
		return nil, err
	}
	if origin.URL == "" {
		return l.makeDeleteReviewInScope(ctx, project, branchLoss, nil)
	}
	var review *control.RemovalBranchLoss
	err = l.git.backend.WithOrigin(ctx, project, func(scope *gitservice.OriginScope) error {
		var e error
		review, e = l.makeDeleteReviewInScope(ctx, project, branchLoss, scope)
		return e
	})
	return review, err
}

// makeDeleteReviewInScope avoids a nested origin lock when a Git-only
// destructive action holds origin and ref authority through its final effect.
func (l *lifecycle) makeDeleteReviewInScope(ctx context.Context, project string, branchLoss gitservice.BranchRemovalLoss, scope *gitservice.OriginScope) (*control.RemovalBranchLoss, error) {
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
	branch.Origin.Status, branch.Origin.ContainingBranches, branch.Origin.UnresolvedRefs = "local_only", []string{}, []string{}
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
