package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type RetainedRenameRequest struct {
	Key            string `json:"key"`
	Project        string `json:"project"`
	OldBranch      string `json:"old_branch"`
	NewBranch      string `json:"new_branch"`
	ExpectedOldTip string `json:"expected_old_tip"`
}

func ValidRetainedRenameRequest(r RetainedRenameRequest) bool {
	return len(r.Key) > 0 && len(r.Key) <= 128 && validProject(r.Project) && validBranch(r.OldBranch) &&
		validBranch(r.NewBranch) && r.OldBranch != r.NewBranch && validOID(r.ExpectedOldTip) &&
		r.ExpectedOldTip != "0000000000000000000000000000000000000000"
}

// BeginRetainedRename binds two unassigned names to one immutable, inspected
// source tip. The callback observes Git while receive-pack is excluded.
func (s *Store) BeginRetainedRename(ctx context.Context, r RetainedRenameRequest, observe func(context.Context) error) (Operation, error) {
	if !ValidRetainedRenameRequest(r) || observe == nil {
		return Operation{}, ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return Operation{}, err
	}
	defer s.gitAuthority.Unlock()
	raw, _ := json.Marshal(r)
	var kind, hash string
	err := s.db.QueryRowContext(ctx, `SELECT kind,request_sha256 FROM operations WHERE idempotency_key=?`, r.Key).Scan(&kind, &hash)
	if err == nil {
		if kind != "project.retained.rename" || hash != digest(raw) {
			return Operation{}, ErrConflict
		}
		return s.GetOperationByKey(ctx, r.Key)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, err
	}
	if err = observe(ctx); err != nil {
		return Operation{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback()
	var active int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE path=? AND registry_state='active'`, r.Project).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, err
	}
	for _, branch := range []string{r.OldBranch, r.NewBranch} {
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE project_path=? AND branch=?`, r.Project, branch).Scan(&active)
		if err == nil {
			return Operation{}, ErrConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Operation{}, err
		}
	}
	id, err := newUUID()
	if err != nil {
		return Operation{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,request_json,request_sha256,status,phase,committed,evidence_json,created_at,updated_at)
	 VALUES(?,?,?,?,?,?,'running','reserved',0,?,?,?)`, id, r.Key, "project.retained.rename", r.Project, string(raw), digest(raw), string(raw), now, now)
	if err != nil {
		return Operation{}, classifyWrite(err)
	}
	for _, branch := range []string{r.OldBranch, r.NewBranch} {
		if _, err = tx.ExecContext(ctx, `INSERT INTO git_ref_guards(project_path,branch,operation_id) VALUES(?,?,?)`, r.Project, branch, id); err != nil {
			return Operation{}, classifyWrite(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return Operation{}, err
	}
	return Operation{ID: id, Key: r.Key, Kind: "project.retained.rename", Project: r.Project, Request: raw, Status: "running", Phase: "reserved", Evidence: raw, CreatedAt: now, UpdatedAt: now}, nil
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

func (s *Store) UnfinishedRetainedRenames(ctx context.Context) ([]Operation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM operations WHERE kind='project.retained.rename' AND status IN ('running','blocked','unknown') ORDER BY rowid`)
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
