package api

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// One request type per method, even where two methods take the same fields:
// each Validate states that method's bounds.

// HelloRequest opens a connection. The daemon refuses a protocol it does not
// speak with unsupported_version.
type HelloRequest struct {
	Protocol string `json:"protocol"`
}

func (r HelloRequest) Validate() error {
	if r.Protocol == "" || len(r.Protocol) > 64 {
		return invalid("system.hello requires a protocol of at most 64 bytes")
	}
	return nil
}

type HealthRequest struct{}

func (HealthRequest) Validate() error { return nil }

type SystemInspectRequest struct{}

func (SystemInspectRequest) Validate() error { return nil }

// CancelRequest names a pending request on the same connection by its id.
type CancelRequest struct {
	ID json.RawMessage `json:"id"`
}

func (r CancelRequest) Validate() error {
	if _, err := CanonicalID(r.ID); err != nil {
		return invalid("rpc.cancel requires the id of a request")
	}
	return nil
}

// CanonicalID returns the canonical form of a JSON-RPC request id: a string
// of at most 128 bytes or an integer.
func CanonicalID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", invalid("missing id")
	}
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return "", err
	}
	switch v := value.(type) {
	case string:
		if len(v) > 128 {
			return "", invalid("id is too long")
		}
		encoded, _ := json.Marshal(v)
		return string(encoded), nil
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return "", invalid("id must be an integer")
		}
		return strconv.FormatInt(n, 10), nil
	default:
		return "", invalid("id must be a string or integer")
	}
}

type ProjectListRequest struct {
	After string `json:"after,omitempty"`
	Limit int    `json:"limit"`
}

func (r ProjectListRequest) Validate() error {
	if r.Limit < 1 || r.Limit > 100 || r.After != "" && !ValidProject(r.After) {
		return invalid("project.list requires limit 1..100 and a project cursor")
	}
	return nil
}

type ProjectBranchesRequest struct {
	Project string `json:"project"`
	After   string `json:"after,omitempty"`
	Limit   int    `json:"limit"`
}

func (r ProjectBranchesRequest) Validate() error {
	if !ValidProject(r.Project) || r.Limit < 1 || r.Limit > 8 || !validBranchCursor(r.After) {
		return invalid("project.branches requires project, limit 1..8 and a branch ref cursor")
	}
	return nil
}

// OriginChangeRequest sets or removes a project's origin. ExpectedURL is the
// current URL, empty for none; remove omits URL.
type OriginChangeRequest struct {
	Key         string `json:"key"`
	Project     string `json:"project"`
	Kind        string `json:"kind"`
	ExpectedURL string `json:"expected_url"`
	URL         string `json:"url,omitempty"`
}

func (r OriginChangeRequest) Validate() error {
	if !validKey(r.Key) || !ValidProject(r.Project) || len(r.ExpectedURL) > 2048 || len(r.URL) > 2048 ||
		!(r.Kind == "remove" && r.URL == "" || r.Kind == "set" && r.URL != "") {
		return invalid("invalid origin.change request")
	}
	return nil
}

type OriginRefreshRequest struct {
	Project string `json:"project"`
}

func (r OriginRefreshRequest) Validate() error { return validOriginProject(r.Project) }

type OriginInspectRequest struct {
	Project string `json:"project"`
}

func (r OriginInspectRequest) Validate() error { return validOriginProject(r.Project) }

func validOriginProject(project string) error {
	if !ValidProject(project) {
		return invalid("invalid origin project")
	}
	return nil
}

type OriginSourcesRequest struct {
	Project string `json:"project"`
	After   string `json:"after,omitempty"`
	Limit   int    `json:"limit"`
}

func (r OriginSourcesRequest) Validate() error {
	if !ValidProject(r.Project) || r.Limit < 1 || r.Limit > 16 || r.After != "" && !validOriginRef(r.After) {
		return invalid("invalid origin source page")
	}
	return nil
}

