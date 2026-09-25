package daemon

import (
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
)

func TestDeleteReviewDigestBindsOriginIdentityAndUnknownEvidence(t *testing.T) {
	review := &control.RemovalBranchLoss{AssignedRef: "refs/heads/main", AssignedTip: strings.Repeat("a", 40),
		CommitsLosingPReachability: []string{strings.Repeat("a", 40)}}
	review.PRefs = append(review.PRefs, struct {
		Name string `json:"name"`
		OID  string `json:"oid"`
	}{"refs/heads/main", strings.Repeat("a", 40)})
	review.Origin.URL = "ssh://git@example.test/team/app.git"
	review.Origin.Status = "unknown"
	review.Origin.Reason = "origin_refresh_unavailable"
	review.Origin.ContainingBranches = []string{}
	review.Origin.UnresolvedRefs = []string{}
	want, err := deleteReviewDigest(review)
	if err != nil {
		t.Fatal(err)
	}
	copyReview := *review
	if got, err := deleteReviewDigest(&copyReview); err != nil || got != want || copyReview.Origin.Status != "unknown" {
		t.Fatalf("explicit unknown review could not be revalidated: %s %v", got, err)
	}
	variants := []func(*control.RemovalBranchLoss){
		func(v *control.RemovalBranchLoss) { v.Origin.URL = "ssh://git@other.test/team/app.git" },
		func(v *control.RemovalBranchLoss) { v.Origin.Status = "observed"; v.Origin.Reason = "" },
		func(v *control.RemovalBranchLoss) { v.Origin.UnresolvedRefs = []string{"refs/heads/main"} },
		func(v *control.RemovalBranchLoss) { v.Origin.ObservedRefsDigest = strings.Repeat("b", 64) },
		func(v *control.RemovalBranchLoss) { v.AssignedTip = strings.Repeat("c", 40) },
	}
	for i, mutate := range variants {
		changed := *review
		mutate(&changed)
		got, err := deleteReviewDigest(&changed)
		if err != nil || got == want {
			t.Fatalf("changed Delete review field %d retained confirmation digest: %s %v", i, got, err)
		}
	}
}
