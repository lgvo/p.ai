package control

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrConflict = errors.New("conflict")
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid input")
)

// Store is the sole daemon writer. The lock stays open for the Store lifetime.
type Store struct {
	db           *sql.DB
	lock         *os.File
	stateDir     string
	gitAuthority sync.RWMutex
	statusMu     sync.Mutex
	attachments  map[string]int
	statusRates  map[string]statusRate
	attemptRates map[string]statusRate
}

type Session struct {
	UUID         string          `json:"uuid"`
	Project      string          `json:"project"`
	Branch       string          `json:"branch"`
	Registry     string          `json:"registry_state"`
	Policy       json.RawMessage `json:"policy"`
	PolicySHA256 string          `json:"policy_sha256"`
}

type Operation struct {
	ID          string          `json:"id"`
	Key         string          `json:"idempotency_key"`
	Kind        string          `json:"kind"`
	Project     string          `json:"project"`
	SessionUUID string          `json:"session_uuid,omitempty"`
	Request     json.RawMessage `json:"request"`
	Status      string          `json:"status"`
	Phase       string          `json:"phase"`
	Committed   bool            `json:"committed"`
	Evidence    json.RawMessage `json:"evidence,omitempty"`
	Diagnostic  string          `json:"diagnostic,omitempty"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

type ReserveSessionRequest struct {
	Key               string `json:"key"`
	Project           string `json:"project"`
	Branch            string `json:"branch"`
	Choice            string `json:"choice"`
	Source            string `json:"source,omitempty"`
	OriginRef         string `json:"origin_ref,omitempty"`
	ExpectedCommitOID string `json:"expected_commit_oid,omitempty"`
}

const migration1 = `
CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE projects (
 path TEXT PRIMARY KEY, policy_json TEXT NOT NULL, policy_sha256 TEXT NOT NULL,
 registry_state TEXT NOT NULL DEFAULT 'active' CHECK(registry_state IN ('active','deleting'))
);
CREATE TABLE sessions (
 uuid TEXT PRIMARY KEY, project_path TEXT NOT NULL REFERENCES projects(path),
 branch TEXT NOT NULL, registry_state TEXT NOT NULL CHECK(registry_state IN ('creating','established','removing')),
 policy_json TEXT NOT NULL, policy_sha256 TEXT NOT NULL,
 UNIQUE(project_path, branch)
);
CREATE TABLE operations (
 id TEXT PRIMARY KEY, idempotency_key TEXT NOT NULL UNIQUE,
 kind TEXT NOT NULL, project_path TEXT NOT NULL,
 session_uuid TEXT, request_json TEXT NOT NULL,
 request_sha256 TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('running','blocked','failed','unknown','superseded','completed')),
 phase TEXT NOT NULL, committed INTEGER NOT NULL DEFAULT 0 CHECK(committed IN (0,1)),
 evidence_json TEXT, diagnostic TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX active_session_operation ON operations(session_uuid)
 WHERE session_uuid IS NOT NULL AND status IN ('running','blocked','unknown');
CREATE TRIGGER immutable_session_policy BEFORE UPDATE OF uuid,project_path,policy_json,policy_sha256 ON sessions
 BEGIN SELECT RAISE(ABORT, 'immutable session identity or policy'); END;
CREATE TRIGGER immutable_operation_request BEFORE UPDATE OF id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,created_at ON operations
 BEGIN SELECT RAISE(ABORT, 'immutable operation request'); END;
PRAGMA user_version = 1;
`

const migration2 = `
CREATE TABLE git_principals (
 fingerprint TEXT PRIMARY KEY, role TEXT NOT NULL CHECK(role IN ('host','session')),
 project_path TEXT, session_uuid TEXT, active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0,1)),
 CHECK((role='host' AND project_path IS NULL AND session_uuid IS NULL) OR
       (role='session' AND project_path IS NOT NULL AND session_uuid IS NOT NULL))
);
CREATE UNIQUE INDEX one_host_git_principal ON git_principals(role) WHERE role='host' AND active=1;
CREATE UNIQUE INDEX one_session_git_principal ON git_principals(session_uuid) WHERE role='session' AND active=1;
CREATE TABLE git_ref_guards (
 project_path TEXT NOT NULL, branch TEXT NOT NULL, operation_id TEXT NOT NULL REFERENCES operations(id),
 PRIMARY KEY(project_path,branch)
);
CREATE TABLE git_unborn_grants (
 session_uuid TEXT PRIMARY KEY REFERENCES sessions(uuid),
 state TEXT NOT NULL CHECK(state IN ('pending','consumed'))
);
PRAGMA user_version = 2;
`

const migration3 = `
CREATE TABLE session_unattended (
 session_uuid TEXT PRIMARY KEY REFERENCES sessions(uuid) ON DELETE CASCADE,
 condition TEXT NOT NULL CHECK(condition IN ('running','attention','idle','failed','unknown')),
 source TEXT NOT NULL, reason TEXT NOT NULL,
 adapter TEXT NOT NULL, adapter_version TEXT NOT NULL,
 received_at TEXT NOT NULL, receive_sequence INTEGER NOT NULL
);
CREATE TABLE status_sequence (id INTEGER PRIMARY KEY CHECK(id=1), next_value INTEGER NOT NULL);
INSERT INTO status_sequence(id,next_value) VALUES(1,1);
PRAGMA user_version = 3;
`

const migration4 = `
CREATE TABLE project_origins (
 project_path TEXT PRIMARY KEY REFERENCES projects(path) ON DELETE CASCADE,
 url TEXT NOT NULL, current_status TEXT NOT NULL CHECK(current_status IN ('fresh','unknown')),
 observed_at TEXT NOT NULL, refs_json TEXT NOT NULL, diagnostic TEXT NOT NULL DEFAULT ''
);
CREATE TABLE origin_requests (
 idempotency_key TEXT PRIMARY KEY, project_path TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('set','remove')),
 expected_url TEXT NOT NULL, proposed_url TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('prepared','completed')),
 created_at TEXT NOT NULL, completed_at TEXT NOT NULL DEFAULT '', result_json TEXT NOT NULL DEFAULT ''
);
CREATE TRIGGER origin_request_key_conflict BEFORE INSERT ON origin_requests
 WHEN EXISTS(SELECT 1 FROM operations WHERE idempotency_key=NEW.idempotency_key)
 BEGIN SELECT RAISE(ABORT,'constraint failed: idempotency key'); END;