// OriginPublicationPreviewRequest names the P source, its exact tip and the
// origin branch to publish to. Source is a session UUID or a retained branch
// name, according to Kind.
type OriginPublicationPreviewRequest struct {
	ExpectedOriginURL string `json:"expected_origin_url"`
	Project           string `json:"project"`
	Kind              string `json:"kind"`
	Source            string `json:"source"`
	SourceOID         string `json:"source_oid"`
	DestinationRef    string `json:"destination_ref"`
}

func (r OriginPublicationPreviewRequest) Validate() error {
	return validPublication(r.ExpectedOriginURL, r.Project, r.Kind, r.Source, r.SourceOID, r.DestinationRef)
}

type OriginPublishRequest struct {
	ExpectedOriginURL string `json:"expected_origin_url"`
	Key               string `json:"key"`
	Project           string `json:"project"`
	Kind              string `json:"kind"`
	Source            string `json:"source"`
	SourceOID         string `json:"source_oid"`
	DestinationRef    string `json:"destination_ref"`
}

func (r OriginPublishRequest) Validate() error {
	if !validKey(r.Key) {
		return invalid("origin.publish requires a key of 1..128 bytes")
	}
	return validPublication(r.ExpectedOriginURL, r.Project, r.Kind, r.Source, r.SourceOID, r.DestinationRef)
}

func validPublication(url, project, kind, source, oid, destination string) error {
	ok := url != "" && len(url) <= 2048 && ValidProject(project) && ValidOID(oid) &&
		strings.HasPrefix(destination, "refs/heads/") && validOriginRef(destination) &&
		(kind == "session" && len(source) == 36 || kind == "retained" && ValidBranch(source))
	if !ok {
		return invalid("invalid publication request; the destination must be a branch")
	}
	return nil
}

type ProjectRetainedBranchesRequest struct {
	Project string `json:"project"`
	After   string `json:"after,omitempty"`
	Limit   int    `json:"limit"`
}

func (r ProjectRetainedBranchesRequest) Validate() error {
	if !ValidProject(r.Project) || r.Limit < 1 || r.Limit > 8 || !validBranchCursor(r.After) {
		return invalid("invalid retained branch page")
	}
	return nil
}

// ProjectCreateRequest creates a blank project, or one with an origin when
// URL is set.
type ProjectCreateRequest struct {
	Key     string `json:"key"`
	Project string `json:"project"`
	URL     string `json:"url,omitempty"`
}

func (r ProjectCreateRequest) Validate() error {
	if r.Key == "" || !ValidProject(r.Project) || len(r.URL) > 2048 {
		return invalid("project.create requires key and project")
	}
	return nil
}

// SessionCreateRequest creates a session on an existing P branch
// (choice "existing") or a new one (choice "new") from a P source, or from
// an origin ref at the commit the client observed. Control stores it as the
// creation's request.
type SessionCreateRequest struct {
	Key               string `json:"key"`
	Project           string `json:"project"`
	Branch            string `json:"branch"`
	Choice            string `json:"choice"`
	Source            string `json:"source,omitempty"`
	OriginRef         string `json:"origin_ref,omitempty"`
	ExpectedCommitOID string `json:"expected_commit_oid,omitempty"`
	ExpectedOriginURL string `json:"expected_origin_url,omitempty"`
}

func (r SessionCreateRequest) Validate() error {
	ok := validKey(r.Key) && ValidProject(r.Project) && ValidBranch(r.Branch) && len(r.ExpectedOriginURL) <= 2048 && (r.ExpectedOriginURL == "" || r.OriginRef != "") &&
		((r.Choice == "existing" && r.Source == "" && r.OriginRef == "" && r.ExpectedCommitOID == "") ||
			(r.Choice == "new" && r.OriginRef == "" && r.ExpectedCommitOID == "" && r.Source != "" && len(r.Source) <= 255) ||
			(r.Choice == "new" && r.Source == "" && validOriginRef(r.OriginRef) && ValidOID(r.ExpectedCommitOID)))
	if !ok {
		return invalid("invalid session.create request")
	}
	return nil
}

