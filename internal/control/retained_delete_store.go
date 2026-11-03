package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

type RetainedDeleteRequest struct {
	Key         string `json:"key"`
	Project     string `json:"project"`
	Branch      string `json:"branch"`
	TokenSHA256 string `json:"token_sha256"`
}

type RetainedDeleteEvidence struct {
	Project          string `json:"project"`
	Branch           string `json:"branch"`
	Tip              string `json:"tip"`
	ReviewSHA256     string `json:"review_sha256"`
	InstanceUUID     string `json:"instance_uuid"`
	PreviewExpiresAt string `json:"preview_expires_at"`
}

func validRetainedDeleteRequest(r RetainedDeleteRequest) bool {
	return len(r.Key) > 0 && len(r.Key) <= 128 && validProject(r.Project) && validBranch(r.Branch) && validFingerprint(r.TokenSHA256)
}
func validRetainedDeleteEvidence(e RetainedDeleteEvidence) bool {
	return validProject(e.Project) && validBranch(e.Branch) && validOID(e.Tip) && strings.Trim(e.Tip, "0") != "" && validFingerprint(e.ReviewSHA256) && validUUID(e.InstanceUUID)
}

// BeginRetainedDelete reserves the one unassigned ref only after a fresh full
// loss review under gitAuthority. The callback must not re-enter Store locks.
func (s *Store) BeginRetainedDelete(ctx context.Context, r RetainedDeleteRequest, e RetainedDeleteEvidence, verify func(context.Context, RetainedDeleteEvidence) error) (api.Operation, error) {
	if !validRetainedDeleteRequest(r) || !validRetainedDeleteEvidence(e) || r.Project != e.Project || r.Branch != e.Branch || verify == nil {
		return api.Operation{}, ErrInvalid
	}
	expires, err := time.Parse(time.RFC3339Nano, e.PreviewExpiresAt)
	if err != nil {
		return api.Operation{}, ErrInvalid
	}
	if err = s.lockGitAuthority(ctx); err != nil {
		return api.Operation{}, err
	}
	defer s.gitAuthority.Unlock()
	raw, _ := json.Marshal(r)
	if replay, err := keyRecordsRequest(ctx, s.db, r.Key, KindRetainedDelete, raw); err != nil {
		return api.Operation{}, err
	} else if replay {
		return s.GetOperationByKey(ctx, r.Key)
	}
	if !time.Now().Before(expires) {
		return api.Operation{}, ErrConflict
	}
	if err = verify(ctx, e); err != nil {
		return api.Operation{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return api.Operation{}, err
	}
	defer tx.Rollback()
	var row int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE path=? AND registry_state='active'`, r.Project).Scan(&row)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Operation{}, ErrNotFound
	}
	if err != nil {
		return api.Operation{}, err
	}
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE project_path=? AND branch=?`, r.Project, r.Branch).Scan(&row)
	if err == nil {
		return api.Operation{}, ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return api.Operation{}, err
	}
	if !time.Now().Before(expires) {
		return api.Operation{}, ErrConflict
	}
	id, err := newUUID()
	if err != nil {
		return api.Operation{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	evRaw, _ := json.Marshal(e)
	rec := operationRecord{ID: id, Key: r.Key, Kind: KindRetainedDelete, Project: r.Project, Request: raw,
		Intent: api.OperationIntent{Project: r.Project, Branch: r.Branch}, Phase: "guarded", Evidence: evRaw, Now: now}
	err = insertOperation(ctx, tx, rec)
	if err != nil {
		return api.Operation{}, classifyWrite(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO git_ref_guards(project_path,branch,operation_id) VALUES(?,?,?)`, r.Project, r.Branch, id); err != nil {
		return api.Operation{}, classifyWrite(err)
	}
	if err = tx.Commit(); err != nil {
		return api.Operation{}, err
	}
	return rec.operation(), nil
}

// IssueRetainedDelete holds the ref authority lock through final review, the
// durable effect marker, and the one selected Git CAS. A failed verification
// is definitely pre-effect; an effect error leaves a guarded issued intent.
func (s *Store) IssueRetainedDelete(ctx context.Context, id string, verify func(context.Context, RetainedDeleteEvidence) error, effect func() error) error {
	if verify == nil || effect == nil {
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
	var e RetainedDeleteEvidence
	if op.Kind != "project.retained.delete" || op.Status != "running" || op.Phase != "guarded" || op.Committed || json.Unmarshal(op.Evidence, &e) != nil || !validRetainedDeleteEvidence(e) || op.Project != e.Project {
		return ErrConflict
	}
	if err = verify(ctx, e); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.retainedDeleteGuard(ctx, tx, id, e); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE operations SET phase='ref-delete-issued',committed=1,updated_at=? WHERE id=? AND status='running' AND phase='guarded'`, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return effect()
}

func (s *Store) retainedDeleteGuard(ctx context.Context, tx *sql.Tx, id string, e RetainedDeleteEvidence) error {
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT operation_id FROM git_ref_guards WHERE project_path=? AND branch=?`, e.Project, e.Branch).Scan(&owner); err != nil || owner != id {
		return ErrConflict
	}
	var row int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE project_path=? AND branch=?`, e.Project, e.Branch).Scan(&row)
	if err == nil {
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE path=? AND registry_state='active'`, e.Project).Scan(&row)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConflict
	}
	return err
}

func (s *Store) FailRetainedDeleteStale(ctx context.Context, id string) error {
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	op, err := s.GetOperation(ctx, id)
	if err != nil {
		return err
	}
	var e RetainedDeleteEvidence
	if op.Kind != "project.retained.delete" || op.Status != "running" || op.Phase != "guarded" || op.Committed || json.Unmarshal(op.Evidence, &e) != nil {
		return ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.retainedDeleteGuard(ctx, tx, id, e); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM git_ref_guards WHERE project_path=? AND branch=? AND operation_id=?`, e.Project, e.Branch, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET status='failed',phase='stale',diagnostic='retained branch delete review changed',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CompleteRetainedDelete(ctx context.Context, id string, verify func(context.Context, RetainedDeleteEvidence) error) error {
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
	var e RetainedDeleteEvidence
	if op.Kind != "project.retained.delete" || op.Status != "running" || op.Phase != "ref-delete-issued" || !op.Committed || json.Unmarshal(op.Evidence, &e) != nil {
		return ErrConflict
	}
	if err = verify(ctx, e); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.retainedDeleteGuard(ctx, tx, id, e); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM git_ref_guards WHERE project_path=? AND branch=? AND operation_id=?`, e.Project, e.Branch, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET status='completed',phase='completed',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	return tx.Commit()
}
