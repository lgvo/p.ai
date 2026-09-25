package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	_ "modernc.org/sqlite"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	store, err := testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store, dir
}

// Nix build sandboxes present a foreign-owned writable / and /tmp, which the
// production authority check correctly rejects. Test the same ownership and
// symlink rules below a private fixture anchor; the VM exercises the exported
// production entrypoint with normal host ancestry.
func privateFixtureChecker(anchor string) func(string) error {
	return func(path string) error {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return ErrInvalid
		}
		rel, err := filepath.Rel(anchor, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return ErrInvalid
		}
		for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
			info, err := os.Lstat(dir)
			if err != nil {
				return err
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 {
				return ErrInvalid
			}
			if dir == anchor {
				return nil
			}
		}
	}
}

func testScopedOpenStore(dir string) (*Store, error) {
	return openStore(dir, privateFixtureChecker(filepath.Dir(dir)))
}

func TestStoreRestartMigrationAndWriterLock(t *testing.T) {
	store, dir := openTestStore(t)
	ctx := context.Background()
	id, err := store.InstanceID(ctx)
	if err != nil || id == "" {
		t.Fatalf("instance identity: %q %v", id, err)
	}
	if _, err := testScopedOpenStore(dir); err == nil {
		t.Fatal("second daemon acquired state lock")
	}
	if err := store.CreateProject(ctx, "team/app", json.RawMessage(`{"network":"none"}`)); err != nil {
		t.Fatal(err)
	}
	request := ReserveSessionRequest{Key: "restart-create", Project: "team/app", Branch: "work", Choice: "new", Source: "refs/heads/main"}
	firstOperation, firstSession, err := store.ReserveSession(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceOperation(ctx, firstOperation.ID, "blocked", "source-ready", false, nil, "waiting for Git"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restartedID, err := reopened.InstanceID(ctx)
	if err != nil || restartedID != id {
		t.Fatalf("identity changed across restart: %q -> %q (%v)", id, restartedID, err)
	}
	projects, _, _, err := reopened.Count(ctx)
	if err != nil || projects != 1 {
		t.Fatalf("project was lost across restart: %d %v", projects, err)
	}
	replayedOperation, replayedSession, err := reopened.ReserveSession(ctx, request)
	if err != nil || replayedOperation.ID != firstOperation.ID || replayedOperation.Status != "blocked" || replayedOperation.Phase != "source-ready" || replayedSession.UUID != firstSession.UUID || replayedSession.PolicySHA256 != firstSession.PolicySHA256 {
		t.Fatalf("operation/reservation changed across restart: %+v %+v %v", replayedOperation, replayedSession, err)
	}
	// A future schema must be refused without attempting a downgrade.
	reopened.Close()
	db, err := sql.Open("sqlite", filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 18"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := testScopedOpenStore(dir); err == nil {
		t.Fatal("future schema accepted")
	}
}

func TestVersionOneGitMigrationIsAtomicAndRestartable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "control.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(migration1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO metadata(key,value) VALUES('instance_id','v1-fixture')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dbPath, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var version int
	if err := reopened.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 17 {
		t.Fatalf("version after restart: %d %v", version, err)
	}
	for _, table := range []string{"git_principals", "git_ref_guards", "git_unborn_grants"} {
		var name string
		if err := reopened.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name); err != nil || name != table {
			t.Fatalf("Git table %q after restart: %q %v", table, name, err)
		}
	}
}

