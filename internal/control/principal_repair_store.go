package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type PrincipalRepairPreview struct {
	Kind              string   `json:"kind"`
	SessionUUID       string   `json:"session_uuid"`
	Project           string   `json:"project"`
	Branch            string   `json:"branch"`
	AssignedRefStatus string   `json:"assigned_ref_status"`
	AssignedTip       string   `json:"assigned_tip"`
	OldFingerprint    string   `json:"old_fingerprint"`
	Registration      string   `json:"registration_status"`
	KeyStatus         string   `json:"key_status"`
	GuestKeyStatus    string   `json:"guest_key_status"`
	GuestKeySHA256    string   `json:"guest_key_sha256"`
	RuntimeStatus     string   `json:"runtime_status"`
	IncusProject      string   `json:"incus_project"`
	InstanceName      string   `json:"instance_name"`
	IncusUUID         string   `json:"incus_uuid"`
	Generation        string   `json:"generation"`
	ImageFingerprint  string   `json:"image_fingerprint"`
	PolicySHA256      string   `json:"policy_sha256"`
	UnsafeReasons     []string `json:"unsafe_reasons"`
	Eligible          bool     `json:"eligible"`
	ConfirmationToken string   `json:"confirmation_token,omitempty"`
	ExpiresAt         string   `json:"expires_at,omitempty"`
}

type PrincipalRepairRequest struct {
	Key         string `json:"key"`
	UUID        string `json:"uuid"`
	TokenSHA256 string `json:"token_sha256"`
}

type PrincipalRepairEvidence struct {
	Project          string `json:"project"`
	Branch           string `json:"branch"`
	AssignedTip      string `json:"assigned_tip"`
	RefPresent       bool   `json:"ref_present"`
	PolicySHA256     string `json:"policy_sha256"`
	OldFingerprint   string `json:"old_fingerprint"`
	OldActive        bool   `json:"old_active"`
	GuestKeyStatus   string `json:"guest_key_status"`
	GuestKeySHA256   string `json:"guest_key_sha256"`
	InstanceUUID     string `json:"instance_uuid"`
	IncusProject     string `json:"incus_project"`
	InstanceName     string `json:"instance_name"`
	IncusUUID        string `json:"incus_uuid"`
	Generation       string `json:"generation"`
	ImageFingerprint string `json:"image_fingerprint"`
	NewFingerprint   string `json:"new_fingerprint,omitempty"`
	ExpiresAt        string `json:"expires_at"`
}

func validPrincipalRepairEvidence(ev PrincipalRepairEvidence) bool {
	return validProject(ev.Project) && validBranch(ev.Branch) && validFingerprint(ev.PolicySHA256) &&
		((ev.RefPresent && validOID(ev.AssignedTip)) || (!ev.RefPresent && ev.AssignedTip == "")) &&
		(ev.OldFingerprint == "" || validFingerprint(ev.OldFingerprint)) &&
		(ev.GuestKeyStatus == "missing" && ev.GuestKeySHA256 == "" ||
			(ev.GuestKeyStatus == "matching" || ev.GuestKeyStatus == "mismatch" || ev.GuestKeyStatus == "invalid") && validFingerprint(ev.GuestKeySHA256)) &&
		(ev.NewFingerprint == "" || validFingerprint(ev.NewFingerprint)) &&
		validUUID(ev.InstanceUUID) && ev.IncusProject != "" && ev.InstanceName != "" &&
		validUUID(ev.IncusUUID) && validUUID(ev.Generation) && validFingerprint(ev.ImageFingerprint)
}

