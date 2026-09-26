package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/plugin"
)

// CreationEvidence is immutable selection data made under the Git authority
// lock. It is stored before an absent-ref CAS or runtime side effect.
type CreationEvidence struct {
	CapturedOID            string                    `json:"captured_oid,omitempty"`
	BranchExisted          bool                      `json:"branch_existed"`
	ImageFingerprint       string                    `json:"image_fingerprint"`
	PolicySHA256           string                    `json:"policy_sha256"`
	RuntimeInitState       string                    `json:"runtime_init_state,omitempty"`
	RefCASIntent           bool                      `json:"ref_cas_intent,omitempty"`
	Selection              CreationSelection         `json:"selection"`
	OriginURL              string                    `json:"origin_url,omitempty"`
	OriginRef              string                    `json:"origin_ref,omitempty"`
	BuilderTreeOID         string                    `json:"builder_tree_oid,omitempty"`
	Environment            *EnvironmentIntent        `json:"environment,omitempty"`
	EnvironmentState       *EnvironmentState         `json:"environment_state,omitempty"`
	SupersedesOperationID  string                    `json:"supersedes_operation_id,omitempty"`
	SupersedesUUID         string                    `json:"supersedes_uuid,omitempty"`
	ReplacementCleanup     *CreateReplacementCleanup `json:"replacement_cleanup,omitempty"`
	ReplacementTokenSHA256 string                    `json:"replacement_token_sha256,omitempty"`
}

// EnvironmentIntent is selected by the trusted host before reservation and
// persisted with the captured commit. Exact retry compares it with the active
// module and builder policy; project source cannot change these values.
type EnvironmentIntent struct {
	ModuleID            string `json:"module_id"`
	ModuleSHA256        string `json:"module_sha256"`
	ConfigSHA256        string `json:"config_sha256"`
	System              string `json:"system"`
	BaseFingerprint     string `json:"base_fingerprint"`
	BuilderStoragePool  string `json:"builder_storage_pool"`
	BuilderPolicySHA256 string `json:"builder_policy_sha256"`
}

func (e EnvironmentIntent) Valid() bool {
	return e.ModuleID != "" && len(e.ModuleID) <= 128 && validFingerprint(e.ModuleSHA256) &&
		validFingerprint(e.ConfigSHA256) && (e.System == "x86_64-linux" || e.System == "aarch64-linux") &&
		validFingerprint(e.BaseFingerprint) && e.BuilderStoragePool != "" && len(e.BuilderStoragePool) <= 63 &&
		validFingerprint(e.BuilderPolicySHA256)
}

// EnvironmentState is written only after a WASI-accepted native stage. An
// empty fingerprint with full properties is a durable publish-attempt marker,
// not permission to publish again after an uncertain outcome.
type EnvironmentState struct {
	Key              string            `json:"key"`
	FlakePresent     bool              `json:"flake_present,omitempty"`
	MaterialDigest   string            `json:"material_digest,omitempty"`
	CaptureStorePath string            `json:"capture_store_path,omitempty"`
	BuilderRequest   string            `json:"builder_request"`
	Properties       map[string]string `json:"properties,omitempty"`
	Fingerprint      string            `json:"fingerprint,omitempty"`
	CacheHit         bool              `json:"cache_hit,omitempty"`
}

type CreationSelection struct {
	RuntimeID     string `json:"runtime_id"`
	RuntimeSHA256 string `json:"runtime_sha256"`
	HostID        string `json:"host_id"`
	HostSHA256    string `json:"host_sha256"`
	SourceID      string `json:"source_id"`
	SourceSHA256  string `json:"source_sha256"`
	AgentID       string `json:"agent_id,omitempty"`
	AgentSHA256   string `json:"agent_sha256,omitempty"`
}

