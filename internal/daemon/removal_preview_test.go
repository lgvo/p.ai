package daemon

import (
	"fmt"
	"github.com/lgvo/p.ai/internal/api"
	"strings"
	"testing"
)

func TestRemovalPreviewRequiresCompleteResponseBelowRPCFrame(t *testing.T) {
	small := api.RemovalPreview{Kind: "delete", SessionUUID: "550e8400-e29b-41d4-a716-446655440000",
		ConfirmationToken: strings.Repeat("a", 32), Runtime: api.RemovalRuntimePreview{Condition: "present", RuntimeDataLost: true}}
	if err := checkRemovalPreviewFrame(small); err != nil {
		t.Fatal(err)
	}
	large := small
	large.BranchLoss = &api.RemovalBranchLoss{}
	for i := 0; i < 1024; i++ {
		large.BranchLoss.PRefs = append(large.BranchLoss.PRefs, struct {
			Name string `json:"name"`
			OID  string `json:"oid"`
		}{Name: "refs/heads/" + strings.Repeat("a", 200) + fmt.Sprintf("%04d", i), OID: strings.Repeat("b", 40)})
	}
	if err := checkRemovalPreviewFrame(large); err == nil {
		t.Fatal("oversized but structurally valid preview would strand invisible token")
	}
}
