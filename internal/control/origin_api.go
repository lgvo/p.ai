package control

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// OriginAPI serves a project's origin: its association, its observation
// and publication to it, and the retained branches a publication may take.
// Core decides and guards, under the project's origin lock (LockOrigin);
// the source driver contacts the origin.
type OriginAPI struct {
	Store *Store
	// Driver is the source driver: it checks the URLs and names origin
	// requests take, observes, fetches from and publishes to origins, and
	// reads the heads of P's repositories.
	Driver sourcedriver.Driver
	// Queue hands a recorded origin operation to the daemon's workers.
	Queue func(id string) error
}

// Change records origin change c as an operation of kind origin.change and
// queues it: its worker contacts the proposed origin (RunOriginChange). A
// recorded request is answered from SQLite alone; a new set has the source
// driver check its URL first.
func (a OriginAPI) Change(ctx context.Context, c OriginChange) (api.Operation, error) {
	if !validOriginChange(c) {
		return api.Operation{}, ErrInvalid
	}
	request, err := json.Marshal(c)
	if err != nil {
		return api.Operation{}, err
	}
	_, replayed, err := a.Store.recorded(ctx, c.Key, KindOriginChange, request)
	if err != nil {
		return api.Operation{}, err
	}
	if !replayed && c.Kind == "set" {
		if _, err = CheckNames(ctx, a.Driver, NameField{"url", sourcedriver.Name{Kind: sourcedriver.NameOriginURL, Value: c.ProposedURL}}); err != nil {
			return api.Operation{}, err
		}
	}
	op, err := a.Store.BeginOriginChange(ctx, c)
	if err != nil || op.Status != "running" {
		return op, err
	}
	return op, a.Queue(op.ID)
}

// RunOriginChange is the work of origin change op: under the project's
// origin lock it observes the proposed origin of a set, then records the
// change and the operation's end together. A failure ends the operation
// failed with its reason and diagnostic; it is never contacted again for op.
func (a OriginAPI) RunOriginChange(ctx context.Context, op api.Operation) {
	var c OriginChange
	if json.Unmarshal(op.Request, &c) != nil {
		a.fail(ctx, op, ErrInvalid)
		return
	}
	release, err := a.Store.LockOrigin(ctx, c.Project)
	if err != nil {
		a.fail(ctx, op, err)
		return
	}
	defer release()
	var refs []sourcedriver.OriginRef
	if c.Kind == "set" {
		if refs, err = a.observe(ctx, c.ProposedURL); err != nil {
			a.fail(ctx, op, Failed(api.ReasonOrigin, err))
			return
		}
	}
	if err = a.Store.CompleteOriginChange(ctx, op, refs); err != nil {
		a.fail(ctx, op, err)
	}
}

// observe has the source driver observe the origin at url, within the
// bounds of a call to an origin (CallOrigin).
func (a OriginAPI) observe(ctx context.Context, url string) (refs []sourcedriver.OriginRef, err error) {
	err = a.Store.CallOrigin(ctx, func(ctx context.Context) error {
		refs, err = a.Driver.ObserveOrigin(ctx, url)
		return err
	})
	return refs, err
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

// Refresh observes project's origin afresh under its origin lock and
// records the observation, or marks the last one unknown when the origin
// cannot be observed, and returns the origin's state.
func (a OriginAPI) Refresh(ctx context.Context, project string) (api.OriginState, error) {
	if !validProject(project) {
		return api.OriginState{}, Invalid("project")
	}
	release, err := a.Store.LockOrigin(ctx, project)
	if err != nil {
		return api.OriginState{}, err
	}
	defer release()
	current, _, err := a.Store.Origin(ctx, project)
	if err != nil || current.Status == "local-only" {
		return current, err
	}
	refs, observeErr := a.observe(ctx, current.URL)
	// Once contact has started, persist its outcome even if the requesting
	// client disconnects. The origin lock still protects this write.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if observeErr != nil {
		err = a.Store.StoreOriginRefresh(writeCtx, project, current.URL, nil, "origin refresh failed; check SSH identity, host key, URL and access")
	} else {
		err = a.Store.StoreOriginRefresh(writeCtx, project, current.URL, refs, "")
	}
	if err != nil {
		return api.OriginState{}, err
	}
	state, _, err := a.Store.Origin(writeCtx, project)
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
				page = []api.OriginRef{}
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
	Register(t, api.OriginPublish, origins.Publish)
	Register(t, api.ProjectRetainedBranches, func(ctx context.Context, r api.ProjectRetainedBranchesRequest) (api.RetainedBranchesResult, error) {
		return retainedBranches(ctx, r, origins)
	})
}
