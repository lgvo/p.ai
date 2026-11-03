package daemon

import (
	"testing"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimeincus"
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
	actual := runtimeincus.Observation{Exists: true, Name: planned.InstanceName, Status: "Stopped", IncusUUID: planned.IncusUUID, Generation: planned.Generation, Fingerprint: planned.ImageFingerprint}
	resource := control.ProjectDeleteResource{Kind: "runtime", ID: "reviewed", Status: "remaining"}
	if decision := projectDeleteRuntimeDecision(resource, planned, actual); decision != "delete-exact" {
		t.Fatal("fresh exact source rejected", decision)
	}
	// Crash immediately after successful DELETE but before a durable absence
	// receipt must request the inventory proof, not another DELETE or completion.
	resource.DeleteIssued = true
	if decision := projectDeleteRuntimeDecision(resource, planned, runtimeincus.Observation{}); decision != "prove-absence" {
		t.Fatal("lost successful receipt could not recover through proof", decision)
	}
	if decision := projectDeleteRuntimeDecision(resource, planned, actual); decision != "issued-unresolved" {
		t.Fatal("present original admitted name redispatch", decision)
	}
	for _, mutate := range []func(*runtimeincus.Observation){func(o *runtimeincus.Observation) { o.Name = "renamed" }, func(o *runtimeincus.Observation) { o.IncusUUID = "competing" }, func(o *runtimeincus.Observation) { o.Generation = "changed" }, func(o *runtimeincus.Observation) { o.Status = "Frozen" }, func(o *runtimeincus.Observation) { o.Fingerprint = "different" }} {
		changed := actual
		mutate(&changed)
		if decision := projectDeleteRuntimeDecision(resource, planned, changed); decision != "identity-conflict" {
			t.Fatal("unfamiliar native source admitted", decision)
		}
	}
	resource.Status = "deleted"
	if decision := projectDeleteRuntimeDecision(resource, planned, actual); decision != "identity-conflict" {
		t.Fatal("positive receipt permitted replacement cleanup", decision)
	}
}

// TestProjectDeleteStopsARunningSessionOnce checks that project deletion
// stops a running session's exact runtime before it deletes it, whatever
// status its plan saw, and never issues a stop twice: a runtime still running
// after its stop was issued is an unresolved outcome.
func TestProjectDeleteStopsARunningSessionOnce(t *testing.T) {
	for _, status := range []string{"Running", "Stopped"} {
		planned := api.RemovalRuntime{Condition: "present", InstanceName: "p-reviewed", IncusUUID: "original", Generation: "generation", ImageFingerprint: "image", OriginalStatus: status}
		running := runtimeincus.Observation{Exists: true, Name: planned.InstanceName, Status: "Running", IncusUUID: planned.IncusUUID, Generation: planned.Generation, Fingerprint: planned.ImageFingerprint}
		resource := control.ProjectDeleteResource{Kind: "runtime", ID: "reviewed", Status: "remaining"}
		if decision := projectDeleteRuntimeDecision(resource, planned, running); decision != "stop-exact" {
			t.Fatalf("planned %s, running: %s", status, decision)
		}
		resource.StopIssued = true
		if decision := projectDeleteRuntimeDecision(resource, planned, running); decision != "stop-unresolved" {
			t.Fatalf("planned %s, running after its stop was issued: %s", status, decision)
		}
		stopped := running
		stopped.Status = "Stopped"
		if decision := projectDeleteRuntimeDecision(resource, planned, stopped); decision != "delete-exact" {
			t.Fatalf("planned %s, stopped after its stop: %s", status, decision)
		}
	}
}
