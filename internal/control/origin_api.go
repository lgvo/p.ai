package control

import (
	"context"
	"encoding/json"
	"sort"
	"time"

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

func (a OriginAPI) Change(ctx context.Context, c OriginChange) (OriginState, error) {
	if !validOriginChange(c) {
		return OriginState{}, ErrInvalid
	}
	if replay, completed, err := a.Store.CompletedOriginChange(ctx, c); err != nil {
		return OriginState{}, err
	} else if completed {
		return replay, nil
	}
	if c.Kind == "set" {
		if err := a.Runner.ValidateURL(c.ProposedURL); err != nil {
			return OriginState{}, ErrInvalid
		}
	}
	var state OriginState
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
					return err
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

func (a OriginAPI) Refresh(ctx context.Context, project string) (OriginState, error) {
	if !validProject(project) {
		return OriginState{}, ErrInvalid
	}
	var state OriginState
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

func OriginHandler(base Handler, api OriginAPI) Handler {
	return func(ctx context.Context, method string, params json.RawMessage) (any, *RPCError) {
		if method == "system.capabilities" {
			result, e := base(ctx, method, params)
			if e != nil {
				return result, e
			}
			object := result.(map[string]any)
			object["available"] = append(object["available"].([]string), "origin.change", "origin.refresh", "origin.inspect", "origin.sources")
			if _, ok := api.Runner.(PublicationRunner); ok {
				object["available"] = append(object["available"].([]string), "origin.publication.preview", "origin.publish")
			}
			if api.Git != nil {
				object["available"] = append(object["available"].([]string), "project.retained_branches")
			}
			return object, nil
		}
		switch method {
		case "project.retained_branches":
			return retainedBranchesHandler(ctx, params, api)
		case "origin.publication.preview", "origin.publish":
			if _, ok := api.Runner.(PublicationRunner); !ok {
				return nil, lifecycleRPC(ErrNotFound)
			}
			return publicationHandler(ctx, method, params, api)
		case "origin.change":
			var p struct {
				V           int    `json:"v"`
				Key         string `json:"key"`
				Project     string `json:"project"`
				Kind        string `json:"kind"`
				ExpectedURL string `json:"expected_url"`
				URL         string `json:"url"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 {
				return nil, errorRPC(-32602, "invalid_params", "invalid origin.change request")
			}
			c := OriginChange{Key: p.Key, Project: p.Project, Kind: p.Kind, ExpectedURL: p.ExpectedURL, ProposedURL: p.URL}
			if !validOriginChange(c) {
				return nil, errorRPC(-32602, "invalid_params", "invalid origin.change request")
			}
			state, e := api.Change(ctx, c)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "origin": state}, nil
		case "origin.refresh", "origin.inspect":
			var p struct {
				V       int    `json:"v"`
				Project string `json:"project"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || !validProject(p.Project) {
				return nil, errorRPC(-32602, "invalid_params", "invalid origin project")
			}
			var state OriginState
			var e error
			if method == "origin.refresh" {
				state, e = api.Refresh(ctx, p.Project)
			} else {
				state, _, e = api.Store.Origin(ctx, p.Project)
			}
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			return map[string]any{"v": 1, "origin": state}, nil
		case "origin.sources":
			var p struct {
				V       int    `json:"v"`
				Project string `json:"project"`
				After   string `json:"after"`
				Limit   int    `json:"limit"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || !validProject(p.Project) || p.Limit < 1 || p.Limit > 16 || p.After != "" && !plugin.ValidGitOriginRef(p.After) {
				return nil, errorRPC(-32602, "invalid_params", "invalid origin source page")
			}
			state, refs, e := api.Store.Origin(ctx, p.Project)
			if e != nil {
				return nil, lifecycleRPC(e)
			}
			start := sort.Search(len(refs), func(i int) bool { return refs[i].Ref > p.After })
			end := start + p.Limit
			if end > len(refs) {
				end = len(refs)
			}
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
				result := map[string]any{"v": 1, "project": p.Project, "origin_url": state.URL, "status": state.Status, "observation_status": presentation, "observed_at": state.ObservedAt, "refs": page, "next": next}
				encoded, err := json.Marshal(result)
				if err != nil {
					return nil, errorRPC(-32603, "internal", "origin source page cannot be encoded")
				}
				if len(encoded) <= lifecyclePageResultCeiling {
					return result, nil
				}
			}
			return nil, errorRPC(-32603, "internal", "single origin source exceeds frame limit")
		default:
			return base(ctx, method, params)
		}
	}
}