func (s CreationSelection) Valid() bool {
	return s.RuntimeID != "" && len(s.RuntimeID) <= 128 && s.HostID != "" && len(s.HostID) <= 128 && s.SourceID != "" && len(s.SourceID) <= 128 && validFingerprint(s.RuntimeSHA256) && validFingerprint(s.HostSHA256) && validFingerprint(s.SourceSHA256) &&
		(s.AgentID == "" && s.AgentSHA256 == "" || s.AgentID != "" && len(s.AgentID) <= 128 && validFingerprint(s.AgentSHA256))
}

type SourceCapture func(context.Context, ReserveSessionRequest) (string, bool, error)

type CapturedSource struct {
	OID       string
	Existed   bool
	OriginURL string
	OriginRef string
}

func ValidSessionCreateRequest(req ReserveSessionRequest) bool {
	return len(req.Key) >= 1 && len(req.Key) <= 128 && validProject(req.Project) && validBranch(req.Branch) &&
		((req.Choice == "existing" && req.Source == "" && req.OriginRef == "" && req.ExpectedCommitOID == "") ||
			(req.Choice == "new" && req.OriginRef == "" && req.ExpectedCommitOID == "" && req.Source != "" && len(req.Source) <= 255) ||
			(req.Choice == "new" && req.Source == "" && plugin.ValidGitOriginRef(req.OriginRef) && validOID(req.ExpectedCommitOID)))
}

// BeginSessionCreate compares the original request before calling capture. The
// callback runs while receive-pack leases are excluded, so its observation and
// the durable reservation agree on the ref boundary.
func (s *Store) BeginSessionCreate(ctx context.Context, req ReserveSessionRequest, image string, selection CreationSelection, capture SourceCapture, environment ...*EnvironmentIntent) (Operation, Session, error) {
	if capture == nil {
		return Operation{}, Session{}, ErrInvalid
	}
	return s.BeginSessionCreateCaptured(ctx, req, image, selection, func(ctx context.Context, req ReserveSessionRequest) (CapturedSource, error) {
		oid, existed, err := capture(ctx, req)
		return CapturedSource{OID: oid, Existed: existed}, err
	}, environment...)
}

func (s *Store) BeginSessionCreateCaptured(ctx context.Context, req ReserveSessionRequest, image string, selection CreationSelection, capture func(context.Context, ReserveSessionRequest) (CapturedSource, error), environment ...*EnvironmentIntent) (Operation, Session, error) {
	return s.beginSessionCreateCaptured(ctx, req, image, selection, capture, nil, environment...)
}

func (s *Store) BeginSessionCreateCapturedWithCapacity(ctx context.Context, req ReserveSessionRequest, image string, selection CreationSelection, capture func(context.Context, ReserveSessionRequest) (CapturedSource, error), observe CapacityObserver, environment ...*EnvironmentIntent) (Operation, Session, error) {
	return s.beginSessionCreateCaptured(ctx, req, image, selection, capture, observe, environment...)
}

