package control

import (
	"context"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/plugin"
)

type PublicationScope interface {
	Observe(context.Context, string) ([]plugin.GitOriginRef, error)
	PreviewPublication(context.Context, string, string, string) (api.PublicationPreview, error)
	PublishPublication(context.Context, api.PublicationPreview) (api.Publication, error)
}
type PublicationRunner interface {
	WithPublication(context.Context, string, func(PublicationScope) error) error
}

// PreviewPublication always contacts the currently configured origin. The
// caller supplies the captured P tip so confirmation can bind to exact work.
func (a OriginAPI) PreviewPublication(ctx context.Context, r PublicationRequest) (api.PublicationPreview, error) {
	if !validPublicationRequest(r) {
		return api.PublicationPreview{}, ErrInvalid
	}
	runner, ok := a.Runner.(PublicationRunner)
	if !ok {
		return api.PublicationPreview{}, NotConfigured(api.ReasonGit)
	}
	var preview api.PublicationPreview
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

// publicationContext returns the origin URL and the source's P ref. A
// local-only project has no origin to publish to, whatever URL the request
// expected; an origin other than the expected one was changed since review.
func (a OriginAPI) publicationContext(ctx context.Context, r PublicationRequest) (string, string, error) {
	state, _, err := a.Store.Origin(ctx, r.Project)
	if err != nil {
		return "", "", err
	}
	if state.Status == "local-only" {
		return "", "", Conflict(api.ReasonOriginNotSet, api.TargetProject(r.Project))
	}
	if state.URL != r.ExpectedOriginURL {
		return "", "", Stale(api.ReasonChanged)
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
		return Failed(api.ReasonOrigin, err)
	}
	return a.Store.StoreOriginRefresh(writeCtx, project, url, refs, "")
}

// PublishPublication marks the key attempted before invoking the external push.
// A crash after that boundary replays outcome_unknown without another contact.
func (a OriginAPI) PublishPublication(ctx context.Context, r PublicationRequest) (api.Publication, error) {
	if !validPublicationRequest(r) || r.Key == "" || len(r.Key) > 128 {
		return api.Publication{}, ErrInvalid
	}
	if result, status, err := a.Store.PublicationRecord(ctx, r); err != nil || status == "completed" || status == "attempted" {
		return result, err
	}
	runner, ok := a.Runner.(PublicationRunner)
	if !ok {
		return api.Publication{}, NotConfigured(api.ReasonGit)
	}
	var result api.Publication
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
			e = Failed(api.ReasonOrigin, e)
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

// addPublicationMethods serves origin.publication.preview and origin.publish.
func addPublicationMethods(t *MethodTable, origins OriginAPI) {
	Register(t, api.OriginPublicationPreview, func(ctx context.Context, r api.OriginPublicationPreviewRequest) (api.PublicationPreviewResult, error) {
		preview, e := origins.PreviewPublication(ctx, PublicationRequest{ExpectedOriginURL: r.ExpectedOriginURL, Project: r.Project, Kind: r.Kind, Source: r.Source, SourceOID: r.SourceOID, DestinationRef: r.DestinationRef})
		return api.PublicationPreviewResult{Preview: preview}, e
	})
	Register(t, api.OriginPublish, func(ctx context.Context, r api.OriginPublishRequest) (api.OriginPublishResult, error) {
		publication, e := origins.PublishPublication(ctx, PublicationRequest(r))
		return api.OriginPublishResult{Publication: publication}, e
	})
}
