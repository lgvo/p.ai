package api

import "reflect"

// Request is a method's params type. Validate checks the request's own
// bounds; the daemon runs it on every request and clients run it before
// sending.
type Request interface{ Validate() error }

// Method binds a method's name to its request and result types. The daemon
// registers its handler with the same value that clients call it with, so a
// changed shape fails to compile at every caller.
type Method[Req Request, Res any] struct{ Name string }

// Capability names a part of the daemon that host configuration enables.
// system.hello reports the configured ones.
type Capability string

const (
	// CapabilityGit serves project reads, origins, publication and retained
	// branches.
	CapabilityGit Capability = "git"
	// CapabilityRuntime serves the lifecycle, operations and attachment.
	CapabilityRuntime Capability = "runtime"
	// CapabilityServices serves the project services methods.
	CapabilityServices Capability = "services"
)

// MethodInfo describes one method of the control protocol.
type MethodInfo struct {
	Name string
	// Capability is the capability that serves the method; empty for the
	// methods every daemon serves.
	Capability Capability
	// Connection marks a method the connection answers itself (rpc.cancel)
	// rather than the daemon's method table.
	Connection      bool
	Request, Result reflect.Type
}

func info[Req Request, Res any](m Method[Req, Res], capability Capability) MethodInfo {
	return MethodInfo{Name: m.Name, Capability: capability,
		Request: reflect.TypeFor[Req](), Result: reflect.TypeFor[Res]()}
}

// The control protocol, p.control/v2. The attachment helper's three methods
// are answered only for the helper (see docs/control-api.md).
var (
	SystemHello   = Method[HelloRequest, HelloResult]{"system.hello"}
	SystemHealth  = Method[HealthRequest, HealthResult]{"system.health"}
	SystemInspect = Method[SystemInspectRequest, SystemInspectResult]{"system.inspect"}
	RPCCancel     = Method[CancelRequest, CancelResult]{"rpc.cancel"}

	ProjectList              = Method[ProjectListRequest, ProjectListResult]{"project.list"}
	ProjectBranches          = Method[ProjectBranchesRequest, ProjectBranchesResult]{"project.branches"}
	OriginChange             = Method[OriginChangeRequest, OriginResult]{"origin.change"}
	OriginRefresh            = Method[OriginRefreshRequest, OriginResult]{"origin.refresh"}
	OriginInspect            = Method[OriginInspectRequest, OriginResult]{"origin.inspect"}
	OriginSources            = Method[OriginSourcesRequest, OriginSourcesResult]{"origin.sources"}
	OriginPublicationPreview = Method[OriginPublicationPreviewRequest, PublicationPreviewResult]{"origin.publication.preview"}
	OriginPublish            = Method[OriginPublishRequest, OriginPublishResult]{"origin.publish"}
	ProjectRetainedBranches  = Method[ProjectRetainedBranchesRequest, RetainedBranchesResult]{"project.retained_branches"}

	ProjectCreate                = Method[ProjectCreateRequest, OperationResult]{"project.create"}
	SessionCreate                = Method[SessionCreateRequest, OperationResult]{"session.create"}
	SessionInspect               = Method[SessionInspectRequest, SessionResult]{"session.inspect"}
	SessionList                  = Method[SessionListRequest, SessionListResult]{"session.list"}
	SessionStart                 = Method[SessionStartRequest, SessionResult]{"session.start"}
	SessionStop                  = Method[SessionStopRequest, SessionResult]{"session.stop"}
	OperationInspect             = Method[OperationInspectRequest, OperationResult]{"operation.inspect"}
	OperationList                = Method[OperationListRequest, OperationListResult]{"operation.list"}
	OperationWait                = Method[OperationWaitRequest, OperationResult]{"operation.wait"}
	OperationRetry               = Method[OperationRetryRequest, OperationResult]{"operation.retry"}
	SessionAttach                = Method[SessionAttachRequest, PendingAttachment]{"session.attach"}
	SessionRemovalPreview        = Method[SessionRemovalPreviewRequest, RemovalPreviewResult]{"session.removal.preview"}
	SessionDiscard               = Method[SessionDiscardRequest, OperationResult]{"session.discard"}
	SessionDelete                = Method[SessionDeleteRequest, OperationResult]{"session.delete"}
	SessionRename                = Method[SessionRenameRequest, OperationResult]{"session.rename"}
	ProjectRetainedRename        = Method[ProjectRetainedRenameRequest, OperationResult]{"project.retained.rename"}
	ProjectDeletePreview         = Method[ProjectDeletePreviewRequest, ProjectDeletePreviewResult]{"project.delete.preview"}
	ProjectDeleteConfirm         = Method[ProjectDeleteConfirmRequest, OperationResult]{"project.delete.confirm"}
	ProjectRetainedDeletePreview = Method[ProjectRetainedDeletePreviewRequest, RetainedDeletePreviewResult]{"project.retained.delete.preview"}
	ProjectRetainedDeleteConfirm = Method[ProjectRetainedDeleteConfirmRequest, OperationResult]{"project.retained.delete.confirm"}
	SessionCreateReplacePreview  = Method[SessionCreateReplacePreviewRequest, CreateReplacePreviewResult]{"session.create.replace.preview"}
	SessionCreateReplaceConfirm  = Method[SessionCreateReplaceConfirmRequest, OperationResult]{"session.create.replace.confirm"}
	SessionCreateCleanupPreview  = Method[SessionCreateCleanupPreviewRequest, CreateCleanupPreviewResult]{"session.create.cleanup.preview"}
	SessionCreateCleanupConfirm  = Method[SessionCreateCleanupConfirmRequest, OperationResult]{"session.create.cleanup.confirm"}
	AttachmentClaim              = Method[AttachmentClaimRequest, AttachmentLaunch]{"attachment.claim"}
	AttachmentConfirm            = Method[AttachmentConfirmRequest, AttachmentResult]{"attachment.confirm"}
	AttachmentPing               = Method[AttachmentPingRequest, AttachmentResult]{"attachment.ping"}

	SessionServices      = Method[SessionServicesRequest, SessionServicesResult]{"session.services"}
	SessionServiceAction = Method[SessionServiceActionRequest, SessionServiceActionResult]{"session.service.action"}
	SessionServiceLogs   = Method[SessionServiceLogsRequest, SessionServiceLogsResult]{"session.service.logs"}
)

