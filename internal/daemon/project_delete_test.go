package daemon

import (
	"testing"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

func TestProjectDeleteCompletionForgetsOnlyItsProject(t *testing.T) {
	l := &lifecycle{changedPolicies: map[string]bool{"bulk": true, "other": true}}
	l.forgetProject("bulk")
	if len(l.changedPolicies) != 1 || !l.changedPolicies["other"] {
		t.Fatal("completed deletion kept its project or forgot another")
	}
}

func TestProjectDeleteIssuedCrashRequiresPositiveAbsenceAndNeverRedispatches(t *testing.T) {
	planned := api.RemovalRuntime{Condition: "present", InstanceName: "p-reviewed", IncusUUID: "original", Generation: "generation", ImageFingerprint: "image", OriginalStatus: "Stopped"}
	spec := runtimedriver.Session{Image: "image"}
	actual := runtimedriver.Observation{State: runtimedriver.StateStopped, Incarnation: recordedIncarnation(planned.IncusUUID, planned.Generation), Complete: true}
	resource := control.ProjectDeleteResource{Kind: "runtime", ID: "reviewed", Status: "remaining"}
	if decision := projectDeleteRuntimeDecision(resource, planned, spec, actual); decision != "delete-exact" {
		t.Fatal("fresh exact source rejected", decision)
	}
	// Crash immediately after successful DELETE but before a durable absence
	// receipt must settle the issued delete, not send another or complete.
	resource.DeleteIssued = true
	if decision := projectDeleteRuntimeDecision(resource, planned, spec, runtimedriver.Observation{State: runtimedriver.StateAbsent}); decision != "issued-unresolved" {
		t.Fatal("lost successful receipt recovered without settling the delete", decision)
	}
	if decision := projectDeleteRuntimeDecision(resource, planned, spec, actual); decision != "issued-unresolved" {
		t.Fatal("present original admitted name redispatch", decision)
	}
	for name, mutate := range map[string]func(*runtimedriver.Session, *runtimedriver.Observation){
		"competing": func(_ *runtimedriver.Session, o *runtimedriver.Observation) {
			o.Incarnation = recordedIncarnation("competing", planned.Generation)
		},
		"restored": func(_ *runtimedriver.Session, o *runtimedriver.Observation) {
			o.Incarnation = recordedIncarnation(planned.IncusUUID, "changed")
		},
		"neither running nor stopped": func(_ *runtimedriver.Session, o *runtimedriver.Observation) { o.State = runtimedriver.StateOther },
		"rebuilt":                     func(s *runtimedriver.Session, _ *runtimedriver.Observation) { s.Image = "different" },
	} {
		changedSpec, changed := spec, actual
		mutate(&changedSpec, &changed)
		if decision := projectDeleteRuntimeDecision(resource, planned, changedSpec, changed); decision != "identity-conflict" {
			t.Fatal("unfamiliar", name, "native source admitted", decision)
		}
	}
	resource.Status = "deleted"
	if decision := projectDeleteRuntimeDecision(resource, planned, spec, actual); decision != "identity-conflict" {
		t.Fatal("positive receipt permitted replacement cleanup", decision)
	}
}

// TestProjectDeleteStopsARunningSession checks that project deletion stops a
// running session's exact runtime before it deletes it, whatever status its
// plan saw, and stops it again when it still runs after its stop was
// recorded: stopping is idempotent.
func TestProjectDeleteStopsARunningSession(t *testing.T) {
	for _, status := range []string{"Running", "Stopped"} {
		planned := api.RemovalRuntime{Condition: "present", InstanceName: "p-reviewed", IncusUUID: "original", Generation: "generation", ImageFingerprint: "image", OriginalStatus: status}
		spec := runtimedriver.Session{Image: "image"}
		running := runtimedriver.Observation{State: runtimedriver.StateRunning, Incarnation: recordedIncarnation(planned.IncusUUID, planned.Generation), Complete: true}
		resource := control.ProjectDeleteResource{Kind: "runtime", ID: "reviewed", Status: "remaining"}
		if decision := projectDeleteRuntimeDecision(resource, planned, spec, running); decision != "stop-exact" {
			t.Fatalf("planned %s, running: %s", status, decision)
		}
		resource.StopIssued = true
		if decision := projectDeleteRuntimeDecision(resource, planned, spec, running); decision != "stop-exact" {
			t.Fatalf("planned %s, running after its stop was recorded: %s", status, decision)
		}
		stopped := running
		stopped.State = runtimedriver.StateStopped
		if decision := projectDeleteRuntimeDecision(resource, planned, spec, stopped); decision != "delete-exact" {
			t.Fatalf("planned %s, stopped after its stop: %s", status, decision)
		}
	}
}
