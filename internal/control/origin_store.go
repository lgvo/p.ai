package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lgvo/p.ai/internal/plugin"
)

// OriginState is the current association and its last completed observation.
// Refs are returned only by the paged source method.
type OriginState struct {
	Project    string `json:"project"`
	URL        string `json:"url,omitempty"`
	Status     string `json:"status"`
	ObservedAt string `json:"observed_at,omitempty"`
	RefCount   int    `json:"ref_count"`
	Diagnostic string `json:"diagnostic,omitempty"`
}

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

// CompletedOriginChange reads a committed response without acquiring Git or
// plugin authority. A repeated key never reevaluates current origin policy.
func (s *Store) CompletedOriginChange(ctx context.Context, c OriginChange) (OriginState, bool, error) {
	if !validOriginChange(c) {
		return OriginState{}, false, ErrInvalid
	}
	if err := s.CheckLifecycleKeyConflict(ctx, c.Key); err != nil {
		return OriginState{}, false, err
	}
	var project, kind, old, next, status, resultJSON string
	err := s.db.QueryRowContext(ctx, `SELECT project_path,kind,expected_url,proposed_url,status,result_json FROM origin_requests WHERE idempotency_key=?`, c.Key).Scan(&project, &kind, &old, &next, &status, &resultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return OriginState{}, false, nil
	}
	if err != nil {
		return OriginState{}, false, err
	}
	if project != c.Project || kind != c.Kind || old != c.ExpectedURL || next != c.ProposedURL {
		return OriginState{}, false, ErrConflict
	}
	if status != "completed" {
		return OriginState{}, false, nil
	}
	var result OriginState
	if err = json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return OriginState{}, false, err
	}
	return result, true, nil
}

// PrepareOriginChange durably binds an idempotency key before external contact.
// Completed keys replay without invoking the origin transport.
func (s *Store) PrepareOriginChange(ctx context.Context, c OriginChange) (bool, OriginState, error) {
	if !validOriginChange(c) {
		return false, OriginState{}, ErrInvalid
	}
	if err := s.CheckLifecycleKeyConflict(ctx, c.Key); err != nil {
		return false, OriginState{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, OriginState{}, err
	}
	defer tx.Rollback()
	var project, kind, old, next, status, resultJSON string
	err = tx.QueryRowContext(ctx, `SELECT project_path,kind,expected_url,proposed_url,status,result_json FROM origin_requests WHERE idempotency_key=?`, c.Key).Scan(&project, &kind, &old, &next, &status, &resultJSON)
	if err == nil {
		if project != c.Project || kind != c.Kind || old != c.ExpectedURL || next != c.ProposedURL {
			return false, OriginState{}, ErrConflict
		}
		if status == "completed" {
			var result OriginState
			if err = json.Unmarshal([]byte(resultJSON), &result); err != nil {
				return false, OriginState{}, err
			}
			return true, result, nil
		}
		return false, OriginState{}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, OriginState{}, err
	}
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT registry_state FROM projects WHERE path=?`, c.Project).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return false, OriginState{}, ErrNotFound
	} else if err != nil {
		return false, OriginState{}, err
	}
	if state != "active" {
		return false, OriginState{}, ErrConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO origin_requests(idempotency_key,project_path,kind,expected_url,proposed_url,status,created_at) VALUES(?,?,?,?,?,'prepared',?)`, c.Key, c.Project, c.Kind, c.ExpectedURL, c.ProposedURL, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, OriginState{}, classifyWrite(err)
	}
	return false, OriginState{}, tx.Commit()
}

