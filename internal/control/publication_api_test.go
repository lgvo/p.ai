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

func TestPublicationPrestartFailureCanRetrySameKey(t *testing.T) {
	s, f, a, r := publicationFixture(t)
	ctx := context.Background()
	f.publishError = errors.New("source plugin unavailable before push")
	if _, err := a.PublishPublication(ctx, r); err == nil {
		t.Fatal("prestart failure returned success")
	}
	if _, status, err := s.PublicationRecord(ctx, r); err != nil || status != "prepared" || f.pushes != 0 {
		t.Fatalf("prestart record: %q %v pushes=%d", status, err, f.pushes)
	}
	f.publishError = nil
	result, err := a.PublishPublication(ctx, r)
	if err != nil || result.Status != "advanced" || f.pushes != 1 {
		t.Fatalf("retry: %+v %v", result, err)
	}
}

func publicationFixture(t *testing.T) (*Store, *fakePublication, OriginAPI, PublicationRequest) {
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
	r := PublicationRequest{ExpectedOriginURL: "ssh://host/repo", Key: "publish-once", Project: "app", Kind: "session", Source: "550e8400-e29b-41d4-a716-446655440000", SourceOID: strings.Repeat("a", 40), DestinationRef: "refs/heads/main"}
	return s, f, OriginAPI{Store: s, Runner: f}, r
}
func TestPublicationDurableReplayAndCrossJournalKeys(t *testing.T) {
	s, f, a, r := publicationFixture(t)
	ctx := context.Background()
	first, err := a.PublishPublication(ctx, r)
	if err != nil || first.Status != "advanced" || first.Preview.SourceRef != "refs/heads/work" || f.pushes != 1 || f.observes != 1 {
		t.Fatalf("first: %+v %v pushes=%d observes=%d", first, err, f.pushes, f.observes)
	}
	again, err := a.PublishPublication(ctx, r)
	if err != nil || again.Status != "advanced" || f.pushes != 1 || f.observes != 1 {
		t.Fatalf("replay: %+v %v pushes=%d observes=%d", again, err, f.pushes, f.observes)
	}
	different := r
	different.DestinationRef = "refs/heads/other"
	if _, err = a.PublishPublication(ctx, different); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed key: %v", err)
	}
	if err = s.CheckOriginKeyConflict(ctx, r.Key); !errors.Is(err, ErrConflict) {
		t.Fatalf("lifecycle key: %v", err)
	}
	if err = s.CheckLifecycleKeyConflict(ctx, r.Key); !errors.Is(err, ErrConflict) {
		t.Fatalf("origin key: %v", err)
	}
}

