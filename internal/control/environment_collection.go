package control

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
)

// EnvironmentCollectionClaim is one reviewed image of a project deletion. It
// is persisted before any Incus deletion and never rewritten on retry.
type EnvironmentCollectionClaim struct {
	Image         EnvironmentImage `json:"image"`
	ImagePresent  bool             `json:"image_present"`
	ObservedSize  int64            `json:"observed_size"`
	RelatedDigest string           `json:"related_digest"`
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

func relatedEnvironmentDigest(ctx context.Context, q collectionQuerier, project, key, fingerprint string) (string, error) {
	h := sha256.New()
	after, count := "", 0
	for {
		items, next, err := relatedEnvironmentRows(ctx, q, project, key, fingerprint, after, 100)
		if err != nil {
			return "", err
		}
		for _, v := range items {
			encoded, _ := json.Marshal(v)
			_, _ = h.Write(encoded)
			_, _ = h.Write([]byte{0})
			count++
			if count > 10000 {
				return "", errors.New("too many related environment sessions")
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Store) RelatedEnvironmentDigest(ctx context.Context, project, key, fingerprint string) (string, error) {
	if !validProject(project) || !validFingerprint(key) || !validFingerprint(fingerprint) {
		return "", ErrInvalid
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
