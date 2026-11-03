package main

import (
	"sort"
	"strings"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
)

// planRemoval plans the discard or delete (action) of session id as P does:
// the same plan shape, which losses apply to Discard and which to Delete, and
// the same refusals. P does not look inside a present container, so the plan
// only states that its data is lost; a missing runtime's data is already
// gone. These are synthetic observations only.
func (f *fixture) planRemoval(action, id string, p *api.Plan[api.RemovalDetails]) error {
	s, err := f.session(id)
	if err != nil {
		return err
	}
	assignedTip := f.refs[s.Project][s.Branch]
	p.Details = api.RemovalDetails{Project: s.Project, Branch: s.Branch, AssignedRef: "refs/heads/" + s.Branch, AssignedTip: assignedTip, AssignedRefMissing: assignedTip == "", PolicySHA256: s.PolicySHA256,
		Runtime: api.RemovalRuntime{IncusProject: "p-mock", InstanceName: "p-" + s.UUID}}
	if s.AttachedCount != 0 {
		return control.Conflict(api.ReasonAttachmentActive, api.TargetSession(id))
	}
	if s.Condition == "missing" {
		p.Details.Runtime.Condition = "missing"
		p.Gone = append(p.Gone, api.Loss{Kind: api.LossRuntimeData, Target: api.TargetSession(id), Certain: true})
	} else {
		status := map[string]string{"ready": "Running", "stopped": "Stopped"}[s.Condition]
		if status == "" {
			return control.Conflict(api.ReasonRuntimeUnsettled, api.TargetSession(id))
		}
		p.Details.Runtime = api.RemovalRuntime{Condition: "present", IncusProject: "p-mock", InstanceName: "p-" + s.UUID, IncusUUID: s.UUID, Generation: "1", ImageFingerprint: strings.Repeat("b", 64), OriginalStatus: status}
		p.Loss = append(p.Loss, api.Loss{Kind: api.LossRuntimeData, Target: api.TargetSession(id), Certain: true})
	}
	if action != "delete" {
		return nil
	}
	branchLoss := &api.RemovalBranchLoss{AssignedRef: p.Details.AssignedRef, AssignedTip: assignedTip, CommitsLosingPReachability: []string{}, PRefs: []api.PRef{}}
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
		p.Loss = append(p.Loss, api.Loss{Kind: api.LossPCommits, Target: api.TargetBranch(s.Project, s.Branch), Count: 1, Sample: []string{assignedTip}, Certain: true})
	}
	p.Details.BranchLoss = branchLoss
	return nil
}
