package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

var cacheStorePath = regexp.MustCompile(`^/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-[A-Za-z0-9+._?=-]+-env$`)
var cacheUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// EnvironmentImage is a project-scoped index entry, never authority for the
// image bytes. A caller must verify the exact Incus object before using it.
type EnvironmentImage struct {
	Project          string            `json:"project"`
	Key              string            `json:"environment_key"`
	Fingerprint      string            `json:"fingerprint"`
	BaseFingerprint  string            `json:"base_fingerprint"`
	System           string            `json:"system"`
	MaterialDigest   string            `json:"material_digest"`
	CaptureStorePath string            `json:"capture_store_path"`
	BuilderRequest   string            `json:"builder_request_uuid"`
	Properties       map[string]string `json:"properties"`
	CreatedAt        string            `json:"created_at,omitempty"`
	LastUsedAt       string            `json:"last_used_at,omitempty"`
	LogicalSize      int64             `json:"logical_size"`
}

func (e EnvironmentImage) valid() bool {
	if !validProject(e.Project) || !validFingerprint(e.Key) || !validFingerprint(e.Fingerprint) ||
		!validFingerprint(e.BaseFingerprint) || !validFingerprint(e.MaterialDigest) ||
		(e.System != "x86_64-linux" && e.System != "aarch64-linux") ||
		!cacheStorePath.MatchString(e.CaptureStorePath) || !cacheUUID.MatchString(e.BuilderRequest) ||
		len(e.Properties) < 10 || len(e.Properties) > 64 || e.LogicalSize < 0 || e.LogicalSize > 1<<50 {
		return false
	}
	checks := map[string]string{
		"p.contract": "p.incus-system-image/v2", "p.compression": "none",
		"p.project_path": e.Project, "p.environment_key": e.Key,
		"p.base_image": e.BaseFingerprint, "p.material": e.MaterialDigest,
		"p.capture_store_path": e.CaptureStorePath, "p.builder_request": e.BuilderRequest,
		"p.system": e.System,
	}
	for key, want := range checks {
		if e.Properties[key] != want {
			return false
		}
	}
	if e.Properties["p.instance"] == "" || e.Properties["p.incus_project"] == "" {
		return false
	}
	for key, value := range e.Properties {
		if key == "" || len(key) > 128 || len(value) > 1024 {
			return false
		}
		if len(key) >= 2 && key[:2] == "p." {
			if _, ok := checks[key]; !ok && key != "p.instance" && key != "p.incus_project" {
				return false
			}
		}
	}
	data, err := json.Marshal(e.Properties)
	return err == nil && len(data) <= 8192
}

func (s *Store) PutEnvironmentImage(ctx context.Context, e EnvironmentImage) error {
	if !e.valid() {
		return ErrInvalid
	}
	properties, _ := json.Marshal(e.Properties)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `INSERT INTO environment_images(project_path,environment_key,fingerprint,base_fingerprint,system,material_digest,capture_store_path,builder_request_uuid,properties_json,created_at,logical_size)
 VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project_path,environment_key) DO UPDATE SET
 fingerprint=excluded.fingerprint,base_fingerprint=excluded.base_fingerprint,system=excluded.system,
 material_digest=excluded.material_digest,capture_store_path=excluded.capture_store_path,
	 builder_request_uuid=excluded.builder_request_uuid,properties_json=excluded.properties_json,
	 created_at=CASE WHEN environment_images.fingerprint=excluded.fingerprint THEN environment_images.created_at ELSE excluded.created_at END,
	 last_used_at=CASE WHEN environment_images.fingerprint=excluded.fingerprint THEN environment_images.last_used_at ELSE '' END,
	 logical_size=excluded.logical_size WHERE environment_images.fingerprint=excluded.fingerprint`,
		e.Project, e.Key, e.Fingerprint, e.BaseFingerprint, e.System, e.MaterialDigest, e.CaptureStorePath, e.BuilderRequest, string(properties), now, e.LogicalSize)
	if err != nil {
		return classifyWrite(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

// PendingEnvironmentPublication prevents a second same-key publish while a
// prior durable attempt has an unresolved Incus outcome. The owning operation
// is excluded so its exact reconciliation may proceed.
func (s *Store) PendingEnvironmentPublication(ctx context.Context, project, key, excludeOperation string) (bool, error) {
	if !validProject(project) || !validFingerprint(key) || (excludeOperation != "" && !cacheUUID.MatchString(excludeOperation)) {
		return false, ErrInvalid
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM operations WHERE project_path=? AND id!=?
	 AND kind IN ('project.create','session.create','session.repair') AND status IN ('running','blocked','unknown')
	 AND (phase='environment-publishing' OR (kind='session.repair' AND phase IN
	  ('image-builder-init-issued','image-builder-created','image-source-transfer-issued',
	   'image-source-ready','image-builder-start-issued','image-builder-running',
	   'image-builder-delete-issued','image-builder-absent')))
	 AND COALESCE(json_extract(evidence_json,'$.environment_state.key'),json_extract(evidence_json,'$.environment_key'))=?`,
		project, excludeOperation, key).Scan(&count)
	return count != 0, err
}

func (s *Store) GetEnvironmentImage(ctx context.Context, project, key string) (EnvironmentImage, bool, error) {
	var e EnvironmentImage
	if !validProject(project) || !validFingerprint(key) {
		return e, false, ErrInvalid
	}
	var properties string
	err := s.db.QueryRowContext(ctx, `SELECT fingerprint,base_fingerprint,system,material_digest,capture_store_path,builder_request_uuid,properties_json,created_at,last_used_at,logical_size FROM environment_images WHERE project_path=? AND environment_key=?`, project, key).
		Scan(&e.Fingerprint, &e.BaseFingerprint, &e.System, &e.MaterialDigest, &e.CaptureStorePath, &e.BuilderRequest, &properties, &e.CreatedAt, &e.LastUsedAt, &e.LogicalSize)
	if errors.Is(err, sql.ErrNoRows) {
		return EnvironmentImage{}, false, nil
	}
	if err != nil {
		return EnvironmentImage{}, false, err
	}
	e.Project, e.Key = project, key
	if len(properties) > 8192 || json.Unmarshal([]byte(properties), &e.Properties) != nil || !e.valid() {
		return EnvironmentImage{}, false, fmt.Errorf("invalid persisted environment image for %s", project)
	}
	return e, true, nil
}

// ForgetEnvironmentImage removes only the exact stale mapping observed by a
// caller. It never deletes an Incus image or a newer cache generation.
func (s *Store) ForgetEnvironmentImage(ctx context.Context, project, key, fingerprint string) error {
	if !validProject(project) || !validFingerprint(key) || !validFingerprint(fingerprint) {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM environment_images WHERE project_path=? AND environment_key=? AND fingerprint=?`, project, key, fingerprint)
	return err
}
