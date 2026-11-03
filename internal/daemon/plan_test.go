package daemon

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
)

// The loss items, one source each: a container that exists is certain
// runtime data loss; a missing runtime's data is already gone; the commits
// that leave P are counted, with a bounded sample in their order, and none
// is listed when none leaves; unreachable objects are uncounted and so not
// certain.
func TestLossItems(t *testing.T) {
	uuid := "550e8400-e29b-41d4-a716-446655440000"
	want := api.Loss{Kind: api.LossRuntimeData, Target: "session/" + uuid, Certain: true}
	if got := containerLoss(uuid); !reflect.DeepEqual(got, want) {
		t.Errorf("container loss %+v", got)
	}
	if got := missingRuntimeGone(uuid); !reflect.DeepEqual(got, want) {
		t.Errorf("missing runtime %+v", got)
	}
	if got := unreachableObjectsLoss("team/app"); !reflect.DeepEqual(got, api.Loss{Kind: api.LossUnreachableObjects, Target: "project/team/app", Reason: api.LossNotEnumerated}) {
		t.Errorf("unreachable objects %+v", got)
	}
	target := api.TargetBranch("team/app", "work")
	for _, none := range []*api.RemovalBranchLoss{nil, {CommitsLosingPReachability: []string{}}} {
		if got := branchLoss(target, none); len(got) != 0 {
			t.Errorf("no commits leave, but %+v", got)
		}
	}
	commits := []string{}
	for _, c := range "1234567" {
		commits = append(commits, strings.Repeat(string(c), 40))
	}
	got := branchLoss(target, &api.RemovalBranchLoss{CommitsLosingPReachability: commits})
	if !reflect.DeepEqual(got, []api.Loss{{Kind: api.LossPCommits, Target: target, Count: 7, Sample: commits[:5], Certain: true}}) {
		t.Errorf("seven commits leave: %+v", got)
	}
	got[0].Sample[0] = "changed"
	if commits[0] == "changed" {
		t.Error("the sample shares the branch loss's list")
	}
	one := branchLoss(target, &api.RemovalBranchLoss{CommitsLosingPReachability: commits[:1]})
	if len(one) != 1 || one[0].Count != 1 || !reflect.DeepEqual(one[0].Sample, commits[:1]) {
		t.Errorf("one commit leaves: %+v", one)
	}
}

// A removal's change hash leaves out whether its container was running: P
// stops a running one itself, so a Start or Stop between the dry run and the
// run does not refuse the run. A replaced container still does.
func TestRemovalHashLeavesOutTheRuntimeStatus(t *testing.T) {
	c := (&lifecycle{}).removal("discard", "550e8400-e29b-41d4-a716-446655440000", api.ChangeRequest{})
	running := api.RemovalDetails{Project: "app", Branch: "work", Runtime: api.RemovalRuntime{Condition: "present", IncusUUID: "22222222-2222-4222-8222-222222222222", Generation: "33333333-3333-4333-8333-333333333333", OriginalStatus: "Running"}}
	stopped := running
	stopped.Runtime.OriginalStatus = "Stopped"
	if !reflect.DeepEqual(c.Shown(running), c.Shown(stopped)) || running.Runtime.OriginalStatus != "Running" {
		t.Fatal("a removal's change hash covers its container's status")
	}
	replaced := stopped
	replaced.Runtime.Generation = "44444444-4444-4444-8444-444444444444"
	if reflect.DeepEqual(c.Shown(replaced), c.Shown(stopped)) {
		t.Fatal("a removal's change hash leaves out its container's generation")
	}
}

// noRecheck is a store Begin's verify that finds the plan unchanged.
func noRecheck(context.Context) error { return nil }
