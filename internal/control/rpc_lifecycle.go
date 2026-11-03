package control

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lgvo/p.ai/internal/api"
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

// boundedPage shortens values until page(values, next) encodes within
// lifecyclePageResultCeiling, continuing at the last value it keeps.
func boundedPage[T, R any](values []T, next string, cursor func(T) string, page func([]T, string) R) (R, error) {
	for count := len(values); count >= 0; count-- {
		if count == 0 && len(values) != 0 {
			break
		}
		pageNext := next
		if count < len(values) {
			pageNext = cursor(values[count-1])
		}
		result := page(values[:count], pageNext)
		encoded, err := json.Marshal(result)
		if err != nil {
			var zero R
			return zero, Internal(api.ReasonEncoding, err)
		}
		if len(encoded) <= lifecyclePageResultCeiling {
			return result, nil
		}
	}
	var zero R
	return zero, Internal(api.ReasonEncoding, errors.New("single lifecycle record exceeds frame limit"))
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
	return api.OperationSummary{ID: o.ID, Key: o.Key, Kind: o.Kind, Project: o.Project, SessionUUID: o.SessionUUID, Intent: o.Intent, Status: o.Status, Phase: o.Phase, Committed: o.Committed, Diagnostic: d, Environment: CreationEnvironmentView(o)}
}

type LifecycleAPI interface {
	CreateProject(context.Context, BlankProjectRequest) (api.Operation, error)
	CreateSession(context.Context, ReserveSessionRequest) (api.Operation, error)
	Retry(context.Context, string) (api.Operation, error)
	InspectSession(context.Context, string) (api.SessionView, error)
	StartSession(context.Context, string) (api.SessionView, error)
	StopSession(context.Context, string) (api.SessionView, error)
}

type RenameAPI interface {
	RenameSession(context.Context, RenameRequest) (api.Operation, error)
}

type RetainedRenameAPI interface {
	RenameRetainedBranch(context.Context, RetainedRenameRequest) (api.Operation, error)
}

type RetainedDeleteAPI interface {
	PreviewRetainedDelete(context.Context, string, string) (api.RetainedDeletePreview, error)
	ConfirmRetainedDelete(context.Context, string, string, string, string) (api.Operation, error)
}

type CreateReplaceAPI interface {
	PreviewCreateReplace(context.Context, string, ReserveSessionRequest) (api.CreateReplacePreview, error)
	ConfirmCreateReplace(context.Context, string, string, string) (api.Operation, error)
}

