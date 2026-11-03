package control

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/sourcedriver"
)

type OriginObserver interface {
	Observe(context.Context, string) ([]plugin.GitOriginRef, error)
}
type OriginRunner interface {
	ValidateURL(string) error
	WithOrigin(context.Context, string, func(OriginObserver) error) error
}

type OriginAPI struct {
	Store  *Store
	Runner OriginRunner
	Git    GitReader
	// Driver is the source driver, which checks the names a publication
	// takes.
	Driver sourcedriver.Driver
	// Queue hands a recorded origin operation to the daemon's workers.
	Queue func(id string) error
}

// Change records origin change c as an operation of kind origin.change and
// queues it: its worker contacts the proposed origin (RunOriginChange).
func (a OriginAPI) Change(ctx context.Context, c OriginChange) (api.Operation, error) {
	if !validOriginChange(c) {
		return api.Operation{}, ErrInvalid
	}
	if c.Kind == "set" {
		if err := a.Runner.ValidateURL(c.ProposedURL); err != nil {
			return api.Operation{}, Invalid("url")
		}
	}
	op, err := a.Store.BeginOriginChange(ctx, c)
	if err != nil || op.Status != "running" {
		return op, err
	}
	return op, a.Queue(op.ID)
}

// RunOriginChange is the work of origin change op: in the project's origin
// scope it observes the proposed origin of a set, then records the change and
// the operation's end together. A failure ends the operation failed with its
// reason and diagnostic; it is never contacted again for op.
func (a OriginAPI) RunOriginChange(ctx context.Context, op api.Operation) {
	var c OriginChange
	if json.Unmarshal(op.Request, &c) != nil {
		a.fail(ctx, op, ErrInvalid)
		return
	}
	err := a.Runner.WithOrigin(ctx, c.Project, func(observer OriginObserver) error {
		var refs []plugin.GitOriginRef
		if c.Kind == "set" {
			var err error
			if refs, err = observer.Observe(ctx, c.ProposedURL); err != nil {
				return Failed(api.ReasonOrigin, err)
			}
		}
		return a.Store.CompleteOriginChange(ctx, op, refs)
	})
	if err != nil {
		a.fail(ctx, op, err)
	}
}

// fail records origin operation op failed with err's reason and detail,
// unless the daemon is stopping: the next start resumes it.
func (a OriginAPI) fail(ctx context.Context, op api.Operation, err error) {
	if ctx.Err() != nil {
		return
	}
	if e := a.Store.AdvanceOperation(ctx, op.ID, "failed", op.Phase, op.Committed, op.Evidence, OperationReason(err), err.Error()); e != nil {
		log.Printf("recording the failure of %s %s: %v", op.Kind, op.ID, e)
	}
}

func (a OriginAPI) Refresh(ctx context.Context, project string) (api.OriginState, error) {
	if !validProject(project) {
		return api.OriginState{}, Invalid("project")
	}
	var state api.OriginState
	err := a.Runner.WithOrigin(ctx, project, func(observer OriginObserver) error {
		current, _, e := a.Store.Origin(ctx, project)
		if e != nil {
			return e
		}
		if current.Status == "local-only" {
			state = current
			return nil
		}
		refs, observeErr := observer.Observe(ctx, current.URL)
		// Once contact has started, persist its outcome even if the requesting
		// client disconnects. The origin lock still protects this write.
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if observeErr != nil {
			if e = a.Store.StoreOriginRefresh(writeCtx, project, current.URL, nil, "origin refresh failed; check SSH identity, host key, URL and access"); e != nil {
				return e
			}
		} else if e = a.Store.StoreOriginRefresh(writeCtx, project, current.URL, refs, ""); e != nil {
			return e
		}
		state, _, e = a.Store.Origin(writeCtx, project)
		return e
	})
	return state, err
}

// addOriginMethods serves origin association, observation and publication.
func addOriginMethods(t *MethodTable, origins OriginAPI) {
	Register(t, api.OriginChange, func(ctx context.Context, r api.OriginChangeRequest) (api.OperationResult, error) {
		return operationResult(origins.Change(ctx, OriginChange{Key: r.Key, Project: r.Project, Kind: r.Kind, ExpectedURL: r.ExpectedURL, ProposedURL: r.URL}))
	})
	Register(t, api.OriginRefresh, func(ctx context.Context, r api.OriginRefreshRequest) (api.OriginResult, error) {
		state, e := origins.Refresh(ctx, r.Project)
		return api.OriginResult{Origin: state}, e
	})
	Register(t, api.OriginInspect, func(ctx context.Context, r api.OriginInspectRequest) (api.OriginResult, error) {
		state, _, e := origins.Store.Origin(ctx, r.Project)
		return api.OriginResult{Origin: state}, e
	})
	Register(t, api.OriginSources, func(ctx context.Context, r api.OriginSourcesRequest) (api.OriginSourcesResult, error) {
		state, refs, e := origins.Store.Origin(ctx, r.Project)
		if e != nil {
			return api.OriginSourcesResult{}, e
		}
		start := sort.Search(len(refs), func(i int) bool { return refs[i].Ref > r.After })
		end := min(start+r.Limit, len(refs))
		presentation := "none"
		if state.Status == "fresh" {
			presentation = "fresh"
		} else if state.Status == "unknown" {
			presentation = "stale"
		}
		for count := end - start; count >= 0; count-- {
			if count == 0 && end > start {
				break
			}
			page := refs[start : start+count]
			if page == nil {
				page = []plugin.GitOriginRef{}
			}
			next := ""
			if start+count < len(refs) {
				next = page[len(page)-1].Ref
			}
			result := api.OriginSourcesResult{Project: r.Project, OriginURL: state.URL, Status: state.Status, ObservationStatus: presentation, ObservedAt: state.ObservedAt, Refs: page, Next: next}
			encoded, err := json.Marshal(result)
			if err != nil {
				return api.OriginSourcesResult{}, Internal(api.ReasonEncoding, err)
			}
			if len(encoded) <= lifecyclePageResultCeiling {
				return result, nil
			}
		}
		return api.OriginSourcesResult{}, Internal(api.ReasonEncoding, errors.New("single origin source exceeds frame limit"))
	})
	if _, ok := origins.Runner.(PublicationRunner); ok {
		addPublicationMethods(t, origins)
	}
	if origins.Git != nil {
		Register(t, api.ProjectRetainedBranches, func(ctx context.Context, r api.ProjectRetainedBranchesRequest) (api.RetainedBranchesResult, error) {
			return retainedBranches(ctx, r, origins)
		})
	}
}
