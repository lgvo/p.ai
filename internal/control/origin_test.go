package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/plugin"
)

type fakeOriginRunner struct {
	refs  []plugin.GitOriginRef
	err   error
	calls int
}

func (f *fakeOriginRunner) ValidateURL(url string) error {
	if strings.HasPrefix(url, "ssh://") {
		return nil
	}
	return ErrInvalid
}
func (f *fakeOriginRunner) WithOrigin(_ context.Context, _ string, fn func(OriginObserver) error) error {
	return fn(f)
}
func (f *fakeOriginRunner) Observe(_ context.Context, _ string) ([]plugin.GitOriginRef, error) {
	f.calls++
	return f.refs, f.err
}

// runQueued makes a's Queue run each queued origin operation's work at once,
// to its end, as a daemon worker would, through a's Store as it is then.
func runQueued(a *OriginAPI) {
	a.Queue = func(id string) error {
		ctx := context.Background()
		op, err := a.Store.GetOperation(ctx, id)
		if err != nil {
			return err
		}
		switch op.Kind {
		case KindOriginChange:
			a.RunOriginChange(ctx, op)
		case KindOriginPublish:
			a.RunPublication(ctx, op)
		default:
			return fmt.Errorf("queued operation %s of kind %s", id, op.Kind)
		}
		return nil
	}
}

