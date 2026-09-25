package control

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"
)

// EnvironmentCollectionClaim is the complete reviewed generation. It is
// persisted before any Incus deletion and never rewritten on retry.
type EnvironmentCollectionClaim struct {
	Image                 EnvironmentImage `json:"image"`
	ImagePresent          bool             `json:"image_present"`
	ObservedSize          int64            `json:"observed_size"`
	RelatedDigest         string           `json:"related_digest"`
	ConfirmationDigest    string           `json:"confirmation_digest"`
	ConfirmationExpiresAt string           `json:"confirmation_expires_at"`
}

func (c EnvironmentCollectionClaim) Valid() bool {
	_, expiryErr := time.Parse(time.RFC3339Nano, c.ConfirmationExpiresAt)
	return c.Image.valid() && validFingerprint(c.RelatedDigest) && validFingerprint(c.ConfirmationDigest) && c.ObservedSize >= 0 && c.ObservedSize <= 1<<50 &&
		(!c.ImagePresent || c.ObservedSize > 0) && expiryErr == nil
}

type RelatedEnvironmentSession struct {
	UUID     string `json:"uuid"`
	Branch   string `json:"branch"`
	Registry string `json:"registry_state"`
}

type collectionQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func relatedEnvironmentRows(ctx context.Context, q collectionQuerier, project, key, fingerprint, after string, limit int) ([]RelatedEnvironmentSession, string, error) {
	rows, err := q.QueryContext(ctx, `SELECT s.uuid,s.branch,s.registry_state FROM sessions s
	 JOIN operations o ON o.session_uuid=s.uuid AND o.kind IN ('project.create','session.create')
	 LEFT JOIN operations r ON r.id=(SELECT id FROM operations WHERE session_uuid=s.uuid AND kind='session.repair'
	  AND status='completed' AND phase='completed' ORDER BY rowid DESC LIMIT 1)
	 WHERE s.project_path=? AND s.uuid>?
	 AND COALESCE(json_extract(r.evidence_json,'$.environment_state.key'),json_extract(o.evidence_json,'$.environment_state.key'))=?
	 AND COALESCE(json_extract(r.evidence_json,'$.environment_state.fingerprint'),json_extract(o.evidence_json,'$.environment_state.fingerprint'))=?
 ORDER BY s.uuid COLLATE BINARY LIMIT ?`, project, after, key, fingerprint, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := make([]RelatedEnvironmentSession, 0, limit+1)
	for rows.Next() {
		var v RelatedEnvironmentSession
		if err := rows.Scan(&v.UUID, &v.Branch, &v.Registry); err != nil {
			return nil, "", err
		}
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > limit {
		next = items[limit-1].UUID
		items = items[:limit]
	}
	return items, next, nil
}

func (s *Store) ListRelatedEnvironmentSessions(ctx context.Context, project, key, fingerprint, after string, limit int) ([]RelatedEnvironmentSession, string, error) {
	if !validProject(project) || !validFingerprint(key) || !validFingerprint(fingerprint) ||
		(after != "" && !cacheUUID.MatchString(after)) || limit < 1 || limit > 100 {
		return nil, "", ErrInvalid
	}
	return relatedEnvironmentRows(ctx, s.db, project, key, fingerprint, after, limit)
}

func relatedEnvironmentDigest(ctx context.Context, q collectionQuerier, project, key, fingerprint string) (string, int, error) {
	h := sha256.New()
	after, count := "", 0
	for {
		items, next, err := relatedEnvironmentRows(ctx, q, project, key, fingerprint, after, 100)
		if err != nil {
			return "", 0, err
		}
		for _, v := range items {
			encoded, _ := json.Marshal(v)
			_, _ = h.Write(encoded)
			_, _ = h.Write([]byte{0})
			count++
			if count > 10000 {
				return "", 0, errors.New("too many related environment sessions")
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	return hex.EncodeToString(h.Sum(nil)), count, nil
}

func (s *Store) RelatedEnvironmentDigest(ctx context.Context, project, key, fingerprint string) (string, int, error) {
	if !validProject(project) || !validFingerprint(key) || !validFingerprint(fingerprint) {
		return "", 0, ErrInvalid
	}
	return relatedEnvironmentDigest(ctx, s.db, project, key, fingerprint)
}

func (s *Store) ListEnvironmentImages(ctx context.Context, project, after string, limit int) ([]EnvironmentImage, string, error) {
	if !validProject(project) || (after != "" && !validFingerprint(after)) || limit < 1 || limit > 20 {
		return nil, "", ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT environment_key FROM environment_images WHERE project_path=? AND environment_key>? ORDER BY environment_key COLLATE BINARY LIMIT ?`, project, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			_ = rows.Close()
			return nil, "", err
		}
		keys = append(keys, key)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(keys) > limit {
		next = keys[limit-1]
		keys = keys[:limit]
	}
	items := make([]EnvironmentImage, 0, len(keys))
	for _, key := range keys {
		entry, found, err := s.GetEnvironmentImage(ctx, project, key)
		if err != nil || !found {
			return nil, "", errors.Join(err, errors.New("cache index changed during listing"))
		}
		items = append(items, entry)
	}
	return items, next, nil
}

func sameEnvironmentImage(a, b EnvironmentImage) bool {
	return a.Project == b.Project && a.Key == b.Key && a.Fingerprint == b.Fingerprint &&
		a.BaseFingerprint == b.BaseFingerprint && a.System == b.System && a.MaterialDigest == b.MaterialDigest &&
		a.CaptureStorePath == b.CaptureStorePath && a.BuilderRequest == b.BuilderRequest &&
		a.CreatedAt == b.CreatedAt && a.LastUsedAt == b.LastUsedAt && a.LogicalSize == b.LogicalSize && maps.Equal(a.Properties, b.Properties)
}

func SameEnvironmentImage(a, b EnvironmentImage) bool { return sameEnvironmentImage(a, b) }

type EnvironmentCacheItem struct {
	Project         string `json:"project"`
	Key             string `json:"environment_key"`
	Fingerprint     string `json:"fingerprint"`
	BaseFingerprint string `json:"base_image_fingerprint"`
	CreatedAt       string `json:"created_at"`
	LastUsedAt      string `json:"last_used_at,omitempty"`
	LogicalSize     int64  `json:"logical_size"`
	ImageStatus     string `json:"image_status"`
	RelatedCount    int    `json:"related_count"`
}

type EnvironmentCollectionPreview struct {
	Token           string                      `json:"confirmation_token"`
	ExpiresAt       string                      `json:"expires_at"`
	Item            EnvironmentCacheItem        `json:"item"`
	RelatedSessions []RelatedEnvironmentSession `json:"related_sessions"`
	RelatedNext     string                      `json:"related_next"`
	Warning         string                      `json:"warning"`
}

func (s *Store) BeginEnvironmentCollection(ctx context.Context, key string, claim EnvironmentCollectionClaim) (Operation, error) {
	if key == "" || len(key) > 128 || !claim.Valid() {
		return Operation{}, ErrInvalid
	}
	request, _ := json.Marshal(claim)
	if len(request) > 16384 {
		return Operation{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, err
	}
	defer tx.Rollback()
	var oldKind, oldHash string
	err = tx.QueryRowContext(ctx, `SELECT kind,request_sha256 FROM operations WHERE idempotency_key=?`, key).Scan(&oldKind, &oldHash)
	if err == nil {
		if oldKind != "environment.collect" || oldHash != digest(request) {
			return Operation{}, ErrConflict
		}
		op, err := getOperationTx(ctx, tx, key)
		return op, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, err
	}
	var current EnvironmentImage
	var props string
	err = tx.QueryRowContext(ctx, `SELECT fingerprint,base_fingerprint,system,material_digest,capture_store_path,builder_request_uuid,properties_json,created_at,last_used_at,logical_size
	 FROM environment_images WHERE project_path=? AND environment_key=?`, claim.Image.Project, claim.Image.Key).
		Scan(&current.Fingerprint, &current.BaseFingerprint, &current.System, &current.MaterialDigest, &current.CaptureStorePath,
			&current.BuilderRequest, &props, &current.CreatedAt, &current.LastUsedAt, &current.LogicalSize)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrConflict
	}
	if err != nil {
		return Operation{}, err
	}
	current.Project, current.Key = claim.Image.Project, claim.Image.Key
	if json.Unmarshal([]byte(props), &current.Properties) != nil || !sameEnvironmentImage(current, claim.Image) {
		return Operation{}, ErrConflict
	}
	related, _, err := relatedEnvironmentDigest(ctx, tx, claim.Image.Project, claim.Image.Key, claim.Image.Fingerprint)
	if err != nil {
		return Operation{}, err
	}
	if related != claim.RelatedDigest {
		return Operation{}, ErrConflict
	}
	expiresAt, _ := time.Parse(time.RFC3339Nano, claim.ConfirmationExpiresAt)
	if !time.Now().Before(expiresAt) {
		return Operation{}, ErrConflict
	}
	opID, err := newUUID()
	if err != nil {
		return Operation{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,request_json,request_sha256,status,phase,created_at,updated_at)
	 VALUES(?,?,?,?,?,?,'running','accepted',?,?)`, opID, key, "environment.collect", claim.Image.Project, string(request), digest(request), now, now)
	if err != nil {
		return Operation{}, classifyWrite(err)
	}
	if err := tx.Commit(); err != nil {
		return Operation{}, err
	}
	return Operation{ID: opID, Key: key, Kind: "environment.collect", Project: claim.Image.Project, Request: request,
		Status: "running", Phase: "accepted", CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) DeleteEnvironmentImageExact(ctx context.Context, want EnvironmentImage) error {
	if !want.valid() || want.CreatedAt == "" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current EnvironmentImage
	var props string
	err = tx.QueryRowContext(ctx, `SELECT fingerprint,base_fingerprint,system,material_digest,capture_store_path,builder_request_uuid,properties_json,created_at,last_used_at,logical_size
	 FROM environment_images WHERE project_path=? AND environment_key=?`, want.Project, want.Key).
		Scan(&current.Fingerprint, &current.BaseFingerprint, &current.System, &current.MaterialDigest, &current.CaptureStorePath,
			&current.BuilderRequest, &props, &current.CreatedAt, &current.LastUsedAt, &current.LogicalSize)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	current.Project, current.Key = want.Project, want.Key
	if json.Unmarshal([]byte(props), &current.Properties) != nil || !sameEnvironmentImage(current, want) {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM environment_images WHERE project_path=? AND environment_key=? AND fingerprint=? AND created_at=?`,
		want.Project, want.Key, want.Fingerprint, want.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UnfinishedEnvironmentCollections(ctx context.Context) ([]Operation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM operations WHERE kind='environment.collect' AND status IN ('running','blocked','unknown') ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	ops := make([]Operation, 0, len(ids))
	for _, id := range ids {
		op, err := s.GetOperation(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("collection operation %s: %w", id, err)
		}
		ops = append(ops, op)
	}
	return ops, nil
}
