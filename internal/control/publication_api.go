package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/sourcedriver"
)

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
	result, err := a.publish(ctx, r)
	// The worker takes the origin lock and the Git authority that the run
	// held: queue it once they are released.
	if err != nil || result.Operation == nil || result.Operation.Status != "running" {
		return result, err
	}
	return result, a.Queue(result.Operation.ID)
}

// publish answers publication r: the plan of a dry run, or the operation a
// run recorded.
func (a OriginAPI) publish(ctx context.Context, r api.OriginPublishRequest) (api.ChangeResult[api.PublicationPreview], error) {
	req := PublicationRequest{ExpectedOriginURL: r.ExpectedOriginURL, Key: r.Key, Project: r.Project, Kind: r.Kind, Source: r.Source,
		SourceOID: r.SourceOID, DestinationRef: r.DestinationRef, IfMatch: r.IfMatch}
	if !validPublicationRequest(req) {
		return api.ChangeResult[api.PublicationPreview]{}, ErrInvalid
	}
	instance, err := a.Store.InstanceID(ctx)
	if err != nil {
		return api.ChangeResult[api.PublicationPreview]{}, err
	}
	request, err := json.Marshal(req)
	if err != nil {
		return api.ChangeResult[api.PublicationPreview]{}, err
	}
	replay := func(ctx context.Context) (api.Operation, bool, error) {
		return a.Store.recorded(ctx, req.Key, KindOriginPublish, request)
	}
	// A recorded run is answered from SQLite alone, before the source driver
	// checks the request's names.
	if !r.DryRun() {
		op, found, err := replay(ctx)
		if err != nil {
			return api.ChangeResult[api.PublicationPreview]{}, err
		}
		if found {
			return api.ChangeResult[api.PublicationPreview]{Operation: &op}, nil
		}
	}
	destination, err := a.checkPublicationNames(ctx, req)
	if err != nil {
		return api.ChangeResult[api.PublicationPreview]{}, err
	}
	change := Change[api.PublicationPreview]{
		Action:   api.OriginPublish.Name,
		Target:   api.TargetOriginBranch(r.Project, destination),
		Instance: instance,
		Inputs:   publicationInputs{ExpectedOriginURL: r.ExpectedOriginURL, Kind: r.Kind, Source: r.Source, SourceOID: r.SourceOID},
		Hold: func(ctx context.Context, fn func(context.Context) error) error {
			return a.holdPublication(ctx, r.Project, fn)
		},
		Plan: func(ctx context.Context, p *api.Plan[api.PublicationPreview]) error {
			p.Details = api.PublicationPreview{Project: r.Project, SourceKind: r.Kind, Source: r.Source, SourceOID: r.SourceOID, DestinationRef: r.DestinationRef, WorkspaceStatus: "unknown"}
			planned, err := a.planPublication(ctx, req, destination, "")
			if err != nil {
				return err
			}
			p.Details = planned.Preview
			if planned.Push.Relation == sourcedriver.RelationDivergent {
				p.Refusals = append(p.Refusals, api.Refusal{Code: api.ReasonDestinationDivergent})
			}
			return nil
		},
		Replay: replay,
		Begin: func(ctx context.Context, run Run[api.PublicationPreview]) (api.Operation, error) {
			return a.Store.BeginPublication(ctx, req, run.Hash, func(ctx context.Context) (string, error) {
				_, branch, err := a.publicationContext(ctx, req, "")
				return branch, err
			})
		},
	}
	return change.Do(ctx, r.ChangeRequest)
}

// holdPublication runs fn under project's origin lock and the Git
// authority, so the source branch, its assignment and the origin stay as fn
// observes them.
func (a OriginAPI) holdPublication(ctx context.Context, project string, fn func(context.Context) error) error {
	release, err := a.Store.LockOrigin(ctx, project)
	if err != nil {
		return err
	}
	defer release()
	if err = a.Store.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer a.Store.gitAuthority.Unlock()
	return fn(ctx)
}