func TestPublicationReplayAcrossStoreRestart(t *testing.T) {
	s, f, a, r := publicationFixture(t)
	ctx := context.Background()
	if result, err := a.PublishPublication(ctx, r); err != nil || result.Status != "advanced" {
		t.Fatalf("first: %+v %v", result, err)
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
	result, err := a.PublishPublication(ctx, r)
	if err != nil || result.Status != "advanced" || f.pushes != 1 || f.observes != 1 {
		t.Fatalf("restart replay: %+v %v pushes=%d observes=%d", result, err, f.pushes, f.observes)
	}
}
func TestPublicationAttemptReplayUnknownAndRefusal(t *testing.T) {
	s, f, a, r := publicationFixture(t)
	ctx := context.Background()
	if _, status, err := s.PreparePublication(ctx, r); err != nil || status != "prepared" {
		t.Fatalf("prepare: %s %v", status, err)
	}
	p := api.Publication{Preview: api.PublicationPreview{Project: r.Project, SourceOID: r.SourceOID, DestinationRef: r.DestinationRef}, Status: "outcome_unknown"}
	if err := s.UpdatePublication(ctx, r.Key, "prepared", "attempted", p); err != nil {
		t.Fatal(err)
	}
	result, err := a.PublishPublication(ctx, r)
	if err != nil || result.Status != "outcome_unknown" || f.observes != 0 || f.pushes != 0 {
		t.Fatalf("uncertain replay: %+v %v", result, err)
	}
	r.Key = "refused"
	f.relation = "divergent"
	result, err = a.PublishPublication(ctx, r)
	if err != nil || result.Status != "refused" || f.pushes != 0 {
		t.Fatalf("divergent: %+v %v", result, err)
	}
}

func TestPublicationUnknownResultNeverRepeatsPush(t *testing.T) {
	_, f, a, r := publicationFixture(t)
	f.status = "outcome_unknown"
	ctx := context.Background()
	result, err := a.PublishPublication(ctx, r)
	if err != nil || result.Status != "outcome_unknown" || f.pushes != 1 {
		t.Fatalf("first uncertain: %+v %v", result, err)
	}
	result, err = a.PublishPublication(ctx, r)
	if err != nil || result.Status != "outcome_unknown" || f.pushes != 1 {
		t.Fatalf("uncertain replay: %+v %v", result, err)
	}
}
func TestPublicationOwnershipAndRetainedSelection(t *testing.T) {
	s, f, a, r := publicationFixture(t)
	ctx := context.Background()
	retained := r
	retained.Kind = "retained"
	retained.Source = "work"
	retained.Key = "retained"
	if _, err := a.PreviewPublication(ctx, retained); !errors.Is(err, ErrConflict) {
		t.Fatalf("assigned branch used as retained: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE uuid=?`, r.Source); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PreviewPublication(ctx, r); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed session: %v", err)
	}
	retained.Key = ""
	p, err := a.PreviewPublication(ctx, retained)
	if err != nil || p.SourceKind != "retained" || p.SourceRef != "refs/heads/work" || f.observes != 1 {
		t.Fatalf("retained preview: %+v %v", p, err)
	}
}

func TestPublicationRPCAndRetainedBranchPage(t *testing.T) {
	s, f, a, r := publicationFixture(t)
	ctx := context.Background()
	a.Git = fixtureGitReader{}
	handler := NewHandler(HandlerConfig{Store: s, Git: a.Git, GitInfo: &api.GitInfo{}, Origins: &a})
	request := func(method, body string) (any, *api.Error) {
		t.Helper()
		return handler(ctx, method, json.RawMessage(body))
	}
	body := `{"expected_origin_url":"ssh://host/repo","project":"app","kind":"session","source":"` + r.Source + `","source_oid":"` + r.SourceOID + `","destination_ref":"refs/heads/main"}`
	result, e := request("origin.publication.preview", body)
	if e != nil || result.(api.PublicationPreviewResult).Preview.SourceRef != "refs/heads/work" {
		t.Fatalf("preview: %v %v", result, e)
	}
	if _, e = request("origin.publish", strings.TrimSuffix(body, "}")+`,"key":"once"}`); e != nil || f.pushes != 1 {
		t.Fatalf("publish: %v pushes=%d", e, f.pushes)
	}
	if _, e = request("origin.publish", strings.TrimSuffix(body, "}")+`,"key":"once"}`); e != nil || f.pushes != 1 {
		t.Fatalf("replay: %v pushes=%d", e, f.pushes)
	}
	if _, e = request("origin.publish", strings.TrimSuffix(body, "}")+`,"key":"once","SOURCE":"wrong"}`); e == nil || e.Kind() != "invalid" {
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
			preview, err := a.PreviewPublication(ctx, r)
			if err != nil || preview.URL != r.ExpectedOriginURL {
				t.Fatalf("preview: %+v %v", preview, err)
			}
			if prepared {
				f.publishError = errors.New("prestart failure")
				if _, err = a.PublishPublication(ctx, r); err == nil {
					t.Fatal("expected prestart error")
				}
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
			if _, err = a.PublishPublication(ctx, r); !errors.Is(err, ErrConflict) {
				t.Fatalf("changed origin accepted: %v", err)
			}
			if f.observes != contacts || f.pushes != 0 {
				t.Fatalf("contacted replacement: reads=%d pushes=%d", f.observes, f.pushes)
			}
			changed := r
			changed.ExpectedOriginURL = "ssh://host/replacement"
			if _, err = a.PublishPublication(ctx, changed); !errors.Is(err, ErrConflict) {
				t.Fatalf("retargeted durable key: %v", err)
			}
			if _, err = a.PreviewPublication(ctx, r); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale preview target accepted: %v", err)
			}
		})
	}
}

