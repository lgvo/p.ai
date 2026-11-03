package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// CreationEvidence is immutable selection data made under the Git authority
// lock. It is stored before an absent-ref CAS or runtime side effect.
// BranchCreateIssued records, right before the source driver is asked to
// create the new branch, that the creation was sent: a creation recorded as
// sent is settled before it is sent again.
type CreationEvidence struct {
	CapturedOID           string                        `json:"captured_oid,omitempty"`
	BranchExisted         bool                          `json:"branch_existed"`
	ImageFingerprint      string                        `json:"image_fingerprint"`
	PolicySHA256          string                        `json:"policy_sha256"`
	EnvironmentBuilder    *CreationBuilderState         `json:"environment_builder,omitempty"`
	RuntimeInitState      string                        `json:"runtime_init_state,omitempty"`
	RefCASIntent          bool                          `json:"ref_cas_intent,omitempty"`
	BranchCreateIssued    bool                          `json:"branch_create_issued,omitempty"`
	Selection             CreationSelection             `json:"selection"`
	OriginURL             string                        `json:"origin_url,omitempty"`
	OriginRef             string                        `json:"origin_ref,omitempty"`
	Environment           *EnvironmentIntent            `json:"environment,omitempty"`
	Image                 *CreationImage                `json:"image,omitempty"`
	SupersedesOperationID string                        `json:"supersedes_operation_id,omitempty"`
	SupersedesUUID        string                        `json:"supersedes_uuid,omitempty"`
	ReplacementCleanup    *api.CreateReplacementCleanup `json:"replacement_cleanup,omitempty"`
	// ReplacementTag is the change tag of the replacement that recorded this
	// creation.
	ReplacementTag string `json:"replacement_tag,omitempty"`
}

// EnvironmentIntent is selected by the trusted host before reservation and
// persisted with the captured commit. Exact retry compares it with the current
// host selection and builder policy; project source cannot change these values.
type EnvironmentIntent struct {
	System              string `json:"system"`
	BaseFingerprint     string `json:"base_fingerprint"`
	BuilderStoragePool  string `json:"builder_storage_pool"`
	BuilderPolicySHA256 string `json:"builder_policy_sha256"`
	ImageBuilder        string `json:"image_builder"`
}

func (e EnvironmentIntent) Valid() bool {
	return validSystem(e.System) && validFingerprint(e.BaseFingerprint) && e.BuilderStoragePool != "" && len(e.BuilderStoragePool) <= 63 &&
		validFingerprint(e.BuilderPolicySHA256) && validImageBuilder(e.ImageBuilder)
}

// CreationImage is the commit image a creation uses, recorded when image-ready
// accepts it: built by this creation, or reused from the commit-image index.
type CreationImage struct {
	Ref            string `json:"ref"`
	Built          bool   `json:"built"`
	Selection      string `json:"selection"`
	MaterialSHA256 string `json:"material_sha256,omitempty"`
}

type CreationSelection struct {
	HostID       string `json:"host_id"`
	HostSHA256   string `json:"host_sha256"`
	SourceID     string `json:"source_id"`
	SourceSHA256 string `json:"source_sha256"`
	AgentID      string `json:"agent_id,omitempty"`
	AgentSHA256  string `json:"agent_sha256,omitempty"`
}

func (s CreationSelection) Valid() bool {
	return s.HostID != "" && len(s.HostID) <= 128 && s.SourceID != "" && len(s.SourceID) <= 128 && validFingerprint(s.HostSHA256) && validFingerprint(s.SourceSHA256) &&
		(s.AgentID == "" && s.AgentSHA256 == "" || s.AgentID != "" && len(s.AgentID) <= 128 && validFingerprint(s.AgentSHA256))
}

type SourceCapture func(context.Context, ReserveSessionRequest) (string, bool, error)

type CapturedSource struct {
	OID       string
	Existed   bool
	OriginURL string
	OriginRef string
}

// creationIntent is what a session creation's caller named: the new session
// is session.
func creationIntent(req ReserveSessionRequest, session string) api.OperationIntent {
	return api.OperationIntent{Project: req.Project, Session: session, Branch: req.Branch, Choice: req.Choice, Source: req.Source, OriginRef: req.OriginRef}
}

func ValidSessionCreateRequest(req ReserveSessionRequest) bool { return req.Validate() == nil }

