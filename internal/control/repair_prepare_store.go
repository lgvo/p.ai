package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

var repairDerivationPath = regexp.MustCompile(`^/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-[A-Za-z0-9+._?=-]+\.drv$`)
var repairSourceNarHash = regexp.MustCompile(`^sha256-[A-Za-z0-9+/]{43}=$`)

// RepairPreparation records the exact committed source used by the restricted
// builder. Its result describes an environment identity, not an image or a
// grant to recreate the missing session runtime.
type RepairPreparation struct {
	Project                  string            `json:"project"`
	Branch                   string            `json:"branch"`
	AssignedTip              string            `json:"assigned_tip"`
	PolicySHA256             string            `json:"policy_sha256"`
	CredentialFingerprint    string            `json:"credential_fingerprint"`
	IncusProject             string            `json:"incus_project"`
	InstanceName             string            `json:"instance_name"`
	InstanceUUID             string            `json:"instance_uuid"`
	RecordedImageFingerprint string            `json:"recorded_image_fingerprint"`
	RecordedEnvironmentKey   string            `json:"recorded_environment_key,omitempty"`
	RecordedSourceCommit     string            `json:"recorded_source_commit,omitempty"`
	Selection                CreationSelection `json:"selection"`
	Environment              EnvironmentIntent `json:"environment"`
	BuilderTreeOID           string            `json:"builder_tree_oid,omitempty"`
	EnvironmentSelection     string            `json:"environment_selection,omitempty"`
	EnvironmentKey           string            `json:"environment_key,omitempty"`
	CommittedInputsDigest    string            `json:"committed_inputs_digest,omitempty"`
	DerivationPath           string            `json:"derivation_path,omitempty"`
	SourceNarHash            string            `json:"source_nar_hash,omitempty"`
	FlakePresent             bool              `json:"flake_present,omitempty"`
}

func validRepairPreparation(e RepairPreparation) bool {
	if !validProject(e.Project) || !validBranch(e.Branch) || !validOID(e.AssignedTip) ||
		!validFingerprint(e.PolicySHA256) || !validFingerprint(e.CredentialFingerprint) ||
		e.IncusProject == "" || e.InstanceName == "" || !validUUID(e.InstanceUUID) ||
		!validFingerprint(e.RecordedImageFingerprint) ||
		(e.RecordedEnvironmentKey != "" && !validFingerprint(e.RecordedEnvironmentKey)) ||
		(e.RecordedSourceCommit != "" && !validOID(e.RecordedSourceCommit)) ||
		!e.Selection.Valid() || !e.Environment.Valid() ||
		(e.BuilderTreeOID != "" && !validOID(e.BuilderTreeOID)) {
		return false
	}
	switch e.EnvironmentSelection {
	case "":
		return e.EnvironmentKey == "" && e.CommittedInputsDigest == "" && e.DerivationPath == "" && e.SourceNarHash == "" && !e.FlakePresent
	case "base-no-flake":
		return e.EnvironmentKey == "" && e.DerivationPath == "" && e.CommittedInputsDigest == "" && e.SourceNarHash == "" && e.BuilderTreeOID != "" && !e.FlakePresent
	case "base-no-default":
		return e.EnvironmentKey == "" && e.DerivationPath == "" && e.CommittedInputsDigest == "" && repairSourceNarHash.MatchString(e.SourceNarHash) && e.BuilderTreeOID != "" && e.FlakePresent
	case "devshell":
		return validFingerprint(e.EnvironmentKey) && validFingerprint(e.CommittedInputsDigest) && repairDerivationPath.MatchString(e.DerivationPath) && repairSourceNarHash.MatchString(e.SourceNarHash) && e.BuilderTreeOID != "" && e.FlakePresent
	default:
		return false
	}
}

