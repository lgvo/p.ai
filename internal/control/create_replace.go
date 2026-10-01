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
	AssignedRef  string                    `json:"assigned_ref"`
	Runtime      string                    `json:"runtime,omitempty"`
	Builder      string                    `json:"builder,omitempty"`
	SessionKey   string                    `json:"session_key,omitempty"`
	Endpoint     string                    `json:"endpoint,omitempty"`
	Principal    string                    `json:"principal,omitempty"`
	RuntimeLocal string                    `json:"runtime_local"`
	Cleanup      *CreateReplacementCleanup `json:"cleanup,omitempty"`
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

// ReplaceableCreationEvidence permits local choices without builder/publication
// evidence; the checkpoint and reviewed local resources are checked separately.
// RefCASIntent records a planned Git CAS; replacement never resets/deletes it.
func ReplaceableCreationEvidence(req ReserveSessionRequest, ev CreationEvidence) bool {
	return ValidSessionCreateRequest(req) && req.OriginRef == "" && ev.OriginURL == "" && ev.OriginRef == "" &&
		ev.BranchExisted == (req.Choice == "existing") && ev.RefCASIntent == (req.Choice == "new") &&
		ev.BuilderTreeOID == "" && ev.EnvironmentState == nil && (ev.RuntimeInitState == "" || ev.RuntimeInitState == "not-attempted")
}

// ReplacementLocalIdentity binds reviewed local metadata, never private bytes.
type ReplacementLocalIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	UID    uint32 `json:"uid"`
	Mode   uint32 `json:"mode"`
	Links  uint64 `json:"links"`
}

// CreateReplacementCleanup survives retirement of the old session authority.
// Completed changes only after native absence and exact local cleanup succeed.
type CreateReplacementCleanup struct {
	OldUUID             string                   `json:"old_uuid"`
	OldOperationID      string                   `json:"old_operation_id"`
	OldImageFingerprint string                   `json:"old_image_fingerprint"`
	KeyFingerprint      string                   `json:"key_fingerprint"`
	KeyDirectory        ReplacementLocalIdentity `json:"key_directory"`
	Key                 ReplacementLocalIdentity `json:"key"`
	EndpointPrefix      ReplacementLocalIdentity `json:"endpoint_prefix"`
	EndpointDirectory   ReplacementLocalIdentity `json:"endpoint_directory"`
	GitSocket           ReplacementLocalIdentity `json:"git_socket"`
	SessionSocket       ReplacementLocalIdentity `json:"session_socket"`
	Completed           bool                     `json:"completed,omitempty"`
}

func (c CreateReplacementCleanup) Valid() bool {
	dir := func(i ReplacementLocalIdentity, mode uint32) bool {
		return i.Inode != 0 && i.Mode == mode && i.Links >= 1
	}
	return validUUID(c.OldUUID) && validUUID(c.OldOperationID) && validFingerprint(c.OldImageFingerprint) && validFingerprint(c.KeyFingerprint) &&
		dir(c.KeyDirectory, 040700) && dir(c.EndpointPrefix, 040700) && dir(c.EndpointDirectory, 040755) &&
		c.Key.Inode != 0 && c.Key.Mode == 0100600 && c.Key.Links == 1 &&
		c.GitSocket.Inode != 0 && c.GitSocket.Mode == 0140666 && c.GitSocket.Links == 1 && c.SessionSocket.Inode != 0 && c.SessionSocket.Mode == 0140666 && c.SessionSocket.Links == 1 &&
		c.KeyDirectory.UID == c.Key.UID && c.Key.UID == c.EndpointPrefix.UID && c.Key.UID == c.EndpointDirectory.UID && c.Key.UID == c.GitSocket.UID && c.Key.UID == c.SessionSocket.UID
}

// ReplaceableCreationPhase names the bounded local checkpoints, with a durable
// committed marker appropriate to each. Principals require base-image evidence.
func ReplaceableCreationPhase(op Operation, ev CreationEvidence) bool {
	switch op.Phase {
	case "source-ready":
		return !op.Committed
	case "branch-assigned":
		return op.Committed
	case "principals-ready":
		return op.Committed && ev.Environment == nil && ev.RuntimeInitState == "not-attempted"
	default:
		return false
	}
}
