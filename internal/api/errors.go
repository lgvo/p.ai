package api

import (
	"strings"
	"unicode/utf8"
)

// ErrorKind names the client's next step after an error. It is the error
// object's message; docs/archive/control-api.md#errors lists each kind with its code
// and reasons.
type ErrorKind string

const (
	KindParseError           ErrorKind = "parse_error"
	KindInvalidRequest       ErrorKind = "invalid_request"
	KindMethodNotFound       ErrorKind = "method_not_found"
	KindInvalid              ErrorKind = "invalid"
	KindInternal             ErrorKind = "internal"
	KindCancelled            ErrorKind = "cancelled"
	KindUnsupportedVersion   ErrorKind = "unsupported_version"
	KindRetry                ErrorKind = "retry"
	KindConflict             ErrorKind = "conflict"
	KindCapacity             ErrorKind = "capacity"
	KindPreconditionFailed   ErrorKind = "precondition_failed"
	KindPreconditionRequired ErrorKind = "precondition_required"
	KindNotFound             ErrorKind = "not_found"
	KindNotConfigured        ErrorKind = "not_configured"
	KindFailed               ErrorKind = "failed"
)

var kindCodes = map[ErrorKind]int{
	KindParseError:           -32700,
	KindInvalidRequest:       -32600,
	KindMethodNotFound:       -32601,
	KindInvalid:              -32602,
	KindInternal:             -32603,
	KindCancelled:            -32001,
	KindUnsupportedVersion:   -32002,
	KindRetry:                -32010,
	KindConflict:             -32011,
	KindCapacity:             -32012,
	KindPreconditionFailed:   -32013,
	KindPreconditionRequired: -32014,
	KindNotFound:             -32015,
	KindNotConfigured:        -32016,
	KindFailed:               -32017,
}

// Kinds lists every error kind of the protocol.
func Kinds() []ErrorKind {
	return []ErrorKind{KindParseError, KindInvalidRequest, KindMethodNotFound, KindInvalid, KindInternal, KindCancelled,
		KindUnsupportedVersion, KindRetry, KindConflict, KindCapacity, KindPreconditionFailed, KindPreconditionRequired,
		KindNotFound, KindNotConfigured, KindFailed}
}

// Code is the kind's JSON-RPC error code; 0 for a name outside the protocol.
func (k ErrorKind) Code() int { return kindCodes[k] }

