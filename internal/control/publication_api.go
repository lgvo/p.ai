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

// publicationInputs are a publication's inputs besides its target: the
// origin and the exact source the caller chose.
type publicationInputs struct {
	ExpectedOriginURL string `json:"expected_origin_url"`
	Kind              string `json:"kind"`
	Source            string `json:"source"`
	SourceOID         string `json:"source_oid"`
}

// Publish answers publication r, a risky change. Its dry run contacts the
// currently configured origin, which refreshes P's view of it, and plans the
// publication of the source's exact tip; a divergent destination refuses it.
// A run records the publication as an operation of kind origin.publish and
// queues it: its worker publishes (RunPublication).
func (a OriginAPI) Publish(ctx context.Context, r api.OriginPublishRequest) (api.ChangeResult[api.PublicationPreview], error) {
	req := PublicationRequest{ExpectedOriginURL: r.ExpectedOriginURL, Key: r.Key, Project: r.Project, Kind: r.Kind, Source: r.Source,
		SourceOID: r.SourceOID, DestinationRef: r.DestinationRef, IfMatch: r.IfMatch}
	if !validPublicationRequest(req) {
		return api.ChangeResult[api.PublicationPreview]{}, ErrInvalid
	}
	runner, ok := a.Runner.(PublicationRunner)
	if !ok {
		return api.ChangeResult[api.PublicationPreview]{}, NotConfigured(api.ReasonGit)
	}
	instance, err := a.Store.InstanceID(ctx)
	if err != nil {
		return api.ChangeResult[api.PublicationPreview]{}, err
	}
	request, err := json.Marshal(req)
	if err != nil {
		return api.ChangeResult[api.PublicationPreview]{}, err
	}
	var scope PublicationScope
	change := Change[api.PublicationPreview]{
		Action:   api.OriginPublish.Name,
		Target:   api.TargetOriginBranch(r.Project, strings.TrimPrefix(r.DestinationRef, "refs/heads/")),
		Instance: instance,
		Inputs:   publicationInputs{ExpectedOriginURL: r.ExpectedOriginURL, Kind: r.Kind, Source: r.Source, SourceOID: r.SourceOID},
		Hold: func(ctx context.Context, fn func(context.Context) error) error {
			return runner.WithPublication(ctx, r.Project, func(held PublicationScope) error {
				if err := a.Store.lockGitAuthority(ctx); err != nil {
					return err
				}
				defer a.Store.gitAuthority.Unlock()
				scope = held
				return fn(ctx)
			})
		},
		Plan: func(ctx context.Context, p *api.Plan[api.PublicationPreview]) error {
			p.Details = api.PublicationPreview{Project: r.Project, SourceKind: r.Kind, Source: r.Source, SourceOID: r.SourceOID, DestinationRef: r.DestinationRef, WorkspaceStatus: "unknown"}
			preview, err := a.planPublication(ctx, scope, req, "")
			if err != nil {
				return err
			}
			p.Details = preview
			if preview.Relation == "divergent" {
				p.Refusals = append(p.Refusals, api.Refusal{Code: api.ReasonDestinationDivergent})
			}
			return nil
		},
		Replay: func(ctx context.Context) (api.Operation, bool, error) {
			return a.Store.recorded(ctx, req.Key, KindOriginPublish, request)
		},
		Begin: func(ctx context.Context, run Run[api.PublicationPreview]) (api.Operation, error) {
			return a.Store.BeginPublication(ctx, req, run.Hash, func(ctx context.Context) (string, error) {
				_, ref, err := a.publicationContext(ctx, req, "")
				return strings.TrimPrefix(ref, "refs/heads/"), err
			})
		},
	}
	result, err := change.Do(ctx, r.ChangeRequest)
	// The worker takes the origin scope and the Git authority that the run
	// held: queue it once they are released.
	if err != nil || result.Operation == nil || result.Operation.Status != "running" {
		return result, err
	}
	return result, a.Queue(result.Operation.ID)
}

// planPublication observes the origin in scope and compares the destination
// with the source of publication r. except is the publication's own
// operation, which holds a session source.
func (a OriginAPI) planPublication(ctx context.Context, scope PublicationScope, r PublicationRequest, except string) (api.PublicationPreview, error) {
	url, ref, err := a.publicationContext(ctx, r, except)
	if err != nil {
		return api.PublicationPreview{}, err
	}
	if err = a.observeForPublication(ctx, scope, r.Project, url); err != nil {
		return api.PublicationPreview{}, err
	}
	preview, err := scope.PreviewPublication(ctx, ref, r.SourceOID, r.DestinationRef)
	if err != nil {
		return api.PublicationPreview{}, err
	}
	preview.SourceKind, preview.Source, preview.WorkspaceStatus = r.Kind, r.Source, "unknown"
	return preview, nil
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

// RunPublication is the work of publication op. In the project's origin
// scope and under the Git authority it checks the source and origin again,
// observes the origin and compares the destination: a destination that
// diverged since the run was admitted fails the operation with reason
// changed, one that has the commit is satisfied, and otherwise it records
// push-issued before the one ordinary push. The operation completes with the
// publication in its evidence. A push whose outcome is unknown, or a resumed
// operation already at push-issued, ends failed with reason outcome_unknown
// and is never pushed again.
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
		preview, err := a.planPublication(ctx, scope, r, op.ID)
		if err != nil {
			return err
		}
		result := api.Publication{Preview: preview}
		switch preview.Relation {
		case "divergent":
			// The run was admitted on a plan that did not diverge.
			return a.endPublication(ctx, &op, "failed", api.ReasonChanged, result, errors.New("the origin branch diverged from the source"))
		case "equal", "destination_contains":
			result.Status = "satisfied"
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

// addPublicationMethods serves origin.publish.
func addPublicationMethods(t *MethodTable, origins OriginAPI) {
	Register(t, api.OriginPublish, origins.Publish)
}