// BeginSessionCreate compares the original request before calling capture. The
// callback runs while receive-pack leases are excluded, so its observation and
// the durable reservation agree on the ref boundary.
func (s *Store) BeginSessionCreate(ctx context.Context, req ReserveSessionRequest, image string, selection CreationSelection, capture SourceCapture, environment ...*EnvironmentIntent) (api.Operation, Session, error) {
	if capture == nil {
		return api.Operation{}, Session{}, ErrInvalid
	}
	return s.BeginSessionCreateCaptured(ctx, req, image, selection, func(ctx context.Context, req ReserveSessionRequest) (CapturedSource, error) {
		oid, existed, err := capture(ctx, req)
		return CapturedSource{OID: oid, Existed: existed}, err
	}, environment...)
}

func (s *Store) BeginSessionCreateCaptured(ctx context.Context, req ReserveSessionRequest, image string, selection CreationSelection, capture func(context.Context, ReserveSessionRequest) (CapturedSource, error), environment ...*EnvironmentIntent) (api.Operation, Session, error) {
	return s.beginSessionCreateCaptured(ctx, req, image, selection, capture, nil, environment...)
}

func (s *Store) BeginSessionCreateCapturedWithCapacity(ctx context.Context, req ReserveSessionRequest, image string, selection CreationSelection, capture func(context.Context, ReserveSessionRequest) (CapturedSource, error), observe CapacityObserver, environment ...*EnvironmentIntent) (api.Operation, Session, error) {
	return s.beginSessionCreateCaptured(ctx, req, image, selection, capture, observe, environment...)
}

