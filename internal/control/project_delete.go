package control

import (
	"context"
	"encoding/json"
)

// ProjectDeletePreview is one bounded whole-project loss review. Sessions must
// already be stopped/detached; unknown native effects are never removal grants.
type ProjectDeletePreviewRequest struct {
	Project            string            `json:"project"`
	LossOperations     map[string]string `json:"loss_operations"`
	AcknowledgeMissing []string          `json:"acknowledge_missing,omitempty"`
}
type ProjectDeleteConfirmRequest struct {
	Key               string `json:"key"`
	Project           string `json:"project"`
	ConfirmationToken string `json:"confirmation_token"`
}
type ProjectDeleteRequest struct {
	Key         string `json:"key"`
	Project     string `json:"project"`
	TokenSHA256 string `json:"token_sha256"`
}
type ProjectDeleteSession struct {
	Session   Session                  `json:"session"`
	Condition string                   `json:"condition"`
	Runtime   RemovalRuntimePreview    `json:"runtime"`
	Local     CreateReplacementCleanup `json:"credentials"`
}
type ProjectDeletePreview struct {
	Project             string                       `json:"project"`
	Outcome             string                       `json:"outcome"`
	StateSHA256         string                       `json:"state_sha256"`
	ProjectPolicySHA256 string                       `json:"project_policy_sha256"`
	ExternalMounts      []FilesystemGrant            `json:"external_mounts_preserved"`
	Sessions            []ProjectDeleteSession       `json:"sessions"`
	Attachments         []string                     `json:"attachments"`
	BranchLoss          *RemovalBranchLoss           `json:"branch_loss"`
	Images              []EnvironmentCollectionClaim `json:"cache_images"`
	Repository          ReplacementLocalIdentity     `json:"repository_identity"`
	RepositoryParent    ReplacementLocalIdentity     `json:"repository_parent_identity"`
	Warnings            []string                     `json:"warnings"`
	ConfirmationToken   string                       `json:"confirmation_token"`
	ExpiresAt           string                       `json:"expires_at"`
}
type ProjectDeleteResource struct {
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	Status       string `json:"status"` // remaining, unreachable, deleted, already_absent
	DeleteIssued bool   `json:"delete_issued,omitempty"`
	Diagnostic   string `json:"diagnostic,omitempty"`
}

// Only exact known resource identities and a review digest survive acceptance;
// loss snapshots are historical display evidence, not a rollback workflow.
type ProjectDeleteEvidence struct {
	InstanceUUID     string                       `json:"instance_uuid"`
	BaseFingerprint  string                       `json:"base_fingerprint"`
	IncusProject     string                       `json:"incus_project"`
	StateSHA256      string                       `json:"state_sha256"`
	ReviewSHA256     string                       `json:"review_sha256"`
	Sessions         []ProjectDeleteSession       `json:"sessions"`
	Images           []EnvironmentCollectionClaim `json:"images"`
	Repository       ReplacementLocalIdentity     `json:"repository_identity"`
	RepositoryParent ReplacementLocalIdentity     `json:"repository_parent_identity"`
	Resources        []ProjectDeleteResource      `json:"resources"`
}
type ProjectDeleteAPI interface {
	PreviewProjectDelete(context.Context, ProjectDeletePreviewRequest) (ProjectDeletePreview, error)
	ConfirmProjectDelete(context.Context, ProjectDeleteConfirmRequest) (Operation, error)
}

func projectDeleteResourceAbsent(r ProjectDeleteResource) bool {
	return r.Status == "deleted" || r.Status == "already_absent"
}
func validProjectDeleteEvidence(ev ProjectDeleteEvidence) bool {
	if !validUUID(ev.InstanceUUID) || !validFingerprint(ev.BaseFingerprint) || ev.IncusProject == "" || !validFingerprint(ev.StateSHA256) || !validFingerprint(ev.ReviewSHA256) || len(ev.Sessions) > 4 || len(ev.Images) > 20 || len(ev.Resources) != 2*len(ev.Sessions)+len(ev.Images)+1 {
		return false
	}
	seen := map[string]bool{}
	for _, s := range ev.Sessions {
		if !validUUID(s.Session.UUID) || s.Session.Registry != "established" || !validProject(s.Session.Project) || !validBranch(s.Session.Branch) || !validFingerprint(s.Session.PolicySHA256) || !s.Local.Valid() || s.Local.OldUUID != s.Session.UUID || seen[s.Session.UUID] {
			return false
		}
		seen[s.Session.UUID] = true
		r := s.Runtime
		if r.IncusProject != ev.IncusProject || r.InstanceName != "p-"+s.Session.UUID {
			return false
		}
		if r.Condition == "present" {
			if !validUUID(r.IncusUUID) || !validUUID(r.Generation) || !validFingerprint(r.ImageFingerprint) || r.OriginalStatus != "Stopped" || !validFingerprint(r.Fingerprint) || !validUUID(r.LossOperationID) {
				return false
			}
		} else if r.Condition != "missing" || !r.RuntimeLossUnknown {
			return false
		}
	}
	for _, c := range ev.Images {
		if !c.Image.valid() || c.Image.Fingerprint == ev.BaseFingerprint || c.Image.Properties["p.instance"] != ev.InstanceUUID || c.Image.Properties["p.incus_project"] != ev.IncusProject {
			return false
		}
	}
	seen = map[string]bool{}
	for _, r := range ev.Resources {
		key := r.Kind + ":" + r.ID
		if seen[key] || r.ID == "" || len(r.Diagnostic) > 512 || r.Status != "remaining" && r.Status != "unreachable" && !projectDeleteResourceAbsent(r) {
			return false
		}
		seen[key] = true
	}
	for _, s := range ev.Sessions {
		if !seen["runtime:"+s.Session.UUID] || !seen["local:"+s.Session.UUID] {
			return false
		}
	}
	for _, c := range ev.Images {
		if !seen["image:"+c.Image.Key] {
			return false
		}
	}
	return seen["repository:bare"]
}
func projectDeleteIdentities(ev ProjectDeleteEvidence) string {
	copy := ev
	copy.Resources = nil
	raw, _ := json.Marshal(copy)
	return digest(raw)
}
