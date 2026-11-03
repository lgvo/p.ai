package control

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
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

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/strictjson"
	_ "modernc.org/sqlite"
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

// ReserveSessionRequest is the session.create request, which the store
// reserves and keeps as the creation's request.
type ReserveSessionRequest = api.SessionCreateRequest

// schemaSQL creates the control state on a fresh database.
//
//go:embed schema.sql
var schemaSQL string

// schemaVersion marks a database built from schemaSQL. P does not migrate
// state: every schema change raises it, and openStore refuses a database of
// any other version.
const schemaVersion = 26

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
	var version, objects int
	if err = db.QueryRowContext(ctx, "SELECT (SELECT user_version FROM pragma_user_version), (SELECT count(*) FROM sqlite_master)").Scan(&version, &objects); err != nil {
		return nil, err
	}
	switch {
	case version == 0 && objects == 0:
		if err = createSchema(ctx, db); err != nil {
			return nil, err
		}
	case version != schemaVersion:
		return nil, fmt.Errorf("state schema %d is not this binary's schema %d; P does not upgrade state, so start from a new state directory", version, schemaVersion)
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

// createSchema builds a fresh database in one transaction: the schema, the
// instance identity and the schema version.
func createSchema(ctx context.Context, db *sql.DB) error {
	id, err := newUUID()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES('instance_id',?)", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
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

// ListProjects reads registered projects in stable bytewise path order.
func (s *Store) ListProjects(ctx context.Context, after string, limit int) ([]api.ProjectSummary, string, error) {
	if limit < 1 || limit > 100 || after != "" && !validProject(after) {
		return nil, "", ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT path,registry_state FROM projects WHERE path>? ORDER BY path COLLATE BINARY LIMIT ?`, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var items []api.ProjectSummary
	for rows.Next() {
		var item api.ProjectSummary
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
	if err := strictjson.RejectDuplicateKeys(data); err != nil {
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

func validProject(path string) bool { return api.ValidProject(path) }

func validBranch(branch string) bool { return api.ValidBranch(branch) }

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
func (s *Store) ReserveSession(ctx context.Context, req ReserveSessionRequest) (api.Operation, Session, error) {
	if err := s.lockGitAuthority(ctx); err != nil {
		return api.Operation{}, Session{}, err
	}
	defer s.gitAuthority.Unlock()
	var op api.Operation
	var session Session
	if len(req.Key) < 1 || len(req.Key) > 128 || !validProject(req.Project) || !validBranch(req.Branch) || len(req.Source) > 255 ||
		!((req.Choice == "existing" && req.Source == "") || (req.Choice == "new" && req.Source != "") || (req.Choice == "blank" && req.Branch == "main" && req.Source == "")) {
		return op, session, fmt.Errorf("%w: reservation request", ErrInvalid)
	}
	requestJSON, err := json.Marshal(req)
	if err != nil {
		return op, session, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return op, session, err
	}
	defer tx.Rollback()
	replay, err := keyRecordsRequest(ctx, tx, req.Key, KindSessionCreate, requestJSON)
	if err != nil {
		return op, session, err
	}
	if replay {
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
	rec := operationRecord{ID: op.ID, Key: req.Key, Kind: KindSessionCreate, Project: req.Project, Session: session.UUID, Request: requestJSON, Intent: creationIntent(req, session.UUID), Phase: "reserved", Now: now}
	err = insertOperation(ctx, tx, rec)
	if err != nil {
		return op, session, classifyWrite(err)
	}
	if err := tx.Commit(); err != nil {
		return op, session, err
	}
	return rec.operation(), session, nil
}

// constraintAnswers maps a SQLite constraint or trigger failure, by a part
// of its text, to the refusal it stands for. A constraint that guards an
// invariant the daemon checks first is not listed: it stays a bare
// ErrConflict, which answers internal.
var constraintAnswers = []struct {
	text   string
	kind   api.ErrorKind
	reason string
}{
	{"constraint failed: idempotency key", api.KindInvalid, api.ReasonKeyReused},
	{"operations.idempotency_key", api.KindInvalid, api.ReasonKeyReused},
	{"origin_requests.idempotency_key", api.KindInvalid, api.ReasonKeyReused},
	{"publication_requests.idempotency_key", api.KindInvalid, api.ReasonKeyReused},
	{"project-deleted request retired", api.KindInvalid, api.ReasonKeyReused},
	{"project deletion authority closed", api.KindConflict, api.ReasonProjectDeleting},
	{"operations.project_path", api.KindConflict, api.ReasonProjectDeleting},
	{"branch reserved by lifecycle", api.KindConflict, api.ReasonBranchReserved},
	{"git_ref_guards.project_path", api.KindConflict, api.ReasonBranchReserved},
	{"sessions.project_path, sessions.branch", api.KindConflict, api.ReasonBranchAssigned},
	{"projects.path", api.KindConflict, api.ReasonProjectExists},
	{"constraint failed: assembled cleanup active", api.KindConflict, api.ReasonCleanupActive},
	{"session unavailable", api.KindConflict, api.ReasonSessionNotEstablished},
	// active_session_operation and active_assembled_cleanup; admissions
	// check the holder first (sessionHolderTx) to name it.
	{"operations.session_uuid", api.KindRetry, api.ReasonSessionHeld},
	{"session_public_addresses.slot", api.KindCapacity, api.ReasonPublicAddresses},
}

// classifyWrite answers a failed write: a listed constraint or trigger
// failure becomes its refusal, any other constraint failure a bare
// ErrConflict. Both keep ErrConflict as their cause.
func classifyWrite(err error) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	if !strings.Contains(text, "constraint failed") && !strings.Contains(text, "UNIQUE constraint") {
		return err
	}
	cause := fmt.Errorf("%w: %v", ErrConflict, err)
	for _, c := range constraintAnswers {
		if strings.Contains(text, c.text) {
			refusal := &Refusal{Kind: c.kind, Reason: c.reason, Cause: cause}
			if c.reason == api.ReasonKeyReused {
				refusal.Field = "key"
			}
			return refusal
		}
	}
	return cause
}

func getOperationTx(ctx context.Context, tx *sql.Tx, key string) (api.Operation, error) {
	var o api.Operation
	var request, intent, evidence string
	var committed int
	err := tx.QueryRowContext(ctx, `SELECT id,idempotency_key,kind,project_path,COALESCE(session_uuid,''),request_json,intent_json,status,phase,committed,COALESCE(evidence_json,''),reason,diagnostic,created_at,updated_at
 FROM operations WHERE idempotency_key=?`, key).Scan(&o.ID, &o.Key, &o.Kind, &o.Project, &o.SessionUUID, &request, &intent, &o.Status, &o.Phase, &committed, &evidence, &o.Reason, &o.Diagnostic, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return o, err
	}
	if err = json.Unmarshal([]byte(intent), &o.Intent); err != nil {
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
		return session, NotFound(api.ReasonSession, api.TargetSession(id))
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

// AdvanceOperation records operation id's next status, phase, evidence and
// outcome. A blocked or failed operation records reason, the code of why it
// stopped, and any other status none; diagnostic is cut to
// api.MaxDiagnosticBytes.
func (s *Store) AdvanceOperation(ctx context.Context, id, status, phase string, committed bool, evidence json.RawMessage, reason, diagnostic string) error {
	if len(id) == 0 || len(phase) == 0 || len(phase) > 80 || len(evidence) > maxProjectDeleteEvidenceBytes {
		return ErrInvalid
	}
	switch status {
	case "running", "blocked", "failed", "unknown", "superseded", "completed":
	default:
		return ErrInvalid
	}
	if !reasonFits(status, reason) {
		return ErrInvalid
	}
	diagnostic = api.BoundDiagnostic(diagnostic)
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
		return NotFound(api.ReasonOperation, api.TargetOperation(id))
	}
	if err != nil {
		return err
	}
	if oldStatus == "completed" || oldStatus == "superseded" || (oldCommitted == 1 && !committed) {
		return ErrConflict
	}
	// Only project deletion keeps evidence beyond 16 KiB, with a diagnostic
	// for each of its resources.
	if kind != KindProjectDelete && len(evidence) > 16384 {
		return ErrInvalid
	}
	if kind == "session.create" {
		if err = monotonicCreationEvidence(oldPhase, []byte(oldEvidence), evidence); err != nil {
			return err
		}
	}
	if kind == "session.create.cleanup" {
		var next CreateCleanupEvidence
		if status != "running" && status != "blocked" || json.Unmarshal(evidence, &next) != nil {
			return ErrConflict
		}
		if next.Review.Runtime != nil {
			if !validAssembledCleanupAdvance(oldPhase, phase, oldCommitted == 1, committed, next) {
				return ErrConflict
			}
		} else if phase != "local-cleanup" && phase != "local-complete" || next.LocalComplete != (phase == "local-complete") || oldPhase == "local-complete" && phase != "local-complete" {
			return ErrConflict
		}
		if err = monotonicCreateCleanupEvidence([]byte(oldEvidence), evidence); err != nil {
			return err
		}
	}
	if kind == "project.delete" && (phase != "ensure-absent" || oldPhase != phase || !committed || oldCommitted != 1 || digest([]byte(oldEvidence)) != digest(evidence) || status != "running" && status != "blocked") {
		return ErrConflict
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
	result, err := tx.ExecContext(ctx, `UPDATE operations SET status=?,phase=?,committed=?,evidence_json=?,reason=?,diagnostic=?,updated_at=?
	 WHERE id=?`, status, phase, committed, nullableJSON(evidence), reason, diagnostic, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return classifyWrite(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return NotFound(api.ReasonOperation, api.TargetOperation(id))
	}
	return tx.Commit()
}

// reasonFits reports whether an operation of status may record reason: a
// blocked or failed one needs a reason code, any other none.
func reasonFits(status, reason string) bool {
	if status == "blocked" || status == "failed" {
		return validReason(reason)
	}
	return reason == ""
}

// validReason reports whether reason is an operation reason code: 1 to 64
// bytes of lowercase letters, digits and underscores.
func validReason(reason string) bool {
	if len(reason) == 0 || len(reason) > 64 {
		return false
	}
	for _, c := range reason {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

func nullableJSON(data json.RawMessage) any {
	if len(data) == 0 {
		return nil
	}
	return string(data)
}
