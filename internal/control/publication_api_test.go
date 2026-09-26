package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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
func (f *fakePublication) PreviewPublication(_ context.Context, ref, oid, dest string) (PublicationPreview, error) {
	return PublicationPreview{Project: "app", URL: "ssh://host/repo", SourceRef: ref, SourceOID: oid, DestinationRef: dest, Relation: f.relation}, nil
}
func (f *fakePublication) PublishPublication(_ context.Context, p PublicationPreview) (PublicationResult, error) {
	if f.publishError != nil {
		return PublicationResult{}, f.publishError
	}
	f.pushes++
	return PublicationResult{Preview: p, Status: f.status}, nil
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
	p := PublicationResult{Preview: PublicationPreview{Project: r.Project, SourceOID: r.SourceOID, DestinationRef: r.DestinationRef}, Status: "outcome_unknown"}
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
	handler := OriginHandler(StateHandlerWithGit(s, a.Git, &GitInfo{}), a)
	request := func(method, body string) (map[string]any, *RPCError) {
		t.Helper()
		result, e := handler(ctx, method, json.RawMessage(body))
		if e != nil {
			return nil, e
		}
		return result.(map[string]any), nil
	}
	body := `{"v":1,"expected_origin_url":"ssh://host/repo","project":"app","kind":"session","source":"` + r.Source + `","source_oid":"` + r.SourceOID + `","destination_ref":"refs/heads/main"}`
	result, e := request("origin.publication.preview", body)
	if e != nil || result["preview"].(PublicationPreview).SourceRef != "refs/heads/work" {
		t.Fatalf("preview: %v %v", result, e)
	}
	if _, e = request("origin.publish", strings.TrimSuffix(body, "}")+`,"key":"once"}`); e != nil || f.pushes != 1 {
		t.Fatalf("publish: %v pushes=%d", e, f.pushes)
	}
	if _, e = request("origin.publish", strings.TrimSuffix(body, "}")+`,"key":"once"}`); e != nil || f.pushes != 1 {
		t.Fatalf("replay: %v pushes=%d", e, f.pushes)
	}
	if _, e = request("origin.publish", strings.TrimSuffix(body, "}")+`,"key":"once","SOURCE":"wrong"}`); e == nil || e.Kind != "invalid_params" {
		t.Fatalf("case-variant field accepted: %v", e)
	}
	page, e := request("project.retained_branches", `{"v":1,"project":"app","limit":8}`)
	if e != nil || len(page["branches"].([]RetainedBranch)) != 1 {
		t.Fatalf("retained page: %v %v", page, e)
	}
	// The fixture Git ref is main, which has no live owner. Assign it and
	// confirm that only Git refs without a session remain in the page.
	if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET branch='main' WHERE uuid=?`, r.Source); err != nil {
		t.Fatal(err)
	}
	page, e = request("project.retained_branches", `{"v":1,"project":"app","limit":8}`)
	if e != nil || len(page["branches"].([]RetainedBranch)) != 0 {
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
	body, _ := json.Marshal(map[string]any{"v": 1, "key": r.Key, "project": r.Project, "kind": r.Kind, "source": r.Source, "source_oid": r.SourceOID, "destination_ref": r.DestinationRef})
	if _, err := publicationRequestParams(body, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing origin identity accepted: %v", err)
	}
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
