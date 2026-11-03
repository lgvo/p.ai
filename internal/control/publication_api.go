package control

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
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
		url, ref, err := a.publicationContext(ctx, r, "")
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
// except is the publication's own operation, which holds a session source.
func (a OriginAPI) publicationContext(ctx context.Context, r PublicationRequest, except string) (string, string, error) {
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
	ref, err := a.Store.PublicationSource(ctx, r, except)
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

// Publish records publication r as an operation of kind origin.publish and
// queues it, once the project has the expected origin and the source can be
// published: its worker publishes (RunPublication).
func (a OriginAPI) Publish(ctx context.Context, r PublicationRequest) (api.Operation, error) {
	if !validPublicationRequest(r) || r.Key == "" || len(r.Key) > 128 {
		return api.Operation{}, ErrInvalid
	}
	if _, ok := a.Runner.(PublicationRunner); !ok {
		return api.Operation{}, NotConfigured(api.ReasonGit)
	}
	op, err := a.Store.BeginPublication(ctx, r, func(ctx context.Context) (string, error) {
		_, ref, err := a.publicationContext(ctx, r, "")
		return strings.TrimPrefix(ref, "refs/heads/"), err
	})
	if err != nil || op.Status != "running" {
		return op, err
	}
	return op, a.Queue(op.ID)
}

// RunPublication is the work of publication op. In the project's origin
// scope and under the Git authority it checks the source and origin again,
// observes the origin and compares the destination: a destination that
// diverged is refused, one that has the commit is satisfied, and otherwise
// it records push-issued before the one ordinary push. The operation
// completes with the publication in its evidence. A push whose outcome is
// unknown, or a resumed operation already at push-issued, ends failed with
// reason outcome_unknown and is never pushed again.
func (a OriginAPI) RunPublication(ctx context.Context, op api.Operation) {
	var r PublicationRequest
	runner, ok := a.Runner.(PublicationRunner)
	if json.Unmarshal(op.Request, &r) != nil || !ok {
		a.fail(ctx, op, ErrInvalid)
		return
	}
	if op.Phase == "push-issued" {
		a.fail(ctx, op, OutcomeUnknown(errors.New("the daemon stopped after issuing the push")))
		return
	}
	err := runner.WithPublication(ctx, r.Project, func(scope PublicationScope) error {
		if err := a.Store.lockGitAuthority(ctx); err != nil {
			return err
		}
		defer a.Store.gitAuthority.Unlock()
		url, ref, err := a.publicationContext(ctx, r, op.ID)
		if err != nil {
			return err
		}
		if err = a.observeForPublication(ctx, scope, r.Project, url); err != nil {
			return err
		}
		preview, err := scope.PreviewPublication(ctx, ref, r.SourceOID, r.DestinationRef)
		if err != nil {
			return err
		}
		preview.SourceKind, preview.Source, preview.WorkspaceStatus = r.Kind, r.Source, "unknown"
		result := api.Publication{Preview: preview}
		switch preview.Relation {
		case "divergent":
			result.Status = "refused"
		case "equal", "destination_contains":
			result.Status = "satisfied"
		}
		if result.Status != "" {
			return a.endPublication(ctx, &op, "completed", "", result, nil)
		}
		// Record the uncertainty boundary before any possible network effect.
		result.Status = "outcome_unknown"
		evidence, err := json.Marshal(api.PublicationEvidence{Publication: result})
		if err != nil {
			return err
		}
		if err = a.Store.AdvanceOperation(ctx, op.ID, "running", "push-issued", false, evidence, "", ""); err != nil {
			return err
		}
		op.Phase, op.Evidence = "push-issued", evidence
		pushed, err := scope.PublishPublication(ctx, preview)
		if err != nil {
			// The source broker returns an error only when no native push
			// started; a started push that failed is an outcome_unknown result.
			op.Phase = "compared"
			return a.endPublication(ctx, &op, "failed", api.ReasonOrigin, api.Publication{Preview: preview}, err)
		}
		op.Committed = true
		pushed.Preview.SourceKind, pushed.Preview.Source, pushed.Preview.WorkspaceStatus = r.Kind, r.Source, "unknown"
		if pushed.Status == "outcome_unknown" {
			return a.endPublication(ctx, &op, "failed", api.ReasonOutcomeUnknown, pushed, errors.New("push outcome unknown"))
		}
		return a.endPublication(ctx, &op, "completed", "", pushed, nil)
	})
	if err != nil {
		a.fail(ctx, op, err)
	}
}

// endPublication records publication op's end, status with reason, and
// result as its evidence; cause is the failure's detail.
func (a OriginAPI) endPublication(ctx context.Context, op *api.Operation, status, reason string, result api.Publication, cause error) error {
	evidence, err := json.Marshal(api.PublicationEvidence{Publication: result})
	if err != nil {
		return err
	}
	phase, diagnostic := "completed", ""
	if status == "failed" {
		phase, diagnostic = op.Phase, cause.Error()
	}
	if err = a.Store.AdvanceOperation(ctx, op.ID, status, phase, op.Committed, evidence, reason, diagnostic); err != nil {
		log.Printf("recording the end of publication %s: %v", op.ID, err)
	}
	return nil
}

// addPublicationMethods serves origin.publication.preview and origin.publish.
func addPublicationMethods(t *MethodTable, origins OriginAPI) {
	Register(t, api.OriginPublicationPreview, func(ctx context.Context, r api.OriginPublicationPreviewRequest) (api.PublicationPreviewResult, error) {
		preview, e := origins.PreviewPublication(ctx, PublicationRequest{ExpectedOriginURL: r.ExpectedOriginURL, Project: r.Project, Kind: r.Kind, Source: r.Source, SourceOID: r.SourceOID, DestinationRef: r.DestinationRef})
		return api.PublicationPreviewResult{Preview: preview}, e
	})
	Register(t, api.OriginPublish, func(ctx context.Context, r api.OriginPublishRequest) (api.OperationResult, error) {
		return operationResult(origins.Publish(ctx, PublicationRequest(r)))
	})
}
