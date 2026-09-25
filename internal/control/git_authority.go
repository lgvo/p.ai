package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"
)

var ErrGitDenied = errors.New("Git authority denied")
var gitFingerprintRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (s *Store) lockGitAuthority(ctx context.Context) error {
	for !s.gitAuthority.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := ctx.Err(); err != nil {
		s.gitAuthority.Unlock()
		return err
	}
	return nil
}

func (s *Store) IsGitPrincipalActive(ctx context.Context, fingerprint string) bool {
	if !gitFingerprintRE.MatchString(fingerprint) {
		return false
	}
	for !s.gitAuthority.TryRLock() {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer s.gitAuthority.RUnlock()
	if ctx.Err() != nil {
		return false
	}
	var active int
	err := s.db.QueryRowContext(ctx, `SELECT active FROM git_principals WHERE fingerprint=?`, fingerprint).Scan(&active)
	return err == nil && active == 1
}

// RegisterGitPrincipal records the fingerprint of an externally provisioned
// SSH public key. Private keys never enter the control store.
func (s *Store) RegisterGitPrincipal(ctx context.Context, fingerprint, role, project, sessionID string) error {
	if !gitFingerprintRE.MatchString(fingerprint) {
		return ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	if role == "host" {
		if project != "" || sessionID != "" {
			return ErrInvalid
		}
		_, err := s.db.ExecContext(ctx, `INSERT INTO git_principals(fingerprint,role) VALUES(?,'host')`, fingerprint)
		return classifyWrite(err)
	}
	if role != "session" || !validProject(project) || sessionID == "" {
		return ErrInvalid
	}
	var actual string
	err := s.db.QueryRowContext(ctx, `SELECT project_path FROM sessions WHERE uuid=? AND project_path=? AND registry_state IN ('creating','established')`, sessionID, project).Scan(&actual)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrGitDenied
	}
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO git_principals(fingerprint,role,project_path,session_uuid) VALUES(?,'session',?,?)`, fingerprint, project, sessionID)
	return classifyWrite(err)
}

// SessionGitPrincipal reports the one registered identity, including a revoked
// row. A missing private key after registration must never imply rotation.
func (s *Store) SessionGitPrincipal(ctx context.Context, sessionID string) (string, bool, error) {
	var fingerprint string
	err := s.db.QueryRowContext(ctx, `SELECT fingerprint FROM git_principals WHERE session_uuid=? AND role='session' ORDER BY active DESC,rowid DESC LIMIT 1`, sessionID).Scan(&fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return fingerprint, true, nil
}

func (s *Store) IsSessionGitPrincipalActive(ctx context.Context, sessionID string) (bool, error) {
	var active int
	err := s.db.QueryRowContext(ctx, `SELECT active FROM git_principals WHERE session_uuid=? AND role='session' ORDER BY active DESC,rowid DESC LIMIT 1`, sessionID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return active == 1, err
}

func (s *Store) EnsureSessionGitPrincipal(ctx context.Context, fingerprint, project, sessionID string) error {
	actual, registered, err := s.SessionGitPrincipal(ctx, sessionID)
	if err != nil {
		return err
	}
	if registered {
		if actual != fingerprint {
			return ErrGitDenied
		}
		active, e := s.IsSessionGitPrincipalActive(ctx, sessionID)
		if e != nil {
			return e
		}
		if !active {
			return ErrGitDenied
		}
		return nil
	}
	return s.RegisterGitPrincipal(ctx, fingerprint, "session", project, sessionID)
}

// EnsureHostGitPrincipal binds a stable host key to the read-only host role.
// A changed or revoked key is an authority conflict, not an implicit rotation.
func (s *Store) EnsureHostGitPrincipal(ctx context.Context, fingerprint string) error {
	if !gitFingerprintRE.MatchString(fingerprint) {
		return ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	var existing, role string
	var active int
	err := s.db.QueryRowContext(ctx, `SELECT fingerprint,role,active FROM git_principals WHERE role='host' OR fingerprint=?`, fingerprint).Scan(&existing, &role, &active)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = s.db.ExecContext(ctx, `INSERT INTO git_principals(fingerprint,role) VALUES(?,'host')`, fingerprint)
		return classifyWrite(err)
	}
	if err != nil {
		return err
	}
	if existing != fingerprint || role != "host" || active != 1 {
		return ErrGitDenied
	}
	return nil
}

// GitServerIdentity returns the pinned public fingerprint, when configured.
func (s *Store) GitServerIdentity(ctx context.Context) (string, bool, error) {
	var fingerprint string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='git_server_fingerprint'`).Scan(&fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !gitFingerprintRE.MatchString(fingerprint) {
		return "", false, ErrGitDenied
	}
	return fingerprint, true, nil
}

// EnsureGitServerIdentity pins the public host-key identity without storing
// private key bytes. A changed key must be an explicit future rotation.
func (s *Store) EnsureGitServerIdentity(ctx context.Context, fingerprint string) error {
	if !gitFingerprintRE.MatchString(fingerprint) {
		return ErrInvalid
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES('git_server_fingerprint',?) ON CONFLICT(key) DO NOTHING`, fingerprint); err != nil {
		return err
	}
	actual, ok, err := s.GitServerIdentity(ctx)
	if err != nil {
		return err
	}
	if !ok || actual != fingerprint {
		return ErrGitDenied
	}
	return nil
}

func (s *Store) RevokeGitPrincipal(ctx context.Context, fingerprint string) error {
	if !gitFingerprintRE.MatchString(fingerprint) {
		return ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	result, err := s.db.ExecContext(ctx, `UPDATE git_principals SET active=0 WHERE fingerprint=? AND active=1`, fingerprint)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetGitRefGuard persists lifecycle intent. Its write lock waits for any
// active receive-pack to finish before the guard can be committed.
func (s *Store) SetGitRefGuard(ctx context.Context, project, branch, operationID string, guarded bool) error {
	if !validProject(project) || !validBranch(branch) || operationID == "" || len(operationID) > 128 {
		return ErrInvalid
	}
	if err := s.lockGitAuthority(ctx); err != nil {
		return err
	}
	defer s.gitAuthority.Unlock()
	if guarded {
		var existing string
		err := s.db.QueryRowContext(ctx, `SELECT operation_id FROM git_ref_guards WHERE project_path=? AND branch=?`, project, branch).Scan(&existing)
		if err == nil {
			if existing == operationID {
				return nil
			}
			return ErrConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var matched int
		err = s.db.QueryRowContext(ctx, `SELECT 1 FROM operations WHERE id=? AND project_path=?`, operationID, project).Scan(&matched)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO git_ref_guards(project_path,branch,operation_id) VALUES(?,?,?)`, project, branch, operationID)
		return classifyWrite(err)
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM git_ref_guards WHERE project_path=? AND branch=? AND operation_id=?`, project, branch, operationID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// AcquireGitLease checks current SQLite authority and holds the shared lock
// through the actual Git service process. Call release exactly once.
func (s *Store) AcquireGitLease(ctx context.Context, fingerprint, project, service string) (allowedRef string, release func(), err error) {
	if !gitFingerprintRE.MatchString(fingerprint) || !validProject(project) || (service != "upload" && service != "receive") {
		return "", nil, ErrGitDenied
	}
	for !s.gitAuthority.TryRLock() {
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	release = s.gitAuthority.RUnlock
	deny := func(err error) (string, func(), error) { release(); return "", nil, err }
	if err := ctx.Err(); err != nil {
		return deny(err)
	}
	var state string
	if err := s.db.QueryRowContext(ctx, `SELECT registry_state FROM projects WHERE path=?`, project).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return deny(ErrGitDenied)
		}
		return deny(err)
	}
	if state != "active" {
		return deny(ErrGitDenied)
	}
	var role, boundProject, sessionID string
	err = s.db.QueryRowContext(ctx, `SELECT role,COALESCE(project_path,''),COALESCE(session_uuid,'') FROM git_principals WHERE fingerprint=? AND active=1`, fingerprint).Scan(&role, &boundProject, &sessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return deny(ErrGitDenied)
		}
		return deny(err)
	}
	if role == "host" {
		if service == "receive" {
			return deny(ErrGitDenied)
		}
		return "", release, nil
	}
	if role != "session" || boundProject != project {
		return deny(ErrGitDenied)
	}
	var branch, registry string
	err = s.db.QueryRowContext(ctx, `SELECT branch,registry_state FROM sessions WHERE uuid=? AND project_path=?`, sessionID, project).Scan(&branch, &registry)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return deny(ErrGitDenied)
		}
		return deny(err)
	}
	if registry != "established" && registry != "creating" {
		return deny(ErrGitDenied)
	}
	if service == "receive" {
		if registry == "creating" {
			var phase string
			var committed int
			err = s.db.QueryRowContext(ctx, `SELECT phase,committed FROM operations WHERE session_uuid=? AND kind IN ('session.create','project.create') ORDER BY created_at DESC LIMIT 1`, sessionID).Scan(&phase, &committed)
			if err != nil {
				return deny(fmt.Errorf("%w: creation phase unreadable", ErrGitDenied))
			}
			if committed != 1 || (phase != "workspace-ready" && phase != "established") {
				return deny(ErrGitDenied)
			}
		}
		var guarded int
		err = s.db.QueryRowContext(ctx, `SELECT 1 FROM git_ref_guards WHERE project_path=? AND branch=?`, project, branch).Scan(&guarded)
		if err == nil {
			return deny(ErrGitDenied)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return deny(err)
		}
	}
	return "refs/heads/" + branch, release, nil
}

// ConsumeGitUnbornGrant is called under a receive lease by the fixed
// pre-receive callback for a validated old-zero update. Consumed is durable
// even if Git then fails or crashes; repair must resolve an absent ref.
func (s *Store) ConsumeGitUnbornGrant(ctx context.Context, fingerprint, project string) (bool, error) {
	var sessionID, branch, registry, state string
	err := s.db.QueryRowContext(ctx, `SELECT s.uuid,s.branch,s.registry_state,g.state
	 FROM git_principals p JOIN sessions s ON s.uuid=p.session_uuid
	 JOIN git_unborn_grants g ON g.session_uuid=s.uuid
	 WHERE p.fingerprint=? AND p.active=1 AND p.role='session' AND p.project_path=?`, fingerprint, project).Scan(&sessionID, &branch, &registry, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if branch != "main" || (registry != "creating" && registry != "established") || state != "pending" {
		return false, nil
	}
	result, err := s.db.ExecContext(ctx, `UPDATE git_unborn_grants SET state='consumed' WHERE session_uuid=? AND state='pending'`, sessionID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