// readBack returns a function that reads back the operation a request of a
// returned, as a's journal reads it now; it fails the test on the request's
// error.
func readBack(t *testing.T, a *OriginAPI) func(api.Operation, error) api.Operation {
	return func(op api.Operation, err error) api.Operation {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		got, err := a.Store.GetOperation(context.Background(), op.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
}

// wantEnded fails unless op ended with status and reason.
func wantEnded(t *testing.T, what string, op api.Operation, status, reason string) {
	t.Helper()
	if op.Status != status || op.Reason != reason {
		t.Fatalf("%s ended %s %q (%s), want %s %q", what, op.Status, op.Reason, op.Diagnostic, status, reason)
	}
}

func TestOriginAssociationReplayFailureRefreshAndRestart(t *testing.T) {
	s, dir := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{"network":"none"}`)); err != nil {
		t.Fatal(err)
	}
	ref := plugin.GitOriginRef{Ref: "refs/heads/main", OID: strings.Repeat("a", 40), CommitOID: strings.Repeat("a", 40)}
	f := &fakeOriginRunner{refs: []plugin.GitOriginRef{ref}}
	a := OriginAPI{Store: s, Runner: f}
	runQueued(&a)
	read := readBack(t, &a)
	first := OriginChange{Key: "first", Project: "app", Kind: "set", ProposedURL: "ssh://host/a"}
	firstOp := read(a.Change(ctx, first))
	wantEnded(t, "first", firstOp, "completed", "")
	state, _, err := s.Origin(ctx, "app")
	if err != nil || state.Status != "fresh" || state.RefCount != 1 || f.calls != 1 {
		t.Fatalf("first: %+v %v %d", state, err, f.calls)
	}
	f.err = errors.New("offline")
	second := OriginChange{Key: "second", Project: "app", Kind: "set", ExpectedURL: first.ProposedURL, ProposedURL: "ssh://host/b"}
	secondOp := read(a.Change(ctx, second))
	wantEnded(t, "failed contact", secondOp, "failed", api.ReasonOrigin)
	if secondOp.Diagnostic != "offline" {
		t.Fatalf("failed contact's diagnostic %q, want the contact's error", secondOp.Diagnostic)
	}
	state, refs, err := s.Origin(ctx, "app")
	if err != nil || state.URL != first.ProposedURL || state.Status != "fresh" || len(refs) != 1 {
		t.Fatalf("failed set: %+v %+v %v", state, refs, err)
	}
	if _, err = a.Change(ctx, OriginChange{Key: "first", Project: "app", Kind: "remove"}); !errors.Is(err, ErrConflict) || f.calls != 2 {
		t.Fatalf("conflicting key: %v calls=%d", err, f.calls)
	}
	if _, err = a.Refresh(ctx, "app"); err != nil {
		t.Fatal(err)
	}
	state, refs, err = s.Origin(ctx, "app")
	if err != nil || state.Status != "unknown" || len(refs) != 1 || state.ObservedAt == "" {
		t.Fatalf("failed refresh: %+v %+v %v", state, refs, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a.Store = s
	replay := read(a.Change(ctx, first))
	if replay.ID != firstOp.ID || replay.Status != "completed" || f.calls != 3 {
		t.Fatalf("completed replay: %+v calls=%d", replay, f.calls)
	}
	f.err = nil
	// A failed change ends its key: the same key returns the failed
	// operation and contacts nothing; a new key changes the origin.
	replay = read(a.Change(ctx, second))
	if replay.ID != secondOp.ID || replay.Status != "failed" || f.calls != 3 {
		t.Fatalf("failed replay: %+v calls=%d", replay, f.calls)
	}
	second.Key = "second-again"
	wantEnded(t, "new key", read(a.Change(ctx, second)), "completed", "")
	state, _, err = s.Origin(ctx, "app")
	if err != nil || state.URL != second.ProposedURL || state.Status != "fresh" {
		t.Fatalf("retry: %+v %v", state, err)
	}
	if err = s.StoreOriginRefresh(ctx, "app", first.ProposedURL, nil, "late failure"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale refresh identity: %v", err)
	}
	state, _, err = s.Origin(ctx, "app")
	if err != nil || state.Status != "fresh" || state.URL != second.ProposedURL {
		t.Fatalf("stale refresh changed new origin: %+v %v", state, err)
	}
	if _, err = a.Change(ctx, OriginChange{Key: "stale", Project: "app", Kind: "set", ExpectedURL: first.ProposedURL, ProposedURL: "ssh://host/c"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("CAS: %v", err)
	}
	wantEnded(t, "remove", read(a.Change(ctx, OriginChange{Key: "remove", Project: "app", Kind: "remove", ExpectedURL: second.ProposedURL})), "completed", "")
	state, refs, err = s.Origin(ctx, "app")
	if err != nil || state.Status != "local-only" || len(refs) != 0 {
		t.Fatalf("removed: %+v %+v %v", state, refs, err)
	}
}

// TestOriginChangeReplayReturnsItsOperation checks that a key's identical
// request returns the operation the key recorded, whatever its status, and
// contacts the origin no more; only a running one is queued again.
func TestOriginChangeReplayReturnsItsOperation(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	f := &fakeOriginRunner{}
	a := OriginAPI{Store: s, Runner: f}
	var queued []string
	a.Queue = func(id string) error { queued = append(queued, id); return nil }
	read := readBack(t, &a)
	set := OriginChange{Key: "set", Project: "app", Kind: "set", ProposedURL: "ssh://host/a"}
	running, err := a.Change(ctx, set)
	if err != nil || running.Status != "running" || len(queued) != 1 || queued[0] != running.ID || f.calls != 0 {
		t.Fatalf("accepted: %+v %v queued=%v calls=%d", running, err, queued, f.calls)
	}
	again, err := a.Change(ctx, set)
	if err != nil || again.ID != running.ID || len(queued) != 2 || queued[1] != running.ID || f.calls != 0 {
		t.Fatalf("running replay: %+v %v queued=%v calls=%d", again, err, queued, f.calls)
	}
	_, _, before, err := s.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a.RunOriginChange(ctx, running)
	wantEnded(t, "set", read(running, nil), "completed", "")
	f.err = errors.New("offline")
	failing := OriginChange{Key: "failing", Project: "app", Kind: "set", ExpectedURL: set.ProposedURL, ProposedURL: "ssh://host/b"}
	failed, err := a.Change(ctx, failing)
	if err != nil {
		t.Fatal(err)
	}
	a.RunOriginChange(ctx, failed)
	wantEnded(t, "unreachable set", read(failed, nil), "failed", api.ReasonOrigin)
	f.err = nil
	calls, queuedBefore := f.calls, len(queued)
	for _, replay := range []struct {
		change OriginChange
		want   api.Operation
	}{{set, running}, {failing, failed}} {
		got, err := a.Change(ctx, replay.change)
		if err != nil || got.ID != replay.want.ID || got.Status == "running" {
			t.Fatalf("replay of %s: %+v %v", replay.change.Key, got, err)
		}
	}
	if f.calls != calls || len(queued) != queuedBefore {
		t.Fatalf("replays contacted the origin (calls %d, was %d) or queued work %v", f.calls, calls, queued)
	}
	if _, _, after, err := s.Count(ctx); err != nil || after != before+1 {
		t.Fatalf("replays recorded operations: %d, want %d (%v)", after, before+1, err)
	}
	state, _, err := s.Origin(ctx, "app")
	if err != nil || state.URL != set.ProposedURL {
		t.Fatalf("replayed failure changed the origin: %+v %v", state, err)
	}
}

// TestOriginChangeStaleExpectationRecordsNothing checks that an expected URL
// that no longer names the origin when the request arrives is refused there:
// no operation records the key and nothing contacts the proposed origin.
func TestOriginChangeStaleExpectationRecordsNothing(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	f := &fakeOriginRunner{}
	a := OriginAPI{Store: s, Runner: f}
	runQueued(&a)
	read := readBack(t, &a)
	wantEnded(t, "set", read(a.Change(ctx, OriginChange{Key: "set", Project: "app", Kind: "set", ProposedURL: "ssh://host/a"})), "completed", "")
	_, _, before, err := s.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	calls := f.calls
	for _, stale := range []OriginChange{
		{Key: "stale", Project: "app", Kind: "set", ExpectedURL: "ssh://host/old", ProposedURL: "ssh://host/b"},
		{Key: "stale", Project: "app", Kind: "set", ProposedURL: "ssh://host/b"},
		{Key: "stale", Project: "app", Kind: "remove", ExpectedURL: "ssh://host/old"},
	} {
		_, err = a.Change(ctx, stale)
		wantAnswer(t, "stale expectation", err, api.KindPreconditionFailed, api.ReasonChanged, "")
	}
	if _, err = s.GetOperationByKey(ctx, "stale"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale request recorded its key: %v", err)
	}
	if _, _, after, err := s.Count(ctx); err != nil || after != before || f.calls != calls {
		t.Fatalf("stale requests recorded %d operations or contacted the origin %d times (%v)", after-before, f.calls-calls, err)
	}
	// The key stays free for a request that expects the current origin.
	wantEnded(t, "current expectation", read(a.Change(ctx, OriginChange{Key: "stale", Project: "app", Kind: "remove", ExpectedURL: "ssh://host/a"})), "completed", "")
}

// TestOriginChangeEndsFailedWhenTheOriginChangedMeanwhile checks that an
// origin changed between the request and its work ends the operation failed
// with reason changed, leaving the newer origin in place.
func TestOriginChangeEndsFailedWhenTheOriginChangedMeanwhile(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	a := OriginAPI{Store: s, Runner: &fakeOriginRunner{}, Queue: func(string) error { return nil }}
	read := readBack(t, &a)
	op, err := a.Change(ctx, OriginChange{Key: "set", Project: "app", Kind: "set", ProposedURL: "ssh://host/a"})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, `INSERT INTO project_origins(project_path,url,current_status,observed_at,refs_json) VALUES('app','ssh://host/meanwhile','fresh','now','[]')`)
	a.RunOriginChange(ctx, op)
	wantEnded(t, "change of a changed origin", read(op, nil), "failed", api.ReasonChanged)
	if state, _, err := s.Origin(ctx, "app"); err != nil || state.URL != "ssh://host/meanwhile" {
		t.Fatalf("failed change replaced the newer origin: %+v %v", state, err)
	}
}

// TestOriginOperationsNameTheirIntent checks the intent each origin operation
// records: the URL a change sets, and the destination, branch and session a
// publication publishes.
func TestOriginOperationsNameTheirIntent(t *testing.T) {
	s, f, a, r := publicationFixture(t)
	ctx := context.Background()
	a.Queue = func(string) error { return nil }
	set, err := a.Change(ctx, OriginChange{Key: "set", Project: "app", Kind: "set", ExpectedURL: r.ExpectedOriginURL, ProposedURL: "ssh://host/next"})
	if err != nil {
		t.Fatal(err)
	}
	checkIntent(t, s, set, api.OperationIntent{Project: "app", URL: "ssh://host/next"})
	remove, err := a.Change(ctx, OriginChange{Key: "remove", Project: "app", Kind: "remove", ExpectedURL: r.ExpectedOriginURL})
	if err != nil {
		t.Fatal(err)
	}
	checkIntent(t, s, remove, api.OperationIntent{Project: "app"})
	session, _, err := publish(a, r)
	if err != nil {
		t.Fatal(err)
	}
	checkIntent(t, s, session, api.OperationIntent{Project: "app", Session: r.Source, Branch: "work", Destination: r.DestinationRef})
	if session.SessionUUID != r.Source {
		t.Fatalf("session publication records session %q, want %q", session.SessionUUID, r.Source)
	}
	retained := r
	retained.Key, retained.Kind, retained.Source, retained.DestinationRef = "retained", "retained", "kept", "refs/heads/release"
	kept, _, err := publish(a, retained)
	if err != nil {
		t.Fatal(err)
	}
	checkIntent(t, s, kept, api.OperationIntent{Project: "app", Branch: "kept", Destination: "refs/heads/release"})
	// Each dry run and run reads the origin; nothing runs the workers.
	if kept.SessionUUID != "" || f.observes != 4 || f.pushes != 0 {
		t.Fatalf("retained publication %+v, or accepting pushed (observes %d, pushes %d)", kept, f.observes, f.pushes)
	}
}

func TestOriginSourcePagesAndStrictRPC(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	refs := make([]plugin.GitOriginRef, 1024)
	for i := range refs {
		refs[i] = plugin.GitOriginRef{Ref: fmt.Sprintf("refs/heads/%04d-%s", i, strings.Repeat("x", 450)), OID: strings.Repeat("a", 40), CommitOID: strings.Repeat("a", 40)}
	}
	f := &fakeOriginRunner{refs: refs}
	origins := OriginAPI{Store: s, Runner: f}
	runQueued(&origins)
	handler := NewHandler(HandlerConfig{Store: s, Origins: &origins})
	_, e := handler(ctx, "origin.change", json.RawMessage(`{"key":"a","project":"app","kind":"set","url":"ssh://host/a","extra":1}`))
	if e == nil || e.Kind() != "invalid" {
		t.Fatalf("unknown field: %+v", e)
	}
	_, e = handler(ctx, "origin.change", json.RawMessage(`{"key":"a","key":"a","project":"app","kind":"set","url":"ssh://host/a"}`))
	if e == nil || e.Kind() != "invalid" {
		t.Fatalf("duplicate field: %+v", e)
	}
	for _, bad := range []string{
		`{"V":2,"key":"a","project":"app","kind":"set","url":"ssh://host/a"}`,
		`{"Key":"a","project":"app","kind":"set","url":"ssh://host/a"}`,
		`{"key":"a","Project":"app","kind":"set","url":"ssh://host/a"}`,
		`{"key":"a","project":"app","kind":"set","URL":"ssh://host/a"}`,
	} {
		if _, e = handler(ctx, "origin.change", json.RawMessage(bad)); e == nil || e.Kind() != "invalid" {
			t.Fatalf("inexact field accepted: %s %+v", bad, e)
		}
	}
	if _, e = handler(ctx, "origin.change", json.RawMessage(`{"key":"a","project":"app","kind":"set","url":"ssh://host/a"}`)); e != nil {
		t.Fatal(e)
	}
	after := ""
	seen := 0
	for {
		page, e := callMethod(t, ctx, handler, api.OriginSources, api.OriginSourcesRequest{Project: "app", After: after, Limit: 16})
		if e != nil {
			t.Fatal(e)
		}
		encoded, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage(`"` + strings.Repeat(`\u0000`, 128) + `"`), Result: page})
		if len(encoded)+1 > api.MaxFrameBytes {
			t.Fatalf("page exceeds frame: %d", len(encoded))
		}
		seen += len(page.Refs)
		after = page.Next
		if after == "" {
			break
		}
	}
	if seen != 1024 {
		t.Fatalf("seen=%d", seen)
	}
}

func TestOriginStoreRejectsInvalidUTF8Ref(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	change := OriginChange{Key: "bad-utf8", Project: "app", Kind: "set", ProposedURL: "ssh://host/a"}
	op, err := s.BeginOriginChange(ctx, change)
	if err != nil {
		t.Fatal(err)
	}
	bad := plugin.GitOriginRef{Ref: "refs/heads/" + string([]byte{0xff}), OID: strings.Repeat("a", 40), CommitOID: strings.Repeat("a", 40)}
	if err := s.CompleteOriginChange(ctx, op, []plugin.GitOriginRef{bad}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid UTF-8 persisted: %v", err)
	}
	state, refs, err := s.Origin(ctx, "app")
	if err != nil || state.Status != "local-only" || len(refs) != 0 {
		t.Fatalf("failed commit changed state: %+v %+v %v", state, refs, err)
	}
	if read, err := s.GetOperation(ctx, op.ID); err != nil || read.Status != "running" {
		t.Fatalf("failed commit ended the operation: %+v %v", read, err)
	}
	valid := plugin.GitOriginRef{Ref: "refs/heads/café", OID: strings.Repeat("a", 40), CommitOID: strings.Repeat("a", 40)}
	if err := s.CompleteOriginChange(ctx, op, []plugin.GitOriginRef{valid}); err != nil {
		t.Fatalf("valid UTF-8 ref refused: %v", err)
	}
	if read, err := s.GetOperation(ctx, op.ID); err != nil || read.Status != "completed" {
		t.Fatalf("commit left the operation %+v %v", read, err)
	}
}

// TestOriginErrorsAnswerTheirKind checks each origin refusal's answer through
// the handler; no answer carries the cause's text. An unreachable origin is
// no refusal: the change's operation ends failed with reason origin.
func TestOriginErrorsAnswerTheirKind(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	ref := plugin.GitOriginRef{Ref: "refs/heads/main", OID: strings.Repeat("a", 40), CommitOID: strings.Repeat("a", 40)}
	f := &fakeOriginRunner{refs: []plugin.GitOriginRef{ref}}
	origins := OriginAPI{Store: s, Runner: f}
	runQueued(&origins)
	handler := NewHandler(HandlerConfig{Store: s, Origins: &origins})
	if _, e := callMethod(t, ctx, handler, api.OriginChange, api.OriginChangeRequest{Key: "a", Project: "app", Kind: "set", URL: "ssh://host/a"}); e != nil {
		t.Fatal(e)
	}
	keyReused := &api.Error{Code: -32602, Message: "invalid", Data: &api.ErrorData{Reason: api.ReasonKeyReused, Field: "key"}}
	for _, test := range []struct {
		name    string
		req     api.OriginChangeRequest
		failure error
		want    *api.Error
	}{
		{"key recorded for another change", api.OriginChangeRequest{Key: "a", Project: "app", Kind: "set", URL: "ssh://host/b"}, nil, keyReused},
		{"expected URL no longer holds", api.OriginChangeRequest{Key: "stale", Project: "app", Kind: "set", URL: "ssh://host/c"}, nil, wantError(api.KindPreconditionFailed, api.ReasonChanged, "")},
		{"unknown project", api.OriginChangeRequest{Key: "missing", Project: "missing", Kind: "set", URL: "ssh://host/a"}, nil, wantError(api.KindNotFound, api.ReasonProject, "project/missing")},
		{"URL syntax", api.OriginChangeRequest{Key: "syntax", Project: "app", Kind: "set", URL: "git@host:a"}, nil, api.InvalidParam("url")},
	} {
		t.Run(test.name, func(t *testing.T) {
			f.err = test.failure
			_, e := callMethod(t, ctx, handler, api.OriginChange, test.req)
			if !reflect.DeepEqual(e, test.want) {
				t.Fatalf("answer %v, want %v", e, test.want)
			}
			if encoded, _ := json.Marshal(e); strings.Contains(string(encoded), "secret") {
				t.Fatalf("answer %s carries the cause's text", encoded)
			}
		})
	}
	f.err = errors.New("offline secret")
	result, e := callMethod(t, ctx, handler, api.OriginChange, api.OriginChangeRequest{Key: "offline", Project: "app", Kind: "set", ExpectedURL: "ssh://host/a", URL: "ssh://host/b"})
	if e != nil || result.Operation.Kind != KindOriginChange || result.Operation.Key != "offline" {
		t.Fatalf("unreachable origin answered %+v %v, want its operation", result, e)
	}
	op, err := s.GetOperation(ctx, result.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantEnded(t, "unreachable origin", op, "failed", api.ReasonOrigin)
	f.err = nil
	for _, m := range []string{api.OriginInspect.Name, api.OriginRefresh.Name} {
		_, e := handler(ctx, m, json.RawMessage(`{"project":"missing"}`))
		if want := wantError(api.KindNotFound, api.ReasonProject, "project/missing"); !reflect.DeepEqual(e, want) {
			t.Fatalf("%s of a missing project answered %v, want %v", m, e, want)
		}
	}
}
