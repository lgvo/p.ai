package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type RecordRepairPreview struct {
	Kind              string   `json:"kind"`
	SessionUUID       string   `json:"session_uuid"`
	Project           string   `json:"project"`
	Branch            string   `json:"branch"`
	Principal         string   `json:"principal_fingerprint"`
	PrincipalActive   bool     `json:"principal_active"`
	ExternalAuthority string   `json:"external_authority"`
	RuntimeStatus     string   `json:"runtime_status"`
	AssignedRefStatus string   `json:"assigned_ref_status"`
	IncusProject      string   `json:"incus_project"`
	InstanceName      string   `json:"instance_name"`
	PolicySHA256      string   `json:"policy_sha256"`
	UnsafeReasons     []string `json:"unsafe_reasons"`
	Eligible          bool     `json:"eligible"`
	ConfirmationToken string   `json:"confirmation_token,omitempty"`
	ExpiresAt         string   `json:"expires_at,omitempty"`
}

type RecordRepairRequest struct {
	Key         string `json:"key"`
	UUID        string `json:"uuid"`
	TokenSHA256 string `json:"token_sha256"`
}

type RecordRepairEvidence struct {
	Project           string `json:"project"`
	Branch            string `json:"branch"`
	Principal         string `json:"principal_fingerprint"`
	PrincipalActive   bool   `json:"principal_active"`
	ExternalAuthority string `json:"external_authority"`
	PolicySHA256      string `json:"policy_sha256"`
	InstanceUUID      string `json:"instance_uuid"`
	IncusProject      string `json:"incus_project"`
	InstanceName      string `json:"instance_name"`
	ExpiresAt         string `json:"expires_at"`
}

func validRecordRepairEvidence(ev RecordRepairEvidence) bool {
	return validProject(ev.Project) && validBranch(ev.Branch) && validFingerprint(ev.PolicySHA256) &&
		(ev.Principal == "" || validFingerprint(ev.Principal)) && ev.ExternalAuthority == "none_registered" && validUUID(ev.InstanceUUID) &&
		ev.IncusProject != "" && ev.InstanceName != "" && ev.ExpiresAt != ""
}

