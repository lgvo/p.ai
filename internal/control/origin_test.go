package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

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

func TestOriginAssociationReplayFailureRefreshAndRestart(t *testing.T) {
	s, dir := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{"network":"none"}`)); err != nil {
		t.Fatal(err)
	}
	ref := plugin.GitOriginRef{Ref: "refs/heads/main", OID: strings.Repeat("a", 40), CommitOID: strings.Repeat("a", 40)}
	f := &fakeOriginRunner{refs: []plugin.GitOriginRef{ref}}
	a := OriginAPI{Store: s, Runner: f}
	first := OriginChange{Key: "first", Project: "app", Kind: "set", ProposedURL: "ssh://host/a"}
	state, err := a.Change(ctx, first)
	if err != nil || state.Status != "fresh" || state.RefCount != 1 || f.calls != 1 {
		t.Fatalf("first: %+v %v %d", state, err, f.calls)
	}
	f.err = errors.New("offline")
	second := OriginChange{Key: "second", Project: "app", Kind: "set", ExpectedURL: first.ProposedURL, ProposedURL: "ssh://host/b"}
	if _, err = a.Change(ctx, second); err == nil {
		t.Fatal("failed contact changed association")
	}
	state, refs, err := s.Origin(ctx, "app")
	if err != nil || state.URL != first.ProposedURL || state.Status != "fresh" || len(refs) != 1 {
		t.Fatalf("failed set: %+v %+v %v", state, refs, err)
	}
	if _, err = a.Change(ctx, OriginChange{Key: "first", Project: "app", Kind: "remove"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting key: %v", err)
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
	state, err = a.Change(ctx, first)
	if err != nil || state.Status != "fresh" || f.calls != 3 {
		t.Fatalf("completed replay: %+v %v calls=%d", state, err, f.calls)
	}
	f.err = nil
	state, err = a.Change(ctx, second)
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
	state, err = a.Change(ctx, OriginChange{Key: "remove", Project: "app", Kind: "remove", ExpectedURL: second.ProposedURL})
	if err != nil || state.Status != "local-only" {
		t.Fatalf("remove: %+v %v", state, err)
	}
	state, refs, err = s.Origin(ctx, "app")
	if err != nil || state.Status != "local-only" || len(refs) != 0 {
		t.Fatalf("removed: %+v %+v %v", state, refs, err)
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
	api := OriginAPI{Store: s, Runner: f}
	handler := OriginHandler(StateHandler(s), api)
	_, e := handler(ctx, "origin.change", json.RawMessage(`{"v":1,"key":"a","project":"app","kind":"set","url":"ssh://host/a","extra":1}`))
	if e == nil || e.Kind != "invalid_params" {
		t.Fatalf("unknown field: %+v", e)
	}
	_, e = handler(ctx, "origin.change", json.RawMessage(`{"v":1,"v":1,"key":"a","project":"app","kind":"set","url":"ssh://host/a"}`))
	if e == nil || e.Kind != "invalid_params" {
		t.Fatalf("duplicate field: %+v", e)
	}
	for _, bad := range []string{
		`{"V":2,"v":1,"key":"a","project":"app","kind":"set","url":"ssh://host/a"}`,
		`{"v":1,"Key":"a","project":"app","kind":"set","url":"ssh://host/a"}`,
		`{"v":1,"key":"a","Project":"app","kind":"set","url":"ssh://host/a"}`,
		`{"v":1,"key":"a","project":"app","kind":"set","URL":"ssh://host/a"}`,
	} {
		if _, e = handler(ctx, "origin.change", json.RawMessage(bad)); e == nil || e.Kind != "invalid_params" {
			t.Fatalf("inexact field accepted: %s %+v", bad, e)
		}
	}
	if _, e = handler(ctx, "origin.change", json.RawMessage(`{"v":1,"key":"a","project":"app","kind":"set","url":"ssh://host/a"}`)); e != nil {
		t.Fatal(e)
	}
	after := ""
	seen := 0
	for {
		raw, _ := json.Marshal(map[string]any{"v": 1, "project": "app", "after": after, "limit": 16})
		out, e := handler(ctx, "origin.sources", raw)
		if e != nil {
			t.Fatal(e)
		}
		page := out.(map[string]any)
		encoded, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage(`"` + strings.Repeat(`\u0000`, 128) + `"`), Result: page})
		if len(encoded)+1 > MaxFrameBytes {
			t.Fatalf("page exceeds frame: %d", len(encoded))
		}
		seen += len(page["refs"].([]plugin.GitOriginRef))
		after = page["next"].(string)
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
	if _, _, err := s.PrepareOriginChange(ctx, change); err != nil {
		t.Fatal(err)
	}
	bad := plugin.GitOriginRef{Ref: "refs/heads/" + string([]byte{0xff}), OID: strings.Repeat("a", 40), CommitOID: strings.Repeat("a", 40)}
	if err := s.CommitOriginChange(ctx, change, []plugin.GitOriginRef{bad}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid UTF-8 persisted: %v", err)
	}
	state, refs, err := s.Origin(ctx, "app")
	if err != nil || state.Status != "local-only" || len(refs) != 0 {
		t.Fatalf("failed commit changed state: %+v %+v %v", state, refs, err)
	}
	valid := plugin.GitOriginRef{Ref: "refs/heads/café", OID: strings.Repeat("a", 40), CommitOID: strings.Repeat("a", 40)}
	if err := s.CommitOriginChange(ctx, change, []plugin.GitOriginRef{valid}); err != nil {
		t.Fatalf("valid UTF-8 ref refused: %v", err)
	}
}