// Reasons in ErrorData. Error reasons and plan refusal codes share one
// namespace, so a client keeps one sentence per code. A reason is stable; a
// client that meets one it does not know uses its sentence for the kind.
const (
	// invalid_request
	ReasonHelloRequired = "hello_required" // a request came before a successful system.hello
	ReasonDuplicateID   = "duplicate_id"   // another request with the same id is in flight
	ReasonEnvelope      = "envelope"       // the line is JSON but not a valid request object
	ReasonConnection    = "connection"     // this connection may not call the method
	// unsupported_version
	ReasonProtocol = "protocol" // the hello named a protocol the daemon does not speak
	ReasonJSONRPC  = "jsonrpc"  // the request's jsonrpc member is not "2.0"
	// invalid
	ReasonParam     = "param"      // a param is missing, unknown or out of bounds; field names it when one does
	ReasonKeyReused = "key_reused" // the idempotency key is recorded for another request
	// internal
	ReasonPanic      = "panic"      // the daemon's code panicked
	ReasonStore      = "store"      // the control store failed
	ReasonEncoding   = "encoding"   // the result could not be encoded
	ReasonUnexpected = "unexpected" // a failure the daemon did not classify
	// operations
	ReasonOutcomeUnknown      = "outcome_unknown"       // an effect the operation issued may or may not have happened; P does not issue it again
	ReasonWorkspaceMismatch   = "workspace_mismatch"    // the session's workspace is not on its assigned branch or tracks another upstream
	ReasonInternal            = "internal"              // the operation's work failed in a way P did not classify, or panicked; the diagnostic holds the detail
	ReasonImageBuilderChanged = "image_builder_changed" // the host's image builder configuration differs from the one the creation recorded
	ReasonImageBuildFailed    = "image_build_failed"    // building the image of the session's source commit failed; the diagnostic says why
	// retry
	ReasonSessionHeld     = "session_held"      // another request or a running operation holds the session
	ReasonTooManyRequests = "too_many_requests" // the connection has 16 requests in flight, or any when it claims an attachment
	ReasonDeadline        = "deadline"          // the daemon's 30-second deadline passed
	ReasonRefsMoved       = "refs_moved"        // P's refs moved while the daemon read them
	ReasonProjectHeld     = "project_held"      // another request or a running operation holds the project
	// conflict
	ReasonAttachmentActive       = "attachment_active"        // a terminal is attached or its end is unproven, or an attachment is pending
	ReasonSessionNotEstablished  = "session_not_established"  // the session is being created, removed or was never created
	ReasonSessionNotStopped      = "session_not_stopped"      // the change requires a stopped session
	ReasonSessionNotRunning      = "session_not_running"      // the request requires a running session
	ReasonRuntimeMissing         = "runtime_missing"          // the session's container does not exist
	ReasonRuntimeUnsettled       = "runtime_unsettled"        // the session\'s container is neither running nor stopped
	ReasonRuntimePresent         = "runtime_present"          // a container of the session exists where the request expected none
	ReasonOperationBlocked       = "operation_blocked"        // a blocked operation holds the target and waits for Retry
	ReasonOperationSuperseded    = "operation_superseded"     // the operation was replaced and cannot run again
	ReasonOperationUnfinished    = "operation_unfinished"     // a branch guard holds the project
	ReasonOperationNotRetryable  = "operation_not_retryable"  // the operation's kind cannot be retried from its state
	ReasonPolicyInvalid          = "policy_invalid"           // the project's policy is not configured or no longer holds
	ReasonPrincipalMissing       = "principal_missing"        // the session's Git identity is missing or revoked
	ReasonBranchExists           = "branch_exists"            // the branch name is already taken
	ReasonBranchAssigned         = "branch_assigned"          // a session holds the branch
	ReasonBranchReserved         = "branch_reserved"          // a lifecycle operation reserves the branch
	ReasonProjectExists          = "project_exists"           // the project already exists
	ReasonProjectDeleting        = "project_deleting"         // the project is being deleted
	ReasonOriginNotSet           = "origin_not_set"           // the project has no origin
	ReasonCleanupActive          = "cleanup_active"           // a failed creation's cleanup holds the session
	ReasonBoundExceeded          = "bound_exceeded"           // the answer would exceed the response bound
	ReasonPublicEgressUnassigned = "public_egress_unassigned" // the session on the public network holds no address slot
	// Refusals in plans only: a risky change's plan names them when it is not
	// allowed (docs/archive/control-api.md#refusals). The conflict reasons above are
	// refusals in plans too.
	ReasonDestinationDivergent = "destination_divergent" // the origin branch has commits the source lacks
	// A blocked creation's replacement.
	ReasonUnsupportedReplacementChoice   = "unsupported_replacement_choice"   // the new request is not a replacement this creation allows
	ReasonOldCreationNotBlocked          = "old_creation_not_blocked"         // the creation is not blocked
	ReasonOldEffectNotProvenAbsent       = "old_effect_not_proven_absent"     // the creation reached a step whose effects P cannot prove absent
	ReasonCurrentPolicyUnavailable       = "current_policy_unavailable"       // the project's current policy is unavailable
	ReasonNewSourceOrBranchUnavailable   = "new_source_or_branch_unavailable" // the new source or branch could not be read, or changed while read
	ReasonTargetAssignmentUnavailable    = "target_assignment_unavailable"    // another session or an unfinished operation holds a branch the replacement needs
	ReasonRequestUnchanged               = "request_unchanged"                // the new request would create the same session again
	ReasonProvisionalResourceUnavailable = "provisional_resource_unavailable" // the creation's provisional resources are not as P left them, or are in use
	// A failed creation's cleanup.
	ReasonBlockedLocalCreationRequired     = "blocked_local_creation_required"                                        // the creation is not a blocked creation of this session
	ReasonAssemblyReadyCreatorRequired     = "blocked_stopped_standalone_base_image_assembly_ready_creator_required"  // the assembled creation is not the stopped base-image creator a cleanup removes
	ReasonActiveWorkspaceOrCleanupGuard    = "active_workspace_or_cleanup_guard"                                      // other work holds the session
	ReasonDispatchUnsettled                = "native_or_builder_dispatch_unsettled_or_publication_origin_unsupported" // a runtime or image build may have started, or the creation is from the origin
	ReasonCreationWorkerOrAttachmentActive = "creation_worker_or_attachment_active"                                   // the creation's worker or an attachment is active
	ReasonAssignedRefUnavailable           = "assigned_P_ref_unavailable"                                             // the session's branch is missing or could not be read
	ReasonAssignmentOrGuardUnavailable     = "assignment_or_lifecycle_guard_unavailable"                              // another session or an unfinished operation holds the branch
	ReasonLocalCredentialsUnverified       = "local_credentials_or_endpoints_unverified"                              // the session's key, Git identity or endpoints are not exactly as P made them
	ReasonExactStoppedRuntimeUnavailable   = "exact_stopped_runtime_unavailable"                                      // the creator's container is not the exact stopped one P made; the daemon's log has the detail
	ReasonNativeEffectsUnsettled           = "native_runtime_or_builder_present_ambiguous_or_unreachable"             // a container or builder of the creation exists, is ambiguous or could not be checked
	// capacity
	ReasonSessionCapacity    = "session_capacity"     // every session slot is taken
	ReasonAttachmentCapacity = "attachment_capacity"  // every pending attachment slot is taken
	ReasonPublicAddresses    = "public_address_slots" // every public egress address is assigned
	// precondition_failed
	ReasonExpired = "expired" // the change tag or the pending attachment expired
	ReasonChanged = "changed" // the state the client reviewed or expected changed
	// precondition_required
	ReasonIfMatchMissing = "if_match_missing" // a run call of a risky change sent a key but no change tag
	// not_found: the kind of record
	ReasonSession   = "session"
	ReasonOperation = "operation"
	ReasonProject   = "project"
	ReasonBranch    = "branch"
	ReasonSource    = "source"  // a source ref or commit to create from
	ReasonService   = "service" // a project service of the session
	// not_configured: the capability; failed: the authority
	ReasonGit           = "git"
	ReasonRuntime       = "runtime"
	ReasonServices      = "services"
	ReasonPublicEgress  = "public_egress"
	ReasonProjectPolicy = "project_policy" // the host configuration has no policy for the project
	ReasonOrigin        = "origin"
	ReasonPlugin        = "plugin"
)

