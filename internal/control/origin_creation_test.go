package control

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/plugin"
)

func TestOriginProjectCommitNonemptyAndReplay(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	req := BlankProjectRequest{Key: "origin-create", Project: "team/app", URL: "ssh://host/repo"}
	op, err := s.BeginBlankProject(ctx, req, json.RawMessage(`{"network":"none"}`), strings.Repeat("d", 64), testSelection())
	if err != nil {
		t.Fatal(err)
	}
	if op.SessionUUID != "" {
		t.Fatalf("nonempty origin intent prematurely reserved session: %+v", op)
	}
	if active, _ := s.HasActiveProject(ctx, req.Project); active {
		t.Fatal("project visible before verified contact")
	}
	ref := plugin.GitOriginRef{Ref: "refs/heads/main", OID: strings.Repeat("a", 40), CommitOID: strings.Repeat("a", 40)}
	_, bootstrap, err := s.CommitOriginProject(ctx, op.ID, []plugin.GitOriginRef{ref})
	if err != nil || bootstrap {
		t.Fatalf("commit: bootstrap=%v err=%v", bootstrap, err)
	}
	state, refs, err := s.Origin(ctx, req.Project)
	if err != nil || state.URL != req.URL || state.Status != "fresh" || len(refs) != 1 || refs[0] != ref {
		t.Fatalf("origin state: %+v %+v %v", state, refs, err)
	}
	var sessions int
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE project_path=?`, req.Project).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("unexpected session: %d %v", sessions, err)
	}
	completed, err := s.GetOperation(ctx, op.ID)
	if err != nil || completed.Status != "completed" || completed.SessionUUID != "" {
		t.Fatalf("completion: %+v %v", completed, err)
	}
	replayed, err := s.BeginBlankProject(ctx, req, json.RawMessage(`{}`), "", CreationSelection{})
	if err != nil || replayed.ID != op.ID {
		t.Fatalf("replay: %+v %v", replayed, err)
	}
	if _, err = s.BeginBlankProject(ctx, BlankProjectRequest{Key: req.Key, Project: req.Project, URL: "ssh://host/other"}, json.RawMessage(`{}`), "", CreationSelection{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed URL replay: %v", err)
	}
}

func TestEmptyOriginCommitsSingleBootstrapAtomically(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	req := BlankProjectRequest{Key: "empty-origin", Project: "app", URL: "ssh://host/empty"}
	op, err := s.BeginBlankProject(ctx, req, json.RawMessage(`{}`), strings.Repeat("d", 64), testSelection())
	if err != nil {
		t.Fatal(err)
	}
	session, bootstrap, err := s.CommitOriginProject(ctx, op.ID, nil)
	if err != nil || !bootstrap || session.UUID == "" || session.Branch != "main" {
		t.Fatalf("bootstrap: %+v %v %v", session, bootstrap, err)
	}
	var count int
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM git_unborn_grants WHERE session_uuid=? AND state='pending'`, session.UUID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("grant count %d: %v", count, err)
	}
	if _, _, err = s.CommitOriginProject(ctx, op.ID, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("second commit: %v", err)
	}
	state, refs, err := s.Origin(ctx, req.Project)
	if err != nil || state.URL != req.URL || state.Status != "fresh" || len(refs) != 0 {
		t.Fatalf("origin: %+v %+v %v", state, refs, err)
	}
	got, err := s.GetOperation(ctx, op.ID)
	if err != nil || got.SessionUUID != session.UUID || got.Phase != "branch-assigned" {
		t.Fatalf("operation: %+v %v", got, err)
	}
}

func TestOriginSourceCaptureAndReplay(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, err := testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	oid := strings.Repeat("a", 40)
	req := ReserveSessionRequest{Key: "source-origin", Project: "app", Branch: "work", Choice: "new", OriginRef: "refs/tags/v1", ExpectedCommitOID: oid}
	called := 0
	capture := func(context.Context, ReserveSessionRequest) (CapturedSource, error) {
		called++
		return CapturedSource{OID: oid, OriginURL: "ssh://host/repo", OriginRef: req.OriginRef}, nil
	}
	op, sess, err := s.BeginSessionCreateCaptured(ctx, req, strings.Repeat("d", 64), testSelection(), capture)
	if err != nil || sess.UUID == "" {
		t.Fatalf("capture: %+v %v", sess, err)
	}
	ev, err := Evidence(op)
	if err != nil || ev.CapturedOID != oid || ev.OriginURL != "ssh://host/repo" || ev.OriginRef != req.OriginRef {
		t.Fatalf("evidence: %+v %v", ev, err)
	}
	if err = s.AdvanceOperation(ctx, op.ID, "blocked", "source-ready", false, op.Evidence, "waiting for branch assignment"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	replay, again, err := s.BeginSessionCreateCaptured(ctx, req, "", CreationSelection{}, func(context.Context, ReserveSessionRequest) (CapturedSource, error) {
		t.Fatal("recontacted on replay")
		return CapturedSource{}, nil
	})
	if err != nil || replay.ID != op.ID || again.UUID != sess.UUID || called != 1 {
		t.Fatalf("replay: %+v %+v %d %v", replay, again, called, err)
	}
	changed := req
	changed.ExpectedCommitOID = strings.Repeat("b", 40)
	if _, _, err = s.BeginSessionCreateCaptured(ctx, changed, "", CreationSelection{}, capture); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed source accepted: %v", err)
	}
	bad := req
	bad.Key = "bad-source"
	bad.Branch = "other"
	if _, _, err = s.BeginSessionCreateCaptured(ctx, bad, strings.Repeat("d", 64), testSelection(), func(context.Context, ReserveSessionRequest) (CapturedSource, error) {
		return CapturedSource{OID: strings.Repeat("b", 40), OriginURL: "ssh://host/repo", OriginRef: bad.OriginRef}, nil
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched fetched commit accepted: %v", err)
	}
	var count int
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE branch='other'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed source reserved branch: %d %v", count, err)
	}
}

