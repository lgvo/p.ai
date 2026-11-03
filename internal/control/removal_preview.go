package control

import (
	"context"

	"github.com/lgvo/p.ai/internal/api"
)

type RemovalPreviewRequest struct {
	UUID                      string `json:"uuid"`
	Kind                      string `json:"kind"`
	AcknowledgeMissingRuntime bool   `json:"acknowledge_missing_runtime,omitempty"`
}

// RemovalRuntimePreview states what removal does to a session's runtime. P
// does not look inside a present container: RuntimeDataLost says that the
// container and everything in it (uncommitted changes, files outside Git,
// commits not pushed to P, home, runtime-local credentials) is lost.
type RemovalRuntimePreview struct {
	Condition          string `json:"condition"`
	IncusProject       string `json:"incus_project"`
	InstanceName       string `json:"instance_name"`
	IncusUUID          string `json:"incus_uuid,omitempty"`
	Generation         string `json:"generation,omitempty"`
	ImageFingerprint   string `json:"image_fingerprint,omitempty"`
	OriginalStatus     string `json:"original_status,omitempty"`
	RuntimeDataLost    bool   `json:"runtime_data_lost,omitempty"`
	RuntimeLossUnknown bool   `json:"runtime_loss_unknown,omitempty"`
}

// RemovalOrigin is what a fresh origin read says about the commits a deletion
// removes from P's repository.
type RemovalOrigin struct {
	URL                string   `json:"url,omitempty"`
	Status             string   `json:"status"`
	Reason             string   `json:"reason,omitempty"`
	ContainingBranches []string `json:"containing_branches"`
	ObservedRefsDigest string   `json:"observed_refs_digest,omitempty"`
	UnresolvedRefs     []string `json:"unresolved_refs"`
}

// RemovalBranchLoss lists the commits that leave P's repository. Only
// retained-branch deletion reads the origin; session and project deletion
// leave Origin nil.
type RemovalBranchLoss struct {
	AssignedRef                string   `json:"assigned_ref"`
	AssignedTip                string   `json:"assigned_tip,omitempty"`
	CommitsLosingPReachability []string `json:"commits_losing_p_reachability"`
	PRefs                      []struct {
		Name string `json:"name"`
		OID  string `json:"oid"`
	} `json:"p_refs"`
	Origin *RemovalOrigin `json:"origin,omitempty"`
}

// RemovalPreview is the reviewed discard or delete of one session.
// AssignedRefMissing says there is no branch: delete then removes nothing from
// P's repository, and discard keeps no branch.
type RemovalPreview struct {
	Kind               string                `json:"kind"`
	SessionUUID        string                `json:"session_uuid"`
	Project            string                `json:"project"`
	Branch             string                `json:"branch"`
	AssignedRef        string                `json:"assigned_ref"`
	AssignedTip        string                `json:"assigned_tip,omitempty"`
	AssignedRefMissing bool                  `json:"assigned_ref_missing,omitempty"`
	PolicySHA256       string                `json:"policy_sha256"`
	Runtime            RemovalRuntimePreview `json:"runtime"`
	BranchLoss         *RemovalBranchLoss    `json:"branch_loss,omitempty"`
	ConfirmationToken  string                `json:"confirmation_token"`
	ExpiresAt          string                `json:"expires_at"`
}

type RemovalPreviewAPI interface {
	PreviewRemoval(context.Context, RemovalPreviewRequest) (RemovalPreview, error)
}

type DiscardConfirmRequest struct {
	Key               string `json:"key"`
	UUID              string `json:"uuid"`
	ConfirmationToken string `json:"confirmation_token"`
}

type DiscardAPI interface {
	DiscardSession(context.Context, DiscardConfirmRequest) (api.Operation, error)
}
type DeleteAPI interface {
	DeleteSession(context.Context, DiscardConfirmRequest) (api.Operation, error)
}

func validHexToken(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
