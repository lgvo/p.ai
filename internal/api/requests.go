package api

import (
	"bytes"
	"encoding/json"
	"errors"
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
		return invalid("protocol")
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
		return invalid("id")
	}
	return nil
}

// CanonicalID returns the canonical form of a JSON-RPC request id: a string
// of at most 128 bytes or an integer.
func CanonicalID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", errors.New("missing id")
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
			return "", errors.New("id is too long")
		}
		encoded, _ := json.Marshal(v)
		return string(encoded), nil
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return "", errors.New("id must be an integer")
		}
		return strconv.FormatInt(n, 10), nil
	default:
		return "", errors.New("id must be a string or integer")
	}
}

type ProjectListRequest struct {
	After string `json:"after,omitempty"`
	Limit int    `json:"limit"`
}

func (r ProjectListRequest) Validate() error {
	switch {
	case r.Limit < 1 || r.Limit > 100:
		return invalid("limit")
	case r.After != "" && !ValidProject(r.After):
		return invalid("after")
	}
	return nil
}

type ProjectBranchesRequest struct {
	Project string `json:"project"`
	After   string `json:"after,omitempty"`
	Limit   int    `json:"limit"`
}

func (r ProjectBranchesRequest) Validate() error {
	return validBranchPage(r.Project, r.After, r.Limit)
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
	switch {
	case !validKey(r.Key):
		return invalid("key")
	case !ValidProject(r.Project):
		return invalid("project")
	case r.Kind != "remove" && r.Kind != "set":
		return invalid("kind")
	case len(r.ExpectedURL) > 2048:
		return invalid("expected_url")
	case len(r.URL) > 2048 || (r.Kind == "remove") != (r.URL == ""):
		return invalid("url")
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
		return invalid("project")
	}
	return nil
}

type OriginSourcesRequest struct {
	Project string `json:"project"`
	After   string `json:"after,omitempty"`
	Limit   int    `json:"limit"`
}

func (r OriginSourcesRequest) Validate() error {
	switch {
	case !ValidProject(r.Project):
		return invalid("project")
	case r.Limit < 1 || r.Limit > 16:
		return invalid("limit")
	case r.After != "" && !validOriginRef(r.After):
		return invalid("after")
	}
	return nil
}

// OriginPublishRequest publishes the P source, at its exact tip, to an
// origin branch: a dry run without Key and IfMatch, a run with both. Source
// is a session UUID or a retained branch name, according to Kind.
type OriginPublishRequest struct {
	ExpectedOriginURL string `json:"expected_origin_url"`
	Project           string `json:"project"`
	Kind              string `json:"kind"`
	Source            string `json:"source"`
	SourceOID         string `json:"source_oid"`
	DestinationRef    string `json:"destination_ref"`
	ChangeRequest
}

func (r OriginPublishRequest) Validate() error {
	if err := validPublication(r.ExpectedOriginURL, r.Project, r.Kind, r.Source, r.SourceOID, r.DestinationRef); err != nil {
		return err
	}
	return r.ChangeRequest.Validate()
}

func validPublication(url, project, kind, source, oid, destination string) error {
	switch {
	case url == "" || len(url) > 2048:
		return invalid("expected_origin_url")
	case !ValidProject(project):
		return invalid("project")
	case kind != "session" && kind != "retained":
		return invalid("kind")
	case !(kind == "session" && len(source) == 36 || kind == "retained" && ValidBranch(source)):
		return invalid("source")
	case !ValidOID(oid):
		return invalid("source_oid")
	case !strings.HasPrefix(destination, "refs/heads/") || !validOriginRef(destination):
		return invalid("destination_ref")
	}
	return nil
}

type ProjectRetainedBranchesRequest struct {
	Project string `json:"project"`
	After   string `json:"after,omitempty"`
	Limit   int    `json:"limit"`
}

func (r ProjectRetainedBranchesRequest) Validate() error {
	return validBranchPage(r.Project, r.After, r.Limit)
}

// ProjectCreateRequest creates a blank project, or one with an origin when
// URL is set.
type ProjectCreateRequest struct {
	Key     string `json:"key"`
	Project string `json:"project"`
	URL     string `json:"url,omitempty"`
}