func (s *Store) beginSessionCreateCaptured(ctx context.Context, req ReserveSessionRequest, image string, selection CreationSelection, capture func(context.Context, ReserveSessionRequest) (CapturedSource, error), observe CapacityObserver, environment ...*EnvironmentIntent) (Operation, Session, error) {
	if !ValidSessionCreateRequest(req) || capture == nil {
		return Operation{}, Session{}, ErrInvalid
	}
	if len(environment) > 1 || len(environment) == 1 && environment[0] != nil && (!environment[0].Valid() || environment[0].BaseFingerprint != image) {
		return Operation{}, Session{}, ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return Operation{}, Session{}, err
	}
	defer s.gitAuthority.Unlock()
	if err := s.CheckOriginKeyConflict(ctx, req.Key); err != nil {
		return Operation{}, Session{}, err
	}
	request, _ := json.Marshal(req)
	var existingKind, existingHash string
	err := s.db.QueryRowContext(ctx, `SELECT kind,request_sha256 FROM operations WHERE idempotency_key=?`, req.Key).Scan(&existingKind, &existingHash)
	if err == nil {
		if existingKind != "session.create" || existingHash != digest(request) {
			return Operation{}, Session{}, ErrConflict
		}
		op, e := s.GetOperationByKey(ctx, req.Key)
		if e != nil {
			return op, Session{}, e
		}
		if op.Status == "superseded" {
			return op, Session{}, nil
		}
		session, e := s.GetSession(ctx, op.SessionUUID)
		return op, session, e
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, Session{}, err
	}
	if !validFingerprint(image) || !selection.Valid() {
		return Operation{}, Session{}, ErrInvalid
	}
	if observe != nil {
		if err := s.checkSessionAdmission(ctx, observe); err != nil {
			return Operation{}, Session{}, err
		}
	}
	selectionResult, err := capture(ctx, req)
	if err != nil {
		return Operation{}, Session{}, err
	}
	oid, existed := selectionResult.OID, selectionResult.Existed
	if !validOID(oid) || (req.Choice == "existing") != existed || (req.OriginRef != "" && (selectionResult.OriginURL == "" || selectionResult.OriginRef != req.OriginRef || oid != req.ExpectedCommitOID)) || (req.OriginRef == "" && (selectionResult.OriginURL != "" || selectionResult.OriginRef != "")) {
		return Operation{}, Session{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, Session{}, err
	}
	defer tx.Rollback()
	var policy, policyHash string
	err = tx.QueryRowContext(ctx, `SELECT policy_json,policy_sha256 FROM projects WHERE path=? AND registry_state='active'`, req.Project).Scan(&policy, &policyHash)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, Session{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, Session{}, err
	}
	sid, err := newUUID()
	if err != nil {
		return Operation{}, Session{}, err
	}
	oidID, err := newUUID()
	if err != nil {
		return Operation{}, Session{}, err
	}
	ev := CreationEvidence{RuntimeInitState: "not-attempted", CapturedOID: oid, BranchExisted: existed, ImageFingerprint: image, PolicySHA256: policyHash, RefCASIntent: req.Choice == "new", Selection: selection, OriginURL: selectionResult.OriginURL, OriginRef: selectionResult.OriginRef}
	if len(environment) == 1 && environment[0] != nil {
		pinned := *environment[0]
		ev.Environment = &pinned
	}
	evJSON, _ := json.Marshal(ev)
	if len(evJSON) > 16384 {
		return Operation{}, Session{}, ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256) VALUES(?,?,?,'creating',?,?)`, sid, req.Project, req.Branch, policy, policyHash); err != nil {
		return Operation{}, Session{}, classifyWrite(err)
	}
	if err = reservePublicAddressTx(ctx, tx, sid, json.RawMessage(policy)); err != nil {
		return Operation{}, Session{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,committed,evidence_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'running','source-ready',0,?,?,?)`, oidID, req.Key, "session.create", req.Project, sid, string(request), digest(request), string(evJSON), now, now); err != nil {
		return Operation{}, Session{}, classifyWrite(err)
	}
	if err = tx.Commit(); err != nil {
		return Operation{}, Session{}, err
	}
	return Operation{ID: oidID, Key: req.Key, Kind: "session.create", Project: req.Project, SessionUUID: sid, Request: request, Status: "running", Phase: "source-ready", Evidence: evJSON, CreatedAt: now, UpdatedAt: now}, Session{UUID: sid, Project: req.Project, Branch: req.Branch, Registry: "creating", Policy: json.RawMessage(policy), PolicySHA256: policyHash}, nil
}

func (s *Store) CheckOriginKeyConflict(ctx context.Context, key string) error {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM publication_requests WHERE idempotency_key=?`, key).Scan(&exists)
	if err == nil {
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM origin_requests WHERE idempotency_key=?`, key).Scan(&exists)
	if err == nil {
		return ErrConflict
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func validFingerprint(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
func validOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' && (c < 'a' || c > 'f') {
			return false
		}
	}
	return strings.Trim(s, "0") != ""
}

