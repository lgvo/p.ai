package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"
)

type projectDeleteQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// ProjectDeleteSnapshot binds the complete bounded registry resource set.
// Settled workspace inspections are read evidence, not project authority.
func (s *Store) ProjectDeleteSnapshot(ctx context.Context, project string) ([]Session, string, error) {
	return projectDeleteSnapshot(ctx, s.db, project)
}
func projectDeleteSnapshot(ctx context.Context, q projectDeleteQuerier, project string) ([]Session, string, error) {
	if !validProject(project) {
		return nil, "", ErrInvalid
	}
	var policy, sha, registry, origin string
	if err := q.QueryRowContext(ctx, `SELECT policy_json,policy_sha256,registry_state,COALESCE((SELECT url FROM project_origins WHERE project_path=path),'') FROM projects WHERE path=?`, project).Scan(&policy, &sha, &registry, &origin); err != nil {
		return nil, "", err
	}
	if registry != "active" {
		return nil, "", ErrConflict
	}
	var count int
	for _, query := range []string{
		`SELECT count(*) FROM operations WHERE project_path=? AND status IN ('running','blocked','unknown')`,
		`SELECT count(*) FROM origin_requests WHERE project_path=? AND status!='completed'`,
		`SELECT count(*) FROM publication_requests WHERE project_path=? AND status!='completed'`,
		`SELECT count(*) FROM git_ref_guards WHERE project_path=?`,
	} {
		if err := q.QueryRowContext(ctx, query, project).Scan(&count); err != nil {
			return nil, "", err
		}
		if count != 0 {
			return nil, "", fmt.Errorf("%w: unfinished project authority; complete existing operation or investigate its exact identity", ErrConflict)
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT uuid,project_path,branch,registry_state,policy_json,policy_sha256 FROM sessions WHERE project_path=? ORDER BY uuid`, project)
	if err != nil {
		return nil, "", err
	}
	sessions := []Session{}
	for rows.Next() {
		var s Session
		var raw string
		if err = rows.Scan(&s.UUID, &s.Project, &s.Branch, &s.Registry, &raw, &s.PolicySHA256); err != nil {
			break
		}
		s.Policy = json.RawMessage(raw)
		sessions = append(sessions, s)
		if s.Registry != "established" || len(sessions) > 4 {
			err = fmt.Errorf("%w: use supported failed-creation cleanup/removal or manual investigation first", ErrConflict)
			break
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		return nil, "", errors.Join(err, rowErr)
	}
	// Include every historical authority proof, including repairs and retired
	// principals; a new settled mutation still invalidates an old report.
	facts := []string{policy, sha, origin}
	for _, query := range []string{
		`SELECT id||':'||kind||':'||request_sha256||':'||status||':'||phase||':'||COALESCE(evidence_json,'') FROM operations WHERE project_path=? AND kind NOT IN ('workspace.inspect','workspace.loss.inspect') ORDER BY id`,
		`SELECT fingerprint||':'||role||':'||COALESCE(session_uuid,'')||':'||active FROM git_principals WHERE project_path=? ORDER BY fingerprint`,
		`SELECT environment_key||':'||fingerprint||':'||properties_json||':'||created_at||':'||last_used_at||':'||logical_size FROM environment_images WHERE project_path=? ORDER BY environment_key`,
	} {
		rows, err = q.QueryContext(ctx, query, project)
		if err != nil {
			return nil, "", err
		}
		n := 0
		for rows.Next() {
			var v string
			if err = rows.Scan(&v); err != nil {
				break
			}
			facts = append(facts, v)
			n++
			if n > 1024 {
				err = ErrConflict
				break
			}
		}
		rowErr = rows.Err()
		rows.Close()
		if err != nil || rowErr != nil {
			return nil, "", errors.Join(err, rowErr)
		}
	}
	raw, _ := json.Marshal(struct {
		Sessions []Session
		Facts    []string
	}{sessions, facts})
	if len(raw) > 512<<10 {
		return nil, "", ErrConflict
	}
	return sessions, digest(raw), nil
}

func (s *Store) BeginProjectDelete(ctx context.Context, req ProjectDeleteRequest, ev ProjectDeleteEvidence, expires string, verify func(context.Context) error) (Operation, error) {
	if !validProject(req.Project) || req.Key == "" || len(req.Key) > 128 || !validFingerprint(req.TokenSHA256) || !validProjectDeleteEvidence(ev) || verify == nil {
		return Operation{}, ErrInvalid
	}
	expiry, e := time.Parse(time.RFC3339Nano, expires)
	if e != nil {
		return Operation{}, ErrInvalid
	}
	raw, _ := json.Marshal(req)
	proof, _ := json.Marshal(ev)
	if len(proof) > 24<<10 {
		return Operation{}, ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return Operation{}, err
	}
	defer s.gitAuthority.Unlock()
	prior, replayErr := s.GetOperationByKey(ctx, req.Key)
	if replayErr == nil {
		if prior.Kind != "project.delete" || digest(prior.Request) != digest(raw) {
			return Operation{}, ErrConflict
		}
		return prior, nil
	}
	if !errors.Is(replayErr, ErrNotFound) {
		return Operation{}, replayErr
	}
	if err := verify(ctx); err != nil {
		return Operation{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback()
	old, err := getOperationTx(ctx, tx, req.Key)
	if err == nil {
		if old.Kind != "project.delete" || digest(old.Request) != digest(raw) {
			return Operation{}, ErrConflict
		}
		return old, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, err
	}
	sessions, sha, err := projectDeleteSnapshot(ctx, tx, req.Project)
	if err != nil || sha != ev.StateSHA256 || len(sessions) != len(ev.Sessions) || !time.Now().Before(expiry) {
		return Operation{}, errors.Join(err, ErrConflict)
	}
	for i, s := range sessions {
		ps := ev.Sessions[i]
		if !reflect.DeepEqual(s, ps.Session) || ps.Local.Completed || s.Project != req.Project {
			return Operation{}, ErrConflict
		}
		var matching, total int
		if tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(CASE WHEN fingerprint=? AND active=1 AND project_path=? AND role='session' THEN 1 ELSE 0 END),0) FROM git_principals WHERE session_uuid=?`, ps.Local.KeyFingerprint, req.Project, s.UUID).Scan(&total, &matching) != nil || total != 1 || matching != 1 {
			return Operation{}, ErrConflict
		}
		creator, e := operationByIDTx(ctx, tx, ps.Local.OldOperationID)
		if e != nil || creator.SessionUUID != s.UUID || creator.Project != req.Project || creator.Kind != "session.create" && creator.Kind != "project.create" || creator.Status != "completed" {
			return Operation{}, ErrConflict
		}
		if ps.Runtime.Condition == "present" {
			proof, e := operationByIDTx(ctx, tx, ps.Runtime.LossOperationID)
			var inspected WorkspaceInspectEvidence
			var result struct {
				Fingerprint string `json:"fingerprint"`
			}
			finished, tErr := time.Parse(time.RFC3339Nano, proof.UpdatedAt)
			if e != nil || tErr != nil || time.Since(finished) < 0 || time.Since(finished) > 2*time.Minute || proof.Kind != "workspace.loss.inspect" || proof.Status != "completed" || proof.Phase != "inspected" || proof.SessionUUID != s.UUID || proof.Project != req.Project || proof.UpdatedAt != ps.Runtime.ObservedAt || json.Unmarshal(proof.Evidence, &inspected) != nil || inspected.CreatorOperationID != "" || inspected.InstanceUUID != ev.InstanceUUID || inspected.BaseFingerprint != ev.BaseFingerprint || inspected.ImageFingerprint != ps.Runtime.ImageFingerprint || inspected.SourceIncusUUID != ps.Runtime.IncusUUID || inspected.SourceGeneration != ps.Runtime.Generation || inspected.OriginalStatus != "Stopped" || json.Unmarshal(inspected.Result, &result) != nil || result.Fingerprint != ps.Runtime.Fingerprint {
				return Operation{}, ErrConflict
			}
		}
	}
	for _, im := range ev.Images {
		if im.Image.Project != req.Project {
			return Operation{}, ErrConflict
		}
	}

	for _, r := range ev.Resources {
		if r.Status != "remaining" || r.DeleteIssued {
			return Operation{}, ErrInvalid
		}
	}
	id, err := newUUID()
	if err != nil {
		return Operation{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// Retire authority first inside this transaction; insert is the sole deletion
	// intent accepted for a deleting project. No resource effects precede commit.
	if _, err = tx.ExecContext(ctx, `UPDATE projects SET registry_state='deleting' WHERE path=?`, req.Project); err != nil {
		return Operation{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sessions SET registry_state='removing' WHERE project_path=?`, req.Project); err != nil {
		return Operation{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE git_principals SET active=0 WHERE project_path=?`, req.Project); err != nil {
		return Operation{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,request_json,request_sha256,status,phase,committed,evidence_json,created_at,updated_at) VALUES(?,?, 'project.delete',?,?,?,'running','ensure-absent',1,?,?,?)`, id, req.Key, req.Project, string(raw), digest(raw), string(proof), now, now); err != nil {
		return Operation{}, classifyWrite(err)
	}
	if err = tx.Commit(); err != nil {
		return Operation{}, err
	}
	return Operation{ID: id, Key: req.Key, Kind: "project.delete", Project: req.Project, Request: raw, Evidence: proof, Status: "running", Phase: "ensure-absent", Committed: true, CreatedAt: now, UpdatedAt: now}, nil
}

// UpdateProjectDelete changes only resource progress; exact accepted identities
// cannot drift and positive absence/delete-issued markers never rewind.
func (s *Store) UpdateProjectDelete(ctx context.Context, observed Operation, next ProjectDeleteEvidence, status string, diagnostic string) error {
	if !validProjectDeleteEvidence(next) || status != "running" && status != "blocked" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old, err := operationByIDTx(ctx, tx, observed.ID)
	var prev ProjectDeleteEvidence
	if err != nil || old.Kind != "project.delete" || old.Phase != "ensure-absent" || !old.Committed || old.Status != "running" && old.Status != "blocked" || digest(old.Evidence) != digest(observed.Evidence) || json.Unmarshal(old.Evidence, &prev) != nil || projectDeleteIdentities(prev) != projectDeleteIdentities(next) {
		return ErrConflict
	}
	for i, r := range prev.Resources {
		n := next.Resources[i]
		if r.Kind != n.Kind || r.ID != n.ID || r.DeleteIssued && !n.DeleteIssued || projectDeleteResourceAbsent(r) && r.Status != n.Status {
			return ErrConflict
		}
	}
	raw, _ := json.Marshal(next)
	if len(raw) > 24<<10 || len(diagnostic) > 900 {
		return ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET status=?,evidence_json=?,diagnostic=?,updated_at=? WHERE id=?`, status, string(raw), diagnostic, time.Now().UTC().Format(time.RFC3339Nano), old.ID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) UnfinishedProjectDeletes(ctx context.Context) ([]Operation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM operations WHERE kind='project.delete' AND status IN ('running','blocked','unknown') ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		return nil, errors.Join(err, rowErr)
	}
	ops := []Operation{}
	for _, id := range ids {
		op, e := s.GetOperation(ctx, id)
		if e != nil {
			return nil, e
		}
		ops = append(ops, op)
	}
	return ops, nil
}
func (s *Store) CompleteProjectDelete(ctx context.Context, observed Operation, verify func(context.Context) error) error {
	if verify == nil {
		return ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	if err := verify(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	op, err := operationByIDTx(ctx, tx, observed.ID)
	var ev ProjectDeleteEvidence
	if err != nil || op.Kind != "project.delete" || op.Status != "running" || op.Phase != "ensure-absent" || !op.Committed || digest(op.Evidence) != digest(observed.Evidence) || json.Unmarshal(op.Evidence, &ev) != nil || !validProjectDeleteEvidence(ev) {
		return ErrConflict
	}
	for _, r := range ev.Resources {
		if !projectDeleteResourceAbsent(r) {
			return ErrConflict
		}
	}
	var state string
	if tx.QueryRowContext(ctx, `SELECT registry_state FROM projects WHERE path=?`, op.Project).Scan(&state) != nil || state != "deleting" {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET phase='finalizing' WHERE id=?`, op.ID); err != nil {
		return err
	}
	if err = retireProjectRequestsTx(ctx, tx, op.Project, op.ID); err != nil {
		return err
	}
	for _, query := range []string{
		`DELETE FROM git_unborn_grants WHERE session_uuid IN (SELECT uuid FROM sessions WHERE project_path=?)`,
		`DELETE FROM git_ref_guards WHERE project_path=?`,
		`DELETE FROM git_principals WHERE project_path=?`,
		`DELETE FROM environment_images WHERE project_path=?`,
		`DELETE FROM sessions WHERE project_path=?`,
		`DELETE FROM origin_requests WHERE project_path=?`,
		`DELETE FROM publication_requests WHERE project_path=?`,
		`DELETE FROM projects WHERE path=?`,
	} {
		if _, err = tx.ExecContext(ctx, query, op.Project); err != nil {
			return err
		}
	}
	// Minimal retired key/hash receipts prevent old requests from becoming new.
	// Full historical requests, policies, origin URLs and loss evidence are erased.
	if _, err = tx.ExecContext(ctx, `DELETE FROM operations WHERE project_path=? AND id!=?`, op.Project, op.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operations SET status='completed',phase='completed',evidence_json=NULL,diagnostic='',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), op.ID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.statusMu.Lock()
	for _, ps := range ev.Sessions {
		delete(s.attachments, ps.Session.UUID)
		delete(s.statusRates, ps.Session.UUID)
		delete(s.attemptRates, ps.Session.UUID)
	}
	s.statusMu.Unlock()
	return nil
}

func (s *Store) ValidateProjectDeleteAuthority(ctx context.Context, op Operation, ev ProjectDeleteEvidence) error {
	current, err := s.GetOperation(ctx, op.ID)
	if err != nil || current.Kind != "project.delete" || current.Phase != "ensure-absent" || !current.Committed || current.Status != "running" && current.Status != "blocked" || digest(current.Evidence) != digest(op.Evidence) {
		return ErrConflict
	}
	var state string
	if s.db.QueryRowContext(ctx, `SELECT registry_state FROM projects WHERE path=?`, op.Project).Scan(&state) != nil || state != "deleting" {
		return ErrConflict
	}
	var count int
	if s.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE project_path=?`, op.Project).Scan(&count) != nil || count != len(ev.Sessions) {
		return ErrConflict
	}
	for _, ps := range ev.Sessions {
		actual, e := s.GetSession(ctx, ps.Session.UUID)
		expected := ps.Session
		expected.Registry = "removing"
		if e != nil || !reflect.DeepEqual(actual, expected) {
			return ErrConflict
		}
	}
	if s.db.QueryRowContext(ctx, `SELECT count(*) FROM git_principals WHERE project_path=? AND active=1`, op.Project).Scan(&count) != nil || count != 0 {
		return ErrConflict
	}
	return nil
}

func (s *Store) checkRetiredKey(ctx context.Context, key string) error {
	var found int
	e := s.db.QueryRowContext(ctx, `SELECT 1 FROM retired_lifecycle_requests WHERE idempotency_key=?`, key).Scan(&found)
	if e == nil {
		return fmt.Errorf("%w: project-deleted request key is retired; a new action requires a new key", ErrConflict)
	}
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	return e
}
func retireProjectRequestsTx(ctx context.Context, tx *sql.Tx, project, keep string) error {
	type receipt struct{ key, id, kind, sha string }
	receipts := []receipt{}
	rows, e := tx.QueryContext(ctx, `SELECT idempotency_key,id,kind,request_sha256 FROM operations WHERE project_path=? AND id!=?`, project, keep)
	if e != nil {
		return e
	}
	for rows.Next() {
		var r receipt
		if e = rows.Scan(&r.key, &r.id, &r.kind, &r.sha); e != nil {
			break
		}
		receipts = append(receipts, r)
	}
	rowErr := rows.Err()
	rows.Close()
	if e != nil || rowErr != nil {
		return errors.Join(e, rowErr)
	}

	for _, table := range []string{"origin_requests", "publication_requests"} {
		query := `SELECT idempotency_key,kind,expected_url,proposed_url FROM origin_requests WHERE project_path=?`
		if table == "publication_requests" {
			query = `SELECT idempotency_key,source_kind,expected_origin_url,source,source_oid,destination_ref FROM publication_requests WHERE project_path=?`
		}
		rows, e = tx.QueryContext(ctx, query, project)
		if e != nil {
			return e
		}
		for rows.Next() {
			var r receipt
			var raw []byte
			if table == "origin_requests" {
				req := OriginChange{Project: project}
				if e = rows.Scan(&req.Key, &req.Kind, &req.ExpectedURL, &req.ProposedURL); e != nil {
					break
				}
				r.key = req.Key
				r.kind = "project.origin." + req.Kind
				raw, _ = json.Marshal(req)
			} else {
				req := PublicationRequest{Project: project}
				if e = rows.Scan(&req.Key, &req.Kind, &req.ExpectedOriginURL, &req.Source, &req.SourceOID, &req.DestinationRef); e != nil {
					break
				}
				r.key = req.Key
				r.kind = "project.publication"
				raw, _ = json.Marshal(req)
			}
			r.sha = digest(raw)
			receipts = append(receipts, r)
		}
		rowErr = rows.Err()
		rows.Close()
		if e != nil || rowErr != nil {
			return errors.Join(e, rowErr)
		}
	}
	for _, r := range receipts {
		if _, e = tx.ExecContext(ctx, `INSERT INTO retired_lifecycle_requests(idempotency_key,operation_id,kind,request_sha256) VALUES(?,?,?,?)`, r.key, r.id, r.kind, r.sha); e != nil {
			return e
		}
	}
	return nil
}