func (s *Store) CheckLifecycleKeyConflict(ctx context.Context, key string) error {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM publication_requests WHERE idempotency_key=?`, key).Scan(&exists)
	if err == nil {
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM operations WHERE idempotency_key=?`, key).Scan(&exists)
	if err == nil {
		return ErrConflict
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func (s *Store) CommitOriginChange(ctx context.Context, c OriginChange, refs []plugin.GitOriginRef) error {
	if !validOriginChange(c) || c.Kind == "remove" && len(refs) != 0 {
		return ErrInvalid
	}
	encoded, err := encodeOriginRefs(refs)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var project, kind, old, next, status string
	if err = tx.QueryRowContext(ctx, `SELECT project_path,kind,expected_url,proposed_url,status FROM origin_requests WHERE idempotency_key=?`, c.Key).Scan(&project, &kind, &old, &next, &status); err != nil {
		return err
	}
	if project != c.Project || kind != c.Kind || old != c.ExpectedURL || next != c.ProposedURL {
		return ErrConflict
	}
	if status == "completed" {
		return nil
	}
	var registry string
	if err = tx.QueryRowContext(ctx, `SELECT registry_state FROM projects WHERE path=?`, c.Project).Scan(&registry); err != nil {
		return err
	}
	if registry != "active" {
		return ErrConflict
	}
	var current string
	err = tx.QueryRowContext(ctx, `SELECT url FROM project_origins WHERE project_path=?`, c.Project).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if current != c.ExpectedURL {
		return ErrConflict
	}
	completedAt := time.Now().UTC().Format(time.RFC3339Nano)
	result := OriginState{Project: c.Project, Status: "local-only"}
	if c.Kind == "remove" {
		_, err = tx.ExecContext(ctx, `DELETE FROM project_origins WHERE project_path=?`, c.Project)
	} else {
		result = OriginState{Project: c.Project, URL: c.ProposedURL, Status: "fresh", ObservedAt: completedAt, RefCount: len(refs)}
		_, err = tx.ExecContext(ctx, `INSERT INTO project_origins(project_path,url,current_status,observed_at,refs_json,diagnostic) VALUES(?,?,'fresh',?,?,'') ON CONFLICT(project_path) DO UPDATE SET url=excluded.url,current_status='fresh',observed_at=excluded.observed_at,refs_json=excluded.refs_json,diagnostic=''`, c.Project, c.ProposedURL, completedAt, string(encoded))
	}
	if err != nil {
		return err
	}
	response, e := json.Marshal(result)
	if e != nil {
		return e
	}
	_, err = tx.ExecContext(ctx, `UPDATE origin_requests SET status='completed',completed_at=?,result_json=? WHERE idempotency_key=?`, completedAt, string(response), c.Key)
	if err != nil {
		return err
	}
	return tx.Commit()
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

func (s *Store) Origin(ctx context.Context, project string) (OriginState, []plugin.GitOriginRef, error) {
	if !validProject(project) {
		return OriginState{}, nil, ErrInvalid
	}
	var registry string
	err := s.db.QueryRowContext(ctx, `SELECT registry_state FROM projects WHERE path=?`, project).Scan(&registry)
	if errors.Is(err, sql.ErrNoRows) {
		return OriginState{}, nil, ErrNotFound
	}
	if err != nil {
		return OriginState{}, nil, err
	}
	if registry != "active" {
		return OriginState{}, nil, ErrConflict
	}
	state := OriginState{Project: project, Status: "local-only"}
	var raw string
	err = s.db.QueryRowContext(ctx, `SELECT url,current_status,observed_at,refs_json,diagnostic FROM project_origins WHERE project_path=?`, project).Scan(&state.URL, &state.Status, &state.ObservedAt, &raw, &state.Diagnostic)
	if errors.Is(err, sql.ErrNoRows) {
		return state, []plugin.GitOriginRef{}, nil
	}
	if err != nil {
		return OriginState{}, nil, err
	}
	var refs []plugin.GitOriginRef
	if err = json.Unmarshal([]byte(raw), &refs); err != nil {
		return OriginState{}, nil, fmt.Errorf("stored origin observation: %w", err)
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
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if registry != "active" || current != url {
		return ErrConflict
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