func (s *Store) beginSessionCreateCaptured(ctx context.Context, req ReserveSessionRequest, image string, selection CreationSelection, capture func(context.Context, ReserveSessionRequest) (CapturedSource, error), observe CapacityObserver, environment ...*EnvironmentIntent) (api.Operation, Session, error) {
	if !ValidSessionCreateRequest(req) || capture == nil {
		return api.Operation{}, Session{}, ErrInvalid
	}
	if len(environment) > 1 || len(environment) == 1 && environment[0] != nil && (!environment[0].Valid() || environment[0].BaseFingerprint != image) {
		return api.Operation{}, Session{}, ErrInvalid
	}
	request, _ := json.Marshal(req)
	// Exact durable replay does not inspect refs or require Git authority. A read
	// transaction keeps the operation/session pair coherent across supersession.
	replay := func() (api.Operation, Session, bool, error) {
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return api.Operation{}, Session{}, true, e
		}
		defer tx.Rollback()
		var journal int
		e = tx.QueryRowContext(ctx, `SELECT 1 FROM retired_lifecycle_requests WHERE idempotency_key=?`, req.Key).Scan(&journal)
		if e == nil {
			return api.Operation{}, Session{}, true, KeyReused()
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return api.Operation{}, Session{}, true, e
		}
		recorded, e := keyRecordsRequest(ctx, tx, req.Key, KindSessionCreate, request)
		if e != nil {
			return api.Operation{}, Session{}, true, e
		}
		if !recorded {
			return api.Operation{}, Session{}, false, nil
		}
		op, e := getOperationTx(ctx, tx, req.Key)
		if e != nil {
			return op, Session{}, true, e
		}
		// The recorded digest matched request; the stored request must match it.
		if digest(op.Request) != digest(request) {
			return api.Operation{}, Session{}, true, ErrConflict
		}
		if op.Status == "superseded" {
			return op, Session{}, true, nil
		}
		session, e := getSessionTx(ctx, tx, op.SessionUUID)
		if errors.Is(e, sql.ErrNoRows) {
			// Discard/Delete may remove the session before this retained
			// completed request is replayed. Its result remains authoritative;
			// a completed replay must not reserve or reconstruct the session.
			if op.Status == "completed" {
				return op, Session{}, true, nil
			}
			e = NotFound(api.ReasonSession, api.TargetSession(op.SessionUUID))
		}
		return op, session, true, e
	}
	if op, session, found, e := replay(); found || e != nil {
		return op, session, e
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return api.Operation{}, Session{}, err
	}
	defer s.gitAuthority.Unlock()
	// Another admission may have completed while authority was contested.
	if op, session, found, e := replay(); found || e != nil {
		return op, session, e
	}
	if !validFingerprint(image) || !selection.Valid() {
		return api.Operation{}, Session{}, ErrInvalid
	}
	if observe != nil {
		if err := s.checkSessionAdmission(ctx, observe); err != nil {
			return api.Operation{}, Session{}, err
		}
	}
	selectionResult, err := capture(ctx, req)
	if err != nil {
		return api.Operation{}, Session{}, err
	}
	oid, existed := selectionResult.OID, selectionResult.Existed
	if !validOID(oid) || (req.Choice == "existing") != existed || (req.ExpectedOriginURL != "" && selectionResult.OriginURL != req.ExpectedOriginURL) || (req.OriginRef != "" && (selectionResult.OriginURL == "" || selectionResult.OriginRef != req.OriginRef || oid != req.ExpectedCommitOID)) || (req.OriginRef == "" && (selectionResult.OriginURL != "" || selectionResult.OriginRef != "")) {
		return api.Operation{}, Session{}, ErrInvalid
	}
	j, err := s.beginJournal(ctx)
	if err != nil {
		return api.Operation{}, Session{}, err
	}
	defer j.Rollback()
	tx := j.Tx
	var policy, policyHash string
	err = tx.QueryRowContext(ctx, `SELECT policy_json,policy_sha256 FROM projects WHERE path=? AND registry_state='active'`, req.Project).Scan(&policy, &policyHash)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Operation{}, Session{}, NotFound(api.ReasonProject, api.TargetProject(req.Project))
	}
	if err != nil {
		return api.Operation{}, Session{}, err
	}
	sid, err := newUUID()
	if err != nil {
		return api.Operation{}, Session{}, err
	}
	oidID, err := newUUID()
	if err != nil {
		return api.Operation{}, Session{}, err
	}
	ev := CreationEvidence{RuntimeInitState: "not-attempted", CapturedOID: oid, BranchExisted: existed, ImageFingerprint: image, PolicySHA256: policyHash, RefCASIntent: req.Choice == "new", Selection: selection, OriginURL: selectionResult.OriginURL, OriginRef: selectionResult.OriginRef}
	if len(environment) == 1 && environment[0] != nil {
		pinned := *environment[0]
		ev.Environment = &pinned
		ev.EnvironmentBuilder = &CreationBuilderState{State: "not-attempted"}
	}
	evJSON, _ := json.Marshal(ev)
	if len(evJSON) > 16384 {
		return api.Operation{}, Session{}, ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256) VALUES(?,?,?,'creating',?,?)`, sid, req.Project, req.Branch, policy, policyHash); err != nil {
		return api.Operation{}, Session{}, withTarget(classifyWrite(err), api.TargetBranch(req.Project, req.Branch))
	}
	if err = reservePublicAddressTx(ctx, tx, sid, json.RawMessage(policy)); err != nil {
		return api.Operation{}, Session{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rec := operationRecord{ID: oidID, Key: req.Key, Kind: KindSessionCreate, Project: req.Project, Session: sid, Request: request, Intent: creationIntent(req, sid), Phase: "source-ready", Evidence: evJSON, Now: now}
	if err = j.insert(ctx, rec); err != nil {
		return api.Operation{}, Session{}, classifyWrite(err)
	}
	if err = j.commit(ctx); err != nil {
		return api.Operation{}, Session{}, err
	}
	return rec.operation(), Session{UUID: sid, Project: req.Project, Branch: req.Branch, Registry: "creating", Policy: json.RawMessage(policy), PolicySHA256: policyHash}, nil
}

func validFingerprint(s string) bool { return api.ValidFingerprint(s) }

// validOID is core's bound for a commit ID, whose syntax the source driver
// decides (CheckNames).
func validOID(s string) bool { return api.BoundedName(s, api.MaxCommitBytes) }

// validIncarnation bounds a runtime driver's incarnation, which is opaque to
// core and recorded by the removals and renames that act on it.
func validIncarnation(at string) bool {
	if at == "" || len(at) > 128 {
		return false
	}
	for i := 0; i < len(at); i++ {
		if at[i] < 0x21 || at[i] > 0x7e {
			return false
		}
	}
	return true
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
func (s *Store) BeginBlankProject(ctx context.Context, req BlankProjectRequest, policy json.RawMessage, image string, selection CreationSelection) (api.Operation, error) {
	return s.beginBlankProject(ctx, req, policy, image, selection, nil)
}

func (s *Store) BeginBlankProjectWithCapacity(ctx context.Context, req BlankProjectRequest, policy json.RawMessage, image string, selection CreationSelection, observe CapacityObserver) (api.Operation, error) {
	return s.beginBlankProject(ctx, req, policy, image, selection, observe)
}

func (s *Store) beginBlankProject(ctx context.Context, req BlankProjectRequest, policy json.RawMessage, image string, selection CreationSelection, observe CapacityObserver) (api.Operation, error) {
	if e := s.checkRetiredKey(ctx, req.Key); e != nil {
		return api.Operation{}, e
	}
	if len(req.Key) < 1 || len(req.Key) > 128 || !validProject(req.Project) || req.URL != "" && !validURL(req.URL) {
		return api.Operation{}, ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return api.Operation{}, err
	}
	defer s.gitAuthority.Unlock()
	request, _ := json.Marshal(req)
	if replay, err := keyRecordsRequest(ctx, s.db, req.Key, KindProjectCreate, request); err != nil {
		return api.Operation{}, err
	} else if replay {
		return s.GetOperationByKey(ctx, req.Key)
	}
	var present int
	var holder, holderStatus string
	err := s.db.QueryRowContext(ctx, `SELECT id,status FROM operations WHERE kind='project.create' AND project_path=? AND status IN ('running','blocked') LIMIT 1`, req.Project).Scan(&holder, &holderStatus)
	if err == nil {
		// Another creation of the project is unfinished: wait while it runs,
		// or Retry it once it is blocked.
		if holderStatus == "running" {
			return api.Operation{}, Retry(api.ReasonProjectHeld, api.TargetOperation(holder))
		}
		return api.Operation{}, Conflict(api.ReasonOperationBlocked, api.TargetOperation(holder))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return api.Operation{}, err
	}
	if !validFingerprint(image) || !selection.Valid() {
		return api.Operation{}, ErrInvalid
	}
	if observe != nil {
		if err := s.checkSessionAdmission(ctx, observe); err != nil {
			return api.Operation{}, err
		}
	}
	normalized, err := normalizedObject(policy)
	if err != nil {
		return api.Operation{}, err
	}
	if len(normalized) > 8192 {
		return api.Operation{}, ErrInvalid
	}
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE path=?`, req.Project).Scan(&present)
	if err == nil {
		return api.Operation{}, Conflict(api.ReasonProjectExists, api.TargetProject(req.Project))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return api.Operation{}, err
	}
	sid, err := newUUID()
	if err != nil {
		return api.Operation{}, err
	}
	opID, err := newUUID()
	if err != nil {
		return api.Operation{}, err
	}
	evJSON, _ := json.Marshal(BlankProjectEvidence{Policy: normalized, PolicySHA256: digest(normalized), ImageFingerprint: image, Selection: selection, BootstrapUUID: sid})
	if len(evJSON) > 16384 {
		return api.Operation{}, ErrInvalid
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	operationSession := sid
	if req.URL != "" {
		operationSession = ""
	}
	rec := operationRecord{ID: opID, Key: req.Key, Kind: KindProjectCreate, Project: req.Project, Session: operationSession, Request: request,
		Intent: api.OperationIntent{Project: req.Project, Session: operationSession, URL: req.URL}, Phase: "repo-pending", Evidence: evJSON, Now: now}
	j, err := s.beginJournal(ctx)
	if err != nil {
		return api.Operation{}, err
	}
	defer j.Rollback()
	if err = j.insert(ctx, rec); err != nil {
		return api.Operation{}, classifyWrite(err)
	}
	if err = j.commit(ctx); err != nil {
		return api.Operation{}, err
	}
	return rec.operation(), nil
}