type SessionInspectRequest struct {
	UUID string `json:"uuid"`
}

func (r SessionInspectRequest) Validate() error { return validSessionID(r.UUID) }

type SessionStartRequest struct {
	UUID string `json:"uuid"`
}

func (r SessionStartRequest) Validate() error { return validSessionID(r.UUID) }

type SessionStopRequest struct {
	UUID string `json:"uuid"`
}

func (r SessionStopRequest) Validate() error { return validSessionID(r.UUID) }

type SessionAttachRequest struct {
	UUID string `json:"uuid"`
}

func (r SessionAttachRequest) Validate() error { return validSessionID(r.UUID) }

// validSessionID admits a 36-byte session UUID; the lifecycle reports one it
// does not know as unavailable.
func validSessionID(id string) error {
	if len(id) != 36 {
		return invalid("session UUID required")
	}
	return nil
}

type SessionListRequest struct {
	Limit int    `json:"limit"`
	After string `json:"after,omitempty"`
}

func (r SessionListRequest) Validate() error {
	if r.Limit < 1 || r.Limit > 8 || len(r.After) > 128 {
		return invalid("session.list requires limit 1..8")
	}
	return nil
}

type OperationListRequest struct {
	Limit int    `json:"limit"`
	After string `json:"after,omitempty"`
}

func (r OperationListRequest) Validate() error {
	if r.Limit < 1 || r.Limit > 20 || len(r.After) > 128 {
		return invalid("operation.list requires limit 1..20")
	}
	return nil
}

type OperationInspectRequest struct {
	ID string `json:"id"`
}

func (r OperationInspectRequest) Validate() error { return validOperationID(r.ID) }

type OperationRetryRequest struct {
	ID string `json:"id"`
}

func (r OperationRetryRequest) Validate() error { return validOperationID(r.ID) }

func validOperationID(id string) error {
	if id == "" || len(id) > 128 {
		return invalid("operation ID required")
	}
	return nil
}

// SessionRemovalPreviewRequest reviews a discard or delete. Acknowledge a
// runtime that Incus reports absent with AcknowledgeMissingRuntime.
type SessionRemovalPreviewRequest struct {
	UUID                      string `json:"uuid"`
	Kind                      string `json:"kind"`
	AcknowledgeMissingRuntime bool   `json:"acknowledge_missing_runtime,omitempty"`
}

func (r SessionRemovalPreviewRequest) Validate() error {
	if !ValidUUID(r.UUID) || r.Kind != "discard" && r.Kind != "delete" {
		return invalid("invalid session.removal.preview request")
	}
	return nil
}

type SessionDiscardRequest struct {
	Key               string `json:"key"`
	UUID              string `json:"uuid"`
	ConfirmationToken string `json:"confirmation_token"`
}

func (r SessionDiscardRequest) Validate() error {
	return validRemoval("session.discard", r.Key, r.UUID, r.ConfirmationToken)
}

type SessionDeleteRequest struct {
	Key               string `json:"key"`
	UUID              string `json:"uuid"`
	ConfirmationToken string `json:"confirmation_token"`
}

func (r SessionDeleteRequest) Validate() error {
	return validRemoval("session.delete", r.Key, r.UUID, r.ConfirmationToken)
}

func validRemoval(method, key, uuid, token string) error {
	if !validKey(key) || !ValidUUID(uuid) || !ValidToken(token) {
		return invalid("invalid " + method + " request")
	}
	return nil
}

type SessionRenameRequest struct {
	Key            string `json:"key"`
	UUID           string `json:"uuid"`
	NewBranch      string `json:"new_branch"`
	ExpectedOldTip string `json:"expected_old_tip"`
}

