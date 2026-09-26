package control

import (
	"context"
	"encoding/json"
	"errors"
)

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

func CreationEnvironmentView(op Operation) *EnvironmentView {
	if op.Kind != "session.create" {
		return nil
	}
	ev, err := Evidence(op)
	if err != nil || ev.Environment == nil {
		return nil
	}
	v := &EnvironmentView{CommitOID: ev.CapturedOID, System: ev.Environment.System, BaseImageFingerprint: ev.Environment.BaseFingerprint,
		Selection: "pending", Cache: "pending", ImageFingerprint: ev.ImageFingerprint}
	if s := ev.EnvironmentState; s != nil {
		if s.Key == "" {
			v.Selection, v.Cache = "base", "none"
		} else {
			v.Selection, v.Key = "devShells."+v.System+".default", s.Key
			if s.Fingerprint == "" {
				v.Cache = "publishing"
			} else if s.CacheHit {
				v.Cache = "hit"
			} else {
				v.Cache = "miss"
			}
		}
	}
	return v
}

func AcceptedRepairEnvironmentView(ev RepairEvidence) *EnvironmentView {
	if ev.Environment == nil || ev.EnvironmentSourceCommit == "" || ev.EnvironmentState == nil || ev.ImageFingerprint == "" {
		return nil
	}
	v := &EnvironmentView{CommitOID: ev.EnvironmentSourceCommit, System: ev.Environment.System,
		BaseImageFingerprint: ev.Environment.BaseFingerprint, ImageFingerprint: ev.ImageFingerprint,
		Selection: ev.EnvironmentSelection, Cache: "none"}
	if ev.EnvironmentKey != "" {
		v.Selection = "devShells." + v.System + ".default"
		v.Key = ev.EnvironmentKey
		v.Cache = "miss"
		if ev.EnvironmentState.CacheHit {
			v.Cache = "hit"
		}
	}
	return v
}

func NewSessionView(s Session) SessionView {
	return SessionView{UUID: s.UUID, Project: s.Project, Branch: s.Branch, Registry: s.Registry, PolicySHA256: s.PolicySHA256}
}