func (s *Store) BeginRepairPreparation(ctx context.Context, key, uuid string, ev RepairPreparation, observe func(context.Context) error, capacity CapacityObserver) (Operation, error) {
	if len(key) == 0 || len(key) > 128 || !validUUID(uuid) || !validRepairPreparation(ev) || ev.EnvironmentSelection != "" || observe == nil {
		return Operation{}, ErrInvalid
	}
	request, _ := json.Marshal(struct {
		Key  string `json:"key"`
		UUID string `json:"uuid"`
	}{key, uuid})
	evidence, _ := json.Marshal(ev)
	if len(evidence) > 4096 {
		return Operation{}, ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return Operation{}, err
	}
	defer s.gitAuthority.Unlock()
	var priorKind, priorHash string
	err := s.db.QueryRowContext(ctx, `SELECT kind,request_sha256 FROM operations WHERE idempotency_key=?`, key).Scan(&priorKind, &priorHash)
	if err == nil {
		if priorKind != "session.repair.prepare" || priorHash != digest(request) {
			return Operation{}, ErrConflict
		}
		return s.GetOperationByKey(ctx, key)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, err
	}
	if err = observe(ctx); err != nil {
		return Operation{}, err
	}
	if err = s.checkSessionRepairCapacity(ctx, capacity); err != nil {
		return Operation{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback()
	var project, branch, policy, registry string
	if err = tx.QueryRowContext(ctx, `SELECT project_path,branch,policy_sha256,registry_state FROM sessions WHERE uuid=?`, uuid).Scan(&project, &branch, &policy, &registry); err != nil {
		return Operation{}, err
	}
	if project != ev.Project || branch != ev.Branch || policy != ev.PolicySHA256 || registry != "established" {
		return Operation{}, ErrConflict
	}
	var currentPolicy string
	if err = tx.QueryRowContext(ctx, `SELECT policy_sha256 FROM projects WHERE path=? AND registry_state='active'`, project).Scan(&currentPolicy); err != nil || currentPolicy != ev.PolicySHA256 {
		return Operation{}, ErrConflict
	}
	var credential string
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT fingerprint,active FROM git_principals WHERE session_uuid=? AND role='session' ORDER BY active DESC,rowid DESC LIMIT 1`, uuid).Scan(&credential, &active); err != nil || credential != ev.CredentialFingerprint || active != 1 {
		return Operation{}, ErrConflict
	}
	id, err := newUUID()
	if err != nil {
		return Operation{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,committed,evidence_json,created_at,updated_at)
	 VALUES(?,?,?,?,?,?,?,'running','reserved',0,?,?,?)`, id, key, "session.repair.prepare", project, uuid, string(request), digest(request), string(evidence), now, now)
	if err != nil {
		return Operation{}, classifyWrite(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO git_ref_guards(project_path,branch,operation_id) VALUES(?,?,?)`, project, branch, id); err != nil {
		return Operation{}, classifyWrite(err)
	}
	if err = tx.Commit(); err != nil {
		return Operation{}, err
	}
	return Operation{ID: id, Key: key, Kind: "session.repair.prepare", Project: project, SessionUUID: uuid, Request: request, Status: "running", Phase: "reserved", Evidence: evidence, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) CompleteRepairPreparation(ctx context.Context, id string, verify func(context.Context, RepairPreparation) error) error {
	if verify == nil {
		return ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw, project, branch, sessionID, phase string
	if err = tx.QueryRowContext(ctx, `SELECT o.evidence_json,o.project_path,s.branch,o.session_uuid,o.phase FROM operations o JOIN sessions s ON s.uuid=o.session_uuid WHERE o.id=? AND o.kind='session.repair.prepare' AND o.status='running'`, id).Scan(&raw, &project, &branch, &sessionID, &phase); err != nil {
		return err
	}
	var ev RepairPreparation
	if json.Unmarshal([]byte(raw), &ev) != nil || !validRepairPreparation(ev) || ev.Project != project || ev.Branch != branch || ev.EnvironmentSelection == "" || phase != "builder-absent" {
		return ErrConflict
	}
	var policy, registry string
	if err = tx.QueryRowContext(ctx, `SELECT policy_sha256,registry_state FROM sessions WHERE uuid=?`, sessionID).Scan(&policy, &registry); err != nil || registry != "established" || policy != ev.PolicySHA256 {
		return ErrConflict
	}
	var currentPolicy string
	if err = tx.QueryRowContext(ctx, `SELECT policy_sha256 FROM projects WHERE path=? AND registry_state='active'`, project).Scan(&currentPolicy); err != nil || currentPolicy != ev.PolicySHA256 {
		return ErrConflict
	}
	if err = verify(ctx, ev); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM git_ref_guards WHERE project_path=? AND branch=? AND operation_id=?`, project, branch, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE operations SET status='completed',phase='completed',committed=1,updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// A stale preparation can release its guard only before builder admission.
func (s *Store) FailRepairPreparationPreInit(ctx context.Context, id string) error {
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var project, branch, phase string
	err = tx.QueryRowContext(ctx, `SELECT o.project_path,s.branch,o.phase FROM operations o JOIN sessions s ON s.uuid=o.session_uuid WHERE o.id=? AND o.kind='session.repair.prepare' AND o.status IN ('running','blocked') AND o.committed=0`, id).Scan(&project, &branch, &phase)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if phase != "reserved" && phase != "source-pinned" {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM git_ref_guards WHERE project_path=? AND branch=? AND operation_id=?`, project, branch, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE operations SET status='failed',phase='stale',diagnostic='repair preparation changed before native effect',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// A failed builder preparation releases authority only after the exact
// deterministic builder name is positively absent. Its operation ID is never
// reused, so a delayed request cannot affect a later builder identity.
func (s *Store) FailRepairPreparationAfterCleanup(ctx context.Context, id string, verify func(context.Context, RepairPreparation) error) error {
	if verify == nil {
		return ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw, project, branch, phase string
	if err = tx.QueryRowContext(ctx, `SELECT o.evidence_json,o.project_path,s.branch,o.phase FROM operations o JOIN sessions s ON s.uuid=o.session_uuid WHERE o.id=? AND o.kind='session.repair.prepare' AND o.status='running'`, id).Scan(&raw, &project, &branch, &phase); err != nil {
		return err
	}
	var ev RepairPreparation
	if json.Unmarshal([]byte(raw), &ev) != nil || !validRepairPreparation(ev) || ev.Project != project || ev.Branch != branch || phase != "builder-absent" || ev.EnvironmentSelection != "" {
		return ErrConflict
	}
	if err = verify(ctx, ev); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM git_ref_guards WHERE project_path=? AND branch=? AND operation_id=?`, project, branch, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE operations SET status='failed',phase='builder-cleaned',committed=1,diagnostic='repair environment preparation failed; exact builder removed',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UnfinishedRepairPreparations(ctx context.Context) ([]Operation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM operations WHERE kind='session.repair.prepare' AND status IN ('running','blocked','unknown') ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := make([]Operation, 0, len(ids))
	for _, id := range ids {
		op, e := s.GetOperation(ctx, id)
		if e != nil {
			return nil, e
		}
		result = append(result, op)
	}
	return result, nil
}

// A completed preparation is evidence only; it never changes the current
// session image. Callers must compare all of its authority fields afresh.
func (s *Store) CompletedRepairPreparation(ctx context.Context, id, uuid string) (RepairPreparation, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT evidence_json FROM operations WHERE id=? AND session_uuid=? AND kind='session.repair.prepare' AND status='completed' AND phase='completed'`, id, uuid).Scan(&raw)
	if err != nil {
		return RepairPreparation{}, err
	}
	var ev RepairPreparation
	if json.Unmarshal(raw, &ev) != nil || !validRepairPreparation(ev) || ev.EnvironmentSelection == "" {
		return RepairPreparation{}, ErrConflict
	}
	return ev, nil
}