// BeginPrincipalRepair binds the exact stopped runtime and current assignment
// before reserving its ref. The private-key observation is made by the caller;
// no key is created by admission or preview.
func (s *Store) BeginPrincipalRepair(ctx context.Context, req PrincipalRepairRequest, ev PrincipalRepairEvidence, observe func(context.Context) error) (Operation, error) {
	if req.Key == "" || len(req.Key) > 128 || !validUUID(req.UUID) || !validFingerprint(req.TokenSHA256) || !validPrincipalRepairEvidence(ev) {
		return Operation{}, ErrInvalid
	}
	if observe == nil {
		return Operation{}, ErrInvalid
	}
	expiry, err := time.Parse(time.RFC3339Nano, ev.ExpiresAt)
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
	if err := observe(ctx); err != nil {
		return Operation{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback()
	var oldKind, oldHash string
	err = tx.QueryRowContext(ctx, `SELECT kind,request_sha256 FROM operations WHERE idempotency_key=?`, req.Key).Scan(&oldKind, &oldHash)
	if err == nil {
		if oldKind != "session.principal.repair" || oldHash != digest(request) {
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
	var currentPolicy string
	err = tx.QueryRowContext(ctx, `SELECT policy_sha256 FROM projects WHERE path=? AND registry_state='active'`, project).Scan(&currentPolicy)
	if err != nil || currentPolicy != policy {
		return Operation{}, errors.Join(err, ErrConflict)
	}
	var old string
	var active int
	err = tx.QueryRowContext(ctx, `SELECT fingerprint,active FROM git_principals WHERE session_uuid=? AND role='session' ORDER BY active DESC,rowid DESC LIMIT 1`, req.UUID).Scan(&old, &active)
	if errors.Is(err, sql.ErrNoRows) {
		old, active, err = "", 0, nil
	}
	if err != nil || old != ev.OldFingerprint || (active == 1) != ev.OldActive {
		return Operation{}, errors.Join(err, ErrConflict)
	}
	id, err := newUUID()
	if err != nil {
		return Operation{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,committed,evidence_json,created_at,updated_at)
	 VALUES(?,?,?,?,?,?,?,'running','guarded',0,?,?,?)`, id, req.Key, "session.principal.repair", project, req.UUID, string(request), digest(request), string(evidence), now, now)
	if err != nil {
		return Operation{}, classifyWrite(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO git_ref_guards(project_path,branch,operation_id) VALUES(?,?,?)`, project, branch, id); err != nil {
		return Operation{}, classifyWrite(err)
	}
	if err = tx.Commit(); err != nil {
		return Operation{}, err
	}
	return Operation{ID: id, Key: req.Key, Kind: "session.principal.repair", Project: project, SessionUUID: req.UUID,
		Request: request, Status: "running", Phase: "guarded", Evidence: evidence, CreatedAt: now, UpdatedAt: now}, nil
}

// RotatePrincipalRepair disables every old session key and inserts exactly
// one new active fingerprint atomically with the durable authority phase.
func (s *Store) RotatePrincipalRepair(ctx context.Context, id string) error {
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sessionID, project, phase, raw string
	err = tx.QueryRowContext(ctx, `SELECT session_uuid,project_path,phase,evidence_json FROM operations WHERE id=? AND kind='session.principal.repair' AND status='running'`, id).Scan(&sessionID, &project, &phase, &raw)
	if err != nil {
		return errors.Join(err, ErrConflict)
	}
	var ev PrincipalRepairEvidence
	if json.Unmarshal([]byte(raw), &ev) != nil || !validPrincipalRepairEvidence(ev) || !validFingerprint(ev.NewFingerprint) ||
		ev.NewFingerprint == ev.OldFingerprint || ev.Project != project || phase != "key-ready" {
		return ErrConflict
	}
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT operation_id FROM git_ref_guards WHERE project_path=? AND branch=?`, project, ev.Branch).Scan(&owner)
	if err != nil || owner != id {
		return ErrConflict
	}
	var branch, policy, registry string
	err = tx.QueryRowContext(ctx, `SELECT branch,policy_sha256,registry_state FROM sessions WHERE uuid=? AND project_path=?`, sessionID, project).Scan(&branch, &policy, &registry)
	if err != nil || branch != ev.Branch || policy != ev.PolicySHA256 || registry != "established" {
		return ErrConflict
	}
	var old string
	var active int
	err = tx.QueryRowContext(ctx, `SELECT fingerprint,active FROM git_principals WHERE session_uuid=? AND role='session' ORDER BY active DESC,rowid DESC LIMIT 1`, sessionID).Scan(&old, &active)
	if errors.Is(err, sql.ErrNoRows) {
		old, active, err = "", 0, nil
	}
	if err != nil || old != ev.OldFingerprint || (active == 1) != ev.OldActive {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE git_principals SET active=0 WHERE session_uuid=? AND role='session'`, sessionID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO git_principals(fingerprint,role,project_path,session_uuid,active) VALUES(?,'session',?,?,1)`, ev.NewFingerprint, project, sessionID); err != nil {
		return classifyWrite(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET phase='authority-rotated',committed=1,updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CompletePrincipalRepair(ctx context.Context, id string, verify func(context.Context, PrincipalRepairEvidence) error) error {
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
	var ev PrincipalRepairEvidence
	if op.Kind != "session.principal.repair" || op.Status != "running" || op.Phase != "guest-key-ready" ||
		json.Unmarshal(op.Evidence, &ev) != nil || !validFingerprint(ev.NewFingerprint) || !validPrincipalRepairEvidence(ev) {
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
	var active int
	err = tx.QueryRowContext(ctx, `SELECT active FROM git_principals WHERE fingerprint=? AND session_uuid=? AND project_path=? AND role='session'`, ev.NewFingerprint, op.SessionUUID, ev.Project).Scan(&active)
	if err != nil || active != 1 {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM git_ref_guards WHERE project_path=? AND branch=? AND operation_id=?`, ev.Project, ev.Branch, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE operations SET status='completed',phase='completed',committed=1,updated_at=? WHERE id=? AND status='running' AND phase='guest-key-ready'`, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UnfinishedPrincipalRepairs(ctx context.Context) ([]Operation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM operations WHERE kind='session.principal.repair' AND status IN ('running','blocked','unknown') ORDER BY rowid`)
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