CREATE TRIGGER lifecycle_request_key_conflict BEFORE INSERT ON operations
 WHEN EXISTS(SELECT 1 FROM origin_requests WHERE idempotency_key=NEW.idempotency_key)
 BEGIN SELECT RAISE(ABORT,'constraint failed: idempotency key'); END;
PRAGMA user_version = 4;
`

const migration5 = `
DROP TRIGGER immutable_operation_request;
CREATE TRIGGER immutable_operation_request BEFORE UPDATE OF id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,created_at ON operations
 WHEN OLD.id != NEW.id OR OLD.idempotency_key != NEW.idempotency_key OR OLD.kind != NEW.kind OR OLD.project_path != NEW.project_path OR OLD.request_json != NEW.request_json OR OLD.request_sha256 != NEW.request_sha256 OR OLD.created_at != NEW.created_at
 OR (OLD.session_uuid IS NOT NEW.session_uuid AND NOT (OLD.kind='project.create' AND OLD.phase='repo-pending' AND OLD.session_uuid IS NULL AND NEW.session_uuid IS NOT NULL))
 BEGIN SELECT RAISE(ABORT, 'immutable operation request'); END;
PRAGMA user_version = 5;
`

const migration6 = `
CREATE TABLE publication_requests (
 idempotency_key TEXT PRIMARY KEY, project_path TEXT NOT NULL,
 expected_origin_url TEXT NOT NULL,
 source_kind TEXT NOT NULL CHECK(source_kind IN ('session','retained')),
 source TEXT NOT NULL, source_oid TEXT NOT NULL, destination_ref TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('prepared','attempted','completed')),
 result_json TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, completed_at TEXT NOT NULL DEFAULT ''
);
CREATE TRIGGER publication_key_operations BEFORE INSERT ON publication_requests
 WHEN EXISTS(SELECT 1 FROM operations WHERE idempotency_key=NEW.idempotency_key)
 BEGIN SELECT RAISE(ABORT,'constraint failed: idempotency key'); END;
CREATE TRIGGER publication_key_origins BEFORE INSERT ON publication_requests
 WHEN EXISTS(SELECT 1 FROM origin_requests WHERE idempotency_key=NEW.idempotency_key)
 BEGIN SELECT RAISE(ABORT,'constraint failed: idempotency key'); END;