// Error is a JSON-RPC error object. Message is the kind's name, an
// identifier rather than a sentence; Data holds the reason and the facts the
// next step needs. An error carries no text from the daemon's cause.
type Error struct {
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Data    *ErrorData `json:"data,omitempty"`
}

// ErrorData holds the facts a client needs to act on an error.
type ErrorData struct {
	Reason string `json:"reason,omitempty"`
	// Target is what the error is about, in target form (TargetSession and
	// the others below).
	Target string `json:"target,omitempty"`
	// Field is the request field at fault, for invalid.
	Field string `json:"field,omitempty"`
	// Supported lists the protocols the daemon speaks, for
	// unsupported_version.
	Supported []string `json:"supported,omitempty"`
}

// NewError is an error of kind with reason, or without data when reason is
// empty.
func NewError(kind ErrorKind, reason string) *Error {
	e := &Error{Code: kind.Code(), Message: string(kind)}
	if reason != "" {
		e.Data = &ErrorData{Reason: reason}
	}
	return e
}

// InvalidParam is the invalid error for a request field; field may be empty
// when no single field is at fault.
func InvalidParam(field string) *Error {
	return &Error{Code: KindInvalid.Code(), Message: string(KindInvalid), Data: &ErrorData{Reason: ReasonParam, Field: field}}
}

// Kind is the error's kind.
func (e *Error) Kind() ErrorKind { return ErrorKind(e.Message) }

// Reason is the error's reason code, or empty.
func (e *Error) Reason() string {
	if e.Data == nil {
		return ""
	}
	return e.Data.Reason
}

// Target is what the error is about, or empty.
func (e *Error) Target() string {
	if e.Data == nil {
		return ""
	}
	return e.Data.Target
}

// Error is the kind, reason and target as identifiers. Clients show their
// own sentence for them instead.
func (e *Error) Error() string {
	parts := []string{e.Message}
	if e.Data != nil {
		for _, p := range []string{e.Data.Reason, e.Data.Field, e.Data.Target} {
			if p != "" {
				parts = append(parts, p)
			}
		}
	}
	return strings.Join(parts, ": ")
}

// Targets name what an error or plan is about.
func TargetSession(uuid string) string         { return "session/" + uuid }
func TargetOperation(id string) string         { return "operation/" + id }
func TargetProject(path string) string         { return "project/" + path }
func TargetBranch(project, name string) string { return "project/" + project + ":branch/" + name }

// TargetOriginBranch names branch name of project's origin.
func TargetOriginBranch(project, name string) string {
	return "project/" + project + ":origin-branch/" + name
}

// MaxDiagnosticBytes bounds every diagnostic P records: an operation's, a
// session's start, and each resource's in project deletion evidence.
const MaxDiagnosticBytes = 1024

// BoundDiagnostic is d cut to at most MaxDiagnosticBytes at a UTF-8 character
// boundary. Every writer of a diagnostic calls it.
func BoundDiagnostic(d string) string {
	if len(d) <= MaxDiagnosticBytes {
		return d
	}
	cut := MaxDiagnosticBytes
	for cut > 0 && !utf8.RuneStart(d[cut]) {
		cut--
	}
	return d[:cut]
}
