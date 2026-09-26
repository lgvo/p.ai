package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/plugin"
)

// PublicationRequest binds a confirmation to one exact P tip and one origin head.
// Source is a session UUID or a retained branch name, according to Kind.
type PublicationRequest struct {
	ExpectedOriginURL string `json:"expected_origin_url"`
	Key               string `json:"key"`
	Project           string `json:"project"`
	Kind              string `json:"kind"`
	Source            string `json:"source"`
	SourceOID         string `json:"source_oid"`
	DestinationRef    string `json:"destination_ref"`
}

type PublicationPreview struct {
	Project        string `json:"project"`
	URL            string `json:"url"`
	SourceKind     string `json:"source_kind"`
	Source         string `json:"source"`
	SourceRef      string `json:"source_ref"`
	SourceOID      string `json:"source_oid"`
	DestinationRef string `json:"destination_ref"`
	DestinationOID string `json:"destination_oid,omitempty"`
	Relation       string `json:"relation"`
	// Runtime workspace divergence is not currently observable here.
	WorkspaceStatus string `json:"workspace_status"`
}

type PublicationResult struct {
	Preview PublicationPreview `json:"preview"`
	Status  string             `json:"status"`
}

func validPublicationRequest(r PublicationRequest) bool {
	if r.ExpectedOriginURL == "" || len(r.ExpectedOriginURL) > 2048 || !validProject(r.Project) || !validOID(r.SourceOID) || !strings.HasPrefix(r.DestinationRef, "refs/heads/") || !plugin.ValidGitOriginRef(r.DestinationRef) {
		return false
	}
	if r.Kind == "session" {
		return len(r.Source) == 36
	}
	return r.Kind == "retained" && validBranch(r.Source)
}

// PublicationSource checks registry assignment, including unfinished lifecycle
// operations. The caller holds the project's origin scope while it checks Git.
func (s *Store) PublicationSource(ctx context.Context, r PublicationRequest) (string, error) {
	if !validPublicationRequest(r) {
		return "", ErrInvalid
	}
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT registry_state FROM projects WHERE path=?`, r.Project).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if state != "active" {
		return "", ErrConflict
	}
	if r.Kind == "session" {
		var branch, registry string
		err = s.db.QueryRowContext(ctx, `SELECT branch,registry_state FROM sessions WHERE uuid=? AND project_path=?`, r.Source, r.Project).Scan(&branch, &registry)
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		if err != nil {
			return "", err
		}
		if registry != "established" {
			return "", ErrConflict
		}
		var busy int
		err = s.db.QueryRowContext(ctx, `SELECT 1 FROM operations WHERE session_uuid=? AND status IN ('running','blocked','unknown') LIMIT 1`, r.Source).Scan(&busy)
		if err == nil {
			return "", ErrConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		err = s.db.QueryRowContext(ctx, `SELECT 1 FROM git_ref_guards WHERE project_path=? AND branch=?`, r.Project, branch).Scan(&busy)
		if err == nil {
			return "", ErrConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		return "refs/heads/" + branch, nil
	}
	var assigned int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE project_path=? AND branch=? LIMIT 1`, r.Project, r.Source).Scan(&assigned)
	if err == nil {
		return "", ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM git_ref_guards WHERE project_path=? AND branch=?`, r.Project, r.Source).Scan(&assigned)
	if err == nil {
		return "", ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	return "refs/heads/" + r.Source, nil
}

// PublicationRecord returns a stable answer for a repeated key. An attempted
// push without a committed answer is unknown; it must never be sent again.
func (s *Store) PublicationRecord(ctx context.Context, r PublicationRequest) (PublicationResult, string, error) {
	var project, originURL, kind, source, oid, dest, status, raw string
	err := s.db.QueryRowContext(ctx, `SELECT project_path,expected_origin_url,source_kind,source,source_oid,destination_ref,status,result_json FROM publication_requests WHERE idempotency_key=?`, r.Key).Scan(&project, &originURL, &kind, &source, &oid, &dest, &status, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return PublicationResult{}, "", nil
	}
	if err != nil {
		return PublicationResult{}, "", err
	}
	if originURL != r.ExpectedOriginURL || project != r.Project || kind != r.Kind || source != r.Source || oid != r.SourceOID || dest != r.DestinationRef {
		return PublicationResult{}, "", ErrConflict
	}
	var result PublicationResult
	if raw != "" {
		if err = json.Unmarshal([]byte(raw), &result); err != nil {
			return PublicationResult{}, "", err
		}
	}
	if status == "attempted" {
		result.Status = "outcome_unknown"
	}
	return result, status, nil
}

func (s *Store) PreparePublication(ctx context.Context, r PublicationRequest) (PublicationResult, string, error) {
	if !validPublicationRequest(r) || r.Key == "" || len(r.Key) > 128 {
		return PublicationResult{}, "", ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PublicationResult{}, "", err
	}
	defer tx.Rollback()
	var existing int
	for _, table := range []string{"operations", "origin_requests"} {
		column := "idempotency_key"
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE `+column+`=?`, r.Key).Scan(&existing)
		if err == nil {
			return PublicationResult{}, "", ErrConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return PublicationResult{}, "", err
		}
	}
	var project, originURL, kind, source, oid, dest, status, raw string
	err = tx.QueryRowContext(ctx, `SELECT project_path,expected_origin_url,source_kind,source,source_oid,destination_ref,status,result_json FROM publication_requests WHERE idempotency_key=?`, r.Key).Scan(&project, &originURL, &kind, &source, &oid, &dest, &status, &raw)
	if err == nil {
		if originURL != r.ExpectedOriginURL || project != r.Project || kind != r.Kind || source != r.Source || oid != r.SourceOID || dest != r.DestinationRef {
			return PublicationResult{}, "", ErrConflict
		}
		var result PublicationResult
		if raw != "" {
			if err = json.Unmarshal([]byte(raw), &result); err != nil {
				return PublicationResult{}, "", err
			}
		}
		if status == "attempted" {
			result.Status = "outcome_unknown"
		}
		return result, status, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return PublicationResult{}, "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO publication_requests(idempotency_key,project_path,expected_origin_url,source_kind,source,source_oid,destination_ref,status,created_at) VALUES(?,?,?,?,?,?,?,'prepared',?)`, r.Key, r.Project, r.ExpectedOriginURL, r.Kind, r.Source, r.SourceOID, r.DestinationRef, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return PublicationResult{}, "", classifyWrite(err)
	}
	if err = tx.Commit(); err != nil {
		return PublicationResult{}, "", err
	}
	return PublicationResult{}, "prepared", nil
}

func (s *Store) UpdatePublication(ctx context.Context, key, from, to string, result PublicationResult) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	updated, err := s.db.ExecContext(ctx, `UPDATE publication_requests SET status=?,result_json=?,completed_at=? WHERE idempotency_key=? AND status=?`, to, string(raw), time.Now().UTC().Format(time.RFC3339Nano), key, from)
	if err != nil {
		return err
	}
	n, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

// RearmPublication is called only when the source plugin/native broker proves
// no push process started. A crash before this write remains conservative.
func (s *Store) RearmPublication(ctx context.Context, key string) error {
	updated, err := s.db.ExecContext(ctx, `UPDATE publication_requests SET status='prepared',result_json='',completed_at='' WHERE idempotency_key=? AND status='attempted'`, key)
	if err != nil {
		return err
	}
	n, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
