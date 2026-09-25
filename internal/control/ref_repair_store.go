package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type RefRepairChange struct {
	Code string `json:"code"`
	Path string `json:"path"`
}

type RefRepairIgnored struct {
	Count        int `json:"count"`
	LogicalBytes int `json:"logical_bytes"`
}

// RefRepairPreview describes an exact local worktree observation. Unsafe
// observations have no token and cannot authorize a P ref mutation.
type RefRepairPreview struct {
	Kind                  string            `json:"kind"`
	SessionUUID           string            `json:"session_uuid"`
	Project               string            `json:"project"`
	Branch                string            `json:"branch"`
	AssignedRef           string            `json:"assigned_ref"`
	AssignedRefStatus     string            `json:"assigned_ref_status"`
	LocalTip              string            `json:"local_tip"`
	RuntimeStatus         string            `json:"runtime_status"`
	IncusProject          string            `json:"incus_project"`
	InstanceName          string            `json:"instance_name"`
	IncusUUID             string            `json:"incus_uuid"`
	Generation            string            `json:"generation"`
	ImageFingerprint      string            `json:"image_fingerprint"`
	PolicySHA256          string            `json:"policy_sha256"`
	CredentialFingerprint string            `json:"credential_fingerprint"`
	LossOperationID       string            `json:"loss_operation_id"`
	LossFingerprint       string            `json:"loss_fingerprint"`
	Changes               []RefRepairChange `json:"changes"`
	Ignored               RefRepairIgnored  `json:"ignored"`
	UnsafeReasons         []string          `json:"unsafe_reasons"`
	Eligible              bool              `json:"eligible"`
	ConfirmationToken     string            `json:"confirmation_token,omitempty"`
	ExpiresAt             string            `json:"expires_at,omitempty"`
}

type RefRepairRequest struct {
	Key         string `json:"key"`
	UUID        string `json:"uuid"`
	TokenSHA256 string `json:"token_sha256"`
}

type RefRepairEvidence struct {
	Project               string `json:"project"`
	Branch                string `json:"branch"`
	Tip                   string `json:"tip"`
	PolicySHA256          string `json:"policy_sha256"`
	CredentialFingerprint string `json:"credential_fingerprint"`
	InstanceUUID          string `json:"instance_uuid"`
	IncusProject          string `json:"incus_project"`
	InstanceName          string `json:"instance_name"`
	ImageFingerprint      string `json:"image_fingerprint"`
	IncusUUID             string `json:"incus_uuid"`
	Generation            string `json:"generation"`
	OriginalStatus        string `json:"original_status"`
	LossOperationID       string `json:"loss_operation_id"`
	LossFingerprint       string `json:"loss_fingerprint"`
	LossResultSHA256      string `json:"loss_result_sha256"`
	PreviewExpiresAt      string `json:"preview_expires_at"`
}

func validRefRepairEvidence(e RefRepairEvidence) bool {
	return validProject(e.Project) && validBranch(e.Branch) && validOID(e.Tip) &&
		validFingerprint(e.PolicySHA256) && validFingerprint(e.CredentialFingerprint) &&
		validUUID(e.InstanceUUID) && e.IncusProject != "" && e.InstanceName != "" &&
		validFingerprint(e.ImageFingerprint) && validUUID(e.IncusUUID) && validUUID(e.Generation) &&
		(e.OriginalStatus == "Running" || e.OriginalStatus == "Stopped") &&
		validUUID(e.LossOperationID) && validFingerprint(e.LossFingerprint) && validFingerprint(e.LossResultSHA256)
}