CREATE TRIGGER operations_key_publication BEFORE INSERT ON operations
 WHEN EXISTS(SELECT 1 FROM publication_requests WHERE idempotency_key=NEW.idempotency_key)
 BEGIN SELECT RAISE(ABORT,'constraint failed: idempotency key'); END;
CREATE TRIGGER origins_key_publication BEFORE INSERT ON origin_requests
 WHEN EXISTS(SELECT 1 FROM publication_requests WHERE idempotency_key=NEW.idempotency_key)
 BEGIN SELECT RAISE(ABORT,'constraint failed: idempotency key'); END;
PRAGMA user_version = 6;
`

const migration7 = `
CREATE TABLE environment_images (
 project_path TEXT NOT NULL REFERENCES projects(path),
 environment_key TEXT NOT NULL,
 fingerprint TEXT NOT NULL,
 base_fingerprint TEXT NOT NULL,
 system TEXT NOT NULL,
 material_digest TEXT NOT NULL,
 capture_store_path TEXT NOT NULL,
 builder_request_uuid TEXT NOT NULL,
 properties_json TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(project_path, environment_key)
);
CREATE INDEX environment_images_fingerprint ON environment_images(fingerprint);
PRAGMA user_version = 7;
`

const migration8 = `
ALTER TABLE environment_images ADD COLUMN logical_size INTEGER NOT NULL DEFAULT 0 CHECK(logical_size >= 0);
ALTER TABLE environment_images ADD COLUMN last_used_at TEXT NOT NULL DEFAULT '';
CREATE TRIGGER collect_requires_quiescent_project BEFORE INSERT ON operations
 WHEN NEW.kind='environment.collect' AND EXISTS(
   SELECT 1 FROM operations WHERE project_path=NEW.project_path
   AND kind IN ('project.create','session.create') AND status='running')
 BEGIN SELECT RAISE(ABORT,'constraint failed: project creation active during cache collection'); END;
CREATE TRIGGER create_excludes_collection BEFORE INSERT ON operations
 WHEN NEW.kind IN ('project.create','session.create') AND EXISTS(
   SELECT 1 FROM operations WHERE project_path=NEW.project_path
   AND kind='environment.collect' AND status IN ('running','blocked','unknown'))
 BEGIN SELECT RAISE(ABORT,'constraint failed: cache collection active'); END;
CREATE TRIGGER retry_excludes_collection BEFORE UPDATE OF status ON operations
 WHEN NEW.kind IN ('project.create','session.create') AND NEW.status='running' AND OLD.status!='running'
 AND EXISTS(SELECT 1 FROM operations WHERE project_path=NEW.project_path
   AND kind='environment.collect' AND status IN ('running','blocked','unknown'))
 BEGIN SELECT RAISE(ABORT,'constraint failed: cache collection active'); END;
CREATE TRIGGER cache_insert_excludes_collection BEFORE INSERT ON environment_images
 WHEN EXISTS(SELECT 1 FROM operations WHERE project_path=NEW.project_path
   AND kind='environment.collect' AND status IN ('running','blocked','unknown'))
 BEGIN SELECT RAISE(ABORT,'constraint failed: cache collection active'); END;
CREATE TRIGGER cache_update_excludes_collection BEFORE UPDATE ON environment_images
 WHEN EXISTS(SELECT 1 FROM operations WHERE project_path=NEW.project_path
   AND kind='environment.collect' AND status IN ('running','blocked','unknown'))
 BEGIN SELECT RAISE(ABORT,'constraint failed: cache collection active'); END;
CREATE UNIQUE INDEX collection_one_active_per_project ON operations(project_path)
 WHERE kind='environment.collect' AND status IN ('running','blocked','unknown');
PRAGMA user_version = 8;
`

// A workspace read holds the same per-session durable exclusion as lifecycle
// mutations. Its immutable request identifies the helper before Incus init;
// phases and evidence are advanced only after exact native postconditions.
const migration9 = `
CREATE TRIGGER workspace_read_requires_established BEFORE INSERT ON operations
 WHEN NEW.kind='workspace.inspect' AND NOT EXISTS(
   SELECT 1 FROM sessions WHERE uuid=NEW.session_uuid AND project_path=NEW.project_path
   AND registry_state='established')
 BEGIN SELECT RAISE(ABORT,'constraint failed: workspace session unavailable'); END;
