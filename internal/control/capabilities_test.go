package control

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/sourcedriver"
	sourcememory "github.com/lgvo/p.ai/internal/sourcedriver/memory"
)

type fixtureGitReader struct{}

func (fixtureGitReader) Branches(context.Context, string) ([]sourcedriver.Head, error) {
	return []sourcedriver.Head{{Name: "main", Tip: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}, nil
}

func TestConfiguredReadOnlyProjectMethods(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.CreateProject(ctx, "team/app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(HandlerConfig{Store: store, Git: fixtureGitReader{}, GitInfo: &api.GitInfo{Endpoint: "127.0.0.1:2222"}})
	for _, test := range []struct {
		method, params string
		wantError      bool
	}{
		{"project.list", `{"limit":10}`, false},
		{"project.branches", `{"project":"team/app","limit":8}`, false},
		{"project.list", `{"limit":101}`, true},
		{"project.branches", `{"project":"team/app","limit":9}`, true},
		{"project.branches", `{"project":"missing","limit":8}`, true},
		{"project.branches", `{"project":"team/app","limit":8,"extra":1}`, true},
	} {
		result, rpcErr := handler(ctx, test.method, json.RawMessage(test.params))
		if (rpcErr != nil) != test.wantError || (rpcErr == nil && result == nil) {
			t.Fatalf("%s %s: result=%v error=%v", test.method, test.params, result, rpcErr)
		}
	}
	projectsResult, rpcErr := handler(ctx, "project.list", json.RawMessage(`{"limit":1}`))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	projects := projectsResult.(api.ProjectListResult).Projects
	if len(projects) != 1 || projects[0].Path != "team/app" {
		t.Fatalf("unexpected projects: %#v", projects)
	}
	branchesResult, rpcErr := handler(ctx, "project.branches", json.RawMessage(`{"project":"team/app","limit":8}`))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	refs := branchesResult.(api.ProjectBranchesResult).Refs
	if len(refs) != 1 || refs[0].Ref != "refs/heads/main" {
		t.Fatalf("unexpected refs: %#v", refs)
	}
	hello, rpcErr := callMethod(t, ctx, handler, api.SystemHello, api.HelloRequest{Protocol: api.Protocol})
	if rpcErr != nil || !hello.Has(api.CapabilityGit) {
		t.Fatalf("missing configured Git capability: %+v %+v", hello, rpcErr)
	}
	inspect, rpcErr := callMethod(t, ctx, handler, api.SystemInspect, api.SystemInspectRequest{})
	if rpcErr != nil || inspect.Git == nil || inspect.Git.Endpoint != "127.0.0.1:2222" {
		t.Fatalf("missing Git connection details: %+v %+v", inspect, rpcErr)
	}
}

// TestProjectBranchesPagesTheSourceDriversHeads pages project.branches over
// the source driver's heads: full refs in byte order after the cursor, at
// most limit of them, and next only while more heads follow.
func TestProjectBranchesPagesTheSourceDriversHeads(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.CreateProject(ctx, "team/app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	world := sourcememory.NewWorld()
	driver := sourcememory.NewDriver(world)
	if _, err := driver.CreateRepository(ctx, "team/app"); err != nil {
		t.Fatal(err)
	}
	tip := world.Commit("team/app")
	world.SetHeads("team/app", []string{"b", "a", "c/d", "e"}, tip)
	handler := NewHandler(HandlerConfig{Store: store, Git: driver, GitInfo: &api.GitInfo{Endpoint: "127.0.0.1:2222"}})
	page := func(after string, limit int) (api.ProjectBranchesResult, *api.Error) {
		result, answer := callMethod(t, ctx, handler, api.ProjectBranches, api.ProjectBranchesRequest{Project: "team/app", After: after, Limit: limit})
		return result, answer
	}
	refs := func(r api.ProjectBranchesResult) []string {
		var names []string
		for _, ref := range r.Refs {
			if ref.OID != string(tip) {
				t.Fatalf("ref %s at %s, want %s", ref.Ref, ref.OID, tip)
			}
			names = append(names, ref.Ref)
		}
		return names
	}
	for _, test := range []struct {
		after string
		limit int
		refs  []string
		next  string
	}{
		{"", 2, []string{"refs/heads/a", "refs/heads/b"}, "refs/heads/b"},
		{"refs/heads/b", 2, []string{"refs/heads/c/d", "refs/heads/e"}, ""},
		{"refs/heads/a", 3, []string{"refs/heads/b", "refs/heads/c/d", "refs/heads/e"}, ""},
		{"refs/heads/e", 8, nil, ""},
	} {
		result, answer := page(test.after, test.limit)
		if answer != nil || !reflect.DeepEqual(refs(result), test.refs) || result.Next != test.next || result.Refs == nil {
			t.Errorf("page after %q limit %d: %+v %v, want %v next %q", test.after, test.limit, result, answer, test.refs, test.next)
		}
	}
	world.MoveDuringRead("team/app", "a", world.Commit("team/app", tip))
	if _, answer := page("", 8); answer == nil || answer.Kind() != api.KindRetry || answer.Reason() != api.ReasonRefsMoved {
		t.Errorf("heads moving during the read answered %v, want retry refs_moved", answer)
	}
	driver.Kill()
	if _, answer := page("", 8); answer == nil || answer.Kind() != api.KindFailed || answer.Reason() != api.ReasonGit {
		t.Errorf("a source driver that cannot read answered %v, want failed git", answer)
	}
}

func TestHostPrincipalIdentityMustMatch(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	const first = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const changed = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := store.EnsureHostGitPrincipal(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureHostGitPrincipal(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureHostGitPrincipal(ctx, changed); err == nil {
		t.Fatal("accepted changed host principal")
	}
	if err := store.RevokeGitPrincipal(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureHostGitPrincipal(ctx, first); err == nil {
		t.Fatal("accepted revoked host principal")
	}
}

func TestGitServerIdentityPersistsInStore(t *testing.T) {
	store, dir := openTestStore(t)
	ctx := context.Background()
	const first = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const changed = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	if _, ok, err := store.GitServerIdentity(ctx); err != nil || ok {
		t.Fatalf("new store has identity: %v %v", ok, err)
	}
	if err := store.EnsureGitServerIdentity(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if got, ok, err := restarted.GitServerIdentity(ctx); err != nil || !ok || got != first {
		t.Fatalf("identity after restart: %q %v %v", got, ok, err)
	}
	if err := restarted.EnsureGitServerIdentity(ctx, changed); err == nil {
		t.Fatal("accepted changed server identity")
	}
}
