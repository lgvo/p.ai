package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/plugin"
	sourcememory "github.com/lgvo/p.ai/internal/sourcedriver/memory"
)

type fakePublication struct {
	observeHook      func()
	observes, pushes int
	relation, status string
	failure          error
	publishError     error
}

func (f *fakePublication) ValidateURL(string) error { return nil }
func (f *fakePublication) WithOrigin(ctx context.Context, project string, fn func(OriginObserver) error) error {
	return fn(f)
}
func (f *fakePublication) WithPublication(ctx context.Context, project string, fn func(PublicationScope) error) error {
	return fn(f)
}
func (f *fakePublication) Observe(context.Context, string) ([]plugin.GitOriginRef, error) {
	f.observes++
	if f.observeHook != nil {
		f.observeHook()
	}
	return []plugin.GitOriginRef{}, f.failure
}
func (f *fakePublication) PreviewPublication(_ context.Context, ref, oid, dest string) (api.PublicationPreview, error) {
	return api.PublicationPreview{Project: "app", URL: "ssh://host/repo", SourceRef: ref, SourceOID: oid, DestinationRef: dest, Relation: f.relation}, nil
}
func (f *fakePublication) PublishPublication(_ context.Context, p api.PublicationPreview) (api.Publication, error) {
	if f.publishError != nil {
		return api.Publication{}, f.publishError
	}
	f.pushes++
	return api.Publication{Preview: p, Status: f.status}, nil
}

// publicationOf returns the publication publication operation op records as
// its evidence.
func publicationOf(t *testing.T, op api.Operation) api.Publication {
	t.Helper()
	var evidence api.PublicationEvidence
	if err := json.Unmarshal(op.Evidence, &evidence); err != nil {
		t.Fatalf("publication %s evidence %s: %v", op.ID, op.Evidence, err)
	}
	return evidence.Publication
}

// publishRequest is publication r as an origin.publish call with change.
func publishRequest(r PublicationRequest, change api.ChangeRequest) api.OriginPublishRequest {
	return api.OriginPublishRequest{ExpectedOriginURL: r.ExpectedOriginURL, Project: r.Project, Kind: r.Kind, Source: r.Source,
		SourceOID: r.SourceOID, DestinationRef: r.DestinationRef, ChangeRequest: change}
}

// planPublication is the dry run of publication r.
func planPublication(a *OriginAPI, r PublicationRequest) (api.ChangeResult[api.PublicationPreview], error) {
	return a.Publish(context.Background(), publishRequest(r, api.ChangeRequest{}))
}

// runPublication runs publication r under r.Key with change tag tag.
func runPublication(a *OriginAPI, r PublicationRequest, tag string) (api.Operation, error) {
	result, err := a.Publish(context.Background(), publishRequest(r, api.ChangeRequest{Key: r.Key, IfMatch: tag}))
	if err != nil {
		return api.Operation{}, err
	}
	return *result.Operation, nil
}

// publish publishes r as a client does: a dry run, then a run under r.Key
// with the dry run's change tag. It returns the operation and the tag, which
// replays the run.
func publish(a *OriginAPI, r PublicationRequest) (api.Operation, string, error) {
	plan, err := planPublication(a, r)
	if err != nil {
		return api.Operation{}, "", err
	}
	if plan.ChangeTag == "" {
		return api.Operation{}, "", fmt.Errorf("publication not allowed: %+v", plan.Plan.Refusals)
	}
	op, err := runPublication(a, r, plan.ChangeTag)
	return op, plan.ChangeTag, err
}

