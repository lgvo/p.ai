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
	Intent      OperationIntent `json:"intent"`
	Request     json.RawMessage `json:"request"`
	Status      string          `json:"status"`
	Phase       string          `json:"phase"`
	Committed   bool            `json:"committed"`
	Evidence    json.RawMessage `json:"evidence,omitempty"`
	// Reason is the code of why a blocked or failed operation stopped;
	// Diagnostic is its detail, at most MaxDiagnosticBytes.
	Reason     string `json:"reason,omitempty"`
	Diagnostic string `json:"diagnostic,omitempty"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// OperationIntent is what the caller of a change named, recorded when the
// daemon accepted it. It never holds a guard read from live state, such as an
// expected tip, URL or commit, or a confirmation token.
type OperationIntent struct {
	Project string `json:"project"`
	// Session is the target session; for a creation, the new one.
	Session string `json:"session,omitempty"`
	// Branch is the P branch named, or held by the session, at acceptance.
	Branch string `json:"branch,omitempty"`
	// NewBranch is a session or retained-branch rename's new name.
	NewBranch string `json:"new_branch,omitempty"`
	// Choice, Source and OriginRef are a creation's: new or existing, a P ref
	// or commit, or an origin ref.
	Choice    string `json:"choice,omitempty"`
	Source    string `json:"source,omitempty"`
	OriginRef string `json:"origin_ref,omitempty"`
	// ReplacesSession and ReplacesBranch name the blocked creation a
	// replacement retired and the branch it held, which may differ from
	// Branch.
	ReplacesSession string `json:"replaces_session,omitempty"`
	ReplacesBranch  string `json:"replaces_branch,omitempty"`
	// URL is a new project's origin.
	URL string `json:"url,omitempty"`
}

type OperationListResult struct {
	Operations []OperationSummary `json:"operations"`
	Next       string             `json:"next"`
}

type OperationSummary struct {
	ID          string          `json:"id"`
	Key         string          `json:"idempotency_key"`
	Kind        string          `json:"kind"`
	Project     string          `json:"project"`
	SessionUUID string          `json:"session_uuid,omitempty"`
	Intent      OperationIntent `json:"intent"`
	Status      string          `json:"status"`
	Phase       string          `json:"phase"`
	Committed   bool            `json:"committed"`
	Reason      string          `json:"reason,omitempty"`
	Diagnostic  string          `json:"diagnostic,omitempty"`
	Image       *ImageView      `json:"image,omitempty"`
}

type SessionResult struct {
	Session SessionView `json:"session"`
}

// SessionChangeResult is the result of session.start and session.stop. A
// change that records an operation answers Changed with it; one that finds the
// session already as asked records nothing and answers its current view; and
// one sent while an operation of the same kind is unfinished answers that
// operation, unchanged.
type SessionChangeResult struct {
	Changed   bool         `json:"changed"`
	Operation *Operation   `json:"operation,omitempty"`
	Session   *SessionView `json:"session,omitempty"`
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
	Image                     *ImageView           `json:"image,omitempty"`
}

// ImageView shows the commit image a session is created from, without store
// paths, activation bytes, or builder credentials. Selection is pending, base
// or devShells.<system>.default; State is pending, building, built or reused.
type ImageView struct {
	CommitOID            string `json:"commit_oid,omitempty"`
	System               string `json:"system"`
	Selection            string `json:"selection"`
	State                string `json:"state"`
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

// ProjectService is one project service of a session. Name is the runtime's
// name for it; Detail is the runtime's own words for its state, for display.
type ProjectService struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	State       ServiceState `json:"state"`
	Detail      string       `json:"detail"`
}

// ServiceState is P's closed set of project service states.
type ServiceState string

const (
	// ServiceStopped has no process: never started, stopped, or exited.
	ServiceStopped ServiceState = "stopped"
	// ServiceStarting was started and is not ready yet.
	ServiceStarting ServiceState = "starting"
	// ServiceRunning runs and is ready.
	ServiceRunning ServiceState = "running"
	// ServiceStopping is being stopped.
	ServiceStopping ServiceState = "stopping"
	// ServiceFailed failed its last run; it stays failed until it starts again.
	ServiceFailed ServiceState = "failed"
)

// SessionServicesResult is a session's complete project service inventory.
type SessionServicesResult struct {
	UUID       string           `json:"uuid"`
	Services   []ProjectService `json:"services"`
	ObservedAt string           `json:"observed_at"`
}

// SessionServiceActionResult acknowledges a service action; the inventory
// shows its effect.
type SessionServiceActionResult struct {
	UUID       string `json:"uuid"`
	Name       string `json:"name"`
	ObservedAt string `json:"observed_at"`
}

// SessionServiceLogsResult is a service's recent log tail, newest last.
type SessionServiceLogsResult struct {
	UUID          string `json:"uuid"`
	Name          string `json:"name"`
	Logs          string `json:"logs,omitempty"`
	LogsTruncated bool   `json:"logs_truncated,omitempty"`
	ObservedAt    string `json:"observed_at"`
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