func (s *Store) HasActiveSessionOperation(ctx context.Context, uuid string) (bool, error) {
	if !validUUID(uuid) {
		return false, ErrInvalid
	}
	var found int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM operations WHERE session_uuid=? AND status IN ('running','blocked','unknown') LIMIT 1`, uuid).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// BeginRecordRepair admits only an already reviewed both-absent session. The
// observation callback runs under P's Git authority lock before the durable
// assigned-ref guard is installed. It must not query this Store connection.
func (s *Store) BeginRecordRepair(ctx context.Context, req RecordRepairRequest, ev RecordRepairEvidence, observe func(context.Context) error) (Operation, error) {
	if req.Key == "" || len(req.Key) > 128 || !validUUID(req.UUID) || !validFingerprint(req.TokenSHA256) || !validRecordRepairEvidence(ev) || ev.InstanceName != "p-"+req.UUID || observe == nil {
		return Operation{}, ErrInvalid
	}
	expiry, err := time.Parse(time.RFC3339Nano, ev.ExpiresAt)
	if err != nil {
		return Operation{}, ErrInvalid
	}
	request, _ := json.Marshal(req)
	evidence, _ := json.Marshal(ev)
	if len(evidence) > 1536 {
		return Operation{}, ErrInvalid
	}
	if err = s.lockGitAuthority(ctx); err != nil {
		return Operation{}, err
	}
	defer s.gitAuthority.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback()
	var oldKind, oldHash string
	err = tx.QueryRowContext(ctx, `SELECT kind,request_sha256 FROM operations WHERE idempotency_key=?`, req.Key).Scan(&oldKind, &oldHash)
	if err == nil {
		if oldKind != "session.record.repair" || oldHash != digest(request) {
			return Operation{}, ErrConflict
		}
		return getOperationTx(ctx, tx, req.Key)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, err
	}
	if !time.Now().Before(expiry) {
		return Operation{}, ErrConflict
	}
	var project, branch, policy, registry string
	err = tx.QueryRowContext(ctx, `SELECT project_path,branch,policy_sha256,registry_state FROM sessions WHERE uuid=?`, req.UUID).Scan(&project, &branch, &policy, &registry)
	if err != nil || project != ev.Project || branch != ev.Branch || policy != ev.PolicySHA256 || registry != "established" {
		return Operation{}, errors.Join(err, ErrConflict)
	}
	var principal string
	var active int
	err = tx.QueryRowContext(ctx, `SELECT fingerprint,active FROM git_principals WHERE session_uuid=? AND role='session' ORDER BY active DESC,rowid DESC LIMIT 1`, req.UUID).Scan(&principal, &active)
	if errors.Is(err, sql.ErrNoRows) {
		principal, active, err = "", 0, nil
	}
	if err != nil || principal != ev.Principal || (active == 1) != ev.PrincipalActive {
		return Operation{}, errors.Join(err, ErrConflict)
	}
	// No native or Git mutation occurs before this exact observation.
	if err = observe(ctx); err != nil {
		return Operation{}, err
	}
	if !time.Now().Before(expiry) {
		return Operation{}, ErrConflict
	}
	id, err := newUUID()
	if err != nil {
		return Operation{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,committed,evidence_json,created_at,updated_at)
	 VALUES(?,?,?,?,?,?,?,'running','guarded',0,?,?,?)`, id, req.Key, "session.record.repair", project, req.UUID, string(request), digest(request), string(evidence), now, now)
	if err != nil {
		return Operation{}, classifyWrite(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO git_ref_guards(project_path,branch,operation_id) VALUES(?,?,?)`, project, branch, id); err != nil {
		return Operation{}, classifyWrite(err)
	}
	if err = tx.Commit(); err != nil {
		return Operation{}, err
	}
	return Operation{ID: id, Key: req.Key, Kind: "session.record.repair", Project: project, SessionUUID: req.UUID,
		Request: request, Status: "running", Phase: "guarded", Evidence: evidence, CreatedAt: now, UpdatedAt: now}, nil
}

// CommitRecordRepair is the irreversible authority boundary: the session is
// marked removing and all UUID-scoped Git principals are disabled together.
func (s *Store) CommitRecordRepair(ctx context.Context, id string, observe func(context.Context) error) error {
	if observe == nil {
		return ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	op, err := s.GetOperation(ctx, id)
	if err != nil || op.Kind != "session.record.repair" || op.Status != "running" || op.Phase != "guarded" {
		return errors.Join(err, ErrConflict)
	}
	var ev RecordRepairEvidence
	if json.Unmarshal(op.Evidence, &ev) != nil || !validRecordRepairEvidence(ev) || ev.Project != op.Project || ev.InstanceName != "p-"+op.SessionUUID {
		return ErrConflict
	}
	if err = observe(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner string
	if err = tx.QueryRowContext(ctx, `SELECT operation_id FROM git_ref_guards WHERE project_path=? AND branch=?`, ev.Project, ev.Branch).Scan(&owner); err != nil || owner != id {
		return ErrConflict
	}
	var principal string
	var active int
	err = tx.QueryRowContext(ctx, `SELECT fingerprint,active FROM git_principals WHERE session_uuid=? AND role='session' ORDER BY active DESC,rowid DESC LIMIT 1`, op.SessionUUID).Scan(&principal, &active)
	if errors.Is(err, sql.ErrNoRows) {
		principal, active, err = "", 0, nil
	}
	if err != nil || principal != ev.Principal || (active == 1) != ev.PrincipalActive {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET registry_state='removing' WHERE uuid=? AND project_path=? AND branch=? AND policy_sha256=? AND registry_state='established'`, op.SessionUUID, ev.Project, ev.Branch, ev.PolicySHA256)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE git_principals SET active=0 WHERE session_uuid=? AND role='session'`, op.SessionUUID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET phase='authority-disabled',committed=1,updated_at=? WHERE id=? AND phase='guarded'`, time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CompleteRecordRepair(ctx context.Context, id string, observe func(context.Context) error) error {
	if observe == nil {
		return ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	op, err := s.GetOperation(ctx, id)
	if err != nil || op.Kind != "session.record.repair" || op.Status != "running" || op.Phase != "secrets-absent" || !op.Committed {
		return errors.Join(err, ErrConflict)
	}
	var ev RecordRepairEvidence
	if json.Unmarshal(op.Evidence, &ev) != nil || !validRecordRepairEvidence(ev) || ev.Project != op.Project {
		return ErrConflict
	}
	if err = observe(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner string
	if err = tx.QueryRowContext(ctx, `SELECT operation_id FROM git_ref_guards WHERE project_path=? AND branch=?`, ev.Project, ev.Branch).Scan(&owner); err != nil || owner != id {
		return ErrConflict
	}
	var registry, branch, policy string
	if err = tx.QueryRowContext(ctx, `SELECT registry_state,branch,policy_sha256 FROM sessions WHERE uuid=? AND project_path=?`, op.SessionUUID, ev.Project).Scan(&registry, &branch, &policy); err != nil || registry != "removing" || branch != ev.Branch || policy != ev.PolicySHA256 {
		return ErrConflict
	}
	var activePrincipalCount int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM git_principals WHERE session_uuid=? AND role='session' AND active=1`, op.SessionUUID).Scan(&activePrincipalCount); err != nil || activePrincipalCount != 0 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM git_unborn_grants WHERE session_uuid=?`, op.SessionUUID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM git_principals WHERE session_uuid=? AND role='session' AND active=0`, op.SessionUUID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE uuid=? AND registry_state='removing'`, op.SessionUUID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM git_ref_guards WHERE project_path=? AND branch=? AND operation_id=?`, ev.Project, ev.Branch, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET status='completed',phase='removed',committed=1,updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UnfinishedRecordRepairs(ctx context.Context) ([]Operation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM operations WHERE kind='session.record.repair' AND status IN ('running','blocked','unknown') ORDER BY rowid`)
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