// BeginRefRepair binds the completed source observation and absent-ref proof
// under the same authority lock used by receive-pack. The callback must not
// re-enter Store.
func (s *Store) BeginRefRepair(ctx context.Context, req RefRepairRequest, ev RefRepairEvidence, observe func(context.Context) error) (Operation, error) {
	if len(req.Key) == 0 || len(req.Key) > 128 || !validUUID(req.UUID) || !validFingerprint(req.TokenSHA256) ||
		!validRefRepairEvidence(ev) || observe == nil {
		return Operation{}, ErrInvalid
	}
	expires, err := time.Parse(time.RFC3339Nano, ev.PreviewExpiresAt)
	if err != nil {
		return Operation{}, ErrInvalid
	}
	request, _ := json.Marshal(req)
	evidence, _ := json.Marshal(ev)
	if len(evidence) > 2048 {
		return Operation{}, ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return Operation{}, err
	}
	defer s.gitAuthority.Unlock()
	var kind, oldHash string
	err = s.db.QueryRowContext(ctx, `SELECT kind,request_sha256 FROM operations WHERE idempotency_key=?`, req.Key).Scan(&kind, &oldHash)
	if err == nil {
		if kind != "session.ref.repair" || oldHash != digest(request) {
			return Operation{}, ErrConflict
		}
		return s.GetOperationByKey(ctx, req.Key)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, err
	}
	if err := observe(ctx); err != nil {
		return Operation{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback()
	if !time.Now().Before(expires) {
		return Operation{}, ErrConflict
	}
	var project, branch, registry, policy string
	if err = tx.QueryRowContext(ctx, `SELECT project_path,branch,registry_state,policy_sha256 FROM sessions WHERE uuid=?`, req.UUID).Scan(&project, &branch, &registry, &policy); err != nil {
		return Operation{}, err
	}
	if project != ev.Project || branch != ev.Branch || registry != "established" || policy != ev.PolicySHA256 {
		return Operation{}, ErrConflict
	}
	var currentPolicy string
	if err = tx.QueryRowContext(ctx, `SELECT policy_sha256 FROM projects WHERE path=? AND registry_state='active'`, project).Scan(&currentPolicy); err != nil || currentPolicy != policy {
		return Operation{}, ErrConflict
	}
	var principal string
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT fingerprint,active FROM git_principals WHERE session_uuid=? AND role='session' ORDER BY active DESC,rowid DESC LIMIT 1`, req.UUID).Scan(&principal, &active); err != nil || principal != ev.CredentialFingerprint || active != 1 {
		return Operation{}, ErrConflict
	}
	var lossRaw []byte
	if err = tx.QueryRowContext(ctx, `SELECT evidence_json FROM operations WHERE id=? AND session_uuid=? AND kind='workspace.loss.inspect' AND status='completed' AND phase='inspected'`, ev.LossOperationID, req.UUID).Scan(&lossRaw); err != nil || digest(lossRaw) != ev.LossResultSHA256 {
		return Operation{}, ErrConflict
	}
	id, err := newUUID()
	if err != nil {
		return Operation{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,committed,evidence_json,created_at,updated_at)
	 VALUES(?,?,?,?,?,?,?,'running','guarded',0,?,?,?)`, id, req.Key, "session.ref.repair", project, req.UUID, string(request), digest(request), string(evidence), now, now)
	if err != nil {
		return Operation{}, classifyWrite(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO git_ref_guards(project_path,branch,operation_id) VALUES(?,?,?)`, project, branch, id); err != nil {
		return Operation{}, classifyWrite(err)
	}
	if err = tx.Commit(); err != nil {
		return Operation{}, err
	}
	return Operation{ID: id, Key: req.Key, Kind: "session.ref.repair", Project: project, SessionUUID: req.UUID,
		Request: request, Status: "running", Phase: "guarded", Evidence: evidence, CreatedAt: now, UpdatedAt: now}, nil
}

// RefRepairGuardedCreate keeps the final ref observation, durable attempt
// marker and one native CAS within one Git authority boundary. A definitive
// pre-marker mismatch can roll back; any post-marker error remains guarded.
func (s *Store) RefRepairGuardedCreate(ctx context.Context, id string,
	verify func(context.Context, RefRepairEvidence) error,
	effect func(context.Context, RefRepairEvidence) error) (bool, error) {
	if verify == nil || effect == nil {
		return false, ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return false, err
	}
	defer s.gitAuthority.Unlock()
	op, err := s.GetOperation(ctx, id)
	if err != nil {
		return false, err
	}
	var ev RefRepairEvidence
	if op.Kind != "session.ref.repair" || op.Status != "running" || op.Phase != "quiesced" ||
		json.Unmarshal(op.Evidence, &ev) != nil || !validRefRepairEvidence(ev) || op.Project != ev.Project {
		return false, ErrConflict
	}
	var owner string
	if err := s.db.QueryRowContext(ctx, `SELECT operation_id FROM git_ref_guards WHERE project_path=? AND branch=?`, ev.Project, ev.Branch).Scan(&owner); err != nil || owner != id {
		return false, ErrConflict
	}
	if err := verify(ctx, ev); err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE operations SET phase='ref-create-issued',updated_at=?
	 WHERE id=? AND kind='session.ref.repair' AND status='running' AND phase='quiesced'`, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return false, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return false, ErrConflict
	}
	return true, effect(ctx, ev)
}

func (s *Store) EndRefRepair(ctx context.Context, id string, completed bool, verify func(context.Context, RefRepairEvidence) error) error {
	if verify == nil {
		return ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	op, err := s.GetOperation(ctx, id)
	if err != nil {
		return err
	}
	var ev RefRepairEvidence
	if op.Kind != "session.ref.repair" || op.Status != "running" || json.Unmarshal(op.Evidence, &ev) != nil ||
		!validRefRepairEvidence(ev) || op.Project != ev.Project ||
		(completed && op.Phase != "source-restored" || !completed &&
			(op.Committed || op.Phase != "guarded" && op.Phase != "quiesced")) {
		return ErrConflict
	}
	if err := verify(ctx, ev); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM git_ref_guards WHERE project_path=? AND branch=? AND operation_id=?`, ev.Project, ev.Branch, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrConflict
	}
	status, phase, committed := "failed", "stale", 0
	if completed {
		status, phase, committed = "completed", "completed", 1
	}
	_, err = tx.ExecContext(ctx, `UPDATE operations SET status=?,phase=?,committed=?,updated_at=? WHERE id=? AND status='running'`, status, phase, committed, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UnfinishedRefRepairs(ctx context.Context) ([]Operation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM operations WHERE kind='session.ref.repair' AND status IN ('running','blocked','unknown') ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Operation, 0, len(ids))
	for _, id := range ids {
		op, e := s.GetOperation(ctx, id)
		if e != nil {
			return nil, e
		}
		out = append(out, op)
	}
	return out, nil
}