// CommitBlankProject is the only transition that exposes a new project to Git
// leases. Project, unborn session and its single-use grant commit together.
func (s *Store) CommitBlankProject(ctx context.Context, opID string) (Session, error) {
	if err := s.lockGitAuthority(ctx); err != nil {
		return Session{}, err
	}
	defer s.gitAuthority.Unlock()
	j, err := s.beginJournal(ctx)
	if err != nil {
		return Session{}, err
	}
	defer j.Rollback()
	tx := j.Tx
	var op api.Operation
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
	if _, err = j.update(ctx, opID, `UPDATE operations SET phase='branch-assigned',committed=1,updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), opID); err != nil {
		return Session{}, err
	}
	if err = j.commit(ctx); err != nil {
		return Session{}, err
	}
	return Session{UUID: op.SessionUUID, Project: op.Project, Branch: "main", Registry: "creating", Policy: ev.Policy, PolicySHA256: ev.PolicySHA256}, nil
}

// CommitOriginProject exposes the project only after successful contact and a
// verified bare repository. The origin and its observation share that commit.
// An empty origin also reserves the single unborn main bootstrap session.
func (s *Store) CommitOriginProject(ctx context.Context, opID string, refs []sourcedriver.OriginRef) (Session, bool, error) {
	encoded, err := encodeOriginRefs(refs)
	if err != nil {
		return Session{}, false, err
	}
	if err = s.lockGitAuthority(ctx); err != nil {
		return Session{}, false, err
	}
	defer s.gitAuthority.Unlock()
	j, err := s.beginJournal(ctx)
	if err != nil {
		return Session{}, false, err
	}
	defer j.Rollback()
	tx := j.Tx
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
		if _, err = j.update(ctx, opID, `UPDATE operations SET status='completed',phase='established',committed=1,reason='',diagnostic='',updated_at=? WHERE id=?`, now, opID); err != nil {
			return Session{}, false, err
		}
		return Session{}, false, j.commit(ctx)
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
	if _, err = j.update(ctx, opID, `UPDATE operations SET session_uuid=?,phase='branch-assigned',committed=1,updated_at=? WHERE id=?`, ev.BootstrapUUID, now, opID); err != nil {
		return Session{}, false, err
	}
	if err = j.commit(ctx); err != nil {
		return Session{}, false, err
	}
	return Session{UUID: ev.BootstrapUUID, Project: project, Branch: "main", Registry: "creating", Policy: ev.Policy, PolicySHA256: ev.PolicySHA256}, true, nil
}

func (s *Store) GetOperationByKey(ctx context.Context, key string) (api.Operation, error) {
	if e := s.checkRetiredKey(ctx, key); e != nil {
		return api.Operation{}, e
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return api.Operation{}, err
	}
	defer tx.Rollback()
	op, err := getOperationTx(ctx, tx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return op, NotFound(api.ReasonOperation, "")
	}
	return op, err
}
func (s *Store) GetOperation(ctx context.Context, id string) (api.Operation, error) {
	var key string
	err := s.db.QueryRowContext(ctx, `SELECT idempotency_key FROM operations WHERE id=?`, id).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Operation{}, NotFound(api.ReasonOperation, api.TargetOperation(id))
	}
	if err != nil {
		return api.Operation{}, err
	}
	return s.GetOperationByKey(ctx, key)
}

func (s *Store) CreationForSession(ctx context.Context, id string) (api.Operation, error) {
	var opID string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM operations WHERE session_uuid=? AND kind IN ('project.create','session.create') ORDER BY created_at LIMIT 1`, id).Scan(&opID)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Operation{}, ErrNotFound
	}
	if err != nil {
		return api.Operation{}, err
	}
	return s.GetOperation(ctx, opID)
}