func (r SessionRenameRequest) Validate() error {
	if !validKey(r.Key) || !ValidUUID(r.UUID) || !ValidBranch(r.NewBranch) || !ValidOID(r.ExpectedOldTip) {
		return invalid("invalid session.rename request")
	}
	return nil
}

type ProjectRetainedRenameRequest struct {
	Key            string `json:"key"`
	Project        string `json:"project"`
	OldBranch      string `json:"old_branch"`
	NewBranch      string `json:"new_branch"`
	ExpectedOldTip string `json:"expected_old_tip"`
}

func (r ProjectRetainedRenameRequest) Validate() error {
	if !validKey(r.Key) || !ValidProject(r.Project) || !ValidBranch(r.OldBranch) || !ValidBranch(r.NewBranch) ||
		r.OldBranch == r.NewBranch || !ValidOID(r.ExpectedOldTip) {
		return invalid("invalid project.retained.rename request")
	}
	return nil
}

// ProjectDeletePreviewRequest reviews deleting a whole project. List each
// session whose runtime Incus reports absent in AcknowledgeMissing.
type ProjectDeletePreviewRequest struct {
	Project            string   `json:"project"`
	AcknowledgeMissing []string `json:"acknowledge_missing,omitempty"`
}

func (r ProjectDeletePreviewRequest) Validate() error {
	if !ValidProject(r.Project) || len(r.AcknowledgeMissing) > 4 {
		return invalid("invalid project deletion preview")
	}
	for _, uuid := range r.AcknowledgeMissing {
		if !ValidUUID(uuid) {
			return invalid("invalid missing runtime acknowledgement")
		}
	}
	return nil
}

type ProjectDeleteConfirmRequest struct {
	Key               string `json:"key"`
	Project           string `json:"project"`
	ConfirmationToken string `json:"confirmation_token"`
}

func (r ProjectDeleteConfirmRequest) Validate() error {
	if !ValidProject(r.Project) || !validKey(r.Key) || !ValidToken(r.ConfirmationToken) {
		return invalid("invalid project deletion confirmation")
	}
	return nil
}

type ProjectRetainedDeletePreviewRequest struct {
	Project string `json:"project"`
	Branch  string `json:"branch"`
}

func (r ProjectRetainedDeletePreviewRequest) Validate() error {
	if !ValidProject(r.Project) || !ValidBranch(r.Branch) {
		return invalid("invalid retained delete preview request")
	}
	return nil
}

type ProjectRetainedDeleteConfirmRequest struct {
	Key               string `json:"key"`
	Project           string `json:"project"`
	Branch            string `json:"branch"`
	ConfirmationToken string `json:"confirmation_token"`
}

func (r ProjectRetainedDeleteConfirmRequest) Validate() error {
	if !validKey(r.Key) || !ValidProject(r.Project) || !ValidBranch(r.Branch) || !ValidToken(r.ConfirmationToken) {
		return invalid("invalid retained delete confirmation request")
	}
	return nil
}

// SessionCreateReplacePreviewRequest reviews replacing the blocked creation
// OldUUID with a new creation request. Origin requests are outside this
// path, so it takes no expected origin URL.
type SessionCreateReplacePreviewRequest struct {
	OldUUID           string `json:"old_uuid"`
	Key               string `json:"key"`
	Project           string `json:"project"`
	Branch            string `json:"branch"`
	Choice            string `json:"choice"`
	Source            string `json:"source,omitempty"`
	OriginRef         string `json:"origin_ref,omitempty"`
	ExpectedCommitOID string `json:"expected_commit_oid,omitempty"`
}

// Create is the creation request the replacement would admit.
func (r SessionCreateReplacePreviewRequest) Create() SessionCreateRequest {
	return SessionCreateRequest{Key: r.Key, Project: r.Project, Branch: r.Branch, Choice: r.Choice, Source: r.Source,
		OriginRef: r.OriginRef, ExpectedCommitOID: r.ExpectedCommitOID}
}

