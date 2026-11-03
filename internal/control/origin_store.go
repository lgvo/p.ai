package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lgvo/p.ai/internal/api"
	"time"

	"github.com/lgvo/p.ai/internal/plugin"
)

type OriginChange struct {
	Key         string `json:"key"`
	Project     string `json:"project"`
	Kind        string `json:"kind"`
	ExpectedURL string `json:"expected_url"`
	ProposedURL string `json:"proposed_url"`
}

func validOriginChange(c OriginChange) bool {
	return len(c.Key) > 0 && len(c.Key) <= 128 && validProject(c.Project) &&
		len(c.ExpectedURL) <= 2048 && len(c.ProposedURL) <= 2048 &&
		(c.Kind == "remove" && c.ProposedURL == "" || c.Kind == "set" && c.ProposedURL != "")
}

// BeginOriginChange records origin change c as an operation of kind
// origin.change and returns it, after checking that c's project is active and
// that c.ExpectedURL still names its origin. An identical earlier request
// under c.Key returns its operation without the checks.
func (s *Store) BeginOriginChange(ctx context.Context, c OriginChange) (api.Operation, error) {
	if !validOriginChange(c) {
		return api.Operation{}, ErrInvalid
	}
	request, err := json.Marshal(c)
	if err != nil {
		return api.Operation{}, err
	}
	if prior, replayed, err := s.recorded(ctx, c.Key, KindOriginChange, request); err != nil || replayed {
		return prior, err
	}
	id, err := newUUID()
	if err != nil {
		return api.Operation{}, err
	}
	rec := operationRecord{ID: id, Key: c.Key, Kind: KindOriginChange, Project: c.Project, Request: request,
		Intent: api.OperationIntent{Project: c.Project, URL: c.ProposedURL}, Phase: "accepted", Now: time.Now().UTC().Format(time.RFC3339Nano)}
	return s.insertForProject(ctx, rec, func(q queryRower) error { return s.expectOrigin(ctx, q, c) })
}

// expectOrigin checks that c's project is active and that c.ExpectedURL, empty
// for none, is its origin's URL.
func (s *Store) expectOrigin(ctx context.Context, q queryRower, c OriginChange) error {
	var registry string
	err := q.QueryRowContext(ctx, `SELECT registry_state FROM projects WHERE path=?`, c.Project).Scan(&registry)
	if errors.Is(err, sql.ErrNoRows) {
		return NotFound(api.ReasonProject, api.TargetProject(c.Project))
	}
	if err != nil {
		return err
	}
	if registry != "active" {
		return Conflict(api.ReasonProjectDeleting, api.TargetProject(c.Project))
	}
	var current string
	err = q.QueryRowContext(ctx, `SELECT url FROM project_origins WHERE project_path=?`, c.Project).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if current != c.ExpectedURL {
		// expected_url no longer names the project's origin.
		return Stale(api.ReasonChanged)
	}
	return nil
}

// CompleteOriginChange records origin change op, with the refs its contact
// observed for a set, and the operation's end, in one transaction. An origin
// changed since the request answers precondition_failed and changes nothing.
func (s *Store) CompleteOriginChange(ctx context.Context, op api.Operation, refs []plugin.GitOriginRef) error {
	var c OriginChange
	if op.Kind != KindOriginChange || json.Unmarshal(op.Request, &c) != nil || !validOriginChange(c) || c.Kind == "remove" && len(refs) != 0 {
		return ErrInvalid
	}
	encoded, err := encodeOriginRefs(refs)
	if err != nil {
		return err
	}
	j, err := s.beginJournal(ctx)
	if err != nil {
		return err
	}
	defer j.Rollback()
	if status, err := j.status(ctx, op.ID); err != nil || status != "running" {
		return errors.Join(err, ErrConflict)
	}
	if err = s.expectOrigin(ctx, j, c); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if c.Kind == "remove" {
		_, err = j.ExecContext(ctx, `DELETE FROM project_origins WHERE project_path=?`, c.Project)
	} else {
		_, err = j.ExecContext(ctx, `INSERT INTO project_origins(project_path,url,current_status,observed_at,refs_json,diagnostic) VALUES(?,?,'fresh',?,?,'') ON CONFLICT(project_path) DO UPDATE SET url=excluded.url,current_status='fresh',observed_at=excluded.observed_at,refs_json=excluded.refs_json,diagnostic=''`, c.Project, c.ProposedURL, now, string(encoded))
	}
	if err != nil {
		return err
	}
	if _, err = j.update(ctx, op.ID, `UPDATE operations SET status='completed',phase='completed',reason='',diagnostic='',updated_at=? WHERE id=?`, now, op.ID); err != nil {
		return err
	}
	return j.commit(ctx)
}

