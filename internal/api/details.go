package api

import "encoding/json"

// The details of each risky change's plan (plans.go): everything the plan
// shows besides its envelope.

// RemovalDetails is the plan of a session's discard or delete.
// AssignedRefMissing says there is no branch: delete then removes nothing from
// P's repository, and discard keeps no branch. BranchLoss is a delete's.
type RemovalDetails struct {
	Project            string             `json:"project"`
	Branch             string             `json:"branch"`
	AssignedRef        string             `json:"assigned_ref"`
	AssignedTip        string             `json:"assigned_tip,omitempty"`
	AssignedRefMissing bool               `json:"assigned_ref_missing,omitempty"`
	PolicySHA256       string             `json:"policy_sha256"`
	Runtime            RemovalRuntime     `json:"runtime"`
	BranchLoss         *RemovalBranchLoss `json:"branch_loss,omitempty"`
}

// RemovalRuntime is a session's container as a removal found it: present,
// with its exact identity and its Incus status, or missing. P does not look
// inside a present container.
type RemovalRuntime struct {
	Condition        string `json:"condition"`
	IncusProject     string `json:"incus_project"`
	InstanceName     string `json:"instance_name"`
	IncusUUID        string `json:"incus_uuid,omitempty"`
	Generation       string `json:"generation,omitempty"`
	ImageFingerprint string `json:"image_fingerprint,omitempty"`
	OriginalStatus   string `json:"original_status,omitempty"`
}

// RemovalBranchLoss lists the commits that leave P's repository, and every
// ref of P's repository at its tip.
type RemovalBranchLoss struct {
	AssignedRef                string   `json:"assigned_ref"`
	AssignedTip                string   `json:"assigned_tip,omitempty"`
	CommitsLosingPReachability []string `json:"commits_losing_p_reachability"`
	PRefs                      []PRef   `json:"p_refs"`
}

// PRef is one ref of P's repository at its tip.
type PRef struct {
	Name string `json:"name"`
	OID  string `json:"oid"`
}

// RetainedDeleteDetails is the plan of deleting a retained branch.
type RetainedDeleteDetails struct {
	Project    string             `json:"project"`
	Branch     string             `json:"branch"`
	Ref        string             `json:"ref"`
	Tip        string             `json:"tip"`
	BranchLoss *RemovalBranchLoss `json:"branch_loss"`
}

// ProjectDeleteDetails is the plan of deleting a whole project: every
// session, image and the repository it removes, the repository at the
// incarnation the source driver reported, which is opaque to P. Attachments
// is always empty: an attached session refuses the deletion.
type ProjectDeleteDetails struct {
	Project               string                 `json:"project"`
	Outcome               string                 `json:"outcome"`
	StateSHA256           string                 `json:"state_sha256"`
	ProjectPolicySHA256   string                 `json:"project_policy_sha256"`
	ExternalMounts        []FilesystemGrant      `json:"external_mounts_preserved"`
	Sessions              []ProjectDeleteSession `json:"sessions"`
	Attachments           []string               `json:"attachments"`
	BranchLoss            *RemovalBranchLoss     `json:"branch_loss"`
	Images                []CommitImageClaim     `json:"images"`
	RepositoryIncarnation string                 `json:"repository_incarnation"`
}

// ProjectDeleteSession is one session that project deletion removes, and the
// exact local resources it holds. Condition is running, stopped or missing:
// P stops a running session itself.
type ProjectDeleteSession struct {
	Session   SessionRecord            `json:"session"`
	Condition string                   `json:"condition"`
	Runtime   RemovalRuntime           `json:"runtime"`
	Local     CreateReplacementCleanup `json:"credentials"`
}

// CommitImageClaim is one commit image that project deletion deletes: its
// index row, whether the runtime still holds it and the size observed, and a
// digest of the sessions started from its commit.
type CommitImageClaim struct {
	Image         CommitImage `json:"image"`
	ImagePresent  bool        `json:"image_present"`
	ObservedSize  int64       `json:"observed_size"`
	RelatedDigest string      `json:"related_digest"`
}

// CommitImage is one row of P's commit-image index: the image P keeps for
// sessions started from a source commit. State is ready, or releasing once its
// delete may have started.
type CommitImage struct {
	Key            CommitImageKey `json:"key"`
	Ref            string         `json:"ref"`
	Build          string         `json:"build"`
	Labels         []ImageLabel   `json:"labels"`
	Selection      string         `json:"selection"`
	MaterialSHA256 string         `json:"material_sha256,omitempty"`
	Size           int64          `json:"size"`
	State          string         `json:"state"`
}

// CommitImageKey names a commit image: the commit's project, the base image
// and system it was built for, and the image builder's version.
type CommitImageKey struct {
	Project        string `json:"project"`
	Commit         string `json:"commit_oid"`
	BaseImage      string `json:"base_image"`
	System         string `json:"system"`
	BuilderVersion string `json:"builder_version"`
}

// ImageLabel is one label P gave an image, in name order within its list.
type ImageLabel struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SessionRecord is a session's registry record as P stores it.
type SessionRecord struct {
	UUID         string          `json:"uuid"`
	Project      string          `json:"project"`
	Branch       string          `json:"branch"`
	Registry     string          `json:"registry_state"`
	Policy       json.RawMessage `json:"policy"`
	PolicySHA256 string          `json:"policy_sha256"`
}

// FilesystemGrant is a trusted host file or directory mounted at
// /mnt/p/<name>. Removal preserves its contents.
type FilesystemGrant struct {
	Name           string               `json:"name"`
	Source         string               `json:"source"`
	Type           string               `json:"type"`
	Access         string               `json:"access"`
	Executable     bool                 `json:"executable"`
	SourceIdentity *GrantSourceIdentity `json:"source_identity,omitempty"`
}