func TestPublicationCompletedReplayKeepsOriginalIdentity(t *testing.T) {
	s, f, a, r := publicationFixture(t)
	ctx := context.Background()
	first, err := a.PublishPublication(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE project_origins SET url='ssh://host/replacement' WHERE project_path='app'`); err != nil {
		t.Fatal(err)
	}
	again, err := a.PublishPublication(ctx, r)
	if err != nil || again.Preview.URL != first.Preview.URL || f.pushes != 1 || f.observes != 1 {
		t.Fatalf("replay retargeted or contacted: %+v %v", again, err)
	}
	changed := r
	changed.ExpectedOriginURL = "ssh://host/replacement"
	if _, err = a.PublishPublication(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("completed key retargeted: %v", err)
	}
}

func TestPublicationRPCRequiresOriginIdentity(t *testing.T) {
	_, _, _, r := publicationFixture(t)
	body, _ := json.Marshal(map[string]any{"key": r.Key, "project": r.Project, "kind": r.Kind, "source": r.Source, "source_oid": r.SourceOID, "destination_ref": r.DestinationRef})
	h := NewHandler(HandlerConfig{Origins: &OriginAPI{Runner: &fakePublication{}}})
	if _, rpcErr := h(context.Background(), api.OriginPublish.Name, body); rpcErr == nil || rpcErr.Kind() != "invalid" {
		t.Fatalf("missing origin identity accepted: %+v", rpcErr)
	}
}

// TestPublicationErrorsAnswerTheirKind checks each publication refusal's
// answer through the handler.
func TestPublicationErrorsAnswerTheirKind(t *testing.T) {
	keyReused := &api.Error{Code: -32602, Message: "invalid", Data: &api.ErrorData{Reason: api.ReasonKeyReused, Field: "key"}}
	session := "550e8400-e29b-41d4-a716-446655440000"
	for _, test := range []struct {
		name    string
		prepare func(t *testing.T, s *Store, f *fakePublication, a OriginAPI, r PublicationRequest)
		change  func(r *PublicationRequest)
		want    *api.Error
	}{
		{"local-only project", func(t *testing.T, s *Store, _ *fakePublication, _ OriginAPI, _ PublicationRequest) {
			mustExec(t, s, `DELETE FROM project_origins WHERE project_path='app'`)
		}, nil, wantError(api.KindConflict, api.ReasonOriginNotSet, "project/app")},
		{"origin changed since review", func(t *testing.T, s *Store, _ *fakePublication, _ OriginAPI, _ PublicationRequest) {
			mustExec(t, s, `UPDATE project_origins SET url='ssh://host/replacement' WHERE project_path='app'`)
		}, nil, wantError(api.KindPreconditionFailed, api.ReasonChanged, "")},
		{"key recorded for another publication", func(t *testing.T, _ *Store, _ *fakePublication, a OriginAPI, r PublicationRequest) {
			if _, err := a.PublishPublication(context.Background(), r); err != nil {
				t.Fatal(err)
			}
		}, func(r *PublicationRequest) { r.DestinationRef = "refs/heads/other" }, keyReused},
		{"origin unreachable", func(_ *testing.T, _ *Store, f *fakePublication, _ OriginAPI, _ PublicationRequest) {
			f.failure = errors.New("offline")
		}, nil, wantError(api.KindFailed, api.ReasonOrigin, "")},
		{"removed session", func(t *testing.T, s *Store, _ *fakePublication, _ OriginAPI, _ PublicationRequest) {
			mustExec(t, s, `DELETE FROM sessions WHERE uuid='`+session+`'`)
		}, nil, wantError(api.KindNotFound, api.ReasonSession, "session/"+session)},
		{"assigned branch as a retained source", nil, func(r *PublicationRequest) { r.Kind, r.Source = "retained", "work" },
			wantError(api.KindConflict, api.ReasonBranchAssigned, "project/app:branch/work")},
		{"session held by a running operation", func(t *testing.T, s *Store, _ *fakePublication, _ OriginAPI, _ PublicationRequest) {
			holdSession(t, s, session, "running")
		}, nil, wantError(api.KindRetry, api.ReasonSessionHeld, "operation/op-holder")},
		{"session held by a blocked operation", func(t *testing.T, s *Store, _ *fakePublication, _ OriginAPI, _ PublicationRequest) {
			holdSession(t, s, session, "blocked")
		}, nil, wantError(api.KindConflict, api.ReasonOperationBlocked, "operation/op-holder")},
		{"unknown project", nil, func(r *PublicationRequest) { r.Project = "missing" }, wantError(api.KindNotFound, api.ReasonProject, "project/missing")},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, f, a, r := publicationFixture(t)
			if test.prepare != nil {
				test.prepare(t, s, f, a, r)
			}
			if test.change != nil {
				test.change(&r)
			}
			handler := NewHandler(HandlerConfig{Store: s, Origins: &a})
			_, e := callMethod(t, context.Background(), handler, api.OriginPublish, api.OriginPublishRequest(r))
			if !reflect.DeepEqual(e, test.want) {
				t.Fatalf("publish answered %v, want %v", e, test.want)
			}
		})
	}
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
	if err := insertOperation(ctx, s.db, operationRecord{ID: "op-holder", Key: "holder", Kind: KindSessionRename, Project: "app", Session: session,
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
	go func() { _, err := a.PublishPublication(ctx, r); done <- err }()
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