func (s *Store) PopulateSessionStatus(ctx context.Context, view SessionView) (SessionView, error) {
	count, latest, err := s.SessionStatus(ctx, view.UUID)
	if err != nil {
		return SessionView{}, err
	}
	view.AttachedCount = count
	view.LatestUnattendedCondition = latest
	return view, nil
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

// The request ID can consume up to 768 encoded bytes (128 escaped bytes).
// Leave room for that ID and the JSON-RPC envelope around every list result.
const lifecyclePageResultCeiling = MaxFrameBytes - 1024

func boundedLifecyclePage[T any](field string, values []T, next string, cursor func(T) string) (map[string]any, *RPCError) {
	for count := len(values); count >= 0; count-- {
		if count == 0 && len(values) != 0 {
			break
		}
		pageNext := next
		if count < len(values) {
			pageNext = cursor(values[count-1])
		}
		page := map[string]any{"v": 1, field: values[:count], "next": pageNext}
		encoded, err := json.Marshal(page)
		if err != nil {
			return nil, errorRPC(-32603, "internal", "lifecycle page cannot be encoded")
		}
		if len(encoded) <= lifecyclePageResultCeiling {
			return page, nil
		}
	}
	return nil, errorRPC(-32603, "internal", "single lifecycle record exceeds frame limit")
}

func SummarizeOperation(o Operation) OperationSummary {
	d := o.Diagnostic
	if len(d) > 256 {
		d = d[:256]
	}
	return OperationSummary{ID: o.ID, Key: o.Key, Kind: o.Kind, Project: o.Project, SessionUUID: o.SessionUUID, Status: o.Status, Phase: o.Phase, Committed: o.Committed, Diagnostic: d, Environment: CreationEnvironmentView(o)}
}

type LifecycleAPI interface {
	CreateProject(context.Context, BlankProjectRequest) (Operation, error)
	CreateSession(context.Context, ReserveSessionRequest) (Operation, error)
	Retry(context.Context, string) (Operation, error)
	InspectSession(context.Context, string) (SessionView, error)
	StartSession(context.Context, string) (SessionView, error)
	StopSession(context.Context, string) (SessionView, error)
}

type EnvironmentCacheAPI interface {
	ListEnvironmentCache(context.Context, string, string, int) ([]EnvironmentCacheItem, string, error)
	PreviewEnvironmentCollection(context.Context, string, string, string, string, int) (EnvironmentCollectionPreview, error)
	CollectEnvironmentCache(context.Context, string, string) (Operation, error)
}

type WorkspaceReadAPI interface {
	InspectWorkspace(context.Context, WorkspaceInspectRequest) (Operation, error)
}

type WorkspaceLossReadAPI interface {
	InspectWorkspaceLoss(context.Context, WorkspaceInspectRequest) (Operation, error)
}

type RenameAPI interface {
	RenameSession(context.Context, RenameRequest) (Operation, error)
}

type RetainedRenameAPI interface {
	RenameRetainedBranch(context.Context, RetainedRenameRequest) (Operation, error)
}

type RetainedDeleteAPI interface {
	PreviewRetainedDelete(context.Context, string, string) (RetainedDeletePreview, error)
	ConfirmRetainedDelete(context.Context, string, string, string, string) (Operation, error)
}

type RepairAPI interface {
	PreviewRepair(context.Context, string) (RepairPreview, error)
	ConfirmRepair(context.Context, RepairConfirmRequest) (Operation, error)
}

type RepairPreparationAPI interface {
	PrepareRepair(context.Context, string, string) (Operation, error)
}

type PreparedRepairPreviewAPI interface {
	PreviewPreparedRepair(context.Context, string, string) (RepairPreview, error)
}

type RefRepairAPI interface {
	PreviewRefRepair(context.Context, string, string) (RefRepairPreview, error)
	ConfirmRefRepair(context.Context, string, string, string) (Operation, error)
}

type PrincipalRepairAPI interface {
	PreviewPrincipalRepair(context.Context, string) (PrincipalRepairPreview, error)
	ConfirmPrincipalRepair(context.Context, string, string, string) (Operation, error)
}

type RecordRepairAPI interface {
	PreviewRecordRepair(context.Context, string) (RecordRepairPreview, error)
	ConfirmRecordRepair(context.Context, string, string, string) (Operation, error)
}

type CreateReplaceAPI interface {
	PreviewCreateReplace(context.Context, string, ReserveSessionRequest) (CreateReplacePreview, error)
	ConfirmCreateReplace(context.Context, string, string, string) (Operation, error)
}

func StateHandlerWithLifecycle(store *Store, git GitReader, info *GitInfo, life LifecycleAPI) Handler {
	base := StateHandlerWithGit(store, git, info)
	return func(ctx context.Context, method string, params json.RawMessage) (any, *RPCError) {
		if method == "session.attach" || method == "attachment.claim" || method == "attachment.confirm" || method == "attachment.ping" {
			if attachment, ok := life.(AttachmentAPI); ok {
				return attachmentHandler(ctx, method, params, attachment)
			}
			return nil, lifecycleRPC(ErrNotFound)
		}
		switch method {
		case "system.capabilities":
			result, err := base(ctx, method, params)
			if err != nil {
				return result, err
			}
			object := result.(map[string]any)
			available := object["available"].([]string)
			object["available"] = append(available, "project.create", "session.create", "session.inspect", "session.list", "session.start", "session.stop", "operation.inspect", "operation.list", "operation.retry")
			if _, ok := life.(AttachmentAPI); ok {
				object["available"] = append(object["available"].([]string), "session.attach")
			}
			if _, ok := life.(EnvironmentCacheAPI); ok {
				object["available"] = append(object["available"].([]string), "environment.cache.list", "environment.cache.preview", "environment.cache.collect")
			}
			if _, ok := life.(WorkspaceReadAPI); ok {
				object["available"] = append(object["available"].([]string), "workspace.inspect")
			}
			if _, ok := life.(WorkspaceLossReadAPI); ok {
				object["available"] = append(object["available"].([]string), "workspace.loss.inspect")
			}
			if _, ok := life.(RemovalPreviewAPI); ok {
				object["available"] = append(object["available"].([]string), "session.removal.preview")
			}
			if _, ok := life.(DiscardAPI); ok {
				object["available"] = append(object["available"].([]string), "session.discard")
			}
			if _, ok := life.(DeleteAPI); ok {
				object["available"] = append(object["available"].([]string), "session.delete")
			}
			if _, ok := life.(RenameAPI); ok {
				object["available"] = append(object["available"].([]string), "session.rename")
			}
			if _, ok := life.(RetainedRenameAPI); ok {
				object["available"] = append(object["available"].([]string), "project.retained.rename")
			}
			if _, ok := life.(RetainedDeleteAPI); ok {
				object["available"] = append(object["available"].([]string), "project.retained.delete.preview", "project.retained.delete.confirm")
			}
			if _, ok := life.(RepairAPI); ok {
				object["available"] = append(object["available"].([]string), "session.repair.preview", "session.repair.confirm")
			}
			if _, ok := life.(RepairPreparationAPI); ok {
				object["available"] = append(object["available"].([]string), "session.repair.prepare")
			}
			if _, ok := life.(RefRepairAPI); ok {
				object["available"] = append(object["available"].([]string), "session.ref.repair.preview", "session.ref.repair.confirm")
			}
			if _, ok := life.(PrincipalRepairAPI); ok {
				object["available"] = append(object["available"].([]string), "session.principal.repair.preview", "session.principal.repair.confirm")
			}
			if _, ok := life.(RecordRepairAPI); ok {
				object["available"] = append(object["available"].([]string), "session.record.repair.preview", "session.record.repair.confirm")
			}
			if _, ok := life.(CreateReplaceAPI); ok {
				object["available"] = append(object["available"].([]string), "session.create.replace.preview", "session.create.replace.confirm")
			}
			object["lifecycle"] = "partial"
			return object, nil
		case "project.create":
			var p struct {
				V       int    `json:"v"`
				Key     string `json:"key"`
				Project string `json:"project"`
				URL     string `json:"url"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || p.Key == "" || !validProject(p.Project) || len(p.URL) > 2048 {
				return nil, errorRPC(-32602, "invalid_params", "project.create requires v=1, key and project")
			}
			op, e := life.CreateProject(ctx, BlankProjectRequest{Key: p.Key, Project: p.Project, URL: p.URL})
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		case "session.create":
			var p struct {
				V                 int    `json:"v"`
				Key               string `json:"key"`
				Project           string `json:"project"`
				Branch            string `json:"branch"`
				Choice            string `json:"choice"`
				Source            string `json:"source"`
				OriginRef         string `json:"origin_ref"`
				ExpectedCommitOID string `json:"expected_commit_oid"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 {
				return nil, errorRPC(-32602, "invalid_params", "invalid session.create request")
			}
			req := ReserveSessionRequest{Key: p.Key, Project: p.Project, Branch: p.Branch, Choice: p.Choice, Source: p.Source, OriginRef: p.OriginRef, ExpectedCommitOID: p.ExpectedCommitOID}
			if !ValidSessionCreateRequest(req) {
				return nil, errorRPC(-32602, "invalid_params", "invalid session.create request")
			}
			op, e := life.CreateSession(ctx, req)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		case "session.create.replace.preview":
			replace, ok := life.(CreateReplaceAPI)
			if !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			var p struct {
				V                 int    `json:"v"`
				OldUUID           string `json:"old_uuid"`
				Key               string `json:"key"`
				Project           string `json:"project"`
				Branch            string `json:"branch"`
				Choice            string `json:"choice"`
				Source            string `json:"source"`
				OriginRef         string `json:"origin_ref"`
				ExpectedCommitOID string `json:"expected_commit_oid"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || !validUUID(p.OldUUID) {
				return nil, errorRPC(-32602, "invalid_params", "invalid create replacement preview")
			}
			req := ReserveSessionRequest{Key: p.Key, Project: p.Project, Branch: p.Branch, Choice: p.Choice, Source: p.Source, OriginRef: p.OriginRef, ExpectedCommitOID: p.ExpectedCommitOID}
			if !ValidSessionCreateRequest(req) {
				return nil, errorRPC(-32602, "invalid_params", "invalid create replacement request")
			}
			preview, e := replace.PreviewCreateReplace(ctx, p.OldUUID, req)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "preview": preview}, nil
		case "session.create.replace.confirm":
			replace, ok := life.(CreateReplaceAPI)
			if !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			var p struct {
				V       int    `json:"v"`
				OldUUID string `json:"old_uuid"`
				Key     string `json:"key"`
				Token   string `json:"confirmation_token"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || !validUUID(p.OldUUID) || p.Key == "" || len(p.Key) > 128 || !validHexToken(p.Token) || len(p.Token) != 32 {
				return nil, errorRPC(-32602, "invalid_params", "invalid create replacement confirmation")
			}
			op, e := replace.ConfirmCreateReplace(ctx, p.OldUUID, p.Key, p.Token)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		case "workspace.inspect":
			workspace, ok := life.(WorkspaceReadAPI)
			if !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			var p struct {
				V    int    `json:"v"`
				Key  string `json:"key"`
				UUID string `json:"uuid"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || len(p.Key) < 1 || len(p.Key) > 128 || !validUUID(p.UUID) {
				return nil, errorRPC(-32602, "invalid_params", "workspace.inspect requires v=1, key and session UUID")
			}
			op, e := workspace.InspectWorkspace(ctx, WorkspaceInspectRequest{Key: p.Key, SessionUUID: p.UUID})
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		case "workspace.loss.inspect":
			workspace, ok := life.(WorkspaceLossReadAPI)
			if !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			var p struct {
				V    int    `json:"v"`
				Key  string `json:"key"`
				UUID string `json:"uuid"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || len(p.Key) < 1 || len(p.Key) > 128 || !validUUID(p.UUID) {
				return nil, errorRPC(-32602, "invalid_params", "workspace.loss.inspect requires v=1, key and session UUID")
			}
			op, e := workspace.InspectWorkspaceLoss(ctx, WorkspaceInspectRequest{Key: p.Key, SessionUUID: p.UUID})
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		case "session.removal.preview":
			removal, ok := life.(RemovalPreviewAPI)
			if !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			var p struct {
				V                         int    `json:"v"`
				UUID                      string `json:"uuid"`
				Kind                      string `json:"kind"`
				LossOperationID           string `json:"loss_operation_id"`
				AcknowledgeMissingRuntime bool   `json:"acknowledge_missing_runtime"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || !validUUID(p.UUID) || p.Kind != "discard" && p.Kind != "delete" ||
				len(p.LossOperationID) > 128 || (p.LossOperationID == "") == !p.AcknowledgeMissingRuntime {
				return nil, errorRPC(-32602, "invalid_params", "invalid session.removal.preview request")
			}
			preview, e := removal.PreviewRemoval(ctx, RemovalPreviewRequest{UUID: p.UUID, Kind: p.Kind,
				LossOperationID: p.LossOperationID, AcknowledgeMissingRuntime: p.AcknowledgeMissingRuntime})
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "preview": preview}, nil
		case "session.discard":
			discard, ok := life.(DiscardAPI)
			if !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			var p struct {
				V     int    `json:"v"`
				Key   string `json:"key"`
				UUID  string `json:"uuid"`
				Token string `json:"confirmation_token"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || len(p.Key) < 1 || len(p.Key) > 128 || !validUUID(p.UUID) || len(p.Token) != 32 || !validHexToken(p.Token) {
				return nil, errorRPC(-32602, "invalid_params", "invalid session.discard request")
			}
			op, e := discard.DiscardSession(ctx, DiscardConfirmRequest{Key: p.Key, UUID: p.UUID, ConfirmationToken: p.Token})
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		case "session.delete":
			deletion, ok := life.(DeleteAPI)
			if !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			var p struct {
				V     int    `json:"v"`
				Key   string `json:"key"`
				UUID  string `json:"uuid"`
				Token string `json:"confirmation_token"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || len(p.Key) < 1 || len(p.Key) > 128 || !validUUID(p.UUID) || !validHexToken(p.Token) {
				return nil, errorRPC(-32602, "invalid_params", "invalid session.delete request")
			}
			op, e := deletion.DeleteSession(ctx, DiscardConfirmRequest{Key: p.Key, UUID: p.UUID, ConfirmationToken: p.Token})
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		case "session.rename":
			rename, ok := life.(RenameAPI)
			if !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			var p struct {
				V              int    `json:"v"`
				Key            string `json:"key"`
				UUID           string `json:"uuid"`
				NewBranch      string `json:"new_branch"`
				ExpectedOldTip string `json:"expected_old_tip"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 {
				return nil, errorRPC(-32602, "invalid_params", "invalid session.rename request")
			}
			req := RenameRequest{Key: p.Key, UUID: p.UUID, NewBranch: p.NewBranch, ExpectedOldTip: p.ExpectedOldTip}
			if !ValidRenameRequest(req) {
				return nil, errorRPC(-32602, "invalid_params", "invalid session.rename request")
			}
			op, e := rename.RenameSession(ctx, req)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		case "project.retained.rename":
			rename, ok := life.(RetainedRenameAPI)
			if !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			var p struct {
				V              int    `json:"v"`
				Key            string `json:"key"`
				Project        string `json:"project"`
				OldBranch      string `json:"old_branch"`
				NewBranch      string `json:"new_branch"`
				ExpectedOldTip string `json:"expected_old_tip"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 {
				return nil, errorRPC(-32602, "invalid_params", "invalid project.retained.rename request")
			}
			req := RetainedRenameRequest{Key: p.Key, Project: p.Project, OldBranch: p.OldBranch, NewBranch: p.NewBranch, ExpectedOldTip: p.ExpectedOldTip}
			if !ValidRetainedRenameRequest(req) {
				return nil, errorRPC(-32602, "invalid_params", "invalid project.retained.rename request")
			}
			op, e := rename.RenameRetainedBranch(ctx, req)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		case "project.retained.delete.preview":
			deletion, ok := life.(RetainedDeleteAPI)
			if !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			var p struct {
				V       int    `json:"v"`
				Project string `json:"project"`
				Branch  string `json:"branch"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || !validProject(p.Project) || !validBranch(p.Branch) {
				return nil, errorRPC(-32602, "invalid_params", "invalid retained delete preview request")
			}
			preview, e := deletion.PreviewRetainedDelete(ctx, p.Project, p.Branch)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "preview": preview}, nil
		case "project.retained.delete.confirm":
			deletion, ok := life.(RetainedDeleteAPI)
			if !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			var p struct {
				V       int    `json:"v"`
				Key     string `json:"key"`
				Project string `json:"project"`
				Branch  string `json:"branch"`
				Token   string `json:"confirmation_token"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || len(p.Key) < 1 || len(p.Key) > 128 || !validProject(p.Project) || !validBranch(p.Branch) || !validHexToken(p.Token) {
				return nil, errorRPC(-32602, "invalid_params", "invalid retained delete confirmation request")
			}
			op, e := deletion.ConfirmRetainedDelete(ctx, p.Key, p.Project, p.Branch, p.Token)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		case "session.principal.repair.preview":
			repair, ok := life.(PrincipalRepairAPI)
			if !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			var p struct {
				V    int    `json:"v"`
				UUID string `json:"uuid"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || !validUUID(p.UUID) {
				return nil, errorRPC(-32602, "invalid_params", "invalid principal repair preview")
			}
			preview, e := repair.PreviewPrincipalRepair(ctx, p.UUID)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "preview": preview}, nil
		case "session.principal.repair.confirm":
			repair, ok := life.(PrincipalRepairAPI)
			if !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			var p struct {
				V     int    `json:"v"`
				Key   string `json:"key"`
				UUID  string `json:"uuid"`
				Token string `json:"confirmation_token"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || p.Key == "" || len(p.Key) > 128 ||
				!validUUID(p.UUID) || len(p.Token) != 32 || !validHexToken(p.Token) {
				return nil, errorRPC(-32602, "invalid_params", "invalid principal repair confirmation")
			}
			op, e := repair.ConfirmPrincipalRepair(ctx, p.Key, p.UUID, p.Token)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		case "session.record.repair.preview":
			repair, ok := life.(RecordRepairAPI)
			if !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			var p struct {
				V    int    `json:"v"`
				UUID string `json:"uuid"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || !validUUID(p.UUID) {
				return nil, errorRPC(-32602, "invalid_params", "invalid record repair preview")
			}
			preview, e := repair.PreviewRecordRepair(ctx, p.UUID)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "preview": preview}, nil
		case "session.record.repair.confirm":
			repair, ok := life.(RecordRepairAPI)
			if !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			var p struct {
				V     int    `json:"v"`
				Key   string `json:"key"`
				UUID  string `json:"uuid"`
				Token string `json:"confirmation_token"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || p.Key == "" || len(p.Key) > 128 ||
				!validUUID(p.UUID) || len(p.Token) != 32 || !validHexToken(p.Token) {
				return nil, errorRPC(-32602, "invalid_params", "invalid record repair confirmation")
			}
			op, e := repair.ConfirmRecordRepair(ctx, p.Key, p.UUID, p.Token)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		case "session.ref.repair.preview":
			repair, ok := life.(RefRepairAPI)
			if !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			var p struct {
				V               int    `json:"v"`
				UUID            string `json:"uuid"`
				LossOperationID string `json:"loss_operation_id"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || !validUUID(p.UUID) || !validUUID(p.LossOperationID) {
				return nil, errorRPC(-32602, "invalid_params", "invalid assigned-ref repair preview")
			}
			preview, e := repair.PreviewRefRepair(ctx, p.UUID, p.LossOperationID)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "preview": preview}, nil
		case "session.ref.repair.confirm":
			repair, ok := life.(RefRepairAPI)
			if !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			var p struct {
				V     int    `json:"v"`
				Key   string `json:"key"`
				UUID  string `json:"uuid"`
				Token string `json:"confirmation_token"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || len(p.Key) == 0 || len(p.Key) > 128 ||
				!validUUID(p.UUID) || !validHexToken(p.Token) || len(p.Token) != 32 {
				return nil, errorRPC(-32602, "invalid_params", "invalid assigned-ref repair confirmation")
			}
			op, e := repair.ConfirmRefRepair(ctx, p.Key, p.UUID, p.Token)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		case "session.repair.preview":
			repair, ok := life.(RepairAPI)
			if !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			var p struct {
				V                      int    `json:"v"`
				UUID                   string `json:"uuid"`
				PreparationOperationID string `json:"preparation_operation_id,omitempty"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || !validUUID(p.UUID) || p.PreparationOperationID != "" && !validUUID(p.PreparationOperationID) {
				return nil, errorRPC(-32602, "invalid_params", "invalid repair preview request")
			}
			var preview RepairPreview
			var e error
			if p.PreparationOperationID == "" {
				preview, e = repair.PreviewRepair(ctx, p.UUID)
			} else if prepared, ok := life.(PreparedRepairPreviewAPI); ok {
				preview, e = prepared.PreviewPreparedRepair(ctx, p.UUID, p.PreparationOperationID)
			} else {
				return nil, lifecycleRPC(ErrNotFound)
			}
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "preview": preview}, nil
		case "session.repair.prepare":
			prepare, ok := life.(RepairPreparationAPI)
			if !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			var p struct {
				V    int    `json:"v"`
				Key  string `json:"key"`
				UUID string `json:"uuid"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || len(p.Key) == 0 || len(p.Key) > 128 || !validUUID(p.UUID) {
				return nil, errorRPC(-32602, "invalid_params", "invalid repair preparation")
			}
			op, e := prepare.PrepareRepair(ctx, p.Key, p.UUID)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		case "session.repair.confirm":
			repair, ok := life.(RepairAPI)
			if !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			var p struct {
				V     int    `json:"v"`
				Key   string `json:"key"`
				UUID  string `json:"uuid"`
				Token string `json:"confirmation_token"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || len(p.Key) < 1 || len(p.Key) > 128 || !validUUID(p.UUID) || len(p.Token) != 32 || !validHexToken(p.Token) {
				return nil, errorRPC(-32602, "invalid_params", "invalid repair confirmation")
			}
			op, e := repair.ConfirmRepair(ctx, RepairConfirmRequest{Key: p.Key, UUID: p.UUID, ConfirmationToken: p.Token})
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		case "operation.inspect", "operation.retry":
			var p struct {
				V  int    `json:"v"`
				ID string `json:"id"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || p.ID == "" || len(p.ID) > 128 {
				return nil, errorRPC(-32602, "invalid_params", "operation ID required")
			}
			var op Operation
			var e error
			if method == "operation.retry" {
				op, e = life.Retry(ctx, p.ID)
			} else {
				op, e = store.GetOperation(ctx, p.ID)
			}
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		case "operation.list", "session.list":
			var p struct {
				V     int    `json:"v"`
				Limit int    `json:"limit"`
				After string `json:"after"`
			}
			maximum := 20
			if method == "session.list" {
				maximum = 8
			}
			if strictDecode(params, &p) != nil || p.V != 1 || p.Limit < 1 || p.Limit > maximum || len(p.After) > 128 {
				return nil, errorRPC(-32602, "invalid_params", "invalid lifecycle page limit")
			}
			if method == "operation.list" {
				ops, next, e := store.ListOperations(ctx, p.After, p.Limit)
				if e != nil {
					return nil, lifecycleRPC(e)
				}
				summaries := make([]OperationSummary, 0, len(ops))
				for _, op := range ops {
					summaries = append(summaries, SummarizeOperation(op))
				}
				return boundedLifecyclePage("operations", summaries, next, func(v OperationSummary) string { return v.ID })
			}
			sessions, next, e := store.ListSessions(ctx, p.After, p.Limit)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			views := make([]SessionView, 0, len(sessions))
			for _, s := range sessions {
				v, e := life.InspectSession(ctx, s.UUID)
				if e != nil {
					return nil, lifecycleRPC(e)
				}
				v, e = store.PopulateSessionStatus(ctx, v)
				if e != nil {
					return nil, lifecycleRPC(e)
				}
				views = append(views, v)
			}
			return boundedLifecyclePage("sessions", views, next, func(v SessionView) string { return v.UUID })
		case "session.inspect", "session.start", "session.stop":
			var p struct {
				V    int    `json:"v"`
				UUID string `json:"uuid"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || len(p.UUID) != 36 {
				return nil, errorRPC(-32602, "invalid_params", "session UUID required")
			}
			var view SessionView
			var e error
			switch method {
			case "session.inspect":
				view, e = life.InspectSession(ctx, p.UUID)
			case "session.start":
				view, e = life.StartSession(ctx, p.UUID)
			case "session.stop":
				view, e = life.StopSession(ctx, p.UUID)
			}
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			view, e = store.PopulateSessionStatus(ctx, view)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "session": view}, nil
		case "environment.cache.list", "environment.cache.preview", "environment.cache.collect":
			cache, ok := life.(EnvironmentCacheAPI)
			if !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			switch method {
			case "environment.cache.list":
				var p struct {
					V       int    `json:"v"`
					Project string `json:"project"`
					After   string `json:"after"`
					Limit   int    `json:"limit"`
				}
				if strictDecode(params, &p) != nil || p.V != 1 || !validProject(p.Project) || p.Limit < 1 || p.Limit > 8 {
					return nil, errorRPC(-32602, "invalid_params", "invalid environment cache page")
				}
				items, next, err := cache.ListEnvironmentCache(ctx, p.Project, p.After, p.Limit)
				if err != nil {
					return nil, lifecycleRPC(err)
				}
				return boundedLifecyclePage("images", items, next, func(v EnvironmentCacheItem) string { return v.Key })
			case "environment.cache.preview":
				var p struct {
					V       int    `json:"v"`
					Project string `json:"project"`
					Key     string `json:"environment_key"`
					Token   string `json:"confirmation_token"`
					After   string `json:"after"`
					Limit   int    `json:"limit"`
				}
				if strictDecode(params, &p) != nil || p.V != 1 || !validProject(p.Project) || !validFingerprint(p.Key) ||
					len(p.Token) > 64 || len(p.After) > 64 || p.Limit < 1 || p.Limit > 20 {
					return nil, errorRPC(-32602, "invalid_params", "invalid environment collection preview")
				}
				preview, err := cache.PreviewEnvironmentCollection(ctx, p.Project, p.Key, p.Token, p.After, p.Limit)
				if err != nil {
					return nil, lifecycleRPC(err)
				}
				return map[string]any{"v": 1, "preview": preview}, nil
			default:
				var p struct {
					V     int    `json:"v"`
					Key   string `json:"key"`
					Token string `json:"confirmation_token"`
				}
				if strictDecode(params, &p) != nil || p.V != 1 || p.Key == "" || len(p.Key) > 128 || len(p.Token) != 32 {
					return nil, errorRPC(-32602, "invalid_params", "invalid environment collection confirmation")
				}
				op, err := cache.CollectEnvironmentCache(ctx, p.Key, p.Token)
				if err != nil {
					return nil, lifecycleRPC(err)
				}
				return map[string]any{"v": 1, "operation": op}, nil
			}
		default:
			return base(ctx, method, params)
		}
	}
}

func lifecycleRPC(err error) *RPCError {
	switch {
	case errors.Is(err, ErrInvalid):
		return errorRPC(-32602, "invalid_params", "invalid lifecycle request")
	case errors.Is(err, ErrConflict):
		return errorRPC(-32003, "busy", "lifecycle request conflicts with current authority")
	case errors.Is(err, ErrNotFound):
		return errorRPC(-32004, "unavailable", "record was not found")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return errorRPC(-32001, "cancelled", "request cancelled or timed out")
	default:
		return errorRPC(-32004, "unavailable", "lifecycle authority is unavailable")
	}
}