// TestPublicationPrestartFailureNeedsANewKey checks that a push that failed
// before any native push started ends the operation failed with reason
// origin: its run returns that operation again, and a new key publishes.
func TestPublicationPrestartFailureNeedsANewKey(t *testing.T) {
	_, f, a, r := publicationFixture(t)
	read := readBack(t, a)
	f.publishError = errors.New("source plugin unavailable before push")
	op, tag, err := publish(a, r)
	failed := read(op, err)
	wantEnded(t, "prestart failure", failed, "failed", api.ReasonOrigin)
	// Nothing was pushed: no commitment, and the evidence decides no outcome.
	// The dry run, the run and the worker each read the origin.
	if failed.Committed || failed.Phase == "push-issued" || publicationOf(t, failed).Status != "" || f.pushes != 0 || f.observes != 3 {
		t.Fatalf("prestart failure: pushes=%d observes=%d", f.pushes, f.observes)
	}
	f.publishError = nil
	if replay := read(runPublication(a, r, tag)); replay.ID != failed.ID || replay.Status != "failed" || f.pushes != 0 || f.observes != 3 {
		t.Fatalf("replay: %+v pushes=%d observes=%d", replay, f.pushes, f.observes)
	}
	r.Key = "publish-again"
	op, _, err = publish(a, r)
	if again := read(op, err); again.Status != "completed" || publicationOf(t, again).Status != "advanced" || f.pushes != 1 {
		t.Fatalf("new key: %+v pushes=%d", again, f.pushes)
	}
}