func (r SessionCreateReplacePreviewRequest) Validate() error {
	if !ValidUUID(r.OldUUID) || r.Create().Validate() != nil {
		return invalid("invalid create replacement preview")
	}
	return nil
}

type SessionCreateReplaceConfirmRequest struct {
	OldUUID           string `json:"old_uuid"`
	Key               string `json:"key"`
	ConfirmationToken string `json:"confirmation_token"`
}

func (r SessionCreateReplaceConfirmRequest) Validate() error {
	if !ValidUUID(r.OldUUID) || !validKey(r.Key) || !ValidToken(r.ConfirmationToken) {
		return invalid("invalid create replacement confirmation")
	}
	return nil
}

type SessionCreateCleanupPreviewRequest struct {
	UUID string `json:"uuid"`
}

func (r SessionCreateCleanupPreviewRequest) Validate() error {
	if !ValidUUID(r.UUID) {
		return invalid("invalid failed creation cleanup preview")
	}
	return nil
}

type SessionCreateCleanupConfirmRequest struct {
	UUID              string `json:"uuid"`
	Key               string `json:"key"`
	ConfirmationToken string `json:"confirmation_token"`
}

func (r SessionCreateCleanupConfirmRequest) Validate() error {
	if !ValidUUID(r.UUID) || !validKey(r.Key) || !ValidToken(r.ConfirmationToken) {
		return invalid("invalid failed creation cleanup confirmation")
	}
	return nil
}

type SessionServicesRequest struct {
	UUID string `json:"uuid"`
}

func (r SessionServicesRequest) Validate() error { return validServiceSession(r.UUID) }

type SessionServiceActionRequest struct {
	UUID   string `json:"uuid"`
	Unit   string `json:"unit"`
	Action string `json:"action"`
}

func (r SessionServiceActionRequest) Validate() error {
	if r.Action != "start" && r.Action != "stop" && r.Action != "restart" {
		return invalid("service action must be start, stop or restart")
	}
	return validServiceUnit(r.UUID, r.Unit)
}

type SessionServiceJournalRequest struct {
	UUID string `json:"uuid"`
	Unit string `json:"unit"`
}

func (r SessionServiceJournalRequest) Validate() error { return validServiceUnit(r.UUID, r.Unit) }

func validServiceSession(uuid string) error {
	if !ValidUUID(uuid) {
		return invalid("services require a session UUID")
	}
	return nil
}

// validServiceUnit admits only a session-user p-project-*.service unit.
func validServiceUnit(uuid, unit string) error {
	if !ValidProjectServiceUnit(unit) {
		return invalid("a project service unit is required")
	}
	return validServiceSession(uuid)
}

// AttachmentClaimRequest redeems a pending attachment's token. Only the
// trusted attachment helper may send it.
type AttachmentClaimRequest struct {
	Token string `json:"token"`
}

func (r AttachmentClaimRequest) Validate() error {
	if len(r.Token) != 64 {
		return invalid("invalid lifecycle request")
	}
	return nil
}

// AttachmentConfirmRequest names the native exec operation that runs the
// claimed attachment.
type AttachmentConfirmRequest struct {
	Operation string `json:"operation"`
}

func (r AttachmentConfirmRequest) Validate() error {
	if len(r.Operation) != len("/1.0/operations/")+36 {
		return invalid("invalid lifecycle request")
	}
	return nil
}

type AttachmentPingRequest struct{}

func (AttachmentPingRequest) Validate() error { return nil }

// SessionRPCRequest is the params of session.identity and
// session.capabilities on the private session socket.
type SessionRPCRequest struct {
	V int `json:"v"`
}

func (r SessionRPCRequest) Validate() error {
	if r.V != SessionRPCVersion {
		return invalid("params must be {\"v\":1}")
	}
	return nil
}
