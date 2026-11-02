package control

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/strictjson"
)

func CreationEnvironmentView(op api.Operation) *api.EnvironmentView {
	if op.Kind != "session.create" {
		return nil
	}
	ev, err := Evidence(op)
	if err != nil || ev.Environment == nil {
		return nil
	}
	v := &api.EnvironmentView{CommitOID: ev.CapturedOID, System: ev.Environment.System, BaseImageFingerprint: ev.Environment.BaseFingerprint,
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

func NewSessionView(s Session) api.SessionView {
	return api.SessionView{UUID: s.UUID, Project: s.Project, Branch: s.Branch, Registry: s.Registry, PolicySHA256: s.PolicySHA256}
}

func (s *Store) PopulateSessionStatus(ctx context.Context, view api.SessionView) (api.SessionView, error) {
	count, latest, err := s.SessionStatus(ctx, view.UUID)
	if err != nil {
		return api.SessionView{}, err
	}
	view.AttachedCount = count
	view.LatestUnattendedCondition = latest
	return view, nil
}

// The request ID can consume up to 768 encoded bytes (128 escaped bytes).
// Leave room for that ID and the JSON-RPC envelope around every list result.
const lifecyclePageResultCeiling = api.MaxFrameBytes - 1024

func boundedLifecyclePage[T any](field string, values []T, next string, cursor func(T) string) (map[string]any, *api.RPCError) {
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

func SummarizeOperation(o api.Operation) api.OperationSummary {
	d := o.Diagnostic
	limit := 256
	if o.Kind == "session.rename" {
		// Preserve both full validated branch/upstream names in mismatch
		// refusals. The durable Rename diagnostic has the same fixed bound.
		limit = 512
	}
	if len(d) > limit {
		d = d[:limit]
	}
	return api.OperationSummary{ID: o.ID, Key: o.Key, Kind: o.Kind, Project: o.Project, SessionUUID: o.SessionUUID, Status: o.Status, Phase: o.Phase, Committed: o.Committed, Diagnostic: d, Environment: CreationEnvironmentView(o)}
}

type LifecycleAPI interface {
	CreateProject(context.Context, BlankProjectRequest) (api.Operation, error)
	CreateSession(context.Context, ReserveSessionRequest) (api.Operation, error)
	Retry(context.Context, string) (api.Operation, error)
	InspectSession(context.Context, string) (api.SessionView, error)
	StartSession(context.Context, string) (api.SessionView, error)
	StopSession(context.Context, string) (api.SessionView, error)
}

type WorkspaceLossReadAPI interface {
	InspectWorkspaceLoss(context.Context, WorkspaceInspectRequest) (api.Operation, error)
}

type RenameAPI interface {
	RenameSession(context.Context, RenameRequest) (api.Operation, error)
}

type RetainedRenameAPI interface {
	RenameRetainedBranch(context.Context, RetainedRenameRequest) (api.Operation, error)
}

type RetainedDeleteAPI interface {
	PreviewRetainedDelete(context.Context, string, string) (RetainedDeletePreview, error)
	ConfirmRetainedDelete(context.Context, string, string, string, string) (api.Operation, error)
}

type CreateReplaceAPI interface {
	PreviewCreateReplace(context.Context, string, ReserveSessionRequest) (CreateReplacePreview, error)
	ConfirmCreateReplace(context.Context, string, string, string) (api.Operation, error)
}

// addLifecycleMethods adds the lifecycle methods in system.capabilities order.
// A method whose optional lifecycle API is absent stays dispatched and answers
// that the record is unavailable, without decoding its params.
func addLifecycleMethods(t *methodTable, store *Store, life LifecycleAPI) {
	createProject := func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
		var p struct {
			V       int    `json:"v"`
			Key     string `json:"key"`
			Project string `json:"project"`
			URL     string `json:"url"`
		}
		if strictjson.Decode(params, &p) != nil || p.V != 1 || p.Key == "" || !validProject(p.Project) || len(p.URL) > 2048 {
			return nil, errorRPC(-32602, "invalid_params", "project.create requires v=1, key and project")
		}
		op, e := life.CreateProject(ctx, BlankProjectRequest{Key: p.Key, Project: p.Project, URL: p.URL})
		if e != nil {
			return nil, lifecycleRPC(e)
		}
		return map[string]any{"v": 1, "operation": op}, nil
	}
	createSession := func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
		var p struct {
			V                 int    `json:"v"`
			Key               string `json:"key"`
			Project           string `json:"project"`
			Branch            string `json:"branch"`
			Choice            string `json:"choice"`
			Source            string `json:"source"`
			OriginRef         string `json:"origin_ref"`
			ExpectedCommitOID string `json:"expected_commit_oid"`
			ExpectedOriginURL string `json:"expected_origin_url"`
		}
		if strictjson.Decode(params, &p) != nil || p.V != 1 {
			return nil, errorRPC(-32602, "invalid_params", "invalid session.create request")
		}
		req := ReserveSessionRequest{Key: p.Key, Project: p.Project, Branch: p.Branch, Choice: p.Choice, Source: p.Source, OriginRef: p.OriginRef, ExpectedCommitOID: p.ExpectedCommitOID, ExpectedOriginURL: p.ExpectedOriginURL}
		if !ValidSessionCreateRequest(req) {
			return nil, errorRPC(-32602, "invalid_params", "invalid session.create request")
		}
		op, e := life.CreateSession(ctx, req)
		if e != nil {
			return nil, lifecycleRPC(e)
		}
		return map[string]any{"v": 1, "operation": op}, nil
	}
	sessionAction := func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
		var p struct {
			V    int    `json:"v"`
			UUID string `json:"uuid"`
		}
		if strictjson.Decode(params, &p) != nil || p.V != 1 || len(p.UUID) != 36 {
			return nil, errorRPC(-32602, "invalid_params", "session UUID required")
		}
		var view api.SessionView
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
	}
	listPage := func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
		var p struct {
			V     int    `json:"v"`
			Limit int    `json:"limit"`
			After string `json:"after"`
		}
		maximum := 20
		if method == "session.list" {
			maximum = 8
		}
		if strictjson.Decode(params, &p) != nil || p.V != 1 || p.Limit < 1 || p.Limit > maximum || len(p.After) > 128 {
			return nil, errorRPC(-32602, "invalid_params", "invalid lifecycle page limit")
		}
		if method == "operation.list" {
			ops, next, e := store.ListOperations(ctx, p.After, p.Limit)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			summaries := make([]api.OperationSummary, 0, len(ops))
			for _, op := range ops {
				summaries = append(summaries, SummarizeOperation(op))
			}
			return boundedLifecyclePage("operations", summaries, next, func(v api.OperationSummary) string { return v.ID })
		}
		sessions, next, e := store.ListSessions(ctx, p.After, p.Limit)
		if e != nil {
			return nil, lifecycleRPC(e)
		}
		views := make([]api.SessionView, 0, len(sessions))
		for _, s := range sessions {
			v, e := life.InspectSession(ctx, s.UUID)
			if e != nil {
				if errors.Is(e, ErrNotFound) {
					// A completed removal can disappear between the page's
					// snapshot and inspection. Missing authority on a surviving
					// record is still an error, never an empty observation.
					_, currentErr := store.GetSession(ctx, s.UUID)
					if errors.Is(currentErr, ErrNotFound) {
						continue
					}
					if currentErr != nil {
						return nil, lifecycleRPC(currentErr)
					}
				}
				return nil, lifecycleRPC(e)
			}
			v, e = store.PopulateSessionStatus(ctx, v)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			views = append(views, v)
		}
		return boundedLifecyclePage("sessions", views, next, func(v api.SessionView) string { return v.UUID })
	}
	operationAction := func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
		var p struct {
			V  int    `json:"v"`
			ID string `json:"id"`
		}
		if strictjson.Decode(params, &p) != nil || p.V != 1 || p.ID == "" || len(p.ID) > 128 {
			return nil, errorRPC(-32602, "invalid_params", "operation ID required")
		}
		var op api.Operation
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
	}
	t.add("project.create", createProject)
	t.add("session.create", createSession)
	t.add("session.inspect", sessionAction)
	t.add("session.list", listPage)
	t.add("session.start", sessionAction)
	t.add("session.stop", sessionAction)
	t.add("operation.inspect", operationAction)
	t.add("operation.list", listPage)
	t.add("operation.retry", operationAction)
	if service, ok := life.(ServiceAPI); ok {
		services := func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
			return serviceHandler(ctx, method, params, service)
		}
		t.add("session.services", services)
		t.add("session.service.journal", services)
		t.add("session.service.action", services)
	} else {
		t.unavailable("session.services", "session.service.journal", "session.service.action")
	}
	if attachment, ok := life.(AttachmentAPI); ok {
		attach := func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
			return attachmentHandler(ctx, method, params, attachment)
		}
		t.add("session.attach", attach)
		// The attachment helper's own methods are not advertised.
		t.addHidden("attachment.claim", attach)
		t.addHidden("attachment.confirm", attach)
		t.addHidden("attachment.ping", attach)
	} else {
		t.unavailable("session.attach", "attachment.claim", "attachment.confirm", "attachment.ping")
	}
	if workspace, ok := life.(WorkspaceLossReadAPI); ok {
		t.add("workspace.loss.inspect", func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
			var p struct {
				V    int    `json:"v"`
				Key  string `json:"key"`
				UUID string `json:"uuid"`
			}
			if strictjson.Decode(params, &p) != nil || p.V != 1 || len(p.Key) < 1 || len(p.Key) > 128 || !validUUID(p.UUID) {
				return nil, errorRPC(-32602, "invalid_params", "workspace.loss.inspect requires v=1, key and session UUID")
			}
			op, e := workspace.InspectWorkspaceLoss(ctx, WorkspaceInspectRequest{Key: p.Key, SessionUUID: p.UUID})
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		})
	} else {
		t.unavailable("workspace.loss.inspect")
	}
	if removal, ok := life.(RemovalPreviewAPI); ok {
		t.add("session.removal.preview", func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
			var p struct {
				V                         int    `json:"v"`
				UUID                      string `json:"uuid"`
				Kind                      string `json:"kind"`
				LossOperationID           string `json:"loss_operation_id"`
				AcknowledgeMissingRuntime bool   `json:"acknowledge_missing_runtime"`
			}
			if strictjson.Decode(params, &p) != nil || p.V != 1 || !validUUID(p.UUID) || p.Kind != "discard" && p.Kind != "delete" ||
				len(p.LossOperationID) > 128 || (p.LossOperationID == "") == !p.AcknowledgeMissingRuntime {
				return nil, errorRPC(-32602, "invalid_params", "invalid session.removal.preview request")
			}
			preview, e := removal.PreviewRemoval(ctx, RemovalPreviewRequest{UUID: p.UUID, Kind: p.Kind,
				LossOperationID: p.LossOperationID, AcknowledgeMissingRuntime: p.AcknowledgeMissingRuntime})
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "preview": preview}, nil
		})
	} else {
		t.unavailable("session.removal.preview")
	}
	if discard, ok := life.(DiscardAPI); ok {
		t.add("session.discard", func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
			var p struct {
				V     int    `json:"v"`
				Key   string `json:"key"`
				UUID  string `json:"uuid"`
				Token string `json:"confirmation_token"`
			}
			if strictjson.Decode(params, &p) != nil || p.V != 1 || len(p.Key) < 1 || len(p.Key) > 128 || !validUUID(p.UUID) || len(p.Token) != 32 || !validHexToken(p.Token) {
				return nil, errorRPC(-32602, "invalid_params", "invalid session.discard request")
			}
			op, e := discard.DiscardSession(ctx, DiscardConfirmRequest{Key: p.Key, UUID: p.UUID, ConfirmationToken: p.Token})
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		})
	} else {
		t.unavailable("session.discard")
	}
	if deletion, ok := life.(DeleteAPI); ok {
		t.add("session.delete", func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
			var p struct {
				V     int    `json:"v"`
				Key   string `json:"key"`
				UUID  string `json:"uuid"`
				Token string `json:"confirmation_token"`
			}
			if strictjson.Decode(params, &p) != nil || p.V != 1 || len(p.Key) < 1 || len(p.Key) > 128 || !validUUID(p.UUID) || !validHexToken(p.Token) {
				return nil, errorRPC(-32602, "invalid_params", "invalid session.delete request")
			}
			op, e := deletion.DeleteSession(ctx, DiscardConfirmRequest{Key: p.Key, UUID: p.UUID, ConfirmationToken: p.Token})
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		})
	} else {
		t.unavailable("session.delete")
	}
	if rename, ok := life.(RenameAPI); ok {
		t.add("session.rename", func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
			var p struct {
				V              int    `json:"v"`
				Key            string `json:"key"`
				UUID           string `json:"uuid"`
				NewBranch      string `json:"new_branch"`
				ExpectedOldTip string `json:"expected_old_tip"`
			}
			if strictjson.Decode(params, &p) != nil || p.V != 1 {
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
		})
	} else {
		t.unavailable("session.rename")
	}
	if rename, ok := life.(RetainedRenameAPI); ok {
		t.add("project.retained.rename", func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
			var p struct {
				V              int    `json:"v"`
				Key            string `json:"key"`
				Project        string `json:"project"`
				OldBranch      string `json:"old_branch"`
				NewBranch      string `json:"new_branch"`
				ExpectedOldTip string `json:"expected_old_tip"`
			}
			if strictjson.Decode(params, &p) != nil || p.V != 1 {
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
		})
	} else {
		t.unavailable("project.retained.rename")
	}
	if deletion, ok := life.(ProjectDeleteAPI); ok {
		t.add("project.delete.preview", func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
			var p struct {
				V                  int               `json:"v"`
				Project            string            `json:"project"`
				LossOperations     map[string]string `json:"loss_operations"`
				AcknowledgeMissing []string          `json:"acknowledge_missing,omitempty"`
			}
			if strictjson.Decode(params, &p) != nil || p.V != 1 || !validProject(p.Project) || len(p.LossOperations) > 4 || len(p.AcknowledgeMissing) > 4 {
				return nil, errorRPC(-32602, "invalid_params", "invalid project deletion preview")
			}
			for uuid, id := range p.LossOperations {
				if !validUUID(uuid) || !validUUID(id) {
					return nil, errorRPC(-32602, "invalid_params", "invalid project loss proof")
				}
			}
			for _, uuid := range p.AcknowledgeMissing {
				if !validUUID(uuid) {
					return nil, errorRPC(-32602, "invalid_params", "invalid missing runtime acknowledgement")
				}
			}
			preview, e := deletion.PreviewProjectDelete(ctx, ProjectDeletePreviewRequest{Project: p.Project, LossOperations: p.LossOperations, AcknowledgeMissing: p.AcknowledgeMissing})
			if e != nil {
				return nil, projectDeleteRPC(e)
			}
			return map[string]any{"v": 1, "preview": preview}, nil
		})
		t.add("project.delete.confirm", func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
			var p struct {
				V       int    `json:"v"`
				Key     string `json:"key"`
				Project string `json:"project"`
				Token   string `json:"confirmation_token"`
			}
			if strictjson.Decode(params, &p) != nil || p.V != 1 || !validProject(p.Project) || p.Key == "" || len(p.Key) > 128 || !validHexToken(p.Token) {
				return nil, errorRPC(-32602, "invalid_params", "invalid project deletion confirmation")
			}
			op, e := deletion.ConfirmProjectDelete(ctx, ProjectDeleteConfirmRequest{Key: p.Key, Project: p.Project, ConfirmationToken: p.Token})
			if e != nil {
				return nil, projectDeleteRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		})
	} else {
		t.unavailable("project.delete.preview", "project.delete.confirm")
	}
	if deletion, ok := life.(RetainedDeleteAPI); ok {
		t.add("project.retained.delete.preview", func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
			var p struct {
				V       int    `json:"v"`
				Project string `json:"project"`
				Branch  string `json:"branch"`
			}
			if strictjson.Decode(params, &p) != nil || p.V != 1 || !validProject(p.Project) || !validBranch(p.Branch) {
				return nil, errorRPC(-32602, "invalid_params", "invalid retained delete preview request")
			}
			preview, e := deletion.PreviewRetainedDelete(ctx, p.Project, p.Branch)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "preview": preview}, nil
		})
		t.add("project.retained.delete.confirm", func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
			var p struct {
				V       int    `json:"v"`
				Key     string `json:"key"`
				Project string `json:"project"`
				Branch  string `json:"branch"`
				Token   string `json:"confirmation_token"`
			}
			if strictjson.Decode(params, &p) != nil || p.V != 1 || len(p.Key) < 1 || len(p.Key) > 128 || !validProject(p.Project) || !validBranch(p.Branch) || !validHexToken(p.Token) {
				return nil, errorRPC(-32602, "invalid_params", "invalid retained delete confirmation request")
			}
			op, e := deletion.ConfirmRetainedDelete(ctx, p.Key, p.Project, p.Branch, p.Token)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		})
	} else {
		t.unavailable("project.retained.delete.preview", "project.retained.delete.confirm")
	}
	if replace, ok := life.(CreateReplaceAPI); ok {
		t.add("session.create.replace.preview", func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
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
			if strictjson.Decode(params, &p) != nil || p.V != 1 || !validUUID(p.OldUUID) {
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
		})
		t.add("session.create.replace.confirm", func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
			var p struct {
				V       int    `json:"v"`
				OldUUID string `json:"old_uuid"`
				Key     string `json:"key"`
				Token   string `json:"confirmation_token"`
			}
			if strictjson.Decode(params, &p) != nil || p.V != 1 || !validUUID(p.OldUUID) || p.Key == "" || len(p.Key) > 128 || !validHexToken(p.Token) || len(p.Token) != 32 {
				return nil, errorRPC(-32602, "invalid_params", "invalid create replacement confirmation")
			}
			op, e := replace.ConfirmCreateReplace(ctx, p.OldUUID, p.Key, p.Token)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		})
	} else {
		t.unavailable("session.create.replace.preview", "session.create.replace.confirm")
	}
	if cleanup, ok := life.(CreateCleanupAPI); ok {
		t.add("session.create.cleanup.preview", func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
			var p struct {
				V               int    `json:"v"`
				UUID            string `json:"uuid"`
				LossOperationID string `json:"loss_operation_id,omitempty"`
			}
			if strictjson.Decode(params, &p) != nil || p.V != 1 || !validUUID(p.UUID) || p.LossOperationID != "" && !validUUID(p.LossOperationID) {
				return nil, errorRPC(-32602, "invalid_params", "invalid failed creation cleanup preview")
			}
			var review CreateCleanupPreview
			var e error
			if p.LossOperationID != "" {
				assembled, supported := life.(AssembledCreateCleanupAPI)
				if !supported {
					return nil, lifecycleRPC(ErrNotFound)
				}
				review, e = assembled.PreviewAssembledCreateCleanup(ctx, p.UUID, p.LossOperationID)
			} else {
				review, e = cleanup.PreviewCreateCleanup(ctx, p.UUID)
			}
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "preview": review}, nil
		})
		t.add("session.create.cleanup.confirm", func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
			var p struct {
				V     int    `json:"v"`
				UUID  string `json:"uuid"`
				Key   string `json:"key"`
				Token string `json:"confirmation_token"`
			}
			if strictjson.Decode(params, &p) != nil || p.V != 1 || !validUUID(p.UUID) || p.Key == "" || len(p.Key) > 128 || !validHexToken(p.Token) || len(p.Token) != 32 {
				return nil, errorRPC(-32602, "invalid_params", "invalid failed creation cleanup confirmation")
			}
			op, e := cleanup.ConfirmCreateCleanup(ctx, p.UUID, p.Key, p.Token)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "operation": op}, nil
		})
	} else {
		t.unavailable("session.create.cleanup.preview", "session.create.cleanup.confirm")
	}
}

func lifecycleRPC(err error) *api.RPCError {
	var capacity *sessionCapacityError
	switch {
	case errors.Is(err, ErrInvalid):
		return errorRPC(-32602, "invalid_params", "invalid lifecycle request")
	case errors.As(err, &capacity):
		return errorRPC(-32003, "busy", capacity.Error())
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

func projectDeleteRPC(err error) *api.RPCError {
	if errors.Is(err, ErrConflict) {
		message := err.Error()
		if len(message) > 900 {
			message = message[:900]
		}
		return errorRPC(-32003, "busy", message)
	}
	if errors.Is(err, ErrInvalid) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return lifecycleRPC(err)
	}
	return errorRPC(-32004, "unavailable", "project deletion facts unavailable; Stop/detach sessions and obtain fresh loss inspections; finish pending cleanup, or investigate exact native/credential/cache ownership before a new review")
}