// checkPublicationNames has the source driver check publication r's names:
// a retained source branch, the source commit and the destination. It
// returns the destination as the origin branch's name.
func (a OriginAPI) checkPublicationNames(ctx context.Context, r PublicationRequest) (string, error) {
	var fields []NameField
	if r.Kind == "retained" {
		fields = append(fields, NameField{"source", sourcedriver.Name{Kind: sourcedriver.NameBranch, Value: r.Source}})
	}
	fields = append(fields,
		NameField{"source_oid", sourcedriver.Name{Kind: sourcedriver.NameCommit, Value: r.SourceOID}},
		NameField{"destination_ref", sourcedriver.Name{Kind: sourcedriver.NameDestination, Value: r.DestinationRef}})
	names, err := CheckNames(ctx, a.Driver, fields...)
	if err != nil {
		return "", err
	}
	return names[len(names)-1].Value, nil
}

// plannedPublication is a publication core compared: the plan's details,
// and the push they lead to, whose Relation is the comparison's.
type plannedPublication struct {
	Preview api.PublicationPreview
	Push    sourcedriver.Publication
}

// planPublication observes the origin and compares the destination with the
// source of publication r, whose destination is the origin branch
// destination. except is the publication's own operation, which holds a
// session source. The caller holds the project's origin lock.
func (a OriginAPI) planPublication(ctx context.Context, r PublicationRequest, destination, except string) (plannedPublication, error) {
	url, branch, err := a.publicationContext(ctx, r, except)
	if err != nil {
		return plannedPublication{}, err
	}
	observed, err := a.observeForPublication(ctx, r.Project, url)
	if err != nil {
		return plannedPublication{}, err
	}
	var dest *sourcedriver.OriginRef
	for i := range observed {
		if observed[i].Ref == r.DestinationRef {
			dest = &observed[i]
		}
	}
	source := sourcedriver.OID(r.SourceOID)
	relation, err := a.comparePublication(ctx, r.Project, branch, url, source, dest)
	if err != nil {
		return plannedPublication{}, err
	}
	// The details show the source as the full ref the wire format carries.
	preview := api.PublicationPreview{Project: r.Project, URL: url, SourceKind: r.Kind, Source: r.Source, SourceRef: "refs/heads/" + branch,
		SourceOID: r.SourceOID, DestinationRef: r.DestinationRef, Relation: string(relation), WorkspaceStatus: "unknown"}
	if dest != nil {
		preview.DestinationOID = string(dest.OID)
	}
	return plannedPublication{Preview: preview,
		Push: sourcedriver.Publication{URL: url, Branch: branch, Commit: source, Destination: destination, Relation: relation}}, nil
}

// comparePublication checks that P branch branch still names source, then
// has the source driver compare dest, the destination as observed or nil
// when the origin has none, with it. The read and the comparison share the
// deadline of one call to an origin (CallOrigin), so a stalled read holds
// the origin lock and the Git authority no longer than a stalled origin
// does.
func (a OriginAPI) comparePublication(ctx context.Context, project, branch, url string, source sourcedriver.OID, dest *sourcedriver.OriginRef) (sourcedriver.Relation, error) {
	bounded, cancel := context.WithTimeout(ctx, a.Store.originCallTimeout)
	defer cancel()
	// failed answers a step's failure as authority's: one the shared
	// deadline cut off never as a context error the caller would take for
	// its own.
	failed := func(authority string, err error) error {
		if ctx.Err() == nil && bounded.Err() != nil {
			err = fmt.Errorf("the publication's comparison did not end within %s", a.Store.originCallTimeout)
		}
		return Failed(authority, err)
	}
	tip, exists, err := a.Driver.InspectBranch(bounded, project, branch)
	if err != nil {
		return "", failed(api.ReasonGit, err)
	}
	if !exists {
		return "", NotFound(api.ReasonBranch, api.TargetBranch(project, branch))
	}
	if tip != source {
		// source_oid is no longer the source's tip.
		return "", Stale(api.ReasonChanged)
	}
	var relation sourcedriver.Relation
	err = a.Store.CallOrigin(bounded, func(ctx context.Context) (err error) {
		relation, err = a.Driver.ComparePublication(ctx, project, url, source, dest)
		return err
	})
	if errors.Is(err, sourcedriver.ErrInvalid) {
		return "", Invalid("source_oid")
	}
	if err != nil {
		return "", failed(api.ReasonOrigin, err)
	}
	return relation, nil
}

// publicationContext returns the origin URL and the source's P branch. A
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
	branch, err := a.Store.PublicationSource(ctx, r, except)
	if err != nil {
		return "", "", err
	}
	return state.URL, branch, nil
}

