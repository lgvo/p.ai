package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/sourcedriver"
)

var errDeleteReviewChanged = errors.New("planned branch loss changed")

// branchReviewDeadline bounds one review of a branch's removal.
const branchReviewDeadline = 45 * time.Second

// freshDeleteReview recomputes a session Delete's branch loss. A missing
// assigned ref leaves nothing of the branch to lose in P's repository.
func (l *lifecycle) freshDeleteReview(ctx context.Context, ev control.DiscardEvidence) (*api.RemovalBranchLoss, error) {
	return l.branchRemovalReview(ctx, ev.Project, ev.Branch, true)
}

// branchRemovalReview reviews removing branch of project from one
// observation of every head: the branch's tip, empty when no head holds its
// name, and, when losing is set, the commits removing it takes out of P's
// repository, which an absent branch has none of. A failed read answers as
// control.HeadsFailure does. Session and project deletion do not read the
// origin: whether the origin keeps those commits is the user's concern.
func (l *lifecycle) branchRemovalReview(ctx context.Context, project, branch string, losing bool) (*api.RemovalBranchLoss, error) {
	ctx, cancel := context.WithTimeout(ctx, branchReviewDeadline)
	defer cancel()
	var loss sourcedriver.Loss
	var err error
	if losing {
		loss, err = l.git.driver.LosingCommits(ctx, project, []string{branch})
	} else {
		loss.Heads, err = l.git.driver.Branches(ctx, project)
	}
	if err != nil {
		return nil, control.HeadsFailure(err)
	}
	review := removalReview("refs/heads/"+branch, loss)
	for _, head := range loss.Heads {
		if head.Name == branch {
			review.AssignedTip = string(head.Tip)
		}
	}
	return review, nil
}

// removalReview is the branch loss of a removal whose removed heads
// assigned names, from the source driver's answer: every head as a full ref,
// and the commits no remaining head reaches.
func removalReview(assigned string, loss sourcedriver.Loss) *api.RemovalBranchLoss {
	review := &api.RemovalBranchLoss{AssignedRef: assigned, CommitsLosingPReachability: make([]string, 0, len(loss.Commits)), PRefs: make([]api.PRef, 0, len(loss.Heads))}
	for _, id := range loss.Commits {
		review.CommitsLosingPReachability = append(review.CommitsLosingPReachability, string(id))
	}
	for _, head := range loss.Heads {
		review.PRefs = append(review.PRefs, api.PRef{Name: "refs/heads/" + head.Name, OID: string(head.Tip)})
	}
	return review
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