type BlankProjectRequest struct {
	Key     string `json:"key"`
	Project string `json:"project"`
	URL     string `json:"url,omitempty"`
}
type BlankProjectEvidence struct {
	Policy           json.RawMessage   `json:"policy"`
	PolicySHA256     string            `json:"policy_sha256"`
	ImageFingerprint string            `json:"image_fingerprint"`
	Selection        CreationSelection `json:"selection"`
	BootstrapUUID    string            `json:"bootstrap_uuid,omitempty"`
}

// BeginBlankProject persists the UUID and immutable trusted selection before
// Git initialization. A completed duplicate returns without reading new policy.
func (s *Store) BeginBlankProject(ctx context.Context, req BlankProjectRequest, policy json.RawMessage, image string, selection CreationSelection) (Operation, error) {
	return s.beginBlankProject(ctx, req, policy, image, selection, nil)
}

func (s *Store) BeginBlankProjectWithCapacity(ctx context.Context, req BlankProjectRequest, policy json.RawMessage, image string, selection CreationSelection, observe CapacityObserver) (Operation, error) {
	return s.beginBlankProject(ctx, req, policy, image, selection, observe)
}

func (s *Store) beginBlankProject(ctx context.Context, req BlankProjectRequest, policy json.RawMessage, image string, selection CreationSelection, observe CapacityObserver) (Operation, error) {
	if len(req.Key) < 1 || len(req.Key) > 128 || !validProject(req.Project) || len(req.URL) > 2048 {
		return Operation{}, ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return Operation{}, err
	}
	defer s.gitAuthority.Unlock()
	request, _ := json.Marshal(req)
	var kind, hash string
	err := s.db.QueryRowContext(ctx, `SELECT kind,request_sha256 FROM operations WHERE idempotency_key=?`, req.Key).Scan(&kind, &hash)
	if err == nil {
		if kind != "project.create" || hash != digest(request) {
			return Operation{}, ErrConflict
		}
		return s.GetOperationByKey(ctx, req.Key)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, err
	}
	var present int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM operations WHERE kind='project.create' AND project_path=? AND status IN ('running','blocked','unknown') LIMIT 1`, req.Project).Scan(&present)
	if err == nil {
		return Operation{}, ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, err
	}
	if !validFingerprint(image) || !selection.Valid() {
		return Operation{}, ErrInvalid
	}
	if observe != nil {
		if err := s.checkSessionAdmission(ctx, observe); err != nil {
			return Operation{}, err
		}
	}
	normalized, err := normalizedObject(policy)
	if err != nil {
		return Operation{}, err
	}
	if len(normalized) > 8192 {
		return Operation{}, ErrInvalid
	}
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE path=?`, req.Project).Scan(&present)
	if err == nil {
		return Operation{}, ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, err
	}
	sid, err := newUUID()
	if err != nil {
		return Operation{}, err
	}
	opID, err := newUUID()
	if err != nil {
		return Operation{}, err
	}
	evJSON, _ := json.Marshal(BlankProjectEvidence{Policy: normalized, PolicySHA256: digest(normalized), ImageFingerprint: image, Selection: selection, BootstrapUUID: sid})
	if len(evJSON) > 16384 {
		return Operation{}, ErrInvalid
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var operationSession any = sid
	if req.URL != "" {
		operationSession = nil
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,evidence_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'running','repo-pending',?,?,?)`, opID, req.Key, "project.create", req.Project, operationSession, string(request), digest(request), string(evJSON), now, now)
	if err != nil {
		return Operation{}, classifyWrite(err)
	}
	opSession := sid
	if req.URL != "" {
		opSession = ""
	}
	return Operation{ID: opID, Key: req.Key, Kind: "project.create", Project: req.Project, SessionUUID: opSession, Request: request, Status: "running", Phase: "repo-pending", Evidence: evJSON, CreatedAt: now, UpdatedAt: now}, nil
}

// CommitBlankProject is the only transition that exposes a new project to Git
// leases. Project, unborn session and its single-use grant commit together.
func (s *Store) CommitBlankProject(ctx context.Context, opID string) (Session, error) {
	if err := s.lockGitAuthority(ctx); err != nil {
		return Session{}, err
	}
	defer s.gitAuthority.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback()
	var op Operation
	var evRaw string
	err = tx.QueryRowContext(ctx, `SELECT project_path,session_uuid,phase,evidence_json FROM operations WHERE id=? AND kind='project.create'`, opID).Scan(&op.Project, &op.SessionUUID, &op.Phase, &evRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	if op.Phase != "repo-pending" {
		session, e := getSessionTx(ctx, tx, op.SessionUUID)
		return session, e
	}
	var ev BlankProjectEvidence
	if err = json.Unmarshal([]byte(evRaw), &ev); err != nil {
		return Session{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO projects(path,policy_json,policy_sha256) VALUES(?,?,?)`, op.Project, string(ev.Policy), ev.PolicySHA256); err != nil {
		return Session{}, classifyWrite(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256) VALUES(?,?,'main','creating',?,?)`, op.SessionUUID, op.Project, string(ev.Policy), ev.PolicySHA256); err != nil {
		return Session{}, classifyWrite(err)
	}
	if err = reservePublicAddressTx(ctx, tx, op.SessionUUID, ev.Policy); err != nil {
		return Session{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO git_unborn_grants(session_uuid,state) VALUES(?,'pending')`, op.SessionUUID); err != nil {
		return Session{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET phase='branch-assigned',committed=1,updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), opID); err != nil {
		return Session{}, err
	}
	if err = tx.Commit(); err != nil {
		return Session{}, err
	}
	return Session{UUID: op.SessionUUID, Project: op.Project, Branch: "main", Registry: "creating", Policy: ev.Policy, PolicySHA256: ev.PolicySHA256}, nil
}