// Methods lists every method of the control protocol: the system methods,
// then those of each capability.
func Methods() []MethodInfo {
	cancel := info(RPCCancel, "")
	cancel.Connection = true
	return []MethodInfo{
		info(SystemHello, ""),
		info(SystemHealth, ""),
		info(SystemInspect, ""),
		cancel,
		info(ProjectList, CapabilityGit),
		info(ProjectBranches, CapabilityGit),
		info(OriginChange, CapabilityGit),
		info(OriginRefresh, CapabilityGit),
		info(OriginInspect, CapabilityGit),
		info(OriginSources, CapabilityGit),
		info(OriginPublicationPreview, CapabilityGit),
		info(OriginPublish, CapabilityGit),
		info(ProjectRetainedBranches, CapabilityGit),
		info(ProjectCreate, CapabilityRuntime),
		info(SessionCreate, CapabilityRuntime),
		info(SessionInspect, CapabilityRuntime),
		info(SessionList, CapabilityRuntime),
		info(SessionStart, CapabilityRuntime),
		info(SessionStop, CapabilityRuntime),
		info(OperationInspect, CapabilityRuntime),
		info(OperationList, CapabilityRuntime),
		info(OperationWait, CapabilityRuntime),
		info(OperationRetry, CapabilityRuntime),
		info(SessionAttach, CapabilityRuntime),
		info(SessionRemovalPreview, CapabilityRuntime),
		info(SessionDiscard, CapabilityRuntime),
		info(SessionDelete, CapabilityRuntime),
		info(SessionRename, CapabilityRuntime),
		info(ProjectRetainedRename, CapabilityRuntime),
		info(ProjectDeletePreview, CapabilityRuntime),
		info(ProjectDeleteConfirm, CapabilityRuntime),
		info(ProjectRetainedDeletePreview, CapabilityRuntime),
		info(ProjectRetainedDeleteConfirm, CapabilityRuntime),
		info(SessionCreateReplacePreview, CapabilityRuntime),
		info(SessionCreateReplaceConfirm, CapabilityRuntime),
		info(SessionCreateCleanupPreview, CapabilityRuntime),
		info(SessionCreateCleanupConfirm, CapabilityRuntime),
		info(AttachmentClaim, CapabilityRuntime),
		info(AttachmentConfirm, CapabilityRuntime),
		info(AttachmentPing, CapabilityRuntime),
		info(SessionServices, CapabilityServices),
		info(SessionServiceAction, CapabilityServices),
		info(SessionServiceLogs, CapabilityServices),
	}
}

// The private session RPC, served on each session's own socket. It keeps its
// per-request version, SessionRPCVersion, until the plugin contract decides
// whether it starts with a hello.
var (
	SessionIdentity     = Method[SessionRPCRequest, SessionIdentityResult]{"session.identity"}
	SessionCapabilities = Method[SessionRPCRequest, SessionCapabilitiesResult]{"session.capabilities"}
	// StatusReportMethod is a notification: it has no result.
	StatusReportMethod = Method[StatusReport, NoResult]{"status.report"}
)

// SessionMethods lists every method of the private session RPC.
func SessionMethods() []MethodInfo {
	return []MethodInfo{info(SessionIdentity, ""), info(SessionCapabilities, ""), info(StatusReportMethod, "")}
}
