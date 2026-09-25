package control

import (
	"context"
	"encoding/json"
)

type RemovalPreviewRequest struct {
	UUID                      string `json:"uuid"`
	Kind                      string `json:"kind"`
	LossOperationID           string `json:"loss_operation_id,omitempty"`
	AcknowledgeMissingRuntime bool   `json:"acknowledge_missing_runtime,omitempty"`
}

type RemovalRuntimePreview struct {
	Condition          string          `json:"condition"`
	IncusProject       string          `json:"incus_project"`
	InstanceName       string          `json:"instance_name"`
	IncusUUID          string          `json:"incus_uuid,omitempty"`
	Generation         string          `json:"generation,omitempty"`
	ImageFingerprint   string          `json:"image_fingerprint,omitempty"`
	OriginalStatus     string          `json:"original_status,omitempty"`
	LossOperationID    string          `json:"loss_operation_id,omitempty"`
	ObservedAt         string          `json:"observed_at,omitempty"`
	Fingerprint        string          `json:"fingerprint,omitempty"`
	Loss               json.RawMessage `json:"loss,omitempty"`
	RuntimeLossUnknown bool            `json:"runtime_loss_unknown,omitempty"`
}

type RemovalBranchLoss struct {
	AssignedRef                string   `json:"assigned_ref"`
	AssignedTip                string   `json:"assigned_tip,omitempty"`
	CommitsLosingPReachability []string `json:"commits_losing_p_reachability"`
	PRefs                      []struct {
		Name string `json:"name"`
		OID  string `json:"oid"`
	} `json:"p_refs"`
	Origin struct {
		URL                string   `json:"url,omitempty"`
		Status             string   `json:"status"`
		Reason             string   `json:"reason,omitempty"`
		ContainingBranches []string `json:"containing_branches"`
		ObservedRefsDigest string   `json:"observed_refs_digest,omitempty"`
		UnresolvedRefs     []string `json:"unresolved_refs"`
	} `json:"origin"`
}

type RemovalPreview struct {
	Kind              string                `json:"kind"`
	SessionUUID       string                `json:"session_uuid"`
	Project           string                `json:"project"`
	Branch            string                `json:"branch"`
	AssignedRef       string                `json:"assigned_ref"`
	AssignedTip       string                `json:"assigned_tip,omitempty"`
	PolicySHA256      string                `json:"policy_sha256"`
	Runtime           RemovalRuntimePreview `json:"runtime"`
	BranchLoss        *RemovalBranchLoss    `json:"branch_loss,omitempty"`
	ConfirmationToken string                `json:"confirmation_token"`
	ExpiresAt         string                `json:"expires_at"`
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
	DiscardSession(context.Context, DiscardConfirmRequest) (Operation, error)
}
type DeleteAPI interface {
	DeleteSession(context.Context, DiscardConfirmRequest) (Operation, error)
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
