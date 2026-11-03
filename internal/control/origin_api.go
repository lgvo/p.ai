package control

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/plugin"
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
}

func (a OriginAPI) Change(ctx context.Context, c OriginChange) (api.OriginState, error) {
	if !validOriginChange(c) {
		return api.OriginState{}, ErrInvalid
	}
	if replay, completed, err := a.Store.CompletedOriginChange(ctx, c); err != nil {
		return api.OriginState{}, err
	} else if completed {
		return replay, nil
	}
	if c.Kind == "set" {
		if err := a.Runner.ValidateURL(c.ProposedURL); err != nil {
			return api.OriginState{}, Invalid("url")
		}
	}
	var state api.OriginState
	err := a.Runner.WithOrigin(ctx, c.Project, func(observer OriginObserver) error {
		completed, replay, err := a.Store.PrepareOriginChange(ctx, c)
		if err != nil {
			return err
		}
		if completed {
			state = replay
			return nil
		}
		if !completed {
			var refs []plugin.GitOriginRef
			if c.Kind == "set" {
				refs, err = observer.Observe(ctx, c.ProposedURL)
				if err != nil {
					return Failed(api.ReasonOrigin, err)
				}
			}
			if err = a.Store.CommitOriginChange(ctx, c, refs); err != nil {
				return err
			}
		}
		state, _, err = a.Store.Origin(ctx, c.Project)
		return err
	})
	return state, err
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
	Register(t, api.OriginChange, func(ctx context.Context, r api.OriginChangeRequest) (api.OriginResult, error) {
		state, e := origins.Change(ctx, OriginChange{Key: r.Key, Project: r.Project, Kind: r.Kind, ExpectedURL: r.ExpectedURL, ProposedURL: r.URL})
		return api.OriginResult{Origin: state}, e
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
