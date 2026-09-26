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
