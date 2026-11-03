package control

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// CreationImageView shows the commit image of a creation that has image
// builders configured.
func CreationImageView(op api.Operation) *api.ImageView {
	if op.Kind != "session.create" {
		return nil
	}
	ev, err := Evidence(op)
	if err != nil || ev.Environment == nil {
		return nil
	}
	v := &api.ImageView{CommitOID: ev.CapturedOID, System: ev.Environment.System, Selection: "pending", State: "pending",
		BaseImageFingerprint: ev.Environment.BaseFingerprint, ImageFingerprint: ev.ImageFingerprint}
	if img := ev.Image; img != nil {
		v.Selection, v.State = "base", "reused"
		if img.Selection == string(runtimedriver.SelectionDevShell) {
			v.Selection = "devShells." + v.System + ".default"
		}
		if img.Built {
			v.State = "built"
		}
	}
	if b := ev.EnvironmentBuilder; b != nil && b.State == "attempted" {
		v.State = "building"
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

// SummarizeOperation is o without its request and evidence, for
// operation.list; its diagnostic is the same as operation.inspect's.
func SummarizeOperation(o api.Operation) api.OperationSummary {
	return api.OperationSummary{ID: o.ID, Key: o.Key, Kind: o.Kind, Project: o.Project, SessionUUID: o.SessionUUID, Intent: o.Intent, Status: o.Status, Phase: o.Phase, Committed: o.Committed, Reason: o.Reason, Diagnostic: o.Diagnostic, Image: CreationImageView(o)}
}

// OperationAPI runs the operation journal's work: it waits for and retries
// operations. With Git configured the daemon serves it, with a runtime too
// as part of the lifecycle.
type OperationAPI interface {
	Retry(context.Context, string) (api.Operation, error)
	// WaitOperation returns the operation as soon as it is not running, or
	// when the timeout passes with it still running.
	WaitOperation(context.Context, string, time.Duration) (api.Operation, error)
}

type LifecycleAPI interface {
	OperationAPI
	CreateProject(context.Context, BlankProjectRequest) (api.Operation, error)
	CreateSession(context.Context, ReserveSessionRequest) (api.Operation, error)
	InspectSession(context.Context, string) (api.SessionView, error)
	// StartSession and StopSession record an operation that starts or stops
	// the session, or record nothing when it is already as asked.
	StartSession(context.Context, string) (api.SessionChangeResult, error)
	StopSession(context.Context, string) (api.SessionChangeResult, error)
}

type RenameAPI interface {
	RenameSession(context.Context, RenameRequest) (api.Operation, error)
}

type RetainedRenameAPI interface {
	RenameRetainedBranch(context.Context, RetainedRenameRequest) (api.Operation, error)
}

// RetainedDeleteAPI deletes retained branches, a risky change: a dry run
// plans it, and a run with the plan's change tag records its operation.
type RetainedDeleteAPI interface {
	DeleteRetainedBranch(ctx context.Context, project, branch string, req api.ChangeRequest) (api.ChangeResult[api.RetainedDeleteDetails], error)
}

// CreateReplaceAPI replaces a blocked creation with a new one, a risky
// change: a dry run plans it, and a run with the plan's change tag records
// the new creation. The run's key is also the new creation's.
type CreateReplaceAPI interface {
	ReplaceCreation(ctx context.Context, oldUUID string, choices api.CreationChoices, req api.ChangeRequest) (api.ChangeResult[api.CreateReplaceDetails], error)
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
	Register(t, api.SessionStart, func(ctx context.Context, r api.SessionStartRequest) (api.SessionChangeResult, error) {
		return life.StartSession(ctx, r.UUID)
	})
	Register(t, api.SessionStop, func(ctx context.Context, r api.SessionStopRequest) (api.SessionChangeResult, error) {
		return life.StopSession(ctx, r.UUID)
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
	if service, ok := life.(ServiceAPI); ok {
		addServiceMethods(t, service)
	}
	if attachment, ok := life.(AttachmentAPI); ok {
		addAttachmentMethods(t, attachment)
	}
	if removal, ok := life.(RemovalAPI); ok {
		Register(t, api.SessionDiscard, func(ctx context.Context, r api.SessionDiscardRequest) (api.ChangeResult[api.RemovalDetails], error) {
			return removal.DiscardSession(ctx, r.UUID, r.ChangeRequest)
		})
		Register(t, api.SessionDelete, func(ctx context.Context, r api.SessionDeleteRequest) (api.ChangeResult[api.RemovalDetails], error) {
			return removal.DeleteSession(ctx, r.UUID, r.ChangeRequest)
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
		Register(t, api.ProjectDelete, func(ctx context.Context, r api.ProjectDeleteRequest) (api.ChangeResult[api.ProjectDeleteDetails], error) {
			return deletion.DeleteProject(ctx, r.Project, r.ChangeRequest)
		})
	}
	if deletion, ok := life.(RetainedDeleteAPI); ok {
		Register(t, api.ProjectRetainedDelete, func(ctx context.Context, r api.ProjectRetainedDeleteRequest) (api.ChangeResult[api.RetainedDeleteDetails], error) {
			return deletion.DeleteRetainedBranch(ctx, r.Project, r.Branch, r.ChangeRequest)
		})
	}
	if replace, ok := life.(CreateReplaceAPI); ok {
		Register(t, api.SessionCreateReplace, func(ctx context.Context, r api.SessionCreateReplaceRequest) (api.ChangeResult[api.CreateReplaceDetails], error) {
			return replace.ReplaceCreation(ctx, r.OldUUID, r.CreationChoices, r.ChangeRequest)
		})
	}
	if cleanup, ok := life.(CreateCleanupAPI); ok {
		Register(t, api.SessionCreateCleanup, func(ctx context.Context, r api.SessionCreateCleanupRequest) (api.ChangeResult[api.CreateCleanupDetails], error) {
			return cleanup.CleanupCreation(ctx, r.UUID, r.ChangeRequest)
		})
	}
}

// addOperationMethods serves the operation journal: its reads, waits and
// Retry.
func addOperationMethods(t *MethodTable, store *Store, ops OperationAPI) {
	Register(t, api.OperationInspect, func(ctx context.Context, r api.OperationInspectRequest) (api.OperationResult, error) {
		return operationResult(store.GetOperation(ctx, r.ID))
	})
	Register(t, api.OperationList, func(ctx context.Context, r api.OperationListRequest) (api.OperationListResult, error) {
		ops, next, e := store.ListOperations(ctx, r.After, r.Limit)
		if e != nil {
			return api.OperationListResult{}, e
		}
		return boundedPage(ops, next, func(o ListedOperation) string { return o.Cursor },
			func(ops []ListedOperation, next string) api.OperationListResult {
				summaries := make([]api.OperationSummary, 0, len(ops))
				for _, o := range ops {
					summaries = append(summaries, SummarizeOperation(o.Operation))
				}
				return api.OperationListResult{Operations: summaries, Next: next}
			})
	})
	Register(t, api.OperationWait, func(ctx context.Context, r api.OperationWaitRequest) (api.OperationResult, error) {
		return operationResult(ops.WaitOperation(ctx, r.ID, time.Duration(r.Timeout)*time.Second))
	})
	Register(t, api.OperationRetry, func(ctx context.Context, r api.OperationRetryRequest) (api.OperationResult, error) {
		return operationResult(ops.Retry(ctx, r.ID))
	})
}

// operationResult wraps the operation a lifecycle change returns.
func operationResult(op api.Operation, err error) (api.OperationResult, error) {
	return api.OperationResult{Operation: op}, err
}
