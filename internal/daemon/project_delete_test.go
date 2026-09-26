package daemon

import (
	"testing"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

func TestProjectDeleteCompletionPurgesOnlyRelatedReviewEvidence(t *testing.T) {
	l := &lifecycle{
		projectDeletePreviews: map[string]projectDeletePreviewState{"old": {Preview: control.ProjectDeletePreview{Project: "bulk"}}, "other": {Preview: control.ProjectDeletePreview{Project: "other"}}},
		removalPreviews:       map[string]removalPreviewState{"old": {Preview: control.RemovalPreview{Project: "bulk"}}, "other": {Preview: control.RemovalPreview{Project: "other"}}},
		collectionPreviews:    map[string]collectionPreviewState{"old": {Claim: control.EnvironmentCollectionClaim{Image: control.EnvironmentImage{Project: "bulk"}}}, "other": {Claim: control.EnvironmentCollectionClaim{Image: control.EnvironmentImage{Project: "other"}}}},
		createCleanupPreviews: map[string]createCleanupPreviewState{"old": {Preview: control.CreateCleanupPreview{OldRequest: control.ReserveSessionRequest{Project: "bulk"}}}},
		changedPolicies:       map[string]bool{"bulk": true, "other": true},
	}
	l.forgetProjectDeletePreviews("bulk")
	if len(l.projectDeletePreviews) != 1 || len(l.removalPreviews) != 1 || len(l.collectionPreviews) != 1 || len(l.createCleanupPreviews) != 0 || len(l.changedPolicies) != 1 {
		t.Fatal("completed deletion retained bulky evidence or purged unrelated reviews")
	}
	if l.projectDeletePreviews["other"].Preview.Project != "other" || l.removalPreviews["other"].Preview.Project != "other" || l.collectionPreviews["other"].Claim.Image.Project != "other" || !l.changedPolicies["other"] {
		t.Fatal("unrelated authority changed")
	}
}

func TestProjectDeleteIssuedCrashRequiresPositiveAbsenceAndNeverRedispatches(t *testing.T) {
	review := control.RemovalRuntimePreview{Condition: "present", InstanceName: "p-reviewed", IncusUUID: "original", Generation: "generation", ImageFingerprint: "image", OriginalStatus: "Stopped"}
	actual := runtimeincus.Observation{Exists: true, Name: review.InstanceName, Status: "Stopped", IncusUUID: review.IncusUUID, Generation: review.Generation, Fingerprint: review.ImageFingerprint}
	resource := control.ProjectDeleteResource{Kind: "runtime", ID: "reviewed", Status: "remaining"}
	if decision := projectDeleteRuntimeDecision(resource, review, actual); decision != "delete-exact" {
		t.Fatal("fresh exact source rejected", decision)
	}
	// Crash immediately after successful DELETE but before a durable absence
	// receipt must request the inventory proof, not another DELETE or completion.
	resource.DeleteIssued = true
	if decision := projectDeleteRuntimeDecision(resource, review, runtimeincus.Observation{}); decision != "prove-absence" {
		t.Fatal("lost successful receipt could not recover through proof", decision)
	}
	if decision := projectDeleteRuntimeDecision(resource, review, actual); decision != "issued-unresolved" {
		t.Fatal("present original admitted name redispatch", decision)
	}
	for _, mutate := range []func(*runtimeincus.Observation){func(o *runtimeincus.Observation) { o.Name = "renamed" }, func(o *runtimeincus.Observation) { o.IncusUUID = "competing" }, func(o *runtimeincus.Observation) { o.Generation = "changed" }, func(o *runtimeincus.Observation) { o.Status = "Running" }, func(o *runtimeincus.Observation) { o.Fingerprint = "different" }} {
		changed := actual
		mutate(&changed)
		if decision := projectDeleteRuntimeDecision(resource, review, changed); decision != "identity-conflict" {
			t.Fatal("unfamiliar native source admitted", decision)
		}
	}
	resource.Status = "deleted"
	if decision := projectDeleteRuntimeDecision(resource, review, actual); decision != "identity-conflict" {
		t.Fatal("positive receipt permitted replacement cleanup", decision)
	}
}
