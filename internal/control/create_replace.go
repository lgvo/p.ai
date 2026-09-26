package control

// CreateReplacePreview names the old immutable request and the only new
// request this short-lived confirmation may admit. Ineligible previews carry
// reasons but no token.
type CreateReplacePreview struct {
	OldUUID             string                 `json:"old_uuid"`
	OldOperationID      string                 `json:"old_operation_id"`
	OldStatus           string                 `json:"old_status"`
	OldPhase            string                 `json:"old_phase"`
	OldRequest          ReserveSessionRequest  `json:"old_request"`
	OldBranch           CreateReplaceBranch    `json:"old_branch"`
	NewBranch           CreateReplaceBranch    `json:"new_branch"`
	OldCapturedOID      string                 `json:"old_captured_oid"`
	OldEvidenceSHA256   string                 `json:"old_evidence_sha256"`
	OldPolicySHA256     string                 `json:"old_policy_sha256"`
	OldImageFingerprint string                 `json:"old_image_fingerprint"`
	NewRequest          ReserveSessionRequest  `json:"new_request"`
	NewCapturedOID      string                 `json:"new_captured_oid,omitempty"`
	NewImageFingerprint string                 `json:"new_image_fingerprint"`
	NewSelection        CreationSelection      `json:"new_selection"`
	NewEnvironment      *EnvironmentIntent     `json:"new_environment,omitempty"`
	NewPolicySHA256     string                 `json:"new_policy_sha256,omitempty"`
	Provisional         CreateReplaceResources `json:"provisional"`
	UnsafeReasons       []string               `json:"unsafe_reasons"`
	Eligible            bool                   `json:"eligible"`
	ConfirmationToken   string                 `json:"confirmation_token,omitempty"`
	ExpiresAt           string                 `json:"expires_at,omitempty"`
}

type CreateReplaceResources struct {
	AssignedRef string `json:"assigned_ref"`
	Runtime     string `json:"runtime,omitempty"`
	Builder     string `json:"builder,omitempty"`
	SessionKey  string `json:"session_key,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
	Principal   string `json:"principal,omitempty"`
}

// CreateReplaceBranch records a fresh exact ref observation, including absence.
type CreateReplaceBranch struct {
	Ref      string `json:"ref"`
	Observed bool   `json:"observed"`
	Exists   bool   `json:"exists"`
	OID      string `json:"oid,omitempty"`
}

// SameCreateSelection ignores only the idempotency key when detecting changes.
func SameCreateSelection(a, b ReserveSessionRequest) bool {
	a.Key, b.Key = "", ""
	return a == b
}

// ReplaceableCreationEvidence permits local choices before UUID effects.
// RefCASIntent records a planned Git CAS; replacement never resets/deletes it.
func ReplaceableCreationEvidence(req ReserveSessionRequest, ev CreationEvidence) bool {
	return ValidSessionCreateRequest(req) && req.OriginRef == "" && ev.OriginURL == "" && ev.OriginRef == "" &&
		ev.BranchExisted == (req.Choice == "existing") && ev.RefCASIntent == (req.Choice == "new") &&
		ev.BuilderTreeOID == "" && ev.EnvironmentState == nil
}