// ListedOperation is an operation of a listing, with the cursor that
// continues the listing after it.
type ListedOperation struct {
	Operation api.Operation
	Cursor    string
}

// ListOperations pages the journal in the order it accepted operations.
// after is the cursor a previous page returned as next: the acceptance
// sequence of its last operation, which stays valid when that operation's row
// is deleted.
func (s *Store) ListOperations(ctx context.Context, after string, limit int) ([]ListedOperation, string, error) {
	if limit < 1 || limit > 100 {
		return nil, "", ErrInvalid
	}
	var from int64
	if after != "" {
		n, err := strconv.ParseInt(after, 10, 64)
		if err != nil || n < 1 || strconv.FormatInt(n, 10) != after {
			return nil, "", Invalid("after")
		}
		from = n
	}
	// One transaction: recording a start or stop deletes an older one.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT seq,id FROM operations WHERE seq>? ORDER BY seq LIMIT ?`, from, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	type entry struct {
		seq int64
		id  string
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.seq, &e.id); err != nil {
			return nil, "", err
		}
		entries = append(entries, e)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	if err = rows.Close(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(entries) > limit {
		next = strconv.FormatInt(entries[limit-1].seq, 10)
		entries = entries[:limit]
	}
	ops := make([]ListedOperation, 0, len(entries))
	for _, e := range entries {
		op, err := operationByIDTx(ctx, tx, e.id)
		if err != nil {
			return nil, "", err
		}
		ops = append(ops, ListedOperation{Operation: op, Cursor: strconv.FormatInt(e.seq, 10)})
	}
	return ops, next, nil
}
func (s *Store) ListSessions(ctx context.Context, after string, limit int) ([]Session, string, error) {
	if limit < 1 || limit > 100 || len(after) > 128 {
		return nil, "", ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT uuid,project_path,branch,registry_state,policy_json,policy_sha256 FROM sessions WHERE uuid>? ORDER BY uuid COLLATE BINARY LIMIT ?`, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	sessions := make([]Session, 0, limit)
	for rows.Next() {
		var session Session
		var policy string
		if err = rows.Scan(&session.UUID, &session.Project, &session.Branch, &session.Registry, &policy, &session.PolicySHA256); err != nil {
			return nil, "", err
		}
		session.Policy = json.RawMessage(policy)
		sessions = append(sessions, session)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	if err = rows.Close(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(sessions) > limit {
		next = sessions[limit-1].UUID
		sessions = sessions[:limit]
	}
	return sessions, next, nil
}
func (s *Store) ProjectPolicySHA(ctx context.Context, project string) (string, error) {
	var sha string
	err := s.db.QueryRowContext(ctx, `SELECT policy_sha256 FROM projects WHERE path=? AND registry_state='active'`, project).Scan(&sha)
	if errors.Is(err, sql.ErrNoRows) {
		return "", NotFound(api.ReasonProject, api.TargetProject(project))
	}
	return sha, err
}

// ProjectPolicyRecord exposes only the trusted project's current stored
// policy snapshot for semantic comparison during daemon restart.
func (s *Store) ProjectPolicyRecord(ctx context.Context, project string) (json.RawMessage, string, error) {
	if !validProject(project) {
		return nil, "", ErrInvalid
	}
	var raw, sha, registry string
	err := s.db.QueryRowContext(ctx, `SELECT policy_json,policy_sha256,registry_state FROM projects WHERE path=?`, project).Scan(&raw, &sha, &registry)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", NotFound(api.ReasonProject, api.TargetProject(project))
	}
	if err == nil && registry != "active" {
		return nil, "", Conflict(api.ReasonProjectDeleting, api.TargetProject(project))
	}
	return json.RawMessage(raw), sha, err
}

// CompleteCreation commits the established row and operation result atomically.
func (s *Store) CompleteCreation(ctx context.Context, opID string) error {
	j, err := s.beginJournal(ctx)
	if err != nil {
		return err
	}
	defer j.Rollback()
	tx := j.Tx
	var sid, kind, status string
	err = tx.QueryRowContext(ctx, `SELECT session_uuid,kind,status FROM operations WHERE id=?`, opID).Scan(&sid, &kind, &status)
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
		return classifyWrite(err)
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	_, err = j.update(ctx, opID, `UPDATE operations SET status='completed',phase='established',committed=1,reason='',diagnostic='',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), opID)
	if err != nil {
		return classifyWrite(err)
	}
	return j.commit(ctx)
}

