package control

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lgvo/p.ai/internal/plugin"
)

type fixtureGitReader struct{}

func (fixtureGitReader) ListRefsPage(_ context.Context, project, after string, limit int) ([]plugin.GitRef, string, error) {
	return []plugin.GitRef{{Ref: "refs/heads/main", OID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}, "", nil
}

func TestConfiguredReadOnlyProjectMethods(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.CreateProject(ctx, "team/app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	handler := StateHandlerWithGit(store, fixtureGitReader{}, &GitInfo{Endpoint: "127.0.0.1:2222"})
	for _, test := range []struct {
		method, params string
		wantError      bool
	}{
		{"project.list", `{"v":1,"limit":10}`, false},
		{"project.branches", `{"v":1,"project":"team/app","limit":8}`, false},
		{"project.list", `{"v":1,"limit":101}`, true},
		{"project.branches", `{"v":1,"project":"team/app","limit":9}`, true},
		{"project.branches", `{"v":1,"project":"missing","limit":8}`, true},
		{"project.branches", `{"v":1,"project":"team/app","limit":8,"extra":1}`, true},
	} {
		result, rpcErr := handler(ctx, test.method, json.RawMessage(test.params))
		if (rpcErr != nil) != test.wantError || (rpcErr == nil && result == nil) {
			t.Fatalf("%s %s: result=%v error=%v", test.method, test.params, result, rpcErr)
		}
	}
	projectsResult, rpcErr := handler(ctx, "project.list", json.RawMessage(`{"v":1,"limit":1}`))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	projects := projectsResult.(map[string]any)["projects"].([]ProjectSummary)
	if len(projects) != 1 || projects[0].Path != "team/app" {
		t.Fatalf("unexpected projects: %#v", projects)
	}
	branchesResult, rpcErr := handler(ctx, "project.branches", json.RawMessage(`{"v":1,"project":"team/app","limit":8}`))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	refs := branchesResult.(map[string]any)["refs"].([]plugin.GitRef)
	if len(refs) != 1 || refs[0].Ref != "refs/heads/main" {
		t.Fatalf("unexpected refs: %#v", refs)
	}
	result, rpcErr := handler(ctx, "system.capabilities", json.RawMessage(`{"v":1}`))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	encoded, _ := json.Marshal(result)
	if !json.Valid(encoded) || result.(map[string]any)["git"] == nil {
		t.Fatal("missing configured Git capability")
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