PRAGMA user_version = 9;
`

const migration10 = `
CREATE TRIGGER IF NOT EXISTS workspace_loss_read_requires_established BEFORE INSERT ON operations
 WHEN NEW.kind='workspace.loss.inspect' AND NOT EXISTS(
   SELECT 1 FROM sessions WHERE uuid=NEW.session_uuid AND project_path=NEW.project_path
   AND registry_state='established')
 BEGIN SELECT RAISE(ABORT,'constraint failed: workspace session unavailable'); END;
PRAGMA user_version = 10;
`

const migration11 = `
CREATE TRIGGER IF NOT EXISTS discard_requires_established BEFORE INSERT ON operations
 WHEN NEW.kind='session.discard' AND NOT EXISTS(
  SELECT 1 FROM sessions WHERE uuid=NEW.session_uuid AND project_path=NEW.project_path
  AND registry_state='established')
 BEGIN SELECT RAISE(ABORT,'constraint failed: discard session unavailable'); END;
PRAGMA user_version = 11;
`

const migration12 = `
CREATE TRIGGER IF NOT EXISTS delete_requires_established BEFORE INSERT ON operations
 WHEN NEW.kind='session.delete' AND NOT EXISTS(
  SELECT 1 FROM sessions WHERE uuid=NEW.session_uuid AND project_path=NEW.project_path
  AND registry_state='established')
 BEGIN SELECT RAISE(ABORT,'constraint failed: delete session unavailable'); END;
PRAGMA user_version = 12;
`

// Rename reserves both names before any native effect. A concurrent create
// cannot claim the new name while its guarded session row still names the old
// branch.
const migration13 = `
CREATE TRIGGER IF NOT EXISTS rename_requires_established BEFORE INSERT ON operations
 WHEN NEW.kind='session.rename' AND NOT EXISTS(
   SELECT 1 FROM sessions WHERE uuid=NEW.session_uuid AND project_path=NEW.project_path
   AND registry_state='established')
 BEGIN SELECT RAISE(ABORT,'constraint failed: rename session unavailable'); END;
CREATE TRIGGER IF NOT EXISTS session_insert_excludes_rename_reservation BEFORE INSERT ON sessions
 WHEN EXISTS(SELECT 1 FROM git_ref_guards g JOIN operations o ON o.id=g.operation_id
   WHERE g.project_path=NEW.project_path AND g.branch=NEW.branch
   AND o.kind='session.rename' AND o.status IN ('running','blocked','unknown'))
 BEGIN SELECT RAISE(ABORT,'constraint failed: branch reserved by rename'); END;
PRAGMA user_version = 13;
`

const migration14 = `
CREATE TRIGGER IF NOT EXISTS repair_requires_established BEFORE INSERT ON operations
 WHEN NEW.kind='session.repair' AND NOT EXISTS(
   SELECT 1 FROM sessions WHERE uuid=NEW.session_uuid AND project_path=NEW.project_path
   AND registry_state='established')
 BEGIN SELECT RAISE(ABORT,'constraint failed: repair session unavailable'); END;
PRAGMA user_version = 14;
`

// Public IPv4 slots are reserved with the session row, including a blank
// project's bootstrap session. The four slots fit the confined Incus ceiling
// and cannot be claimed twice by concurrent creation transactions.
const migration15 = `
CREATE TABLE session_public_addresses (
 session_uuid TEXT PRIMARY KEY REFERENCES sessions(uuid) ON DELETE CASCADE,
 slot INTEGER NOT NULL UNIQUE CHECK(slot BETWEEN 0 AND 3)
);
PRAGMA user_version = 15;
`

// Repair preparation and confirmed image reconstruction use the same image
// cache/publisher authority as creation. Collection must not race either.
const migration16 = `
DROP TRIGGER collect_requires_quiescent_project;
CREATE TRIGGER collect_requires_quiescent_project BEFORE INSERT ON operations
 WHEN NEW.kind='environment.collect' AND EXISTS(
   SELECT 1 FROM operations WHERE project_path=NEW.project_path
   AND kind IN ('project.create','session.create','session.repair','session.repair.prepare')
   AND status IN ('running','blocked','unknown'))
 BEGIN SELECT RAISE(ABORT,'constraint failed: project environment effect active during cache collection'); END;
DROP TRIGGER create_excludes_collection;
CREATE TRIGGER create_excludes_collection BEFORE INSERT ON operations
 WHEN NEW.kind IN ('project.create','session.create','session.repair','session.repair.prepare') AND EXISTS(
   SELECT 1 FROM operations WHERE project_path=NEW.project_path
   AND kind='environment.collect' AND status IN ('running','blocked','unknown'))
 BEGIN SELECT RAISE(ABORT,'constraint failed: cache collection active'); END;