// addLifecycleMethods serves the lifecycle methods. A method whose optional
// lifecycle interface life lacks stays unregistered, so it answers that its
// capability is not configured; the daemon's lifecycle implements them all.
func addLifecycleMethods(t *MethodTable, store *Store, life LifecycleAPI) {
	Register(t, api.ProjectCreate, func(ctx context.Context, r api.ProjectCreateRequest) (api.OperationResult, error) {
		return operationResult(life.CreateProject(ctx, BlankProjectRequest{Key: r.Key, Project: r.Project, URL: r.URL}))
	})
	Register(t, api.SessionCreate, func(ctx context.Context, r api.SessionCreateRequest) (api.OperationResult, error) {
		return operationResult(life.CreateSession(ctx, r))
	})
	session := func(ctx context.Context, view api.SessionView, err error) (api.SessionResult, error) {
		if err == nil {
			view, err = store.PopulateSessionStatus(ctx, view)
		}
		if err != nil {
			return api.SessionResult{}, err
		}
		return api.SessionResult{Session: view}, nil
	}
	Register(t, api.SessionInspect, func(ctx context.Context, r api.SessionInspectRequest) (api.SessionResult, error) {
		view, err := life.InspectSession(ctx, r.UUID)
		return session(ctx, view, err)
	})
	Register(t, api.SessionStart, func(ctx context.Context, r api.SessionStartRequest) (api.SessionResult, error) {
		view, err := life.StartSession(ctx, r.UUID)
		return session(ctx, view, err)
	})
	Register(t, api.SessionStop, func(ctx context.Context, r api.SessionStopRequest) (api.SessionResult, error) {
		view, err := life.StopSession(ctx, r.UUID)
		return session(ctx, view, err)
	})
	Register(t, api.SessionList, func(ctx context.Context, r api.SessionListRequest) (api.SessionListResult, error) {
		sessions, next, e := store.ListSessions(ctx, r.After, r.Limit)
		if e != nil {
			return api.SessionListResult{}, e
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
						return api.SessionListResult{}, currentErr
					}
				}
				return api.SessionListResult{}, e
			}
			v, e = store.PopulateSessionStatus(ctx, v)
			if e != nil {
				return api.SessionListResult{}, e
			}
			views = append(views, v)
		}
		return boundedPage(views, next, func(v api.SessionView) string { return v.UUID },
			func(views []api.SessionView, next string) api.SessionListResult {
				return api.SessionListResult{Sessions: views, Next: next}
			})
	})
	Register(t, api.OperationInspect, func(ctx context.Context, r api.OperationInspectRequest) (api.OperationResult, error) {
		return operationResult(store.GetOperation(ctx, r.ID))
	})
	Register(t, api.OperationList, func(ctx context.Context, r api.OperationListRequest) (api.OperationListResult, error) {
		ops, next, e := store.ListOperations(ctx, r.After, r.Limit)
		if e != nil {
			return api.OperationListResult{}, e
		}
		summaries := make([]api.OperationSummary, 0, len(ops))
		for _, op := range ops {
			summaries = append(summaries, SummarizeOperation(op))
		}
		return boundedPage(summaries, next, func(v api.OperationSummary) string { return v.ID },
			func(summaries []api.OperationSummary, next string) api.OperationListResult {
				return api.OperationListResult{Operations: summaries, Next: next}
			})
	})
	Register(t, api.OperationRetry, func(ctx context.Context, r api.OperationRetryRequest) (api.OperationResult, error) {
		return operationResult(life.Retry(ctx, r.ID))
	})
	if service, ok := life.(ServiceAPI); ok {
		addServiceMethods(t, service)
	}
	if attachment, ok := life.(AttachmentAPI); ok {
		addAttachmentMethods(t, attachment)
	}
	if removal, ok := life.(RemovalPreviewAPI); ok {
		Register(t, api.SessionRemovalPreview, func(ctx context.Context, r api.SessionRemovalPreviewRequest) (api.RemovalPreviewResult, error) {
			preview, e := removal.PreviewRemoval(ctx, RemovalPreviewRequest(r))
			return api.RemovalPreviewResult{Preview: preview}, e
		})
	}
	if discard, ok := life.(DiscardAPI); ok {
		Register(t, api.SessionDiscard, func(ctx context.Context, r api.SessionDiscardRequest) (api.OperationResult, error) {
			return operationResult(discard.DiscardSession(ctx, DiscardConfirmRequest(r)))
		})
	}
	if deletion, ok := life.(DeleteAPI); ok {
		Register(t, api.SessionDelete, func(ctx context.Context, r api.SessionDeleteRequest) (api.OperationResult, error) {
			return operationResult(deletion.DeleteSession(ctx, DiscardConfirmRequest(r)))
		})
	}
	if rename, ok := life.(RenameAPI); ok {
		Register(t, api.SessionRename, func(ctx context.Context, r api.SessionRenameRequest) (api.OperationResult, error) {
			return operationResult(rename.RenameSession(ctx, RenameRequest(r)))
		})
	}
	if rename, ok := life.(RetainedRenameAPI); ok {
		Register(t, api.ProjectRetainedRename, func(ctx context.Context, r api.ProjectRetainedRenameRequest) (api.OperationResult, error) {
			return operationResult(rename.RenameRetainedBranch(ctx, RetainedRenameRequest(r)))
		})
	}
	if deletion, ok := life.(ProjectDeleteAPI); ok {
		Register(t, api.ProjectDeletePreview, func(ctx context.Context, r api.ProjectDeletePreviewRequest) (api.ProjectDeletePreviewResult, error) {
			preview, e := deletion.PreviewProjectDelete(ctx, ProjectDeletePreviewRequest(r))
			return api.ProjectDeletePreviewResult{Preview: preview}, e
		})
		Register(t, api.ProjectDeleteConfirm, func(ctx context.Context, r api.ProjectDeleteConfirmRequest) (api.OperationResult, error) {
			return operationResult(deletion.ConfirmProjectDelete(ctx, ProjectDeleteConfirmRequest(r)))
		})
	}
	if deletion, ok := life.(RetainedDeleteAPI); ok {
		Register(t, api.ProjectRetainedDeletePreview, func(ctx context.Context, r api.ProjectRetainedDeletePreviewRequest) (api.RetainedDeletePreviewResult, error) {
			preview, e := deletion.PreviewRetainedDelete(ctx, r.Project, r.Branch)
			return api.RetainedDeletePreviewResult{Preview: preview}, e
		})
		Register(t, api.ProjectRetainedDeleteConfirm, func(ctx context.Context, r api.ProjectRetainedDeleteConfirmRequest) (api.OperationResult, error) {
			return operationResult(deletion.ConfirmRetainedDelete(ctx, r.Key, r.Project, r.Branch, r.ConfirmationToken))
		})
	}
	if replace, ok := life.(CreateReplaceAPI); ok {
		Register(t, api.SessionCreateReplacePreview, func(ctx context.Context, r api.SessionCreateReplacePreviewRequest) (api.CreateReplacePreviewResult, error) {
			preview, e := replace.PreviewCreateReplace(ctx, r.OldUUID, r.Create())
			return api.CreateReplacePreviewResult{Preview: preview}, e
		})
		Register(t, api.SessionCreateReplaceConfirm, func(ctx context.Context, r api.SessionCreateReplaceConfirmRequest) (api.OperationResult, error) {
			return operationResult(replace.ConfirmCreateReplace(ctx, r.OldUUID, r.Key, r.ConfirmationToken))
		})
	}
	if cleanup, ok := life.(CreateCleanupAPI); ok {
		Register(t, api.SessionCreateCleanupPreview, func(ctx context.Context, r api.SessionCreateCleanupPreviewRequest) (api.CreateCleanupPreviewResult, error) {
			preview, e := cleanup.PreviewCreateCleanup(ctx, r.UUID)
			return api.CreateCleanupPreviewResult{Preview: preview}, e
		})
		Register(t, api.SessionCreateCleanupConfirm, func(ctx context.Context, r api.SessionCreateCleanupConfirmRequest) (api.OperationResult, error) {
			return operationResult(cleanup.ConfirmCreateCleanup(ctx, r.UUID, r.Key, r.ConfirmationToken))
		})
	}
}

// operationResult wraps the operation a lifecycle change returns.
func operationResult(op api.Operation, err error) (api.OperationResult, error) {
	return api.OperationResult{Operation: op}, err
}