// observeForPublication observes the origin at url afresh and records the
// observation, or marks the last one unknown when the origin cannot be
// observed.
func (a OriginAPI) observeForPublication(ctx context.Context, project, url string) ([]sourcedriver.OriginRef, error) {
	refs, err := a.observe(ctx, url)
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err != nil {
		if storeErr := a.Store.StoreOriginRefresh(writeCtx, project, url, nil, "origin refresh failed; check SSH identity, host key, URL and access"); storeErr != nil {
			return nil, storeErr
		}
		return nil, Failed(api.ReasonOrigin, err)
	}
	return refs, a.Store.StoreOriginRefresh(writeCtx, project, url, refs, "")
}

// RunPublication is the work of publication op. Under the project's origin
// lock and the Git authority it checks the source and origin again,
// observes the origin and compares the destination: a destination that
// diverged since the run was admitted fails the operation with reason
// changed, and one that has the commit is satisfied. Otherwise the source
// driver pushes once, recording push-issued at its gate right before the
// push. The operation completes with the publication in its evidence. A push
// whose outcome is unknown, or a resumed operation already at push-issued,
// ends failed with reason outcome_unknown and is never pushed again.
func (a OriginAPI) RunPublication(ctx context.Context, op api.Operation) {
	var r PublicationRequest
	if json.Unmarshal(op.Request, &r) != nil {
		a.fail(ctx, op, ErrInvalid)
		return
	}
	if op.Phase == "push-issued" {
		a.fail(ctx, op, OutcomeUnknown(errors.New("the daemon stopped after issuing the push")))
		return
	}
	destination, err := a.checkPublicationNames(ctx, r)
	if err == nil {
		err = a.holdPublication(ctx, r.Project, func(ctx context.Context) error {
			return a.publishHeld(ctx, &op, r, destination)
		})
	}
	if err != nil {
		a.fail(ctx, op, err)
	}
}

// publishHeld compares and pushes publication op of r to the origin branch
// destination, and records its end. The caller holds the project's origin
// lock and the Git authority.
func (a OriginAPI) publishHeld(ctx context.Context, op *api.Operation, r PublicationRequest, destination string) error {
	planned, err := a.planPublication(ctx, r, destination, op.ID)
	if err != nil {
		return err
	}
	result := api.Publication{Preview: planned.Preview}
	switch planned.Push.Relation {
	case sourcedriver.RelationDivergent:
		// The run was admitted on a plan that did not diverge.
		return a.endPublication(ctx, op, "failed", api.ReasonChanged, result, errors.New("the origin branch diverged from the source"))
	case sourcedriver.RelationEqual, sourcedriver.RelationDestinationContains:
		result.Status = "satisfied"
		return a.endPublication(ctx, op, "completed", "", result, nil)
	}
	// The gate records the uncertainty boundary right before the push, with
	// the worker's context: the push's own may end first.
	var gateErr error
	var status sourcedriver.PublishStatus
	err = a.Store.CallOrigin(ctx, func(callCtx context.Context) (err error) {
		status, err = a.Driver.Publish(callCtx, r.Project, planned.Push, func() error {
			gateErr = a.issuePush(ctx, op, api.Publication{Preview: planned.Preview, Status: "outcome_unknown"})
			return gateErr
		})
		return err
	})
	switch {
	case gateErr != nil:
		return gateErr
	case errors.Is(err, sourcedriver.ErrUnknownOutcome):
		op.Committed = true
		result.Status = "outcome_unknown"
		return a.endPublication(ctx, op, "failed", api.ReasonOutcomeUnknown, result, err)
	case err != nil:
		// No push started: nothing reached the origin.
		op.Phase = "compared"
		return a.endPublication(ctx, op, "failed", api.ReasonOrigin, result, err)
	}
	op.Committed = true
	result.Status = string(status)
	return a.endPublication(ctx, op, "completed", "", result, nil)
}

// issuePush records publication op at push-issued, with evidence its
// publication whose outcome is unknown until the push answers.
func (a OriginAPI) issuePush(ctx context.Context, op *api.Operation, unknown api.Publication) error {
	evidence, err := json.Marshal(api.PublicationEvidence{Publication: unknown})
	if err != nil {
		return err
	}
	if err = a.Store.AdvanceOperation(ctx, op.ID, "running", "push-issued", false, evidence, "", ""); err != nil {
		return err
	}
	op.Phase, op.Evidence = "push-issued", evidence
	return nil
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
