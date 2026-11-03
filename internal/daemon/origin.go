package daemon

import (
	"context"
	"github.com/lgvo/p.ai/internal/api"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
)

type originRunner struct{ backend *gitservice.Backend }

// processOriginChange runs an origin change operation.
func (l *lifecycle) processOriginChange(op api.Operation) {
	if op.Status == "running" {
		l.origins.RunOriginChange(l.ctx, op)
	}
}

// processPublication runs a publication operation.
func (l *lifecycle) processPublication(op api.Operation) {
	if op.Status == "running" {
		l.origins.RunPublication(l.ctx, op)
	}
}

// newGitLifecycle is the lifecycle of a daemon with Git and no runtime: it
// runs only the journal's origin operations.
func newGitLifecycle(ctx context.Context, store *control.Store, git *gitCapability) *lifecycle {
	l := &lifecycle{store: store, git: git, ctx: ctx, gitOnly: true, working: map[string]bool{}, queueSlots: make(chan struct{}, 8),
		sessionLocks: map[string]chan struct{}{}, changedPolicies: map[string]bool{}}
	store.ObserveJournal(l)
	return l
}

func (r originRunner) ValidateURL(url string) error { return gitservice.ValidateOriginURL(url) }

func (r originRunner) WithOrigin(ctx context.Context, project string, fn func(control.OriginObserver) error) error {
	return withOriginScope(ctx, r.backend, project, func(scope *gitservice.OriginScope) error { return fn(scope) })
}

func (r originRunner) WithPublication(ctx context.Context, project string, fn func(control.PublicationScope) error) error {
	return withOriginScope(ctx, r.backend, project, func(scope *gitservice.OriginScope) error { return fn(publicationScope{scope}) })
}

// withOriginScope runs fn in project's origin scope. A failure before fn runs
// is the source-Git plugin's, which the backend checks before it takes the
// scope; fn's own errors keep their answers.
func withOriginScope(ctx context.Context, backend *gitservice.Backend, project string, fn func(*gitservice.OriginScope) error) error {
	ran := false
	err := backend.WithOrigin(ctx, project, func(scope *gitservice.OriginScope) error {
		ran = true
		return fn(scope)
	})
	if err != nil && !ran {
		return control.Failed(api.ReasonPlugin, err)
	}
	return err
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
