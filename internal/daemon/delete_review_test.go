package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/plugin"
)

func TestSessionDeleteReviewReadsNoOrigin(t *testing.T) {
	tip := strings.Repeat("a", 40)
	review := deleteReview(gitservice.BranchRemovalLoss{AssignedRef: "refs/heads/work", AssignedTip: tip,
		CommitsLosingPReachability: []string{tip}, PRefs: []plugin.GitRef{{Ref: "refs/heads/work", OID: tip}}})
	raw, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, found := fields["origin"]; found || len(review.PRefs) != 1 || review.CommitsLosingPReachability[0] != tip {
		t.Fatalf("session Delete review: %s", raw)
	}
}

func TestDeleteReviewDigestBindsOriginIdentityAndUnknownEvidence(t *testing.T) {
	reviewed := func() *api.RemovalBranchLoss {
		review := &api.RemovalBranchLoss{AssignedRef: "refs/heads/main", AssignedTip: strings.Repeat("a", 40),
			CommitsLosingPReachability: []string{strings.Repeat("a", 40)}}
		review.PRefs = append(review.PRefs, struct {
			Name string `json:"name"`
			OID  string `json:"oid"`
		}{"refs/heads/main", strings.Repeat("a", 40)})
		review.Origin = &api.RemovalOrigin{URL: "ssh://git@example.test/team/app.git", Status: "unknown", Reason: "origin_refresh_unavailable",
			ContainingBranches: []string{}, UnresolvedRefs: []string{}}
		return review
	}
	want, err := deleteReviewDigest(reviewed())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := deleteReviewDigest(reviewed()); err != nil || got != want {
		t.Fatalf("explicit unknown review could not be revalidated: %s %v", got, err)
	}
	variants := []func(*api.RemovalBranchLoss){
		func(v *api.RemovalBranchLoss) { v.Origin.URL = "ssh://git@other.test/team/app.git" },
		func(v *api.RemovalBranchLoss) { v.Origin.Status = "observed"; v.Origin.Reason = "" },
		func(v *api.RemovalBranchLoss) { v.Origin.UnresolvedRefs = []string{"refs/heads/main"} },
		func(v *api.RemovalBranchLoss) { v.Origin.ObservedRefsDigest = strings.Repeat("b", 64) },
		func(v *api.RemovalBranchLoss) { v.AssignedTip = strings.Repeat("c", 40) },
	}
	for i, mutate := range variants {
		changed := reviewed()
		mutate(changed)
		got, err := deleteReviewDigest(changed)
		if err != nil || got == want {
			t.Fatalf("changed Delete review field %d retained confirmation digest: %s %v", i, got, err)
		}
	}
}

type staleRetainedDeleteRPC struct {
	control.LifecycleAPI
	stale error
}

func (p staleRetainedDeleteRPC) PreviewRetainedDelete(context.Context, string, string) (api.RetainedDeletePreview, error) {
	return api.RetainedDeletePreview{}, nil
}
func (p staleRetainedDeleteRPC) ConfirmRetainedDelete(context.Context, string, string, string, string) (api.Operation, error) {
	return api.Operation{}, p.stale
}

func TestRetainedDeleteChangedOriginReviewIsPublicBusy(t *testing.T) {
	reviewed := func(observed string) *api.RemovalBranchLoss {
		return &api.RemovalBranchLoss{AssignedRef: "refs/heads/saved", AssignedTip: strings.Repeat("a", 40), CommitsLosingPReachability: []string{strings.Repeat("a", 40)},
			Origin: &api.RemovalOrigin{URL: "ssh://git@example.test/app.git", Status: "unknown", Reason: "origin_object_or_comparison_unavailable",
				ContainingBranches: []string{}, UnresolvedRefs: []string{"refs/heads/divergent"}, ObservedRefsDigest: observed}}
	}
	want, err := deleteReviewDigest(reviewed(strings.Repeat("b", 64)))
	if err != nil {
		t.Fatal(err)
	}
	changed := *reviewed(strings.Repeat("c", 64))
	stale := matchRetainedDeleteReview(&changed, want)
	if !errors.Is(stale, errDeleteReviewChanged) || !errors.Is(stale, control.ErrConflict) {
		t.Fatalf("changed origin did not produce public conflict: %v", stale)
	}
	h := control.NewHandler(control.HandlerConfig{Lifecycle: staleRetainedDeleteRPC{stale: stale}})
	token := strings.Repeat("d", 32)
	_, rpcErr := h(context.Background(), "project.retained.delete.confirm", json.RawMessage(`{"key":"delete","project":"app","branch":"saved","confirmation_token":"`+token+`"}`))
	if rpcErr == nil || rpcErr.Kind != "busy" || rpcErr.Code != -32003 {
		t.Fatalf("stale review exposed wrong RPC error: %+v", rpcErr)
	}
}