func TestReservationIsAtomicIdempotentAndPolicyIsImmutable(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.CreateProject(ctx, "app", json.RawMessage(`{"b":2,"a":1}`)); err != nil {
		t.Fatal(err)
	}
	request := ReserveSessionRequest{Key: "create-1", Project: "app", Branch: "feature/a", Choice: "new", Source: "refs/heads/main"}
	op, session, err := store.ReserveSession(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if string(session.Policy) != `{"a":1,"b":2}` || session.UUID == "" || op.ID == "" {
		t.Fatalf("wrong snapshot or identity: %+v %+v", session, op)
	}
	if err := store.SetProjectPolicy(ctx, "app", json.RawMessage(`{"a":3}`)); err != nil {
		t.Fatal(err)
	}
	again, againSession, err := store.ReserveSession(ctx, request)
	if err != nil || again.ID != op.ID || againSession.UUID != session.UUID || string(againSession.Policy) != string(session.Policy) {
		t.Fatalf("idempotent request changed identity or policy: %+v %+v %v", again, againSession, err)
	}
	changed := request
	changed.Source = "refs/heads/other"
	if _, _, err := store.ReserveSession(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed request reused key: %v", err)
	}
	branchConflict := request
	branchConflict.Key, branchConflict.Source = "create-2", "refs/heads/main"
	if _, _, err := store.ReserveSession(ctx, branchConflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("assigned branch reused: %v", err)
	}
	_, sessions, operations, err := store.Count(ctx)
	if err != nil || sessions != 1 || operations != 1 {
		t.Fatalf("failed reservation left partial rows: sessions=%d operations=%d err=%v", sessions, operations, err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE sessions SET policy_json='{}' WHERE uuid=?", session.UUID); err == nil {
		t.Fatal("session policy changed in place")
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE operations SET request_json='{}' WHERE id=?", op.ID); err == nil {
		t.Fatal("operation request changed in place")
	}
	if err := store.AdvanceOperation(ctx, op.ID, "blocked", "source-ready", true, json.RawMessage(`{"expected_ref":"refs/heads/feature/a"}`), "waiting for source"); err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceOperation(ctx, op.ID, "running", "source-ready", false, nil, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("commit point moved backwards: %v", err)
	}
	if err := store.AdvanceOperation(ctx, op.ID, "completed", "established", true, nil, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("unresolved creation completed: %v", err)
	}
	stored, err := store.GetSession(ctx, session.UUID)
	if err != nil || string(stored.Policy) != string(session.Policy) {
		t.Fatalf("session policy corrupted: %+v %v", stored, err)
	}
}

func TestOperationResultOutlivesRemovedSessionAndProject(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	request := ReserveSessionRequest{Key: "old-create", Project: "app", Branch: "old", Choice: "new", Source: "refs/heads/main"}
	op, session, err := store.ReserveSession(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, "DELETE FROM sessions WHERE uuid=?", session.UUID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, "DELETE FROM projects WHERE path='app'"); err != nil {
		t.Fatal(err)
	}
	replayed, removed, err := store.ReserveSession(ctx, request)
	if err != nil || replayed.ID != op.ID || removed.UUID != "" {
		t.Fatalf("retained result replay: %+v %+v %v", replayed, removed, err)
	}
}

func TestConcurrentBranchReservationHasOneWinner(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	const n = 16
	var wg sync.WaitGroup
	results := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := store.ReserveSession(ctx, ReserveSessionRequest{Key: string(rune('a' + i)), Project: "app", Branch: "same", Choice: "new", Source: "refs/heads/main"})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected reservation error: %v", err)
		}
	}
	if success != 1 || conflicts != n-1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
}

func TestConfigRejectsWritableOrSymlinkedAuthority(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	config := filepath.Join(dir, "host.json")
	data := []byte(`{"schema":"p.host/v1","state_dir":"` + state + `"}`)
	if err := os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	loadConfig := func(path string) (HostConfig, error) { return loadHostConfig(path, privateFixtureChecker(dir)) }
	if _, err := loadConfig(config); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(config, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(config); err == nil {
		t.Fatal("readable host config accepted")
	}
	if err := os.Chmod(config, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(config, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(link); err == nil {
		t.Fatal("symlinked host config accepted")
	}
	if err := os.WriteFile(config, []byte(`{"schema":"p.host/v1","schema":"p.host/v1","state_dir":"`+state+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(config); err == nil {
		t.Fatal("duplicate config key accepted")
	}
	if err := storePolicyRejectsDuplicate(); err == nil {
		t.Fatal("duplicate policy key accepted")
	}
}

func TestProductionPathCheckRefusesForeignRoot(t *testing.T) {
	root, err := os.Lstat("/")
	if err != nil {
		t.Fatal(err)
	}
	stat := root.Sys().(*syscall.Stat_t)
	if stat.Uid == 0 {
		t.Skip("root is owned by root on this host")
	}
	if err := CheckTrustedAncestors(filepath.Join(t.TempDir(), "probe")); err == nil {
		t.Fatal("foreign-owned root was trusted")
	}
}

func storePolicyRejectsDuplicate() error {
	_, err := normalizedObject(json.RawMessage(`{"grants":{"network":"none","network":"public-egress"}}`))
	return err
}

func TestPolicyNumbersRetainExactValue(t *testing.T) {
	normalized, err := normalizedObject(json.RawMessage(`{"limit":9007199254740993,"nested":{"value":18446744073709551615}}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(normalized) != `{"limit":9007199254740993,"nested":{"value":18446744073709551615}}` {
		t.Fatalf("policy number changed: %s", normalized)
	}
}
