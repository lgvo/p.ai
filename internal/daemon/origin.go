package daemon

import (
	"context"
	"github.com/lgvo/p.ai/internal/api"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
)

type originRunner struct{ backend *gitservice.Backend }

func (r originRunner) ValidateURL(url string) error { return gitservice.ValidateOriginURL(url) }

func (r originRunner) WithOrigin(ctx context.Context, project string, fn func(control.OriginObserver) error) error {
	return r.backend.WithOrigin(ctx, project, func(scope *gitservice.OriginScope) error { return fn(scope) })
}

func (r originRunner) WithPublication(ctx context.Context, project string, fn func(control.PublicationScope) error) error {
	return r.backend.WithOrigin(ctx, project, func(scope *gitservice.OriginScope) error { return fn(publicationScope{scope}) })
}

type publicationScope struct{ *gitservice.OriginScope }

var _ control.PublicationScope = publicationScope{}

func (s publicationScope) PreviewPublication(ctx context.Context, ref, oid, dest string) (api.PublicationPreview, error) {
	p, err := s.OriginScope.PreviewPublication(ctx, ref, oid, dest)
	return api.PublicationPreview{Project: p.Project, URL: p.URL, SourceRef: p.SourceRef, SourceOID: p.SourceOID, DestinationRef: p.DestinationRef, DestinationOID: p.DestinationOID, Relation: p.Relation}, err
}

func (s publicationScope) PublishPublication(ctx context.Context, p api.PublicationPreview) (api.Publication, error) {
	result, err := s.OriginScope.PublishPublication(ctx, gitservice.OriginPublicationPreview{Project: p.Project, URL: p.URL, SourceRef: p.SourceRef, SourceOID: p.SourceOID, DestinationRef: p.DestinationRef, DestinationOID: p.DestinationOID, Relation: p.Relation})
	return api.Publication{Preview: p, Status: result.Status}, err
}