// GrantSourceIdentity is the grant source P captured into the session's
// immutable policy.
type GrantSourceIdentity struct {
	Device   uint64 `json:"device"`
	Inode    uint64 `json:"inode"`
	OwnerUID uint32 `json:"owner_uid"`
	OwnerGID uint32 `json:"owner_gid"`
}

// CreateReplaceDetails is the plan of replacing a blocked creation with a new
// one: the old creation and its branch, the new request without its key, and
// the provisional resources of the old one.
type CreateReplaceDetails struct {
	OldOperationID      string                 `json:"old_operation_id"`
	OldStatus           string                 `json:"old_status"`
	OldPhase            string                 `json:"old_phase"`
	OldRequest          SessionCreateRequest   `json:"old_request"`
	OldBranch           CreateReplaceBranch    `json:"old_branch"`
	NewBranch           CreateReplaceBranch    `json:"new_branch"`
	OldCapturedOID      string                 `json:"old_captured_oid"`
	OldEvidenceSHA256   string                 `json:"old_evidence_sha256"`
	OldPolicySHA256     string                 `json:"old_policy_sha256"`
	OldImageFingerprint string                 `json:"old_image_fingerprint"`
	NewRequest          CreationChoices        `json:"new_request"`
	NewCapturedOID      string                 `json:"new_captured_oid,omitempty"`
	NewImageFingerprint string                 `json:"new_image_fingerprint"`
	NewSelection        CreationSelection      `json:"new_selection"`
	NewEnvironment      *EnvironmentIntent     `json:"new_environment,omitempty"`
	NewPolicySHA256     string                 `json:"new_policy_sha256,omitempty"`
	Provisional         CreateReplaceResources `json:"provisional"`
}

// CreationSelection is the host, source and agent plugins that a creation
// pins, each by ID and digest.
type CreationSelection struct {
	HostID       string `json:"host_id"`
	HostSHA256   string `json:"host_sha256"`
	SourceID     string `json:"source_id"`
	SourceSHA256 string `json:"source_sha256"`
	AgentID      string `json:"agent_id,omitempty"`
	AgentSHA256  string `json:"agent_sha256,omitempty"`
}

// EnvironmentIntent is the image-builder configuration that a creation pins
// when image builders are configured: the system, base image, builder pool
// and policy, and the image builder's version.
type EnvironmentIntent struct {
	System              string `json:"system"`
	BaseFingerprint     string `json:"base_fingerprint"`
	BuilderStoragePool  string `json:"builder_storage_pool"`
	BuilderPolicySHA256 string `json:"builder_policy_sha256"`
	ImageBuilder        string `json:"image_builder"`
}

// CreateReplaceResources reports the provisional resources of a failed
// creation.
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

// CreateReplaceBranch is one observed P branch. Observed false means the
// inspection was unavailable, not that the branch is absent.
type CreateReplaceBranch struct {
	Ref      string `json:"ref"`
	Observed bool   `json:"observed"`
	Exists   bool   `json:"exists"`
	OID      string `json:"oid,omitempty"`
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

// Valid reports whether c names a private key directory and key, the endpoint
// directories and both endpoint sockets, all owned by one user.
func (c CreateReplacementCleanup) Valid() bool {
	dir := func(i ReplacementLocalIdentity, mode uint32) bool {
		return i.Inode != 0 && i.Mode == mode && i.Links >= 1
	}
	return ValidUUID(c.OldUUID) && ValidUUID(c.OldOperationID) && ValidFingerprint(c.OldImageFingerprint) && ValidFingerprint(c.KeyFingerprint) &&
		dir(c.KeyDirectory, 040700) && dir(c.EndpointPrefix, 040700) && dir(c.EndpointDirectory, 040755) &&
		c.Key.Inode != 0 && c.Key.Mode == 0100600 && c.Key.Links == 1 &&
		c.GitSocket.Inode != 0 && c.GitSocket.Mode == 0140666 && c.GitSocket.Links == 1 && c.SessionSocket.Inode != 0 && c.SessionSocket.Mode == 0140666 && c.SessionSocket.Links == 1 &&
		c.KeyDirectory.UID == c.Key.UID && c.Key.UID == c.EndpointPrefix.UID && c.Key.UID == c.EndpointDirectory.UID && c.Key.UID == c.GitSocket.UID && c.Key.UID == c.SessionSocket.UID
}

// CreateCleanupDetails is the plan of cleaning up a failed creation. Runtime
// is set for an assembled creator's stopped container, which the cleanup
// deletes.
type CreateCleanupDetails struct {
	OldOperationID     string                 `json:"old_operation_id"`
	OldRequest         SessionCreateRequest   `json:"old_request"`
	OldPhase           string                 `json:"old_phase"`
	OldEvidenceSHA256  string                 `json:"old_evidence_sha256"`
	OldRequestSHA256   string                 `json:"old_request_sha256,omitempty"`
	PolicySHA256       string                 `json:"policy_sha256"`
	AssignedBranch     CreateReplaceBranch    `json:"assigned_branch"`
	ImageFingerprint   string                 `json:"image_fingerprint"`
	EnvironmentBuilder *CreationBuilderState  `json:"environment_builder,omitempty"`
	Provisional        CreateReplaceResources `json:"provisional"`
	ExternalMounts     string                 `json:"external_mounts"`
	SharedImages       string                 `json:"shared_images"`
	Runtime            *RemovalRuntime        `json:"runtime,omitempty"`
}

// CreationBuilderState is a creation's latest image build: its cycle, and its
// state, not-attempted, attempted, abandoned or absent.
type CreationBuilderState struct {
	Cycle uint64 `json:"cycle"`
	State string `json:"state"`
}
