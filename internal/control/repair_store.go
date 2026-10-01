package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// RepairPreview is a read-only, expiring description of one missing runtime.
// A blocked plan has no token and grants no mutation authority.
type RepairPreview struct {
	Kind                   string                    `json:"kind"`
	SessionUUID            string                    `json:"session_uuid"`
	Project                string                    `json:"project"`
	Branch                 string                    `json:"branch"`
	AssignedTip            string                    `json:"assigned_tip,omitempty"`
	ImageFingerprint       string                    `json:"image_fingerprint"`
	ImageSourceCommit      string                    `json:"image_source_commit,omitempty"`
	ImageSourceDiffers     bool                      `json:"image_source_differs"`
	PreparationOperationID string                    `json:"preparation_operation_id,omitempty"`
	Environment            *RepairEnvironmentPreview `json:"environment,omitempty"`
	ImageStatus            string                    `json:"image_status"`
	PolicySHA256           string                    `json:"policy_sha256"`
	CredentialFingerprint  string                    `json:"credential_fingerprint,omitempty"`
	IncusProject           string                    `json:"incus_project"`
	InstanceName           string                    `json:"instance_name"`
	RuntimeLocalLoss       string                    `json:"runtime_local_loss"`
	Eligible               bool                      `json:"eligible"`
	BlockedReason          string                    `json:"blocked_reason,omitempty"`
	ConfirmationToken      string                    `json:"confirmation_token,omitempty"`
	ExpiresAt              string                    `json:"expires_at,omitempty"`
}

// RepairEnvironmentPreview is a bounded selection resolved from the reviewed
// committed tip. A needs_build selection has no candidate image fingerprint.
type RepairEnvironmentPreview struct {
	SourceCommit              string `json:"source_commit"`
	System                    string `json:"system"`
	Selection                 string `json:"selection"`
	EnvironmentKey            string `json:"environment_key,omitempty"`
	BaseImageFingerprint      string `json:"base_image_fingerprint"`
	RecordedImageFingerprint  string `json:"recorded_image_fingerprint"`
	RecordedEnvironmentKey    string `json:"recorded_environment_key,omitempty"`
	CandidateImageFingerprint string `json:"candidate_image_fingerprint,omitempty"`
	IdentityDiffers           bool   `json:"identity_differs"`
	SourceDiffers             bool   `json:"source_differs"`
	ImageStatus               string `json:"image_status"`
}

type RepairConfirmRequest struct {
	Key               string `json:"key"`
	UUID              string `json:"uuid"`
	ConfirmationToken string `json:"confirmation_token"`
}

type RepairRequest struct {
	Key         string `json:"key"`
	UUID        string `json:"uuid"`
	TokenSHA256 string `json:"token_sha256"`
}

type RepairEvidence struct {
	Project                  string             `json:"project"`
	Branch                   string             `json:"branch"`
	AssignedTip              string             `json:"assigned_tip"`
	PolicySHA256             string             `json:"policy_sha256"`
	CredentialFingerprint    string             `json:"credential_fingerprint"`
	IncusProject             string             `json:"incus_project"`
	InstanceName             string             `json:"instance_name"`
	InstanceUUID             string             `json:"instance_uuid"`
	ImageFingerprint         string             `json:"image_fingerprint"`
	RecordedImageFingerprint string             `json:"recorded_image_fingerprint,omitempty"`
	PreparationOperationID   string             `json:"preparation_operation_id,omitempty"`
	PreparationSHA256        string             `json:"preparation_sha256,omitempty"`
	EnvironmentKey           string             `json:"environment_key,omitempty"`
	EnvironmentSelection     string             `json:"environment_selection,omitempty"`
	EnvironmentSourceCommit  string             `json:"environment_source_commit,omitempty"`
	Environment              *EnvironmentIntent `json:"environment,omitempty"`
	EnvironmentState         *EnvironmentState  `json:"environment_state,omitempty"`
	BuilderTreeOID           string             `json:"builder_tree_oid,omitempty"`
	ImageSourceCommit        string             `json:"image_source_commit,omitempty"`
	BaseFingerprint          string             `json:"base_fingerprint"`
	PreviewExpiresAt         string             `json:"preview_expires_at"`
	IncusUUID                string             `json:"incus_uuid,omitempty"`
	Generation               string             `json:"generation,omitempty"`
}

