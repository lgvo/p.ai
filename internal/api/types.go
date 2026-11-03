package api

import (
	"encoding/json"
	"regexp"
	"time"

	"github.com/lgvo/p.ai/internal/plugin"
)

// Results of the system methods.

// HelloResult agrees the protocol for the rest of the connection and reports
// which capabilities the daemon has configured.
type HelloResult struct {
	Protocol     string       `json:"protocol"`
	BuildVersion string       `json:"build_version"`
	InstanceID   string       `json:"instance_id"`
	Capabilities []Capability `json:"capabilities"`
}

// Has reports whether the daemon configured capability c.
func (h HelloResult) Has(c Capability) bool {
	for _, have := range h.Capabilities {
		if have == c {
			return true
		}
	}
	return false
}

// HealthResult says that the control store answered; it makes no claim about
// Git, Incus or plugins.
type HealthResult struct {
	ControlState string `json:"control_state"`
}

// SystemInspectResult counts the persisted control records; Git is present
// when Git is configured.
type SystemInspectResult struct {
	Projects   int      `json:"projects"`
	Sessions   int      `json:"sessions"`
	Operations int      `json:"operations"`
	Git        *GitInfo `json:"git,omitempty"`
}

// GitInfo is how a host client reaches P's Git service. It holds public keys
// and a key path, never private key bytes.
type GitInfo struct {
	Endpoint        string `json:"endpoint"`
	URLTemplate     string `json:"url_template"`
	HostPublicKey   string `json:"host_public_key"`
	KnownHosts      string `json:"known_hosts"`
	ClientPublicKey string `json:"client_public_key"`
	ClientKeyPath   string `json:"client_key_path"`
	SourcePluginID  string `json:"source_plugin_id"`
	SourceSHA256    string `json:"source_sha256"`
}

// CancelResult acknowledges a cancellation request, including when its
// target already finished.
type CancelResult struct {
	CancelRequested bool `json:"cancel_requested"`
}

// Results of the project and origin methods.

type ProjectListResult struct {
	Projects []ProjectSummary `json:"projects"`
	Next     string           `json:"next"`
}

type ProjectSummary struct {
	Path     string `json:"path"`
	Registry string `json:"registry_state"`
}

type ProjectBranchesResult struct {
	Project string          `json:"project"`
	Refs    []plugin.GitRef `json:"refs"`
	Next    string          `json:"next"`
}

type OriginResult struct {
	Origin OriginState `json:"origin"`
}

// OriginState is a project's origin association and its last observation.
type OriginState struct {
	Project    string `json:"project"`
	URL        string `json:"url,omitempty"`
	Status     string `json:"status"`
	ObservedAt string `json:"observed_at,omitempty"`
	RefCount   int    `json:"ref_count"`
	Diagnostic string `json:"diagnostic,omitempty"`
}

// OriginSourcesResult pages the refs of the last origin observation.
// ObservationStatus is fresh, stale or none.
type OriginSourcesResult struct {
	Project           string                `json:"project"`
	OriginURL         string                `json:"origin_url"`
	Status            string                `json:"status"`
	ObservationStatus string                `json:"observation_status"`
	ObservedAt        string                `json:"observed_at"`
	Refs              []plugin.GitOriginRef `json:"refs"`
	Next              string                `json:"next"`
}

type PublicationPreviewResult struct {
	Preview PublicationPreview `json:"preview"`
}

type PublicationPreview struct {
	Project        string `json:"project"`
	URL            string `json:"url"`
	SourceKind     string `json:"source_kind"`
	Source         string `json:"source"`
	SourceRef      string `json:"source_ref"`
	SourceOID      string `json:"source_oid"`
	DestinationRef string `json:"destination_ref"`
	DestinationOID string `json:"destination_oid,omitempty"`
	Relation       string `json:"relation"`
	// Runtime workspace divergence is not currently observable here.
	WorkspaceStatus string `json:"workspace_status"`
}

type OriginPublishResult struct {
	Publication Publication `json:"publication"`
}

// Publication is one publication's outcome: created, advanced, satisfied,
// refused or outcome_unknown.
type Publication struct {
	Preview PublicationPreview `json:"preview"`
	Status  string             `json:"status"`
}

type RetainedBranchesResult struct {
	Branches []RetainedBranch `json:"branches"`
	Next     string           `json:"next"`
}

type RetainedBranch struct {
	Branch string `json:"branch"`
	Ref    string `json:"ref"`
	OID    string `json:"oid"`
}

// Results of the lifecycle methods.

// OperationResult is the result of every change that records an operation.
type OperationResult struct {
	Operation Operation `json:"operation"`
}

