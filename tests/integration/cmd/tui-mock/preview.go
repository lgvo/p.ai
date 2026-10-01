package main

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

// Removal is rendered as raw JSON by the TUI, so its fixture must preserve
// the public preview and p.workspace-loss/v1 shapes, including which losses
// apply to Discard versus Delete. These are synthetic observations only.
func (f *fixture) removalReview(s control.SessionView, kind string, loss control.Operation, token string, expires time.Time) control.RemovalPreview {
	assigned := "refs/heads/" + s.Branch
	assignedTip := f.refs[s.Project][s.Branch]
	pRefs := []runtimeincus.WorkspaceRef{}
	for name, oid := range f.refs[s.Project] {
		pRefs = append(pRefs, runtimeincus.WorkspaceRef{Name: "refs/heads/" + name, OID: oid})
	}
	sort.Slice(pRefs, func(i, j int) bool { return pRefs[i].Name < pRefs[j].Name })
	fingerprint := strings.Repeat("f", 64)
	raw, _ := json.Marshal(object{
		"schema": "p.workspace-loss/v1", "fingerprint": fingerprint, "runtime_data_will_be_removed": true,
		"worktrees":          []runtimeincus.WorkspaceLossTree{{Path: "/workspace", Location: "runtime", Branch: s.Branch, HeadOID: assignedTip, Changes: []runtimeincus.WorkspaceChange{{Code: " M", Path: "notes.py"}, {Code: "??", Path: "scratch.txt"}}}},
		"external_worktrees": []string{}, "local_refs": []runtimeincus.WorkspaceRef{{Name: assigned, OID: assignedTip}}, "local_only_commits": []string{}, "p_refs": pRefs,
	})
	result := control.RemovalPreview{Kind: kind, SessionUUID: s.UUID, Project: s.Project, Branch: s.Branch, AssignedRef: assigned, AssignedTip: assignedTip, PolicySHA256: s.PolicySHA256, ConfirmationToken: token, ExpiresAt: expires.UTC().Format(time.RFC3339Nano),
		Runtime: control.RemovalRuntimePreview{Condition: "present", IncusProject: "p-mock", InstanceName: "p-" + s.UUID, IncusUUID: s.UUID, Generation: "1", ImageFingerprint: strings.Repeat("b", 64), OriginalStatus: "Stopped", LossOperationID: loss.ID, ObservedAt: loss.UpdatedAt, Fingerprint: fingerprint, Loss: raw},
	}
	if kind == "delete" {
		branchLoss := &control.RemovalBranchLoss{AssignedRef: assigned, AssignedTip: assignedTip, CommitsLosingPReachability: []string{}}
		retained := false
		for _, ref := range pRefs {
			branchLoss.PRefs = append(branchLoss.PRefs, struct {
				Name string `json:"name"`
				OID  string `json:"oid"`
			}{Name: ref.Name, OID: ref.OID})
			if ref.Name != assigned && ref.OID == assignedTip {
				retained = true
			}
		}
		if !retained && assignedTip != "" {
			branchLoss.CommitsLosingPReachability = append(branchLoss.CommitsLosingPReachability, assignedTip)
		}
		branchLoss.Origin.Status = "unknown"
		branchLoss.Origin.URL = f.origins[s.Project]
		branchLoss.Origin.Reason = "fixture: remote publication is not verified"
		branchLoss.Origin.ContainingBranches = []string{}
		branchLoss.Origin.UnresolvedRefs = []string{}
		result.BranchLoss = branchLoss
	}
	return result
}
