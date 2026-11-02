package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func testEnvironmentImage(project string) EnvironmentImage {
	e := EnvironmentImage{
		Project: project, Key: strings.Repeat("a", 64), Fingerprint: strings.Repeat("b", 64),
		BaseFingerprint: strings.Repeat("c", 64), System: "x86_64-linux",
		MaterialDigest:   strings.Repeat("d", 64),
		CaptureStorePath: "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-capture-env",
		BuilderRequest:   "550e8400-e29b-41d4-a716-446655440000",
	}
	e.Properties = map[string]string{
		"os": "NixOS", "p.contract": "p.incus-system-image/v2", "p.compression": "none",
		"p.instance": "11111111-1111-4111-8111-111111111111", "p.incus_project": "user-1000",
		"p.project_path": e.Project, "p.environment_key": e.Key, "p.base_image": e.BaseFingerprint,
		"p.material": e.MaterialDigest, "p.capture_store_path": e.CaptureStorePath,
		"p.builder_request": e.BuilderRequest, "p.system": e.System,
	}
	return e
}

func TestEnvironmentCacheProjectScopeAndExactMissForget(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	for _, project := range []string{"team/a", "team/b"} {
		if err := s.CreateProject(ctx, project, json.RawMessage(`{"network":"none"}`)); err != nil {
			t.Fatal(err)
		}
	}
	first := testEnvironmentImage("team/a")
	if err := s.PutEnvironmentImage(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetEnvironmentImage(ctx, "team/b", first.Key); err != nil || found {
		t.Fatalf("cross-project cache reuse: found=%v err=%v", found, err)
	}
	got, found, err := s.GetEnvironmentImage(ctx, first.Project, first.Key)
	if err != nil || !found || got.Fingerprint != first.Fingerprint {
		t.Fatalf("cache lookup: %+v %v %v", got, found, err)
	}
	if err := s.ForgetEnvironmentImage(ctx, first.Project, first.Key, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.GetEnvironmentImage(ctx, first.Project, first.Key); !found {
		t.Fatal("wrong fingerprint removed mapping")
	}
	if err := s.ForgetEnvironmentImage(ctx, first.Project, first.Key, first.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.GetEnvironmentImage(ctx, first.Project, first.Key); found {
		t.Fatal("externally absent image remained cached")
	}
}

func TestEnvironmentCacheRejectsForgedLabels(t *testing.T) {
	s, _ := openTestStore(t)
	e := testEnvironmentImage("team/a")
	e.Properties["p.project_path"] = "team/b"
	if err := s.PutEnvironmentImage(context.Background(), e); err == nil {
		t.Fatal("accepted changed project identity")
	}
	e = testEnvironmentImage("team/a")
	e.Properties["p.foreign"] = "owned?"
	if err := s.PutEnvironmentImage(context.Background(), e); err == nil {
		t.Fatal("accepted extra P-owned label")
	}
}