// CommitOriginProject exposes the project only after successful contact and a
// verified bare repository. The origin and its observation share that commit.
// An empty origin also reserves the single unborn main bootstrap session.
func (s *Store) CommitOriginProject(ctx context.Context, opID string, refs []plugin.GitOriginRef) (Session, bool, error) {
	encoded, err := encodeOriginRefs(refs)
	if err != nil {
		return Session{}, false, err
	}
	if err = s.lockGitAuthority(ctx); err != nil {
		return Session{}, false, err
	}
	defer s.gitAuthority.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, false, err
	}
	defer tx.Rollback()
	var project, phase, requestJSON, evidenceJSON string
	err = tx.QueryRowContext(ctx, `SELECT project_path,phase,request_json,evidence_json FROM operations WHERE id=? AND kind='project.create'`, opID).Scan(&project, &phase, &requestJSON, &evidenceJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, ErrNotFound
	}
	if err != nil {
		return Session{}, false, err
	}
	if phase != "repo-pending" {
		return Session{}, false, ErrConflict
	}
	var req BlankProjectRequest
	var ev BlankProjectEvidence
	if json.Unmarshal([]byte(requestJSON), &req) != nil || json.Unmarshal([]byte(evidenceJSON), &ev) != nil || req.URL == "" || req.Project != project || ev.BootstrapUUID == "" {
		return Session{}, false, ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO projects(path,policy_json,policy_sha256) VALUES(?,?,?)`, project, string(ev.Policy), ev.PolicySHA256); err != nil {
		return Session{}, false, classifyWrite(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO project_origins(project_path,url,current_status,observed_at,refs_json,diagnostic) VALUES(?,?,'fresh',?,?,'')`, project, req.URL, now, string(encoded)); err != nil {
		return Session{}, false, err
	}
	if len(refs) != 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE operations SET status='completed',phase='established',committed=1,updated_at=? WHERE id=?`, now, opID); err != nil {
			return Session{}, false, err
		}
		return Session{}, false, tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256) VALUES(?,?,'main','creating',?,?)`, ev.BootstrapUUID, project, string(ev.Policy), ev.PolicySHA256); err != nil {
		return Session{}, false, classifyWrite(err)
	}
	if err = reservePublicAddressTx(ctx, tx, ev.BootstrapUUID, ev.Policy); err != nil {
		return Session{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO git_unborn_grants(session_uuid,state) VALUES(?,'pending')`, ev.BootstrapUUID); err != nil {
		return Session{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET session_uuid=?,phase='branch-assigned',committed=1,updated_at=? WHERE id=?`, ev.BootstrapUUID, now, opID); err != nil {
		return Session{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return Session{}, false, err
	}
	return Session{UUID: ev.BootstrapUUID, Project: project, Branch: "main", Registry: "creating", Policy: ev.Policy, PolicySHA256: ev.PolicySHA256}, true, nil
}

func (s *Store) GetOperationByKey(ctx context.Context, key string) (Operation, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback()
	op, err := getOperationTx(ctx, tx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return op, ErrNotFound
	}
	return op, err
}
func (s *Store) GetOperation(ctx context.Context, id string) (Operation, error) {
	var key string
	err := s.db.QueryRowContext(ctx, `SELECT idempotency_key FROM operations WHERE id=?`, id).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, err
	}
	return s.GetOperationByKey(ctx, key)
}

