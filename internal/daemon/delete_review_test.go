package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/sourcedriver"
)

func TestSessionDeleteReviewReadsNoOrigin(t *testing.T) {
	tip := strings.Repeat("a", 40)
	review := removalReview("refs/heads/work", sourcedriver.Loss{Heads: []sourcedriver.Head{{Name: "work", Tip: sourcedriver.OID(tip)}}, Commits: []sourcedriver.OID{sourcedriver.OID(tip)}})
	raw, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, found := fields["origin"]; found || len(review.PRefs) != 1 || review.PRefs[0] != (api.PRef{Name: "refs/heads/work", OID: tip}) || review.CommitsLosingPReachability[0] != tip {
		t.Fatalf("session Delete review: %s", raw)
	}
}

func TestDeleteReviewDigestBindsTheBranchLoss(t *testing.T) {
	reviewed := func() *api.RemovalBranchLoss {
		return &api.RemovalBranchLoss{AssignedRef: "refs/heads/main", AssignedTip: strings.Repeat("a", 40),
			CommitsLosingPReachability: []string{strings.Repeat("a", 40)}, PRefs: []api.PRef{{Name: "refs/heads/main", OID: strings.Repeat("a", 40)}}}
	}
	want, err := deleteReviewDigest(reviewed())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := deleteReviewDigest(reviewed()); err != nil || got != want {
		t.Fatalf("the same review could not be revalidated: %s %v", got, err)
	}
	variants := []func(*api.RemovalBranchLoss){
		func(v *api.RemovalBranchLoss) { v.AssignedTip = strings.Repeat("c", 40) },
		func(v *api.RemovalBranchLoss) { v.CommitsLosingPReachability = []string{} },
		func(v *api.RemovalBranchLoss) {
			v.PRefs = append(v.PRefs, api.PRef{Name: "refs/heads/keep", OID: strings.Repeat("a", 40)})
		},
	}
	for i, mutate := range variants {
		changed := reviewed()
		mutate(changed)
		got, err := deleteReviewDigest(changed)
		if err != nil || got == want {
			t.Fatalf("changed Delete review field %d retained its digest: %s %v", i, got, err)
		}
	}
}

type staleRetainedDeleteRPC struct {
	control.LifecycleAPI
	stale error
}

func (p staleRetainedDeleteRPC) DeleteRetainedBranch(context.Context, string, string, api.ChangeRequest) (api.ChangeResult[api.RetainedDeleteDetails], error) {
	return api.ChangeResult[api.RetainedDeleteDetails]{}, p.stale
}

// A retained branch whose commits that leave P changed since its plan fails
// its worker's check as stale before its effect, and its run answers
// precondition_failed, changed.
func TestRetainedDeleteChangedReviewIsPreconditionFailed(t *testing.T) {
	reviewed := func(lost ...string) *api.RemovalBranchLoss {
		return &api.RemovalBranchLoss{AssignedRef: "refs/heads/saved", AssignedTip: strings.Repeat("a", 40), CommitsLosingPReachability: lost, PRefs: []api.PRef{}}
	}
	want, err := deleteReviewDigest(reviewed(strings.Repeat("a", 40)))
	if err != nil {
		t.Fatal(err)
	}
	stale := matchRetainedDeleteReview(reviewed(), want)
	// The operation fails as stale before its effect on ErrConflict or the
	// changed-review error, so both stay in the chain.
	if !errors.Is(stale, errDeleteReviewChanged) || !errors.Is(stale, control.ErrConflict) {
		t.Fatalf("a changed branch loss did not produce a stale review: %v", stale)
	}
	h := control.NewHandler(control.HandlerConfig{Lifecycle: staleRetainedDeleteRPC{stale: stale}})
	tag := "20261102T153012Z-" + strings.Repeat("d", 64)
	_, rpcErr := h(context.Background(), "project.retained.delete", json.RawMessage(`{"key":"delete","project":"app","branch":"saved","if_match":"`+tag+`"}`))
	if rpcErr == nil || rpcErr.Kind() != api.KindPreconditionFailed || rpcErr.Code != -32013 || rpcErr.Reason() != api.ReasonChanged {
		t.Fatalf("stale review answered %v, want precondition_failed changed", rpcErr)
	}
}