DROP TRIGGER retry_excludes_collection;
CREATE TRIGGER retry_excludes_collection BEFORE UPDATE OF status ON operations
 WHEN NEW.kind IN ('project.create','session.create','session.repair','session.repair.prepare')
 AND NEW.status='running' AND OLD.status!='running' AND EXISTS(
   SELECT 1 FROM operations WHERE project_path=NEW.project_path
   AND kind='environment.collect' AND status IN ('running','blocked','unknown'))
 BEGIN SELECT RAISE(ABORT,'constraint failed: cache collection active'); END;
CREATE TRIGGER IF NOT EXISTS repair_prepare_requires_established BEFORE INSERT ON operations
 WHEN NEW.kind='session.repair.prepare' AND NOT EXISTS(
   SELECT 1 FROM sessions WHERE uuid=NEW.session_uuid AND project_path=NEW.project_path
   AND registry_state='established')
 BEGIN SELECT RAISE(ABORT,'constraint failed: repair preparation session unavailable'); END;
PRAGMA user_version = 16;
`

const migration17 = `
DROP TRIGGER session_insert_excludes_rename_reservation;
CREATE TRIGGER session_insert_excludes_rename_reservation BEFORE INSERT ON sessions
 WHEN EXISTS(SELECT 1 FROM git_ref_guards g JOIN operations o ON o.id=g.operation_id
   WHERE g.project_path=NEW.project_path AND g.branch=NEW.branch
   AND o.kind IN ('session.rename','project.retained.rename') AND o.status IN ('running','blocked','unknown'))
 BEGIN SELECT RAISE(ABORT,'constraint failed: branch reserved by rename'); END;
PRAGMA user_version = 17;
`

const migration18 = `
DROP TRIGGER session_insert_excludes_rename_reservation;
CREATE TRIGGER session_insert_excludes_rename_reservation BEFORE INSERT ON sessions
 WHEN EXISTS(SELECT 1 FROM git_ref_guards g JOIN operations o ON o.id=g.operation_id
   WHERE g.project_path=NEW.project_path AND g.branch=NEW.branch
   AND o.kind IN ('session.rename','project.retained.rename','project.retained.delete')
   AND o.status IN ('running','blocked','unknown'))
 BEGIN SELECT RAISE(ABORT,'constraint failed: branch reserved by lifecycle'); END;
