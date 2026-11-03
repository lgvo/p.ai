package api

import (
	"encoding/json"
	"regexp"
	"time"

	"github.com/lgvo/p.ai/internal/plugin"
)

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

type ProjectSummary struct {
	Path     string `json:"path"`
	Registry string `json:"registry_state"`
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

var projectServicePattern = regexp.MustCompile(`^p-project-[A-Za-z0-9][A-Za-z0-9_.-]{0,100}\.service$`)

func ValidProjectServiceUnit(unit string) bool { return projectServicePattern.MatchString(unit) }

// Project services belong to the session user manager, never host/system units.
type ProjectService struct {
	Unit        string `json:"unit"`
	Description string `json:"description"`
	ActiveState string `json:"active_state"`
	SubState    string `json:"sub_state"`
}

type ServiceResult struct {
	V                int              `json:"v"`
	UUID             string           `json:"uuid"`
	Services         []ProjectService `json:"services,omitempty"`
	Unit             string           `json:"unit,omitempty"`
	Journal          string           `json:"journal,omitempty"`
	JournalTruncated bool             `json:"journal_truncated,omitempty"`
	ObservedAt       string           `json:"observed_at"`
}

type PendingAttachment struct {
	V         int               `json:"v"`
	Token     string            `json:"token"`
	ExpiresAt time.Time         `json:"expires_at"`
	Spec      plugin.AttachSpec `json:"spec"`
}

// AttachmentLaunch is sent only to the trusted host helper on its dedicated
// connection. It cannot be selected or amended by the ordinary client.
type AttachmentLaunch struct {
	V      int               `json:"v"`
	Socket string            `json:"socket"`
	Spec   plugin.AttachSpec `json:"spec"`
}
