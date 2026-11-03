package api

import "encoding/json"

// The reviews that a risky change's preview method returns. Each carries a
// short-lived confirmation token that its confirmation method takes.

type RemovalPreviewResult struct {
	Preview RemovalPreview `json:"preview"`
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

// RemovalBranchLoss lists the commits that leave P's repository. Only
// retained-branch deletion reads the origin; session and project deletion
// leave Origin nil.
type RemovalBranchLoss struct {
	AssignedRef                string         `json:"assigned_ref"`
	AssignedTip                string         `json:"assigned_tip,omitempty"`
	CommitsLosingPReachability []string       `json:"commits_losing_p_reachability"`
	PRefs                      []PRef         `json:"p_refs"`
	Origin                     *RemovalOrigin `json:"origin,omitempty"`
}

// PRef is one ref of P's repository at its tip.
type PRef struct {
	Name string `json:"name"`
	OID  string `json:"oid"`
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

type RetainedDeletePreviewResult struct {
	Preview RetainedDeletePreview `json:"preview"`
}

type RetainedDeletePreview struct {
	Project           string             `json:"project"`
	Branch            string             `json:"branch"`
	Ref               string             `json:"ref"`
	Tip               string             `json:"tip"`
	BranchLoss        *RemovalBranchLoss `json:"branch_loss"`
	ConfirmationToken string             `json:"confirmation_token"`
	ExpiresAt         string             `json:"expires_at"`
}

type ProjectDeletePreviewResult struct {
	Preview ProjectDeletionPreview `json:"preview"`
}

// ProjectDeletionPreview is one bounded whole-project loss review. Sessions
// must already be stopped and detached; unknown native effects are never
// removal grants.
type ProjectDeletionPreview struct {
	Project             string                   `json:"project"`
	Outcome             string                   `json:"outcome"`
	StateSHA256         string                   `json:"state_sha256"`
	ProjectPolicySHA256 string                   `json:"project_policy_sha256"`
	ExternalMounts      []FilesystemGrant        `json:"external_mounts_preserved"`
	Sessions            []ProjectDeleteSession   `json:"sessions"`
	Attachments         []string                 `json:"attachments"`
	BranchLoss          *RemovalBranchLoss       `json:"branch_loss"`
	Images              []CommitImageClaim       `json:"images"`
	Repository          ReplacementLocalIdentity `json:"repository_identity"`
	RepositoryParent    ReplacementLocalIdentity `json:"repository_parent_identity"`
	Warnings            []string                 `json:"warnings"`
	ConfirmationToken   string                   `json:"confirmation_token"`
	ExpiresAt           string                   `json:"expires_at"`
}

// ProjectDeleteSession is one session that project deletion removes, and the
// exact local resources it holds.
type ProjectDeleteSession struct {
	Session   SessionRecord            `json:"session"`
	Condition string                   `json:"condition"`
	Runtime   RemovalRuntimePreview    `json:"runtime"`
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

type CreateReplacePreviewResult struct {
	Preview CreateReplacePreview `json:"preview"`
}

// CreateReplacePreview names the old immutable request and the only new
// request this short-lived confirmation may admit. Ineligible previews carry
// reasons but no token.
type CreateReplacePreview struct {
	OldUUID             string                 `json:"old_uuid"`
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
	NewRequest          SessionCreateRequest   `json:"new_request"`
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

// CreationSelection is the runtime, host, source and agent plugins that a
// creation pins, each by ID and digest.
type CreationSelection struct {
	RuntimeID     string `json:"runtime_id"`
	RuntimeSHA256 string `json:"runtime_sha256"`
	HostID        string `json:"host_id"`
	HostSHA256    string `json:"host_sha256"`
	SourceID      string `json:"source_id"`
	SourceSHA256  string `json:"source_sha256"`
	AgentID       string `json:"agent_id,omitempty"`
	AgentSHA256   string `json:"agent_sha256,omitempty"`
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

type CreateCleanupPreviewResult struct {
	Preview CreateCleanupPreview `json:"preview"`
}

// CreateCleanupPreview reviews cleaning up a failed creation. Runtime and
// LossWarnings are set for an assembled creator's stopped container.
type CreateCleanupPreview struct {
	UUID               string                 `json:"uuid"`
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
	Runtime            *RemovalRuntimePreview `json:"runtime,omitempty"`
	LossWarnings       []string               `json:"loss_warnings,omitempty"`
	Eligible           bool                   `json:"eligible"`
	UnsafeReasons      []string               `json:"unsafe_reasons"`
	ConfirmationToken  string                 `json:"confirmation_token,omitempty"`
	ExpiresAt          string                 `json:"expires_at,omitempty"`
}

// CreationBuilderState is a creation's latest image build: its cycle, and its
// state, not-attempted, attempted, abandoned or absent.
type CreationBuilderState struct {
	Cycle uint64 `json:"cycle"`
	State string `json:"state"`
}