type captureOriginLifecycle struct {
	statusLife
	project BlankProjectRequest
	session ReserveSessionRequest
}

func (l *captureOriginLifecycle) CreateProject(_ context.Context, req BlankProjectRequest) (Operation, error) {
	l.project = req
	return Operation{Kind: "project.create"}, nil
}
func (l *captureOriginLifecycle) CreateSession(_ context.Context, req ReserveSessionRequest) (Operation, error) {
	l.session = req
	return Operation{Kind: "session.create"}, nil
}

func TestOriginCreationRPCStrictInputs(t *testing.T) {
	s, _ := openTestStore(t)
	life := &captureOriginLifecycle{statusLife: statusLife{store: s}}
	h := StateHandlerWithLifecycle(s, nil, nil, life)
	call := func(method, params string) *RPCError {
		_, e := h(context.Background(), method, json.RawMessage(params))
		return e
	}
	if e := call("project.create", `{"v":1,"key":"a","project":"app","url":"ssh://host/repo"}`); e != nil {
		t.Fatal(e)
	}
	if life.project.URL != "ssh://host/repo" {
		t.Fatalf("URL lost: %+v", life.project)
	}
	for _, input := range []string{
		`{"v":1,"key":"a","project":"app","url":"ssh://host/repo","extra":1}`,
		`{"v":1,"key":"a","project":"app","url":"a","url":"b"}`,
	} {
		if e := call("project.create", input); e == nil || e.Kind != "invalid_params" {
			t.Fatalf("accepted %s: %+v", input, e)
		}
	}
	oid := strings.Repeat("a", 40)
	if e := call("session.create", `{"v":1,"key":"b","project":"app","branch":"work","choice":"new","origin_ref":"refs/tags/v1","expected_commit_oid":"`+oid+`"}`); e != nil {
		t.Fatal(e)
	}
	if life.session.OriginRef != "refs/tags/v1" || life.session.ExpectedCommitOID != oid {
		t.Fatalf("source lost: %+v", life.session)
	}
	for _, input := range []string{
		`{"v":1,"key":"b","project":"app","branch":"work","choice":"existing","origin_ref":"refs/heads/main","expected_commit_oid":"` + oid + `"}`,
		`{"v":1,"key":"b","project":"app","branch":"work","choice":"new","source":"refs/heads/main","origin_ref":"refs/heads/main","expected_commit_oid":"` + oid + `"}`,
		`{"v":1,"key":"b","project":"app","branch":"work","choice":"new","origin_ref":"refs/heads/main","expected_commit_oid":"bad"}`,
		`{"v":1,"key":"b","project":"app","branch":"work","choice":"new","origin_ref":"refs/heads/main","expected_commit_oid":"` + oid + `","extra":1}`,
	} {
		if e := call("session.create", input); e == nil || e.Kind != "invalid_params" {
			t.Fatalf("accepted %s: %+v", input, e)
		}
	}
}

func TestCreationAndOriginJournalsRejectSharedKey(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	_, err := s.BeginBlankProject(ctx, BlankProjectRequest{Key: "shared", Project: "pending", URL: "ssh://host/repo"}, json.RawMessage(`{}`), strings.Repeat("d", 64), testSelection())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.PrepareOriginChange(ctx, OriginChange{Key: "shared", Project: "pending", Kind: "set", ProposedURL: "ssh://host/repo"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("pending project key: %v", err)
	}
	if err = s.CreateProject(ctx, "existing", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.PrepareOriginChange(ctx, OriginChange{Key: "origin-key", Project: "existing", Kind: "remove"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginBlankProject(ctx, BlankProjectRequest{Key: "origin-key", Project: "new"}, json.RawMessage(`{}`), strings.Repeat("d", 64), testSelection()); !errors.Is(err, ErrConflict) {
		t.Fatalf("origin key reused: %v", err)
	}
}
