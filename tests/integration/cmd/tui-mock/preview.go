package main

import (
	"sort"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

// The fixture preserves the public removal preview shape, including which
// losses apply to Discard versus Delete. P does not look inside a present
// container, so the runtime only states that its data is lost. These are
// synthetic observations only.
func (f *fixture) removalReview(s api.SessionView, kind, originalStatus, token string, expires time.Time) api.RemovalPreview {
	assigned := "refs/heads/" + s.Branch
	assignedTip := f.refs[s.Project][s.Branch]
	result := api.RemovalPreview{Kind: kind, SessionUUID: s.UUID, Project: s.Project, Branch: s.Branch, AssignedRef: assigned, AssignedTip: assignedTip, AssignedRefMissing: assignedTip == "", PolicySHA256: s.PolicySHA256, ConfirmationToken: token, ExpiresAt: expires.UTC().Format(time.RFC3339Nano),
		Runtime: api.RemovalRuntimePreview{Condition: "present", IncusProject: "p-mock", InstanceName: "p-" + s.UUID, IncusUUID: s.UUID, Generation: "1", ImageFingerprint: strings.Repeat("b", 64), OriginalStatus: originalStatus, RuntimeDataLost: true},
	}
	if kind == "delete" {
		branchLoss := &api.RemovalBranchLoss{AssignedRef: assigned, AssignedTip: assignedTip, CommitsLosingPReachability: []string{}}
		names := []string{}
		for name := range f.refs[s.Project] {
			names = append(names, name)
		}
		sort.Strings(names)
		retained := false
		for _, name := range names {
			oid := f.refs[s.Project][name]
			branchLoss.PRefs = append(branchLoss.PRefs, api.PRef{Name: "refs/heads/" + name, OID: oid})
			if name != s.Branch && oid == assignedTip {
				retained = true
			}
		}
		if !retained && assignedTip != "" {
			branchLoss.CommitsLosingPReachability = append(branchLoss.CommitsLosingPReachability, assignedTip)
		}
		result.BranchLoss = branchLoss
	}
	return result
}
