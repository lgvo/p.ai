package daemon

import (
	"context"
	"encoding/json"
	"errors"
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

type staleRetainedDeleteRPC struct {
	control.LifecycleAPI
	stale error
}

func (p staleRetainedDeleteRPC) PreviewRetainedDelete(context.Context, string, string) (control.RetainedDeletePreview, error) {
	return control.RetainedDeletePreview{}, nil
}
func (p staleRetainedDeleteRPC) ConfirmRetainedDelete(context.Context, string, string, string, string) (control.Operation, error) {
	return control.Operation{}, p.stale
}

func TestRetainedDeleteChangedOriginReviewIsPublicBusy(t *testing.T) {
	review := &control.RemovalBranchLoss{AssignedRef: "refs/heads/saved", AssignedTip: strings.Repeat("a", 40), CommitsLosingPReachability: []string{strings.Repeat("a", 40)}}
	review.Origin.URL = "ssh://git@example.test/app.git"
	review.Origin.Status = "unknown"
	review.Origin.Reason = "origin_object_or_comparison_unavailable"
	review.Origin.ContainingBranches = []string{}
	review.Origin.UnresolvedRefs = []string{"refs/heads/divergent"}
	review.Origin.ObservedRefsDigest = strings.Repeat("b", 64)
	want, err := deleteReviewDigest(review)
	if err != nil {
		t.Fatal(err)
	}
	changed := *review
	changed.Origin.ObservedRefsDigest = strings.Repeat("c", 64)
	stale := matchRetainedDeleteReview(&changed, want)
	if !errors.Is(stale, errDeleteReviewChanged) || !errors.Is(stale, control.ErrConflict) {
		t.Fatalf("changed origin did not produce public conflict: %v", stale)
	}
	h := control.StateHandlerWithLifecycle(nil, nil, nil, staleRetainedDeleteRPC{stale: stale})
	token := strings.Repeat("d", 32)
	_, rpcErr := h(context.Background(), "project.retained.delete.confirm", json.RawMessage(`{"v":1,"key":"delete","project":"app","branch":"saved","confirmation_token":"`+token+`"}`))
	if rpcErr == nil || rpcErr.Kind != "busy" || rpcErr.Code != -32003 {
		t.Fatalf("stale review exposed wrong RPC error: %+v", rpcErr)
	}
}