func Evidence(op api.Operation) (CreationEvidence, error) {
	var ev CreationEvidence
	if err := json.Unmarshal(op.Evidence, &ev); err != nil {
		return ev, fmt.Errorf("creation evidence: %w", err)
	}
	if ev.EnvironmentBuilder != nil && !ev.EnvironmentBuilder.Valid() {
		return ev, ErrInvalid
	}
	if ev.RuntimeInitState != "" && ev.RuntimeInitState != "not-attempted" && ev.RuntimeInitState != "attempted" {
		return ev, ErrInvalid
	}
	if ev.ReplacementCleanup != nil && (!ev.ReplacementCleanup.Valid() || ev.ReplacementCleanup.OldUUID != ev.SupersedesUUID || ev.ReplacementCleanup.OldOperationID != ev.SupersedesOperationID || ev.ReplacementCleanup.OldUUID == op.SessionUUID || !ev.ReplacementCleanup.Completed && op.Phase != "replacement-cleanup") {
		return ev, ErrInvalid
	}
	if !validFingerprint(ev.ImageFingerprint) || ev.Environment != nil && !ev.Environment.Valid() {
		return ev, ErrInvalid
	}
	// The runtime's image is the pinned base until image-ready, then the
	// commit image.
	if img := ev.Image; img == nil {
		if ev.Environment != nil && ev.Environment.BaseFingerprint != ev.ImageFingerprint {
			return ev, ErrInvalid
		}
	} else if ev.Environment == nil || !validImageRef(img.Ref) || img.Ref != ev.ImageFingerprint || !validImageSelection(img.Selection, img.MaterialSHA256) {
		return ev, ErrInvalid
	}
	return ev, nil
}