func encodeOriginRefs(refs []plugin.GitOriginRef) ([]byte, error) {
	if len(refs) > 1024 {
		return nil, ErrInvalid
	}
	previous := ""
	for _, r := range refs {
		if !plugin.ValidGitOriginRef(r.Ref) || r.Ref <= previous || !validOID(r.OID) || !validOID(r.CommitOID) || len(r.OID) != len(r.CommitOID) {
			return nil, ErrInvalid
		}
		previous = r.Ref
	}
	return json.Marshal(refs)
}

func (s *Store) Origin(ctx context.Context, project string) (api.OriginState, []plugin.GitOriginRef, error) {
	if !validProject(project) {
		return api.OriginState{}, nil, Invalid("project")
	}
	var registry string
	err := s.db.QueryRowContext(ctx, `SELECT registry_state FROM projects WHERE path=?`, project).Scan(&registry)
	if errors.Is(err, sql.ErrNoRows) {
		return api.OriginState{}, nil, NotFound(api.ReasonProject, api.TargetProject(project))
	}
	if err != nil {
		return api.OriginState{}, nil, err
	}
	if registry != "active" {
		return api.OriginState{}, nil, Conflict(api.ReasonProjectDeleting, api.TargetProject(project))
	}
	state := api.OriginState{Project: project, Status: "local-only"}
	var raw string
	err = s.db.QueryRowContext(ctx, `SELECT url,current_status,observed_at,refs_json,diagnostic FROM project_origins WHERE project_path=?`, project).Scan(&state.URL, &state.Status, &state.ObservedAt, &raw, &state.Diagnostic)
	if errors.Is(err, sql.ErrNoRows) {
		return state, []plugin.GitOriginRef{}, nil
	}
	if err != nil {
		return api.OriginState{}, nil, err
	}
	var refs []plugin.GitOriginRef
	if err = json.Unmarshal([]byte(raw), &refs); err != nil {
		return api.OriginState{}, nil, fmt.Errorf("stored origin observation: %w", err)
	}
	state.RefCount = len(refs)
	return state, refs, nil
}

// StoreOriginRefresh records a completed observation or marks the current one
// unknown. Identity matching prevents an old failed refresh from staining a
// newly configured origin.
func (s *Store) StoreOriginRefresh(ctx context.Context, project, url string, refs []plugin.GitOriginRef, failure string) error {
	if !validProject(project) || url == "" || len(url) > 2048 {
		return ErrInvalid
	}
	if len(failure) > 256 {
		failure = failure[:256]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current, registry string
	err = tx.QueryRowContext(ctx, `SELECT p.registry_state,o.url FROM projects p JOIN project_origins o ON o.project_path=p.path WHERE p.path=?`, project).Scan(&registry, &current)
	if errors.Is(err, sql.ErrNoRows) {
		// The project or its origin is gone: the URL observed is no longer
		// the project's origin.
		return Stale(api.ReasonChanged)
	}
	if err != nil {
		return err
	}
	if registry != "active" {
		return Conflict(api.ReasonProjectDeleting, api.TargetProject(project))
	}
	if current != url {
		return Stale(api.ReasonChanged)
	}
	if failure != "" {
		_, err = tx.ExecContext(ctx, `UPDATE project_origins SET current_status='unknown',diagnostic=? WHERE project_path=?`, failure, project)
	} else {
		encoded, e := encodeOriginRefs(refs)
		if e != nil {
			return e
		}
		_, err = tx.ExecContext(ctx, `UPDATE project_origins SET current_status='fresh',observed_at=?,refs_json=?,diagnostic='' WHERE project_path=?`, time.Now().UTC().Format(time.RFC3339Nano), string(encoded), project)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
