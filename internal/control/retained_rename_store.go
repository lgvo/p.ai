package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

type RetainedRenameRequest struct {
	Key            string `json:"key"`
	Project        string `json:"project"`
	OldBranch      string `json:"old_branch"`
	NewBranch      string `json:"new_branch"`
	ExpectedOldTip string `json:"expected_old_tip"`
}

func ValidRetainedRenameRequest(r RetainedRenameRequest) bool {
	return api.ProjectRetainedRenameRequest(r).Validate() == nil
}

// BeginRetainedRename binds two unassigned names to one immutable, inspected
// source tip. The callback observes Git while receive-pack is excluded.
func (s *Store) BeginRetainedRename(ctx context.Context, r RetainedRenameRequest, observe func(context.Context) error) (api.Operation, error) {
	if !ValidRetainedRenameRequest(r) || observe == nil {
		return api.Operation{}, ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return api.Operation{}, err
	}
	defer s.gitAuthority.Unlock()
	raw, _ := json.Marshal(r)
	if replay, err := keyRecordsRequest(ctx, s.db, r.Key, KindRetainedRename, raw); err != nil {
		return api.Operation{}, err
	} else if replay {
		return s.GetOperationByKey(ctx, r.Key)
	}
	if err := observe(ctx); err != nil {
		return api.Operation{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return api.Operation{}, err
	}
	defer tx.Rollback()
	var active int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE path=? AND registry_state='active'`, r.Project).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Operation{}, ErrNotFound
	}
	if err != nil {
		return api.Operation{}, err
	}
	for _, branch := range []string{r.OldBranch, r.NewBranch} {
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE project_path=? AND branch=?`, r.Project, branch).Scan(&active)
		if err == nil {
			return api.Operation{}, ErrConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return api.Operation{}, err
		}
	}
	id, err := newUUID()
	if err != nil {
		return api.Operation{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rec := operationRecord{ID: id, Key: r.Key, Kind: KindRetainedRename, Project: r.Project, Request: raw,
		Intent: api.OperationIntent{Project: r.Project, Branch: r.OldBranch, NewBranch: r.NewBranch}, Phase: "reserved", Evidence: raw, Now: now}
	err = insertOperation(ctx, tx, rec)
	if err != nil {
		return api.Operation{}, classifyWrite(err)
	}
	for _, branch := range []string{r.OldBranch, r.NewBranch} {
		if _, err = tx.ExecContext(ctx, `INSERT INTO git_ref_guards(project_path,branch,operation_id) VALUES(?,?,?)`, r.Project, branch, id); err != nil {
			return api.Operation{}, classifyWrite(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return api.Operation{}, err
	}
	return rec.operation(), nil
}

func (s *Store) retainedRenameGuards(ctx context.Context, tx *sql.Tx, id string, r RetainedRenameRequest) error {
	for _, branch := range []string{r.OldBranch, r.NewBranch} {
		var owner string
		if err := tx.QueryRowContext(ctx, `SELECT operation_id FROM git_ref_guards WHERE project_path=? AND branch=?`, r.Project, branch).Scan(&owner); err != nil || owner != id {
			return ErrConflict
		}
		var occupied int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE project_path=? AND branch=?`, r.Project, branch).Scan(&occupied)
		if err == nil {
			return ErrConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}

// TransitionRetainedRename keeps the Git observation, assignment check and
// durable phase transition behind the same authority lock.
func (s *Store) TransitionRetainedRename(ctx context.Context, id, from, to string, committed bool, verify func(context.Context, RetainedRenameRequest) error) error {
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
	var r RetainedRenameRequest
	if op.Kind != "project.retained.rename" || op.Status != "running" || op.Phase != from || json.Unmarshal(op.Request, &r) != nil || !ValidRetainedRenameRequest(r) || op.Project != r.Project {
		return ErrConflict
	}
	if err = verify(ctx, r); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.retainedRenameGuards(ctx, tx, id, r); err != nil {
		return err
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE path=? AND registry_state='active'`, r.Project).Scan(&active); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		}
		return err
	}
	if to == "completed" {
		for _, branch := range []string{r.OldBranch, r.NewBranch} {
			if _, err = tx.ExecContext(ctx, `DELETE FROM git_ref_guards WHERE project_path=? AND branch=? AND operation_id=?`, r.Project, branch, id); err != nil {
				return err
			}
		}
	}
	status := "running"
	if to == "completed" {
		status = "completed"
	}
	result, err := tx.ExecContext(ctx, `UPDATE operations SET status=?,phase=?,committed=?,updated_at=? WHERE id=? AND phase=? AND status='running'`, status, to, committed, time.Now().UTC().Format(time.RFC3339Nano), id, from)
	if err != nil {
		return classifyWrite(err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrConflict
	}
	return tx.Commit()
}

func (s *Store) FailRetainedRenamePreEffect(ctx context.Context, id string) error {
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	op, err := s.GetOperation(ctx, id)
	if err != nil {
		return err
	}
	var r RetainedRenameRequest
	if op.Kind != "project.retained.rename" || op.Status != "running" || op.Phase != "reserved" || op.Committed || json.Unmarshal(op.Request, &r) != nil {
		return ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.retainedRenameGuards(ctx, tx, id, r); err != nil {
		return err
	}
	for _, branch := range []string{r.OldBranch, r.NewBranch} {
		if _, err = tx.ExecContext(ctx, `DELETE FROM git_ref_guards WHERE project_path=? AND branch=? AND operation_id=?`, r.Project, branch, id); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE operations SET status='failed',phase='stale',diagnostic='retained branch rename input changed',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}
