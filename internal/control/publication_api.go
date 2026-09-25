package control

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/plugin"
)

type PublicationScope interface {
	Observe(context.Context, string) ([]plugin.GitOriginRef, error)
	PreviewPublication(context.Context, string, string, string) (PublicationPreview, error)
	PublishPublication(context.Context, PublicationPreview) (PublicationResult, error)
}
type PublicationRunner interface {
	WithPublication(context.Context, string, func(PublicationScope) error) error
}

// PreviewPublication always contacts the currently configured origin. The
// caller supplies the captured P tip so confirmation can bind to exact work.
func (a OriginAPI) PreviewPublication(ctx context.Context, r PublicationRequest) (PublicationPreview, error) {
	if !validPublicationRequest(r) {
		return PublicationPreview{}, ErrInvalid
	}
	runner, ok := a.Runner.(PublicationRunner)
	if !ok {
		return PublicationPreview{}, ErrNotFound
	}
	var preview PublicationPreview
	err := runner.WithPublication(ctx, r.Project, func(scope PublicationScope) error {
		if err := a.Store.lockGitAuthority(ctx); err != nil {
			return err
		}
		defer a.Store.gitAuthority.Unlock()
		url, ref, err := a.publicationContext(ctx, r)
		if err != nil {
			return err
		}
		if err = a.observeForPublication(ctx, scope, r.Project, url); err != nil {
			return err
		}
		preview, err = scope.PreviewPublication(ctx, ref, r.SourceOID, r.DestinationRef)
		if err != nil {
			return err
		}
		preview.SourceKind, preview.Source, preview.WorkspaceStatus = r.Kind, r.Source, "unknown"
		return nil
	})
	return preview, err
}

func (a OriginAPI) publicationContext(ctx context.Context, r PublicationRequest) (string, string, error) {
	state, _, err := a.Store.Origin(ctx, r.Project)
	if err != nil {
		return "", "", err
	}
	if state.URL != r.ExpectedOriginURL {
		return "", "", ErrConflict
	}
	if state.Status == "local-only" {
		return "", "", ErrNotFound
	}
	ref, err := a.Store.PublicationSource(ctx, r)
	if err != nil {
		return "", "", err
	}
	return state.URL, ref, nil
}

func (a OriginAPI) observeForPublication(ctx context.Context, scope PublicationScope, project, url string) error {
	refs, err := scope.Observe(ctx, url)
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err != nil {
		if storeErr := a.Store.StoreOriginRefresh(writeCtx, project, url, nil, "origin refresh failed; check SSH identity, host key, URL and access"); storeErr != nil {
			return storeErr
		}
		return err
	}
	return a.Store.StoreOriginRefresh(writeCtx, project, url, refs, "")
}

// PublishPublication marks the key attempted before invoking the external push.
// A crash after that boundary replays outcome_unknown without another contact.
func (a OriginAPI) PublishPublication(ctx context.Context, r PublicationRequest) (PublicationResult, error) {
	if !validPublicationRequest(r) || r.Key == "" || len(r.Key) > 128 {
		return PublicationResult{}, ErrInvalid
	}
	if result, status, err := a.Store.PublicationRecord(ctx, r); err != nil || status == "completed" || status == "attempted" {
		return result, err
	}
	runner, ok := a.Runner.(PublicationRunner)
	if !ok {
		return PublicationResult{}, ErrNotFound
	}
	var result PublicationResult
	err := runner.WithPublication(ctx, r.Project, func(scope PublicationScope) error {
		if err := a.Store.lockGitAuthority(ctx); err != nil {
			return err
		}
		defer a.Store.gitAuthority.Unlock()
		replay, status, e := a.Store.PreparePublication(ctx, r)
		if e != nil {
			return e
		}
		if status == "completed" || status == "attempted" {
			result = replay
			return nil
		}
		url, ref, e := a.publicationContext(ctx, r)
		if e != nil {
			return e
		}
		if e = a.observeForPublication(ctx, scope, r.Project, url); e != nil {
			return e
		}
		preview, e := scope.PreviewPublication(ctx, ref, r.SourceOID, r.DestinationRef)
		if e != nil {
			return e
		}
		preview.SourceKind, preview.Source, preview.WorkspaceStatus = r.Kind, r.Source, "unknown"
		result.Preview = preview
		if preview.Relation == "divergent" {
			result.Status = "refused"
		} else if preview.Relation == "equal" || preview.Relation == "destination_contains" {
			result.Status = "satisfied"
		}
		if result.Status != "" {
			return a.Store.UpdatePublication(ctx, r.Key, "prepared", "completed", result)
		}
		// Persist the uncertainty boundary before any possible network side effect.
		result.Status = "outcome_unknown"
		durableCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		e = a.Store.UpdatePublication(durableCtx, r.Key, "prepared", "attempted", result)
		cancel()
		if e != nil {
			return e
		}
		result, e = scope.PublishPublication(ctx, preview)
		if e != nil {
			// The closed Git broker returns an error only when no native push
			// started. Its started-process failures are outcome_unknown values.
			durableCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			rearmErr := a.Store.RearmPublication(durableCtx, r.Key)
			cancel()
			if rearmErr != nil {
				return rearmErr
			}
			return e
		}
		result.Preview.SourceKind, result.Preview.Source, result.Preview.WorkspaceStatus = r.Kind, r.Source, "unknown"
		durableCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		saveErr := a.Store.UpdatePublication(durableCtx, r.Key, "attempted", "completed", result)
		cancel()
		if saveErr != nil {
			return saveErr
		}
		return nil
	})
	return result, err
}

func publicationRequestParams(params json.RawMessage, publish bool) (PublicationRequest, error) {
	var p struct {
		ExpectedOriginURL string `json:"expected_origin_url"`
		V                 int    `json:"v"`
		Key               string `json:"key"`
		Project           string `json:"project"`
		Kind              string `json:"kind"`
		Source            string `json:"source"`
		SourceOID         string `json:"source_oid"`
		DestinationRef    string `json:"destination_ref"`
	}
	if strictDecode(params, &p) != nil || p.V != 1 || !validPublicationRequest(PublicationRequest{ExpectedOriginURL: p.ExpectedOriginURL, Project: p.Project, Kind: p.Kind, Source: p.Source, SourceOID: p.SourceOID, DestinationRef: p.DestinationRef}) || publish && (p.Key == "" || len(p.Key) > 128) || !publish && p.Key != "" {
		return PublicationRequest{}, ErrInvalid
	}
	return PublicationRequest{ExpectedOriginURL: p.ExpectedOriginURL, Key: p.Key, Project: p.Project, Kind: p.Kind, Source: p.Source, SourceOID: p.SourceOID, DestinationRef: p.DestinationRef}, nil
}

func publicationHandler(ctx context.Context, method string, params json.RawMessage, api OriginAPI) (any, *RPCError) {
	r, err := publicationRequestParams(params, method == "origin.publish")
	if err != nil {
		return nil, errorRPC(-32602, "invalid_params", "invalid publication request")
	}
	if !strings.HasPrefix(r.DestinationRef, "refs/heads/") {
		return nil, errorRPC(-32602, "invalid_params", "destination must be a branch")
	}
	if method == "origin.publication.preview" {
		p, e := api.PreviewPublication(ctx, r)
		if e != nil {
			return nil, lifecycleRPC(e)
		}
		return map[string]any{"v": 1, "preview": p}, nil
	}
	result, e := api.PublishPublication(ctx, r)
	if e != nil {
		return nil, lifecycleRPC(e)
	}
	return map[string]any{"v": 1, "publication": result}, nil
}
