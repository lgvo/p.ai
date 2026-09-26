package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestEnvironmentCacheMigratesVersionSixOnReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "control.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range []string{migration1, migration2, migration3, migration4, migration5, migration6} {
		if _, err := db.Exec(migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO metadata(key,value) VALUES('instance_id','schema-six-fixture')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s, err := testScopedOpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		var version int
		if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 18 {
			t.Fatalf("migration version %d: %v", version, err)
		}
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM environment_images`).Scan(&version); err != nil || version != 0 {
			t.Fatalf("environment table unavailable: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEnvironmentCacheMigratesVersionSevenRowsOnReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "control.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range []string{migration1, migration2, migration3, migration4, migration5, migration6, migration7} {
		if _, err := db.Exec(migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO metadata(key,value) VALUES('instance_id','schema-seven-fixture')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects(path,policy_json,policy_sha256) VALUES('team/a','{}','fixture')`); err != nil {
		t.Fatal(err)
	}
	e := testEnvironmentImage("team/a")
	props, _ := json.Marshal(e.Properties)
	if _, err := db.Exec(`INSERT INTO environment_images(project_path,environment_key,fingerprint,base_fingerprint,system,material_digest,capture_store_path,builder_request_uuid,properties_json,created_at)
	 VALUES(?,?,?,?,?,?,?,?,?,?)`, e.Project, e.Key, e.Fingerprint, e.BaseFingerprint, e.System, e.MaterialDigest, e.CaptureStorePath, e.BuilderRequest, string(props), "2026-09-24T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s, err := testScopedOpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		got, found, err := s.GetEnvironmentImage(context.Background(), e.Project, e.Key)
		if err != nil || !found || got.Fingerprint != e.Fingerprint || got.CreatedAt != "2026-09-24T00:00:00Z" || got.LogicalSize != 0 || got.LastUsedAt != "" {
			t.Fatalf("schema-seven cache row changed: %+v found=%v err=%v", got, found, err)
		}
		var version int
		if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 18 {
			t.Fatalf("version %d: %v", version, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
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

func TestEnvironmentCacheRejectsForgedLabelsAndSchemaMigration(t *testing.T) {
	s, _ := openTestStore(t)
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 18 {
		t.Fatalf("schema version %d: %v", version, err)
	}
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