func publicationFixture(t *testing.T) (*Store, *fakePublication, *OriginAPI, PublicationRequest) {
	t.Helper()
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO project_origins(project_path,url,current_status,observed_at,refs_json) VALUES('app','ssh://host/repo','fresh','now','[]')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256) VALUES('550e8400-e29b-41d4-a716-446655440000','app','work','established','{}','hash')`); err != nil {
		t.Fatal(err)
	}
	f := &fakePublication{relation: "fast_forward", status: "advanced"}
	// The store's own tests record r with its tag; a client's run takes the
	// tag from its dry run (publish).
	r := PublicationRequest{ExpectedOriginURL: "ssh://host/repo", Key: "publish-once", Project: "app", Kind: "session", Source: "550e8400-e29b-41d4-a716-446655440000", SourceOID: strings.Repeat("a", 40), DestinationRef: "refs/heads/main", IfMatch: testTag(time.Hour)}
	a := &OriginAPI{Store: s, Runner: f, Driver: sourcememory.NewDriver(sourcememory.NewWorld())}
	runQueued(a)
	return s, f, a, r
}

// TestPublicationRefusesNamesTheSourceDriverRefuses checks that the dry run
// and the run of a publication whose retained source branch, source commit
// or destination the source driver refuses answer invalid naming that field,
// before the origin is contacted or anything is recorded.
func TestPublicationRefusesNamesTheSourceDriverRefuses(t *testing.T) {
	for _, test := range []struct {
		field  string
		change func(r *PublicationRequest)
	}{
		{"source", func(r *PublicationRequest) { r.Kind, r.Source = "retained", "a..b" }},
		{"source_oid", func(r *PublicationRequest) { r.SourceOID = "not-a-commit" }},
		{"destination_ref", func(r *PublicationRequest) { r.DestinationRef = "refs/tags/v1" }},
	} {
		s, f, a, r := publicationFixture(t)
		ctx := context.Background()
		test.change(&r)
		for _, change := range []api.ChangeRequest{{}, {Key: r.Key, IfMatch: r.IfMatch}} {
			_, err := a.Publish(ctx, publishRequest(r, change))
			if answer := answerError(ctx, err); answer == nil || answer.Kind() != api.KindInvalid || answer.Data.Field != test.field {
				t.Errorf("a publication with an invalid %s (run %t) answered %v", test.field, change.Key != "", err)
			}
		}
		if _, err := s.GetOperationByKey(ctx, r.Key); !errors.Is(err, ErrNotFound) || f.observes != 0 {
			t.Errorf("a publication with an invalid %s recorded its key (%v) or read the origin %d times", test.field, err, f.observes)
		}
	}
}

// TestPublicationReplaysARecordedRunWithoutTheSourceDriver checks that a
// recorded run is answered from SQLite alone: its operation comes back while
// the source driver is down.
func TestPublicationReplaysARecordedRunWithoutTheSourceDriver(t *testing.T) {
	_, f, a, r := publicationFixture(t)
	read := readBack(t, a)
	op, tag, err := publish(a, r)
	first := read(op, err)
	a.Driver.(*sourcememory.Driver).Kill()
	if replay := read(runPublication(a, r, tag)); replay.ID != first.ID || replay.Status != "completed" || f.pushes != 1 {
		t.Fatalf("replay with the source driver down: %+v pushes=%d", replay, f.pushes)
	}
}
func TestPublicationDurableReplayAndCrossJournalKeys(t *testing.T) {
	s, f, a, r := publicationFixture(t)
	ctx := context.Background()
	read := readBack(t, a)
	op, tag, err := publish(a, r)
	first := read(op, err)
	if p := publicationOf(t, first); first.Status != "completed" || p.Status != "advanced" || p.Preview.SourceRef != "refs/heads/work" || f.pushes != 1 || f.observes != 3 {
		t.Fatalf("first: %+v pushes=%d observes=%d", first, f.pushes, f.observes)
	}
	again := read(runPublication(a, r, tag))
	if again.ID != first.ID || publicationOf(t, again).Status != "advanced" || f.pushes != 1 || f.observes != 3 {
		t.Fatalf("replay: %+v pushes=%d observes=%d", again, f.pushes, f.observes)
	}
	different := r
	different.DestinationRef = "refs/heads/other"
	if _, err := runPublication(a, different, tag); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed key: %v", err)
	}
	var recorded string
	if err := s.db.QueryRowContext(ctx, `SELECT change_hash FROM operations WHERE id=?`, first.ID).Scan(&recorded); err != nil || !validFingerprint(recorded) || strings.Contains(string(first.Request)+string(first.Evidence), recorded) {
		t.Fatalf("publication recorded change hash %q, or shows it: %v", recorded, err)
	}
	// Keys share the journal's one namespace.
	if _, err := a.Change(ctx, OriginChange{Key: r.Key, Project: "app", Kind: "remove", ExpectedURL: r.ExpectedOriginURL}); !errors.Is(err, ErrConflict) {
		t.Fatalf("origin key: %v", err)
	}
	if _, err := s.BeginBlankProject(ctx, BlankProjectRequest{Key: r.Key, Project: "new"}, json.RawMessage(`{}`), strings.Repeat("d", 64), testSelection()); !errors.Is(err, ErrConflict) {
		t.Fatalf("lifecycle key: %v", err)
	}
}

func TestPublicationReplayAcrossStoreRestart(t *testing.T) {
	s, f, a, r := publicationFixture(t)
	read := readBack(t, a)
	op, tag, err := publish(a, r)
	first := read(op, err)
	if first.Status != "completed" || publicationOf(t, first).Status != "advanced" {
		t.Fatalf("first: %+v", first)
	}
	dir := s.StateDir()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	a.Store = reopened
	result := read(runPublication(a, r, tag))
	if result.ID != first.ID || publicationOf(t, result).Status != "advanced" || f.pushes != 1 || f.observes != 3 {
		t.Fatalf("restart replay: %+v pushes=%d observes=%d", result, f.pushes, f.observes)
	}
}

// TestPublicationResumedAfterPushIssuedNeverPushes checks that a publication
// the daemon resumes once it recorded push-issued ends failed with reason
// outcome_unknown, without contacting the origin; its run returns that
// operation again.
func TestPublicationResumedAfterPushIssuedNeverPushes(t *testing.T) {
	s, f, a, r := publicationFixture(t)
	ctx := context.Background()
	read := readBack(t, a)
	run := a.Queue
	a.Queue = func(string) error { return nil }
	op, tag, err := publish(a, r)
	if err != nil {
		t.Fatal(err)
	}
	// The daemon stopped right after its worker recorded the push boundary.
	p := api.Publication{Preview: api.PublicationPreview{Project: r.Project, SourceOID: r.SourceOID, DestinationRef: r.DestinationRef}, Status: "outcome_unknown"}
	evidence, _ := json.Marshal(api.PublicationEvidence{Publication: p})
	if err = s.AdvanceOperation(ctx, op.ID, "running", "push-issued", false, evidence, "", ""); err != nil {
		t.Fatal(err)
	}
	a.Queue = run
	observed := f.observes
	if err = a.Queue(op.ID); err != nil {
		t.Fatal(err)
	}
	resumed := read(op, nil)
	wantEnded(t, "resumed publication", resumed, "failed", api.ReasonOutcomeUnknown)
	if resumed.Phase != "push-issued" || publicationOf(t, resumed).Status != "outcome_unknown" || f.observes != observed || f.pushes != 0 {
		t.Fatalf("resumed: %+v observes=%d pushes=%d", resumed, f.observes, f.pushes)
	}
	if replay := read(runPublication(a, r, tag)); replay.ID != op.ID || replay.Status != "failed" || f.observes != observed || f.pushes != 0 {
		t.Fatalf("uncertain replay: %+v observes=%d pushes=%d", replay, f.observes, f.pushes)
	}
}

// TestPublicationRefusesADivergentDestination checks that the dry run of a
// publication to a divergent destination is not allowed and has no tag, and
// that a destination that diverges after its run was admitted fails the
// operation with reason changed; neither pushes.
func TestPublicationRefusesADivergentDestination(t *testing.T) {
	s, f, a, r := publicationFixture(t)
	ctx := context.Background()
	read := readBack(t, a)
	f.relation = "divergent"
	plan, err := planPublication(a, r)
	if err != nil || plan.Plan.Allowed || plan.ChangeTag != "" || !reflect.DeepEqual(plan.Plan.Refusals, []api.Refusal{{Code: api.ReasonDestinationDivergent}}) ||
		plan.Plan.Details.Relation != "divergent" || plan.Plan.Target != "project/app:origin-branch/main" {
		t.Fatalf("divergent plan: %+v %v", plan, err)
	}
	f.relation = "fast_forward"
	plan, err = planPublication(a, r)
	if err != nil || plan.ChangeTag == "" {
		t.Fatalf("fast-forward plan: %+v %v", plan, err)
	}
	f.relation = "divergent"
	if _, err = runPublication(a, r, plan.ChangeTag); !answers(err, api.KindPreconditionFailed, api.ReasonChanged) {
		t.Fatalf("run of a plan that diverged answered %v", err)
	}
	if _, err = s.GetOperationByKey(ctx, r.Key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refused run recorded its key: %v", err)
	}
	// It diverges between the run's admission and its worker.
	f.relation = "fast_forward"
	plan, _ = planPublication(a, r)
	run := a.Queue
	a.Queue = func(string) error { return nil }
	op, err := runPublication(a, r, plan.ChangeTag)
	if err != nil {
		t.Fatal(err)
	}
	f.relation = "divergent"
	if err = run(op.ID); err != nil {
		t.Fatal(err)
	}
	failed := read(op, nil)
	wantEnded(t, "a publication that diverged after admission", failed, "failed", api.ReasonChanged)
	if failed.Committed || publicationOf(t, failed).Status != "" || publicationOf(t, failed).Preview.Relation != "divergent" || f.pushes != 0 {
		t.Fatalf("diverged publication: %+v pushes=%d", failed, f.pushes)
	}
}

func TestPublicationUnknownResultNeverRepeatsPush(t *testing.T) {
	_, f, a, r := publicationFixture(t)
	f.status = "outcome_unknown"
	read := readBack(t, a)
	op, tag, err := publish(a, r)
	first := read(op, err)
	wantEnded(t, "uncertain push", first, "failed", api.ReasonOutcomeUnknown)
	if publicationOf(t, first).Status != "outcome_unknown" || f.pushes != 1 {
		t.Fatalf("first uncertain: %+v pushes=%d", first, f.pushes)
	}
	replay := read(runPublication(a, r, tag))
	if replay.ID != first.ID || replay.Status != "failed" || replay.Reason != api.ReasonOutcomeUnknown || f.pushes != 1 {
		t.Fatalf("uncertain replay: %+v pushes=%d", replay, f.pushes)
	}
}

// TestPublicationHoldsItsSession checks that an unfinished publication of a
// session holds the session against other work, but not against its own.
func TestPublicationHoldsItsSession(t *testing.T) {
	s, f, a, r := publicationFixture(t)
	ctx := context.Background()
	read := readBack(t, a)
	run := a.Queue
	a.Queue = func(string) error { return nil }
	op, _, err := publish(a, r)
	if err != nil {
		t.Fatal(err)
	}
	other := r
	other.Key, other.DestinationRef = "other", "refs/heads/other"
	_, err = planPublication(a, other)
	wantAnswer(t, "publication of a held session", err, api.KindRetry, api.ReasonSessionHeld, api.TargetOperation(op.ID))
	a.Queue = run
	if err = a.Queue(op.ID); err != nil {
		t.Fatal(err)
	}
	if done := read(op, nil); done.Status != "completed" || publicationOf(t, done).Status != "advanced" || f.pushes != 1 {
		t.Fatalf("held publication: %+v pushes=%d", done, f.pushes)
	}
	if _, err = s.GetOperationByKey(ctx, other.Key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refused publication recorded its key: %v", err)
	}
	op, _, err = publish(a, other)
	if done := read(op, err); done.Status != "completed" {
		t.Fatalf("publication once the session is free: %+v", done)
	}
}
func TestPublicationOwnershipAndRetainedSelection(t *testing.T) {
	s, f, a, r := publicationFixture(t)
	ctx := context.Background()
	retained := r
	retained.Kind = "retained"
	retained.Source = "work"
	retained.Key = "retained"
	plan, err := planPublication(a, retained)
	if err != nil || plan.Plan.Allowed || plan.ChangeTag != "" ||
		!reflect.DeepEqual(plan.Plan.Refusals, []api.Refusal{{Code: api.ReasonBranchAssigned, Target: "project/app:branch/work"}}) {
		t.Fatalf("assigned branch used as retained: %+v %v", plan, err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE uuid=?`, r.Source); err != nil {
		t.Fatal(err)
	}
	if _, err := planPublication(a, r); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed session: %v", err)
	}
	plan, err = planPublication(a, retained)
	p := plan.Plan
	if err != nil || !p.Allowed || plan.ChangeTag == "" || p.Details.SourceKind != "retained" || p.Details.SourceRef != "refs/heads/work" || len(p.Loss) != 0 || f.observes != 1 {
		t.Fatalf("retained plan: %+v %v", plan, err)
	}
}