type Operation struct {
	ID          string          `json:"id"`
	Key         string          `json:"idempotency_key"`
	Kind        string          `json:"kind"`
	Project     string          `json:"project"`
	SessionUUID string          `json:"session_uuid,omitempty"`
	Request     json.RawMessage `json:"request"`
	Status      string          `json:"status"`
	Phase       string          `json:"phase"`
	Committed   bool            `json:"committed"`
	Evidence    json.RawMessage `json:"evidence,omitempty"`
	Diagnostic  string          `json:"diagnostic,omitempty"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

type OperationListResult struct {
	Operations []OperationSummary `json:"operations"`
	Next       string             `json:"next"`
}

type OperationSummary struct {
	ID          string           `json:"id"`
	Key         string           `json:"idempotency_key"`
	Kind        string           `json:"kind"`
	Project     string           `json:"project"`
	SessionUUID string           `json:"session_uuid,omitempty"`
	Status      string           `json:"status"`
	Phase       string           `json:"phase"`
	Committed   bool             `json:"committed"`
	Diagnostic  string           `json:"diagnostic,omitempty"`
	Environment *EnvironmentView `json:"environment,omitempty"`
}

type SessionResult struct {
	Session SessionView `json:"session"`
}

type SessionListResult struct {
	Sessions []SessionView `json:"sessions"`
	Next     string        `json:"next"`
}

type SessionView struct {
	UUID                      string               `json:"uuid"`
	Project                   string               `json:"project"`
	Branch                    string               `json:"branch"`
	Registry                  string               `json:"registry_state"`
	PolicySHA256              string               `json:"policy_sha256"`
	Condition                 string               `json:"session_condition"`
	PolicyCondition           string               `json:"policy_condition"`
	AttachedCount             int                  `json:"attached_count"`
	LatestUnattendedCondition *UnattendedCondition `json:"latest_unattended_condition"`
	Diagnostic                string               `json:"diagnostic,omitempty"`
	Environment               *EnvironmentView     `json:"environment,omitempty"`
}

// EnvironmentView exposes bounded selection and cache state without store
// paths, activation bytes, or builder credentials.
type EnvironmentView struct {
	CommitOID            string `json:"commit_oid,omitempty"`
	System               string `json:"system"`
	Selection            string `json:"selection"`
	Cache                string `json:"cache"`
	Key                  string `json:"environment_key,omitempty"`
	BaseImageFingerprint string `json:"base_image_fingerprint"`
	ImageFingerprint     string `json:"image_fingerprint"`
}

// UnattendedCondition is the last valid report received with no confirmed
// attachment. It is an observation, not an assertion of current agent state.
type UnattendedCondition struct {
	Condition       string `json:"condition"`
	Source          string `json:"source,omitempty"`
	Reason          string `json:"reason,omitempty"`
	Adapter         string `json:"adapter,omitempty"`
	AdapterVersion  string `json:"adapter_version,omitempty"`
	ReceivedAt      string `json:"received_at"`
	ReceiveSequence int64  `json:"receive_sequence"`
}

// Results of the attachment methods.

// PendingAttachment is a one-use, short-lived token that the attachment
// helper claims, and the fixed native target it attaches to.
type PendingAttachment struct {
	Token     string            `json:"token"`
	ExpiresAt time.Time         `json:"expires_at"`
	Spec      plugin.AttachSpec `json:"spec"`
}

// AttachmentLaunch is sent only to the trusted host helper on its dedicated
// connection. It cannot be selected or amended by the ordinary client.
type AttachmentLaunch struct {
	Socket string            `json:"socket"`
	Spec   plugin.AttachSpec `json:"spec"`
}

// AttachmentResult acknowledges attachment.confirm and attachment.ping.
type AttachmentResult struct{}

// Results of the project services methods.

var projectServicePattern = regexp.MustCompile(`^p-project-[A-Za-z0-9][A-Za-z0-9_.-]{0,100}\.service$`)

func ValidProjectServiceUnit(unit string) bool { return projectServicePattern.MatchString(unit) }

// ProjectService belongs to the session user manager, never host/system units.
type ProjectService struct {
	Unit        string `json:"unit"`
	Description string `json:"description"`
	ActiveState string `json:"active_state"`
	SubState    string `json:"sub_state"`
}

// ServiceResult is the result of the three services methods: the inventory
// sets Services, an action and the journal set Unit.
type ServiceResult struct {
	UUID             string           `json:"uuid"`
	Services         []ProjectService `json:"services,omitempty"`
	Unit             string           `json:"unit,omitempty"`
	Journal          string           `json:"journal,omitempty"`
	JournalTruncated bool             `json:"journal_truncated,omitempty"`
	ObservedAt       string           `json:"observed_at"`
}

// The private session RPC.

// SessionRPCVersion is the version every private session RPC request and
// result carries.
const SessionRPCVersion = 1

type SessionIdentityResult struct {
	V      int    `json:"v"`
	UUID   string `json:"uuid"`
	Branch string `json:"branch"`
}

type SessionCapabilitiesResult struct {
	V                     int                   `json:"v"`
	UUID                  string                `json:"uuid"`
	Branch                string                `json:"branch"`
	PolicySHA256          string                `json:"policy_sha256"`
	EffectiveCapabilities EffectiveCapabilities `json:"effective_capabilities"`
}

// EffectiveCapabilities come from the session's captured policy.
type EffectiveCapabilities struct {
	Network          string         `json:"network"`
	FilesystemMounts []SessionMount `json:"filesystem_mounts"`
	SourceGit        bool           `json:"source_git"`
	StatusReport     bool           `json:"status_report"`
}

// SessionMount is one filesystem grant as the session sees it, at
// /mnt/p/<name>.
type SessionMount struct {
	Name       string `json:"name"`
	Target     string `json:"target"`
	Type       string `json:"type"`
	Access     string `json:"access"`
	Executable bool   `json:"executable"`
}

// NoResult is the result of a notification, which receives no reply.
type NoResult struct{}