PRAGMA user_version = 18;
`

func OpenStore(stateDir string) (_ *Store, err error) {
	return openStore(stateDir, CheckTrustedAncestors)
}

func openStore(stateDir string, checkPath func(string) error) (_ *Store, err error) {
	if err := ensureStateDir(stateDir, checkPath); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(stateDir, "daemon.lock")
	fd, err := syscall.Open(lockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), lockPath)
	defer func() {
		if err != nil {
			lock.Close()
		}
	}()
	info, err := lock.Stat()
	if err != nil {
		return nil, err
	}
	if !privateRegular(info) {
		return nil, errors.New("daemon lock must be a private regular file")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, fmt.Errorf("another daemon owns this state directory: %w", err)
	}
	dbPath := filepath.Join(stateDir, "control.sqlite")
	dbFD, err := syscall.Open(dbPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	dbFile := os.NewFile(uintptr(dbFD), dbPath)
	dbInfo, statErr := dbFile.Stat()
	dbFile.Close()
	if statErr != nil {
		return nil, statErr
	}
	if !privateRegular(dbInfo) {
		return nil, errors.New("state database must be a private regular file")
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() {
		if err != nil {
			db.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, pragma := range []string{"PRAGMA foreign_keys = ON", "PRAGMA busy_timeout = 5000"} {
		if _, err = db.ExecContext(ctx, pragma); err != nil {
			return nil, err
		}
	}
	var journalMode string
	if err = db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		return nil, err
	}
	if journalMode != "wal" {
		return nil, fmt.Errorf("SQLite refused WAL mode: %s", journalMode)
	}
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if version > 18 {
		return nil, fmt.Errorf("state schema %d is newer than this binary (supports 18)", version)
	}
	if version == 0 {
		var id string
		id, err = newUUID()
		if err != nil {
			return nil, err
		}
		var tx *sql.Tx
		tx, err = db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, migration1); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES('instance_id',?)", id); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
	}
	if version < 2 {
		var tx *sql.Tx
		tx, err = db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, migration2); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
	}
	if version < 3 {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, migration3); e != nil {
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
	}
	if version < 4 {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, migration4); e != nil {
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
	}
	if version < 5 {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, migration5); e != nil {
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
	}
	if version < 6 {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, migration6); e != nil {
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
	}
	if version < 7 {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, migration7); e != nil {
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
	}
	if version < 8 {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, migration8); e != nil {
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
	}
	if version < 9 {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, migration9); e != nil {
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
	}
	if version < 10 {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, migration10); e != nil {
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
	}
	if version < 11 {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, migration11); e != nil {
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
	}
	if version < 12 {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, migration12); e != nil {
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
	}
	if version < 13 {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, migration13); e != nil {
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
	}
	if version < 14 {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, migration14); e != nil {
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
	}
	if version < 15 {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, migration15); e != nil {
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
	}
	if version < 16 {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, migration16); e != nil {
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
	}
	if version < 17 {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, migration17); e != nil {
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
	}
	if version < 18 {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, migration18); e != nil {
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
	}
	var integrity string
	if err = db.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&integrity); err != nil {
		return nil, err
	}
	if integrity != "ok" {
		return nil, fmt.Errorf("SQLite integrity check failed: %s", integrity)
	}
	var persistedID string
	if err = db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='instance_id'").Scan(&persistedID); err != nil || persistedID == "" {
		return nil, fmt.Errorf("state identity is missing or unreadable: %w", err)
	}
	return &Store{db: db, lock: lock, stateDir: stateDir, attachments: map[string]int{}, statusRates: map[string]statusRate{}, attemptRates: map[string]statusRate{}}, nil
}

func (s *Store) StateDir() string { return s.stateDir }

func privateRegular(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1 && info.Mode().Perm()&0077 == 0
}

func (s *Store) Close() error {
	err := s.db.Close()
	if lockErr := s.lock.Close(); err == nil {
		err = lockErr
	}
	return err
}

func (s *Store) InstanceID(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='instance_id'").Scan(&id)
	return id, err
}

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

type ProjectSummary struct {
	Path     string `json:"path"`
	Registry string `json:"registry_state"`
}

// ListProjects reads registered projects in stable bytewise path order.
func (s *Store) ListProjects(ctx context.Context, after string, limit int) ([]ProjectSummary, string, error) {
	if limit < 1 || limit > 100 || after != "" && !validProject(after) {
		return nil, "", ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT path,registry_state FROM projects WHERE path>? ORDER BY path COLLATE BINARY LIMIT ?`, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var items []ProjectSummary
	for rows.Next() {
		var item ProjectSummary
		if err := rows.Scan(&item.Path, &item.Registry); err != nil {
			return nil, "", err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(items) <= limit {
		return items, "", nil
	}
	return items[:limit], items[limit-1].Path, nil
}

func (s *Store) HasActiveProject(ctx context.Context, project string) (bool, error) {
	if !validProject(project) {
		return false, ErrInvalid
	}
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT registry_state FROM projects WHERE path=?`, project).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return state == "active", err
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func normalizedObject(data json.RawMessage) (json.RawMessage, error) {
	if len(data) == 0 || len(data) > 16384 {
		return nil, fmt.Errorf("%w: policy must be a bounded JSON object", ErrInvalid)
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	var value map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, fmt.Errorf("%w: policy must be a JSON object", ErrInvalid)
	}
	return json.Marshal(value)
}

func validProject(path string) bool {
	if len(path) == 0 || len(path) > 255 || strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return false
	}
	for _, c := range strings.Split(path, "/") {
		if c == "" || c == "." || c == ".." || len(c) > 100 {
			return false
		}
		for _, r := range c {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
				return false
			}
		}
	}
	return true
}

func validBranch(branch string) bool {
	if len(branch) == 0 || len(branch) > 200 || strings.HasSuffix(branch, ".lock") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") {
		return false
	}
	for _, c := range strings.Split(branch, "/") {
		if c == "" || strings.HasPrefix(c, ".") || strings.HasSuffix(c, ".") || strings.HasSuffix(c, ".lock") {
			return false
		}
		for _, r := range c {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
				return false
			}
		}
	}
	return true
}

// CreateProject records registry and normalized trusted policy together. Git
// creation is not exposed until its cross-authority workflow is implemented.
func (s *Store) CreateProject(ctx context.Context, path string, policy json.RawMessage) error {
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	if !validProject(path) {
		return fmt.Errorf("%w: project path", ErrInvalid)
	}
	normalized, err := normalizedObject(policy)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO projects(path,policy_json,policy_sha256) VALUES(?,?,?)", path, string(normalized), digest(normalized))
	return classifyWrite(err)
}

func (s *Store) SetProjectPolicy(ctx context.Context, path string, policy json.RawMessage) error {
	normalized, err := normalizedObject(policy)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, "UPDATE projects SET policy_json=?,policy_sha256=? WHERE path=? AND registry_state='active'", string(normalized), digest(normalized), path)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ReserveSession atomically binds one immutable request to a UUID, branch and
// policy snapshot. It performs no Git or runtime mutation.
func (s *Store) ReserveSession(ctx context.Context, req ReserveSessionRequest) (Operation, Session, error) {
	if err := s.lockGitAuthority(ctx); err != nil {
		return Operation{}, Session{}, err
	}
	defer s.gitAuthority.Unlock()
	var op Operation
	var session Session
	if len(req.Key) < 1 || len(req.Key) > 128 || !validProject(req.Project) || !validBranch(req.Branch) || len(req.Source) > 255 ||
		!((req.Choice == "existing" && req.Source == "") || (req.Choice == "new" && req.Source != "") || (req.Choice == "blank" && req.Branch == "main" && req.Source == "")) {
		return op, session, fmt.Errorf("%w: reservation request", ErrInvalid)
	}
	requestJSON, err := json.Marshal(req)
	if err != nil {
		return op, session, err
	}
	requestHash := digest(requestJSON)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return op, session, err
	}
	defer tx.Rollback()
	var existingHash, existingKind string
	err = tx.QueryRowContext(ctx, "SELECT kind,request_sha256 FROM operations WHERE idempotency_key=?", req.Key).Scan(&existingKind, &existingHash)
	if err == nil {
		if existingKind != "session.create" || existingHash != requestHash {
			return op, session, fmt.Errorf("%w: idempotency key has different request", ErrConflict)
		}
		op, err = getOperationTx(ctx, tx, req.Key)
		if err != nil {
			return op, session, err
		}
		session, err = getSessionTx(ctx, tx, op.SessionUUID)
		if errors.Is(err, sql.ErrNoRows) {
			// The bounded operation result can outlive its removed session.
			return op, Session{}, nil
		}
		return op, session, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return op, session, err
	}
	var policy string
	var policyHash string
	err = tx.QueryRowContext(ctx, "SELECT policy_json,policy_sha256 FROM projects WHERE path=? AND registry_state='active'", req.Project).Scan(&policy, &policyHash)
	if errors.Is(err, sql.ErrNoRows) {
		return op, session, ErrNotFound
	}
	if err != nil {
		return op, session, err
	}
	session.UUID, err = newUUID()
	if err != nil {
		return op, session, err
	}
	op.ID, err = newUUID()
	if err != nil {
		return op, session, err
	}
	session.Project, session.Branch, session.Registry = req.Project, req.Branch, "creating"
	session.Policy, session.PolicySHA256 = json.RawMessage(policy), policyHash
	_, err = tx.ExecContext(ctx, "INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256) VALUES(?,?,?,?,?,?)", session.UUID, session.Project, session.Branch, session.Registry, policy, policyHash)
	if err != nil {
		return op, session, classifyWrite(err)
	}
	if req.Choice == "blank" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO git_unborn_grants(session_uuid,state) VALUES(?,'pending')`, session.UUID); err != nil {
			return op, session, err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,created_at,updated_at)
 VALUES(?,?,?,?,?,?,?,'running','reserved',?,?)`, op.ID, req.Key, "session.create", req.Project, session.UUID, string(requestJSON), requestHash, now, now)
	if err != nil {
		return op, session, classifyWrite(err)
	}
	if err := tx.Commit(); err != nil {
		return op, session, err
	}
	op = Operation{ID: op.ID, Key: req.Key, Kind: "session.create", Project: req.Project, SessionUUID: session.UUID, Request: requestJSON, Status: "running", Phase: "reserved", CreatedAt: now, UpdatedAt: now}
	return op, session, nil
}

func classifyWrite(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "constraint failed") || strings.Contains(err.Error(), "UNIQUE constraint") {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return err
}

func getOperationTx(ctx context.Context, tx *sql.Tx, key string) (Operation, error) {
	var o Operation
	var request, evidence string
	var committed int
	err := tx.QueryRowContext(ctx, `SELECT id,idempotency_key,kind,project_path,COALESCE(session_uuid,''),request_json,status,phase,committed,COALESCE(evidence_json,''),diagnostic,created_at,updated_at
 FROM operations WHERE idempotency_key=?`, key).Scan(&o.ID, &o.Key, &o.Kind, &o.Project, &o.SessionUUID, &request, &o.Status, &o.Phase, &committed, &evidence, &o.Diagnostic, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return o, err
	}
	o.Request, o.Evidence, o.Committed = json.RawMessage(request), json.RawMessage(evidence), committed == 1
	return o, nil
}

func getSessionTx(ctx context.Context, tx *sql.Tx, id string) (Session, error) {
	var s Session
	var policy string
	err := tx.QueryRowContext(ctx, "SELECT uuid,project_path,branch,registry_state,policy_json,policy_sha256 FROM sessions WHERE uuid=?", id).Scan(&s.UUID, &s.Project, &s.Branch, &s.Registry, &policy, &s.PolicySHA256)
	s.Policy = json.RawMessage(policy)
	return s, err
}

func (s *Store) GetSession(ctx context.Context, id string) (Session, error) {
	var session Session
	var policy string
	err := s.db.QueryRowContext(ctx, "SELECT uuid,project_path,branch,registry_state,policy_json,policy_sha256 FROM sessions WHERE uuid=?", id).Scan(&session.UUID, &session.Project, &session.Branch, &session.Registry, &policy, &session.PolicySHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return session, ErrNotFound
	}
	session.Policy = json.RawMessage(policy)
	return session, err
}

// AdvanceSessionRegistry changes only the recorded lifecycle condition. The
// caller must complete and verify the external runtime/Git phase first.
func (s *Store) AdvanceSessionRegistry(ctx context.Context, id, from, to string) error {
	if id == "" || !((from == "creating" && to == "established") || (from == "established" && to == "removing")) {
		return ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	result, err := s.db.ExecContext(ctx, `UPDATE sessions SET registry_state=? WHERE uuid=? AND registry_state=?`, to, id, from)
	if err != nil {
		return classifyWrite(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrConflict
	}
	return nil
}

func (s *Store) Count(ctx context.Context) (projects, sessions, operations int, err error) {
	for i, target := range []*int{&projects, &sessions, &operations} {
		name := []string{"projects", "sessions", "operations"}[i]
		if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+name).Scan(target); err != nil {
			return
		}
	}
	return
}

func (s *Store) AdvanceOperation(ctx context.Context, id, status, phase string, committed bool, evidence json.RawMessage, diagnostic string) error {
	if len(id) == 0 || len(phase) == 0 || len(phase) > 80 || len(diagnostic) > 1024 || len(evidence) > 16384 {
		return ErrInvalid
	}
	switch status {
	case "running", "blocked", "failed", "unknown", "superseded", "completed":
	default:
		return ErrInvalid
	}
	if len(evidence) > 0 && !json.Valid(evidence) {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var kind, sessionID, oldStatus, oldPhase, oldEvidence string
	var oldCommitted int
	err = tx.QueryRowContext(ctx, "SELECT kind,COALESCE(session_uuid,''),status,committed,phase,COALESCE(evidence_json,'') FROM operations WHERE id=?", id).Scan(&kind, &sessionID, &oldStatus, &oldCommitted, &oldPhase, &oldEvidence)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if oldStatus == "completed" || oldStatus == "superseded" || (oldCommitted == 1 && !committed) {
		return ErrConflict
	}
	if kind == "session.create" {
		if err = monotonicCreationEvidence(oldPhase, []byte(oldEvidence), evidence); err != nil {
			return err
		}
	}
	if kind == "session.create" && sessionID != "" && (status == "failed" || status == "completed" || status == "superseded") {
		var registry string
		err = tx.QueryRowContext(ctx, "SELECT registry_state FROM sessions WHERE uuid=?", sessionID).Scan(&registry)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if registry == "creating" {
			return fmt.Errorf("%w: unresolved creation must remain running or blocked", ErrConflict)
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE operations SET status=?,phase=?,committed=?,evidence_json=?,diagnostic=?,updated_at=?
	 WHERE id=?`, status, phase, committed, nullableJSON(evidence), diagnostic, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return classifyWrite(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func nullableJSON(data json.RawMessage) any {
	if len(data) == 0 {
		return nil
	}
	return string(data)
}