func validRepairEvidence(e RepairEvidence) bool {
	validImage := validFingerprint(e.ImageFingerprint)
	if e.PreparationOperationID != "" {
		validImage = validUUID(e.PreparationOperationID) && validFingerprint(e.PreparationSHA256) && validFingerprint(e.RecordedImageFingerprint) &&
			e.Environment != nil && e.Environment.Valid() && validOID(e.EnvironmentSourceCommit) &&
			(e.EnvironmentKey == "" || validFingerprint(e.EnvironmentKey)) &&
			(e.ImageFingerprint == "" || validFingerprint(e.ImageFingerprint)) &&
			(e.BuilderTreeOID == "" || validOID(e.BuilderTreeOID)) &&
			(e.EnvironmentSelection == "base-no-flake" || e.EnvironmentSelection == "base-no-default" || e.EnvironmentSelection == "devshell")
		if e.EnvironmentSelection == "devshell" && e.EnvironmentKey == "" || e.EnvironmentSelection != "devshell" && e.EnvironmentKey != "" {
			validImage = false
		}
		if e.ImageFingerprint != "" {
			if e.EnvironmentState == nil || e.EnvironmentState.Fingerprint != e.ImageFingerprint {
				validImage = false
			} else if e.EnvironmentSelection == "devshell" &&
				(e.EnvironmentState.Key != e.EnvironmentKey || !validFingerprint(e.EnvironmentState.MaterialDigest) ||
					e.EnvironmentState.CaptureStorePath == "" || !validUUID(e.EnvironmentState.BuilderRequest)) {
				validImage = false
			}
		}
	}
	return validProject(e.Project) && validBranch(e.Branch) && validOID(e.AssignedTip) &&
		validFingerprint(e.PolicySHA256) && validFingerprint(e.CredentialFingerprint) &&
		e.IncusProject != "" && e.InstanceName != "" && validUUID(e.InstanceUUID) &&
		validImage && validFingerprint(e.BaseFingerprint) &&
		(e.ImageSourceCommit == "" || validOID(e.ImageSourceCommit)) &&
		(e.IncusUUID == "" && e.Generation == "" || validUUID(e.IncusUUID) && validUUID(e.Generation))
}