func (r ProjectCreateRequest) Validate() error {
	switch {
	case r.Key == "":
		return invalid("key")
	case !ValidProject(r.Project):
		return invalid("project")
	case len(r.URL) > 2048:
		return invalid("url")
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
	switch {
	case !validKey(r.Key):
		return invalid("key")
	case !ValidProject(r.Project):
		return invalid("project")
	case !ValidBranch(r.Branch):
		return invalid("branch")
	case r.Choice != "existing" && r.Choice != "new":
		return invalid("choice")
	case len(r.ExpectedOriginURL) > 2048 || r.ExpectedOriginURL != "" && r.OriginRef == "":
		return invalid("expected_origin_url")
	case r.Choice == "existing" && r.Source != "", r.Choice == "new" && r.OriginRef == "" && (r.Source == "" || len(r.Source) > 255),
		r.Choice == "new" && r.OriginRef != "" && r.Source != "":
		return invalid("source")
	case r.Choice == "existing" && r.OriginRef != "", r.OriginRef != "" && !validOriginRef(r.OriginRef):
		return invalid("origin_ref")
	case r.OriginRef == "" && r.ExpectedCommitOID != "", r.OriginRef != "" && !ValidOID(r.ExpectedCommitOID):
		return invalid("expected_commit_oid")
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
// does not know as not_found.
func validSessionID(id string) error {
	if len(id) != 36 {
		return invalid("uuid")
	}
	return nil
}

type SessionListRequest struct {
	Limit int    `json:"limit"`
	After string `json:"after,omitempty"`
}

func (r SessionListRequest) Validate() error {
	return validPage(r.Limit, 8, r.After)
}

type OperationListRequest struct {
	Limit int    `json:"limit"`
	After string `json:"after,omitempty"`
}

func (r OperationListRequest) Validate() error {
	if err := validPage(r.Limit, 20, r.After); err != nil {
		return err
	}
	if !validOperationCursor(r.After) {
		return invalid("after")
	}
	return nil
}

// validOperationCursor reports whether after is a cursor operation.list
// returned as next, or empty for the first page. The cursor is opaque to
// clients; the daemon writes it as the acceptance sequence of the page's last
// operation.
func validOperationCursor(after string) bool {
	if after == "" {
		return true
	}
	if after[0] == '0' {
		return false
	}
	for _, c := range after {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(after) <= 19
}

// OperationWaitRequest waits for operation ID to leave running, for at most
// Timeout seconds.
type OperationWaitRequest struct {
	ID      string `json:"id"`
	Timeout int    `json:"timeout"`
}

// MaxWaitSeconds is the longest operation.wait: it leaves time to answer
// inside the server's 30-second request deadline.
const MaxWaitSeconds = 25

func (r OperationWaitRequest) Validate() error {
	if err := validOperationID(r.ID); err != nil {
		return err
	}
	if r.Timeout < 1 || r.Timeout > MaxWaitSeconds {
		return invalid("timeout")
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
		return invalid("id")
	}
	return nil
}

// SessionDiscardRequest discards session UUID: a dry run without Key and
// IfMatch, a run with both.
type SessionDiscardRequest struct {
	UUID string `json:"uuid"`
	ChangeRequest
}

func (r SessionDiscardRequest) Validate() error { return validRemoval(r.UUID, r.ChangeRequest) }

// SessionDeleteRequest deletes session UUID and its branch: a dry run without
// Key and IfMatch, a run with both.
type SessionDeleteRequest struct {
	UUID string `json:"uuid"`
	ChangeRequest
}

func (r SessionDeleteRequest) Validate() error { return validRemoval(r.UUID, r.ChangeRequest) }

func validRemoval(uuid string, change ChangeRequest) error {
	if !ValidUUID(uuid) {
		return invalid("uuid")
	}
	return change.Validate()
}

type SessionRenameRequest struct {
	Key            string `json:"key"`
	UUID           string `json:"uuid"`
	NewBranch      string `json:"new_branch"`
	ExpectedOldTip string `json:"expected_old_tip"`
}

func (r SessionRenameRequest) Validate() error {
	switch {
	case !validKey(r.Key):
		return invalid("key")
	case !ValidUUID(r.UUID):
		return invalid("uuid")
	case !ValidBranch(r.NewBranch):
		return invalid("new_branch")
	case !ValidOID(r.ExpectedOldTip):
		return invalid("expected_old_tip")
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
	switch {
	case !validKey(r.Key):
		return invalid("key")
	case !ValidProject(r.Project):
		return invalid("project")
	case !ValidBranch(r.OldBranch):
		return invalid("old_branch")
	case !ValidBranch(r.NewBranch) || r.OldBranch == r.NewBranch:
		return invalid("new_branch")
	case !ValidOID(r.ExpectedOldTip):
		return invalid("expected_old_tip")
	}
	return nil
}

// ProjectDeleteRequest deletes project Project and everything P keeps of
// it: a dry run without Key and IfMatch, a run with both.
type ProjectDeleteRequest struct {
	Project string `json:"project"`
	ChangeRequest
}

func (r ProjectDeleteRequest) Validate() error {
	if !ValidProject(r.Project) {
		return invalid("project")
	}
	return r.ChangeRequest.Validate()
}

// ProjectRetainedDeleteRequest deletes retained branch Branch of Project: a
// dry run without Key and IfMatch, a run with both.
type ProjectRetainedDeleteRequest struct {
	Project string `json:"project"`
	Branch  string `json:"branch"`
	ChangeRequest
}

func (r ProjectRetainedDeleteRequest) Validate() error {
	switch {
	case !ValidProject(r.Project):
		return invalid("project")
	case !ValidBranch(r.Branch):
		return invalid("branch")
	}
	return r.ChangeRequest.Validate()
}

// CreationChoices is what a session creation chooses, without its
// idempotency key: the project and branch, existing or new, and its source.
type CreationChoices struct {
	Project           string `json:"project"`
	Branch            string `json:"branch"`
	Choice            string `json:"choice"`
	Source            string `json:"source,omitempty"`
	OriginRef         string `json:"origin_ref,omitempty"`
	ExpectedCommitOID string `json:"expected_commit_oid,omitempty"`
}

// Create is the creation request of choices c under key.
func (c CreationChoices) Create(key string) SessionCreateRequest {
	return SessionCreateRequest{Key: key, Project: c.Project, Branch: c.Branch, Choice: c.Choice, Source: c.Source,
		OriginRef: c.OriginRef, ExpectedCommitOID: c.ExpectedCommitOID}
}

// SessionCreateReplaceRequest replaces the blocked creation OldUUID with a new
// creation of CreationChoices: a dry run without Key and IfMatch, a run with
// both. The run's Key is also the new creation's idempotency key. Origin
// requests are outside this path, so it takes no expected origin URL.
type SessionCreateReplaceRequest struct {
	OldUUID string `json:"old_uuid"`
	CreationChoices
	ChangeRequest
}

func (r SessionCreateReplaceRequest) Validate() error {
	if !ValidUUID(r.OldUUID) {
		return invalid("old_uuid")
	}
	// A dry run has no key; the choices' bounds are a creation's.
	if err := r.Create("k").Validate(); err != nil {
		return err
	}
	return r.ChangeRequest.Validate()
}

// SessionCreateCleanupRequest cleans up the failed creation of session UUID:
// a dry run without Key and IfMatch, a run with both.
type SessionCreateCleanupRequest struct {
	UUID string `json:"uuid"`
	ChangeRequest
}

func (r SessionCreateCleanupRequest) Validate() error {
	if !ValidUUID(r.UUID) {
		return invalid("uuid")
	}
	return r.ChangeRequest.Validate()
}

type SessionServicesRequest struct {
	UUID string `json:"uuid"`
}

func (r SessionServicesRequest) Validate() error { return validServiceSession(r.UUID) }

type SessionServiceActionRequest struct {
	UUID   string `json:"uuid"`
	Name   string `json:"name"`
	Action string `json:"action"`
}

func (r SessionServiceActionRequest) Validate() error {
	if r.Action != "start" && r.Action != "stop" && r.Action != "restart" {
		return invalid("action")
	}
	return validServiceName(r.UUID, r.Name)
}

type SessionServiceLogsRequest struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

func (r SessionServiceLogsRequest) Validate() error { return validServiceName(r.UUID, r.Name) }

func validServiceSession(uuid string) error {
	if !ValidUUID(uuid) {
		return invalid("uuid")
	}
	return nil
}

// validServiceName admits only a session-user p-project-*.service unit.
func validServiceName(uuid, name string) error {
	if !ValidProjectServiceUnit(name) {
		return invalid("name")
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
		return invalid("token")
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
		return invalid("operation")
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
		return invalid("v")
	}
	return nil
}