func TestPublicationRPCAndRetainedBranchPage(t *testing.T) {
	s, f, a, r := publicationFixture(t)
	ctx := context.Background()
	a.Git = fixtureGitReader{}
	handler := NewHandler(HandlerConfig{Store: s, Git: a.Git, GitInfo: &api.GitInfo{}, Origins: a})
	request := func(method, body string) (any, *api.Error) {
		t.Helper()
		return handler(ctx, method, json.RawMessage(body))
	}
	body := `{"expected_origin_url":"ssh://host/repo","project":"app","kind":"session","source":"` + r.Source + `","source_oid":"` + r.SourceOID + `","destination_ref":"refs/heads/main"}`
	result, e := request("origin.publish", body)
	plan, _ := result.(api.ChangeResult[api.PublicationPreview])
	if e != nil || plan.Plan == nil || plan.Plan.Details.SourceRef != "refs/heads/work" || plan.ChangeTag == "" || plan.Operation != nil || f.pushes != 0 {
		t.Fatalf("dry run: %v %v", result, e)
	}
	run := strings.TrimSuffix(body, "}") + `,"key":"once","if_match":"` + plan.ChangeTag + `"}`
	published, e := request("origin.publish", run)
	if e != nil || f.pushes != 1 || published.(api.ChangeResult[api.PublicationPreview]).Plan != nil {
		t.Fatalf("publish: %v pushes=%d", e, f.pushes)
	}
	replayed, e := request("origin.publish", run)
	if e != nil || f.pushes != 1 || replayed.(api.ChangeResult[api.PublicationPreview]).Operation.ID != published.(api.ChangeResult[api.PublicationPreview]).Operation.ID {
		t.Fatalf("replay: %v %v pushes=%d", replayed, e, f.pushes)
	}
	if _, e = request("origin.publish", strings.TrimSuffix(run, "}")+`,"SOURCE":"wrong"}`); e == nil || e.Kind() != "invalid" {
		t.Fatalf("case-variant field accepted: %v", e)
	}
	page, e := callMethod(t, ctx, handler, api.ProjectRetainedBranches, api.ProjectRetainedBranchesRequest{Project: "app", Limit: 8})
	if e != nil || len(page.Branches) != 1 {
		t.Fatalf("retained page: %v %v", page, e)
	}
	_, e = callMethod(t, ctx, handler, api.ProjectRetainedBranches, api.ProjectRetainedBranchesRequest{Project: "missing", Limit: 8})
	if want := wantError(api.KindNotFound, api.ReasonProject, "project/missing"); !reflect.DeepEqual(e, want) {
		t.Fatalf("missing project's retained page answered %v, want %v", e, want)
	}
	// The fixture Git ref is main, which has no live owner. Assign it and
	// confirm that only Git refs without a session remain in the page.
	if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET branch='main' WHERE uuid=?`, r.Source); err != nil {
		t.Fatal(err)
	}
	page, e = callMethod(t, ctx, handler, api.ProjectRetainedBranches, api.ProjectRetainedBranchesRequest{Project: "app", Limit: 8})
	if e != nil || len(page.Branches) != 0 {
		t.Fatalf("assigned page: %v %v", page, e)
	}
}

func TestPublicationRejectsChangedOriginBeforeContact(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(fmt.Sprintf("prepared=%v", prepared), func(t *testing.T) {
			s, f, a, r := publicationFixture(t)
			ctx := context.Background()
			read := readBack(t, a)
			plan, err := planPublication(a, r)
			if err != nil || plan.Plan.Details.URL != r.ExpectedOriginURL {
				t.Fatalf("dry run: %+v %v", plan, err)
			}
			var failed api.Operation
			if prepared {
				f.publishError = errors.New("prestart failure")
				failed = read(runPublication(a, r, plan.ChangeTag))
				wantEnded(t, "prestart failure", failed, "failed", api.ReasonOrigin)
				f.publishError = nil
			}
			contacts := f.observes
			if _, err = s.db.ExecContext(ctx, `UPDATE project_origins SET url='ssh://host/replacement' WHERE project_path='app'`); err != nil {
				t.Fatal(err)
			}
			dir := s.StateDir()
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := testScopedOpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			a.Store = reopened
			op, err := runPublication(a, r, plan.ChangeTag)
			if prepared {
				// The key's failed operation answers; it never runs again.
				if err != nil || op.ID != failed.ID || op.Status != "failed" {
					t.Fatalf("failed key replayed %+v %v", op, err)
				}
			} else if !answers(err, api.KindPreconditionFailed, api.ReasonChanged) {
				t.Fatalf("changed origin accepted: %v", err)
			}
			if f.observes != contacts || f.pushes != 0 {
				t.Fatalf("contacted replacement: reads=%d pushes=%d", f.observes, f.pushes)
			}
			changed := r
			changed.ExpectedOriginURL = "ssh://host/replacement"
			if prepared {
				if _, err = runPublication(a, changed, plan.ChangeTag); !errors.Is(err, ErrConflict) {
					t.Fatalf("retargeted durable key: %v", err)
				}
			} else if _, err = reopened.GetOperationByKey(ctx, r.Key); !errors.Is(err, ErrNotFound) {
				// A refused request records nothing under its key.
				t.Fatalf("refused publication recorded its key: %v", err)
			}
			if _, err = planPublication(a, r); !answers(err, api.KindPreconditionFailed, api.ReasonChanged) {
				t.Fatalf("stale publication target accepted: %v", err)
			}
		})
	}
}

func TestPublicationCompletedReplayKeepsOriginalIdentity(t *testing.T) {
	s, f, a, r := publicationFixture(t)
	ctx := context.Background()
	read := readBack(t, a)
	op, tag, err := publish(a, r)
	first := read(op, err)
	if _, err := s.db.ExecContext(ctx, `UPDATE project_origins SET url='ssh://host/replacement' WHERE project_path='app'`); err != nil {
		t.Fatal(err)
	}
	again := read(runPublication(a, r, tag))
	if again.ID != first.ID || publicationOf(t, again).Preview.URL != publicationOf(t, first).Preview.URL || f.pushes != 1 || f.observes != 3 {
		t.Fatalf("replay retargeted or contacted: %+v", again)
	}
	changed := r
	changed.ExpectedOriginURL = "ssh://host/replacement"
	if _, err := runPublication(a, changed, tag); !errors.Is(err, ErrConflict) {
		t.Fatalf("completed key retargeted: %v", err)
	}
}

func TestPublicationRPCRequiresOriginIdentity(t *testing.T) {
	_, _, _, r := publicationFixture(t)
	body, _ := json.Marshal(map[string]any{"project": r.Project, "kind": r.Kind, "source": r.Source, "source_oid": r.SourceOID, "destination_ref": r.DestinationRef})
	h := NewHandler(HandlerConfig{Origins: &OriginAPI{Runner: &fakePublication{}}})
	if _, rpcErr := h(context.Background(), api.OriginPublish.Name, body); rpcErr == nil || rpcErr.Kind() != "invalid" {
		t.Fatalf("missing origin identity accepted: %+v", rpcErr)
	}
}

// TestPublicationRefusalsAndErrors checks, through the handler, each state
// that refuses a publication's plan and each error its dry run answers.
func TestPublicationRefusalsAndErrors(t *testing.T) {
	keyReused := &api.Error{Code: -32602, Message: "invalid", Data: &api.ErrorData{Reason: api.ReasonKeyReused, Field: "key"}}
	session := "550e8400-e29b-41d4-a716-446655440000"
	for _, test := range []struct {
		name    string
		prepare func(t *testing.T, s *Store, f *fakePublication, a *OriginAPI, r PublicationRequest)
		change  func(r *PublicationRequest)
		refusal *api.Refusal
		want    *api.Error
	}{
		{"local-only project", func(t *testing.T, s *Store, _ *fakePublication, _ *OriginAPI, _ PublicationRequest) {
			mustExec(t, s, `DELETE FROM project_origins WHERE project_path='app'`)
		}, nil, &api.Refusal{Code: api.ReasonOriginNotSet, Target: "project/app"}, nil},
		{"origin changed since the client read it", func(t *testing.T, s *Store, _ *fakePublication, _ *OriginAPI, _ PublicationRequest) {
			mustExec(t, s, `UPDATE project_origins SET url='ssh://host/replacement' WHERE project_path='app'`)
		}, nil, nil, wantError(api.KindPreconditionFailed, api.ReasonChanged, "")},
		{"removed session", func(t *testing.T, s *Store, _ *fakePublication, _ *OriginAPI, _ PublicationRequest) {
			mustExec(t, s, `DELETE FROM sessions WHERE uuid='`+session+`'`)
		}, nil, nil, wantError(api.KindNotFound, api.ReasonSession, "session/"+session)},
		{"assigned branch as a retained source", nil, func(r *PublicationRequest) { r.Kind, r.Source = "retained", "work" },
			&api.Refusal{Code: api.ReasonBranchAssigned, Target: "project/app:branch/work"}, nil},
		{"session held by a running operation", func(t *testing.T, s *Store, _ *fakePublication, _ *OriginAPI, _ PublicationRequest) {
			holdSession(t, s, session, "running")
		}, nil, nil, wantError(api.KindRetry, api.ReasonSessionHeld, "operation/op-holder")},
		{"session held by a blocked operation", func(t *testing.T, s *Store, _ *fakePublication, _ *OriginAPI, _ PublicationRequest) {
			holdSession(t, s, session, "blocked")
		}, nil, &api.Refusal{Code: api.ReasonOperationBlocked, Target: "operation/op-holder"}, nil},
		// The journal's one admission check names a session's unfinished
		// creation as it names any other holder.
		{"session being created", func(t *testing.T, s *Store, _ *fakePublication, _ *OriginAPI, _ PublicationRequest) {
			holdSession(t, s, session, "running")
			mustExec(t, s, `UPDATE sessions SET registry_state='creating' WHERE uuid='`+session+`'`)
		}, nil, nil, wantError(api.KindRetry, api.ReasonSessionHeld, "operation/op-holder")},
		{"unknown project", nil, func(r *PublicationRequest) { r.Project = "missing" }, nil, wantError(api.KindNotFound, api.ReasonProject, "project/missing")},
		// An unreachable origin is no refusal: the dry run cannot plan.
		{"origin unreachable", func(_ *testing.T, _ *Store, f *fakePublication, _ *OriginAPI, _ PublicationRequest) {
			f.failure = errors.New("offline")
		}, nil, nil, wantError(api.KindFailed, api.ReasonOrigin, "")},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, f, a, r := publicationFixture(t)
			if test.prepare != nil {
				test.prepare(t, s, f, a, r)
			}
			if test.change != nil {
				test.change(&r)
			}
			handler := NewHandler(HandlerConfig{Store: s, Origins: a})
			result, e := callMethod(t, context.Background(), handler, api.OriginPublish, publishRequest(r, api.ChangeRequest{}))
			if !reflect.DeepEqual(e, test.want) {
				t.Fatalf("dry run answered %v, want %v", e, test.want)
			}
			if test.refusal != nil && (result.Plan == nil || result.Plan.Allowed || result.ChangeTag != "" || !reflect.DeepEqual(result.Plan.Refusals, []api.Refusal{*test.refusal})) {
				t.Fatalf("dry run planned %+v, want refusal %+v", result, test.refusal)
			}
			if f.pushes != 0 {
				t.Fatalf("pushed: %d", f.pushes)
			}
		})
	}
	t.Run("key recorded for another publication", func(t *testing.T) {
		s, _, a, r := publicationFixture(t)
		_, tag, err := publish(a, r)
		if err != nil {
			t.Fatal(err)
		}
		r.DestinationRef = "refs/heads/other"
		handler := NewHandler(HandlerConfig{Store: s, Origins: a})
		if _, e := callMethod(t, context.Background(), handler, api.OriginPublish, publishRequest(r, api.ChangeRequest{Key: r.Key, IfMatch: tag})); !reflect.DeepEqual(e, keyReused) {
			t.Fatalf("run answered %v, want %v", e, keyReused)
		}
	})
}

func mustExec(t *testing.T, s *Store, statement string) {
	t.Helper()
	if _, err := s.db.ExecContext(context.Background(), statement); err != nil {
		t.Fatal(err)
	}
}

// holdSession records an unfinished rename of session with status.
func holdSession(t *testing.T, s *Store, session, status string) {
	t.Helper()
	ctx := context.Background()
	if err := insertOperation(ctx, s, operationRecord{ID: "op-holder", Key: "holder", Kind: KindSessionRename, Project: "app", Session: session,
		Request: []byte(`{}`), Phase: "rename", Now: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, `UPDATE operations SET status='`+status+`' WHERE id='op-holder'`)
}

func TestPublicationHoldsSourceAssignmentAuthority(t *testing.T) {
	s, f, a, r := publicationFixture(t)
	ctx := context.Background()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	f.observeHook = func() { close(entered); <-release }
	go func() { _, err := planPublication(a, r); done <- err }()
	<-entered
	lockCtx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	_, _, err := s.ReserveSession(lockCtx, ReserveSessionRequest{Key: "competing", Project: "app", Branch: "retained", Choice: "existing"})
	cancel()
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("assignment wasn't excluded: %v", err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

// A publication whose tag expires while it waits for the store's connection
// is refused as expired inside its transaction, and records nothing.
func TestPublicationTagExpiringBeforeItsTransactionIsRefused(t *testing.T) {
	s, _, _, r := publicationFixture(t)
	ctx := context.Background()
	expiry := time.Now().Add(2 * time.Second).Truncate(time.Second)
	r.IfMatch = changeTag(expiry, testHash)
	_, err := s.BeginPublication(ctx, r, testHash, func(ctx context.Context) (string, error) {
		// Another transaction holds the connection past the tag's expiry.
		held, err := s.beginJournal(ctx)
		if err != nil {
			return "", err
		}
		go func() { time.Sleep(time.Until(expiry) + 50*time.Millisecond); held.Rollback() }()
		return "work", nil
	})
	if !answers(err, api.KindPreconditionFailed, api.ReasonExpired) {
		t.Fatalf("a tag that expired before the transaction answered %v", err)
	}
	if _, err = s.GetOperationByKey(ctx, r.Key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an expired run recorded its operation: %v", err)
	}
}