// BeginRepair serializes a fresh exact observation with receive-pack and
// persists the single-session reservation before any native create effect.
// The callback must not re-enter Store while gitAuthority is held.
func (s *Store) BeginRepair(ctx context.Context, req RepairRequest, ev RepairEvidence, observe func(context.Context) error, capacity CapacityObserver) (Operation, error) {
	if len(req.Key) < 1 || len(req.Key) > 128 || !validUUID(req.UUID) || !validFingerprint(req.TokenSHA256) ||
		!validRepairEvidence(ev) || ev.IncusUUID != "" || observe == nil {
		return Operation{}, ErrInvalid
	}
	expires, err := time.Parse(time.RFC3339Nano, ev.PreviewExpiresAt)
	if err != nil {
		return Operation{}, ErrInvalid
	}
	request, _ := json.Marshal(req)
	evidence, _ := json.Marshal(ev)
	if len(evidence) > 16384 {
		return Operation{}, ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return Operation{}, err
	}
	defer s.gitAuthority.Unlock()
	var kind, oldHash string
	err = s.db.QueryRowContext(ctx, `SELECT kind,request_sha256 FROM operations WHERE idempotency_key=?`, req.Key).Scan(&kind, &oldHash)
	if err == nil {
		if kind != "session.repair" || oldHash != digest(request) {
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
	if err := s.checkSessionRepairCapacity(ctx, capacity); err != nil {
		return Operation{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback()
	var project, branch, registry, policy string
	err = tx.QueryRowContext(ctx, `SELECT project_path,branch,registry_state,policy_sha256 FROM sessions WHERE uuid=?`, req.UUID).Scan(&project, &branch, &registry, &policy)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
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
	err = tx.QueryRowContext(ctx, `SELECT fingerprint,active FROM git_principals WHERE session_uuid=? AND role='session' ORDER BY active DESC,rowid DESC LIMIT 1`, req.UUID).Scan(&credential, &active)
	if err != nil || credential != ev.CredentialFingerprint || active != 1 {
		return Operation{}, ErrConflict
	}
	if ev.PreparationOperationID != "" {
		var prepared []byte
		if err = tx.QueryRowContext(ctx, `SELECT evidence_json FROM operations WHERE id=? AND session_uuid=? AND kind='session.repair.prepare' AND status='completed' AND phase='completed'`, ev.PreparationOperationID, req.UUID).Scan(&prepared); err != nil || digest(prepared) != ev.PreparationSHA256 {
			return Operation{}, ErrConflict
		}
		var pinned RepairPreparation
		if json.Unmarshal(prepared, &pinned) != nil || !validRepairPreparation(pinned) ||
			pinned.Project != ev.Project || pinned.Branch != ev.Branch || pinned.AssignedTip != ev.AssignedTip ||
			pinned.PolicySHA256 != ev.PolicySHA256 || pinned.CredentialFingerprint != ev.CredentialFingerprint ||
			pinned.RecordedImageFingerprint != ev.RecordedImageFingerprint || pinned.RecordedSourceCommit != ev.ImageSourceCommit ||
			pinned.EnvironmentKey != ev.EnvironmentKey || pinned.EnvironmentSelection != ev.EnvironmentSelection ||
			pinned.Environment != *ev.Environment || pinned.BuilderTreeOID != ev.BuilderTreeOID ||
			ev.EnvironmentSourceCommit != ev.AssignedTip {
			return Operation{}, ErrConflict
		}
	}
	if !time.Now().Before(expires) {
		return Operation{}, ErrConflict
	}
	id, err := newUUID()
	if err != nil {
		return Operation{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,committed,evidence_json,created_at,updated_at)
	 VALUES(?,?,?,?,?,?,?,'running','reserved',0,?,?,?)`, id, req.Key, "session.repair", project, req.UUID, string(request), digest(request), string(evidence), now, now)
	if err != nil {
		return Operation{}, classifyWrite(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO git_ref_guards(project_path,branch,operation_id) VALUES(?,?,?)`, project, branch, id); err != nil {
		return Operation{}, classifyWrite(err)
	}
	if err = tx.Commit(); err != nil {
		return Operation{}, err
	}
	return Operation{ID: id, Key: req.Key, Kind: "session.repair", Project: project, SessionUUID: req.UUID, Request: request, Status: "running", Phase: "reserved", Evidence: evidence, CreatedAt: now, UpdatedAt: now}, nil
}

// FailRepairPreInit releases only a positively no-effect stale reservation.
func (s *Store) FailRepairPreInit(ctx context.Context, id string) error {
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
	err = tx.QueryRowContext(ctx, `SELECT o.project_path,s.branch,o.phase FROM operations o JOIN sessions s ON s.uuid=o.session_uuid WHERE o.id=? AND o.kind='session.repair' AND o.status IN ('running','blocked') AND o.committed=0`, id).Scan(&project, &branch, &phase)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if phase != "reserved" && phase != "guarded" {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM git_ref_guards WHERE project_path=? AND branch=? AND operation_id=?`, project, branch, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET status='failed',phase='stale',diagnostic='repair preview changed before native effect',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CompleteRepair(ctx context.Context, id string, verify func(context.Context, RepairEvidence) error) error {
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
	var raw, project, sessionID, phase string
	err = tx.QueryRowContext(ctx, `SELECT evidence_json,project_path,session_uuid,phase FROM operations WHERE id=? AND kind='session.repair' AND status='running'`, id).Scan(&raw, &project, &sessionID, &phase)
	if err != nil {
		return err
	}
	var ev RepairEvidence
	if json.Unmarshal([]byte(raw), &ev) != nil || !validRepairEvidence(ev) || !validFingerprint(ev.ImageFingerprint) || ev.Project != project || phase != "host-ready" {
		return ErrConflict
	}
	var branch, policy, registry string
	err = tx.QueryRowContext(ctx, `SELECT branch,policy_sha256,registry_state FROM sessions WHERE uuid=?`, sessionID).Scan(&branch, &policy, &registry)
	if err != nil {
		return err
	}
	if branch != ev.Branch || policy != ev.PolicySHA256 || registry != "established" {
		return ErrConflict
	}
	var currentPolicy string
	if err = tx.QueryRowContext(ctx, `SELECT policy_sha256 FROM projects WHERE path=? AND registry_state='active'`, project).Scan(&currentPolicy); err != nil || currentPolicy != ev.PolicySHA256 {
		return ErrConflict
	}
	var credential string
	var active int
	err = tx.QueryRowContext(ctx, `SELECT fingerprint,active FROM git_principals WHERE session_uuid=? AND role='session' ORDER BY active DESC,rowid DESC LIMIT 1`, sessionID).Scan(&credential, &active)
	if err != nil || credential != ev.CredentialFingerprint || active != 1 {
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

func (s *Store) UnfinishedRepairs(ctx context.Context) ([]Operation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM operations WHERE kind='session.repair' AND status IN ('running','blocked','unknown') ORDER BY created_at`)
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

// AcceptedRepairImage is the last completed same-UUID repair's immutable
// image provenance. A pending repair never changes the current runtime image.
func (s *Store) AcceptedRepairImage(ctx context.Context, uuid string) (RepairEvidence, bool, error) {
	if !validUUID(uuid) {
		return RepairEvidence{}, false, ErrInvalid
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT evidence_json FROM operations WHERE session_uuid=? AND kind='session.repair' AND status='completed' AND phase='completed' ORDER BY rowid DESC LIMIT 1`, uuid).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return RepairEvidence{}, false, nil
	}
	if err != nil {
		return RepairEvidence{}, false, err
	}
	var ev RepairEvidence
	if json.Unmarshal(raw, &ev) != nil || !validRepairEvidence(ev) || !validFingerprint(ev.ImageFingerprint) {
		return RepairEvidence{}, false, ErrConflict
	}
	return ev, true, nil
}