func (s *Store) CreationForSession(ctx context.Context, id string) (Operation, error) {
	var opID string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM operations WHERE session_uuid=? AND kind IN ('project.create','session.create') ORDER BY created_at LIMIT 1`, id).Scan(&opID)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, err
	}
	return s.GetOperation(ctx, opID)
}
func (s *Store) ListOperations(ctx context.Context, after string, limit int) ([]Operation, string, error) {
	if limit < 1 || limit > 100 || len(after) > 128 {
		return nil, "", ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM operations WHERE id>? ORDER BY id COLLATE BINARY LIMIT ?`, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, "", err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	if err = rows.Close(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(ids) > limit {
		next = ids[limit-1]
		ids = ids[:limit]
	}
	ops := make([]Operation, 0, len(ids))
	for _, id := range ids {
		op, e := s.GetOperation(ctx, id)
		if e != nil {
			return nil, "", e
		}
		ops = append(ops, op)
	}
	return ops, next, nil
}
func (s *Store) ListSessions(ctx context.Context, after string, limit int) ([]Session, string, error) {
	if limit < 1 || limit > 100 || len(after) > 128 {
		return nil, "", ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT uuid FROM sessions WHERE uuid>? ORDER BY uuid COLLATE BINARY LIMIT ?`, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, "", err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	if err = rows.Close(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(ids) > limit {
		next = ids[limit-1]
		ids = ids[:limit]
	}
	sessions := make([]Session, 0, len(ids))
	for _, id := range ids {
		v, e := s.GetSession(ctx, id)
		if e != nil {
			return nil, "", e
		}
		sessions = append(sessions, v)
	}
	return sessions, next, nil
}
func (s *Store) UnfinishedCreates(ctx context.Context) ([]Operation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM operations WHERE kind IN ('project.create','session.create') AND status IN ('running','blocked','unknown') ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	ops := make([]Operation, 0, len(ids))
	for _, id := range ids {
		v, e := s.GetOperation(ctx, id)
		if e != nil {
			return nil, e
		}
		ops = append(ops, v)
	}
	return ops, nil
}

func (s *Store) ProjectPolicySHA(ctx context.Context, project string) (string, error) {
	var sha string
	err := s.db.QueryRowContext(ctx, `SELECT policy_sha256 FROM projects WHERE path=? AND registry_state='active'`, project).Scan(&sha)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return sha, err
}

// ProjectPolicyRecord exposes only the trusted project's current stored
// policy snapshot for semantic comparison during daemon restart.
func (s *Store) ProjectPolicyRecord(ctx context.Context, project string) (json.RawMessage, string, error) {
	if !validProject(project) {
		return nil, "", ErrInvalid
	}
	var raw, sha string
	err := s.db.QueryRowContext(ctx, `SELECT policy_json,policy_sha256 FROM projects WHERE path=? AND registry_state='active'`, project).Scan(&raw, &sha)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	return json.RawMessage(raw), sha, err
}

// Latest bounded Start diagnostic is not a second runtime state machine.
func (s *Store) SetSessionStartDiagnostic(ctx context.Context, id, diagnostic string) error {
	if len(id) != 36 || len(diagnostic) > 1024 {
		return ErrInvalid
	}
	var present int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE uuid=?`, id).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, `session.start.diag:`+id, diagnostic)
	return err
}
func (s *Store) SessionStartDiagnostic(ctx context.Context, id string) (string, error) {
	if len(id) != 36 {
		return "", ErrInvalid
	}
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key=?`, `session.start.diag:`+id).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

// CompleteCreation commits the established row and operation result atomically.
func (s *Store) CompleteCreation(ctx context.Context, opID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sid, kind, status, project, evidence string
	err = tx.QueryRowContext(ctx, `SELECT session_uuid,kind,status,project_path,COALESCE(evidence_json,'') FROM operations WHERE id=?`, opID).Scan(&sid, &kind, &status, &project, &evidence)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if kind != "session.create" && kind != "project.create" {
		return ErrInvalid
	}
	if status == "completed" {
		var state string
		if err = tx.QueryRowContext(ctx, `SELECT registry_state FROM sessions WHERE uuid=?`, sid).Scan(&state); err != nil {
			return err
		}
		if state != "established" {
			return ErrConflict
		}
		return nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET registry_state='established' WHERE uuid=? AND registry_state='creating'`, sid)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if evidence != "" {
		var ev CreationEvidence
		if err = json.Unmarshal([]byte(evidence), &ev); err != nil {
			return err
		}
		if ev.EnvironmentState != nil && ev.EnvironmentState.Key != "" && ev.EnvironmentState.Fingerprint != "" {
			// A use counts only when the session becomes established, and only
			// for the exact cache generation it actually used.
			if _, err = tx.ExecContext(ctx, `UPDATE environment_images SET last_used_at=? WHERE project_path=? AND environment_key=? AND fingerprint=?`,
				now, project, ev.EnvironmentState.Key, ev.EnvironmentState.Fingerprint); err != nil {
				return err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE operations SET status='completed',phase='established',committed=1,diagnostic='',updated_at=? WHERE id=?`, now, opID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func Evidence(op Operation) (CreationEvidence, error) {
	var ev CreationEvidence
	if err := json.Unmarshal(op.Evidence, &ev); err != nil {
		return ev, fmt.Errorf("creation evidence: %w", err)
	}
	if ev.RuntimeInitState != "" && ev.RuntimeInitState != "not-attempted" && ev.RuntimeInitState != "attempted" {
		return ev, ErrInvalid
	}
	if ev.ReplacementCleanup != nil && (!ev.ReplacementCleanup.Valid() || ev.ReplacementCleanup.OldUUID != ev.SupersedesUUID || ev.ReplacementCleanup.OldOperationID != ev.SupersedesOperationID || ev.ReplacementCleanup.OldUUID == op.SessionUUID || !ev.ReplacementCleanup.Completed && op.Phase != "replacement-cleanup") {
		return ev, ErrInvalid
	}
	if !validFingerprint(ev.ImageFingerprint) || ev.Environment != nil && (!ev.Environment.Valid() || ev.Environment.BaseFingerprint != ev.ImageFingerprint && ev.EnvironmentState == nil) {
		return ev, ErrInvalid
	}
	return ev, nil
}
