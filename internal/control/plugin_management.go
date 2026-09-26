package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// CheckPluginRemovalDependencies is called while the offline manager owns the
// daemon lock. Ambiguous inventories preserve the staged package. Historical
// completed requests without a session/cache holder do not create new authority.
func (s *Store) CheckPluginRemovalDependencies(ctx context.Context, digest string) error {
	if !validFingerprint(digest) {
		return ErrInvalid
	}
	// Validate the actual live creator binding, not merely non-NULL JSON keys.
	rows, err := s.db.QueryContext(ctx, `SELECT s.uuid,s.project_path,o.project_path,COALESCE(o.evidence_json,'') FROM sessions s LEFT JOIN operations o ON o.session_uuid=s.uuid AND o.kind IN ('project.create','session.create') ORDER BY s.uuid`)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	proofBytes := 0
	for rows.Next() {
		var id, project, raw string
		var creatorProject sql.NullString
		if err = rows.Scan(&id, &project, &creatorProject, &raw); err != nil {
			break
		}
		proofBytes += len(raw)
		var ev CreationEvidence
		if seen[id] || len(seen) >= 1024 || proofBytes > 8<<20 || !creatorProject.Valid || creatorProject.String != project || json.Unmarshal([]byte(raw), &ev) != nil || !ev.Selection.Valid() {
			err = fmt.Errorf("%w: session plugin provenance unavailable; preserve package and use existing recovery/manual investigation", ErrConflict)
			break
		}
		seen[id] = true
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		return errors.Join(err, rowErr)
	}
	queries := []string{
		`SELECT request_json,COALESCE(evidence_json,'') FROM operations WHERE status NOT IN ('completed','superseded') OR session_uuid IN (SELECT uuid FROM sessions)`,
		`SELECT policy_json,'' FROM sessions`,
		`SELECT properties_json,'' FROM environment_images`,
	}
	count, total := 0, 0
	for _, q := range queries {
		rows, e := s.db.QueryContext(ctx, q)
		if e != nil {
			return e
		}
		for rows.Next() {
			var a, b string
			if e = rows.Scan(&a, &b); e != nil {
				break
			}
			count++
			total += len(a) + len(b)
			if count > 1024 || total > 8<<20 {
				e = errors.New("plugin dependency inventory exceeds bound; preserve package and investigate")
				break
			}
			for _, raw := range []string{a, b} {
				if raw == "" {
					continue
				}
				var data any
				if e = json.Unmarshal([]byte(raw), &data); e != nil {
					e = errors.New("plugin dependency evidence unavailable; preserve package")
					break
				}
				if containsPluginDigest(data, digest) {
					e = fmt.Errorf("%w: durable session/operation/cache depends on package %s; finish existing cleanup/collection with the pinned package before removal", ErrConflict, digest)
					break
				}
			}
			if e != nil {
				break
			}
		}
		rowErr := rows.Err()
		rows.Close()
		if e != nil || rowErr != nil {
			return errors.Join(e, rowErr)
		}
	}
	// Indexed cache material does not independently encode the module digest.
	// Preserve all cache entries rather than guess whether the package is needed.
	var caches int
	if e := s.db.QueryRowContext(ctx, `SELECT count(*) FROM environment_images`).Scan(&caches); e != nil {
		return e
	}
	if caches != 0 {
		return fmt.Errorf("%w: collect indexed environment images before offline package removal; cache module provenance is not independently indexed", ErrConflict)
	}
	// Origin/publication rows do not pin a source module independently. Pending
	// requests require the current selected capability for exact recovery.
	var pending int
	if e := s.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM origin_requests WHERE status='prepared')+(SELECT count(*) FROM publication_requests WHERE status IN ('prepared','attempted'))`).Scan(&pending); e != nil {
		return e
	}
	if pending != 0 {
		return fmt.Errorf("%w: finish pending origin/publication recovery before package removal", ErrConflict)
	}
	return nil
}
func containsPluginDigest(v any, digest string) bool {
	switch x := v.(type) {
	case string:
		return x == digest || strings.Contains(x, "/packages/"+digest)
	case []any:
		for _, v := range x {
			if containsPluginDigest(v, digest) {
				return true
			}
		}
	case map[string]any:
		for _, v := range x {
			if containsPluginDigest(v, digest) {
				return true
			}
		}
	}
	return false
}

// PluginActivationPaths is the closed set of activation authorities supplied by
// this host configuration, including optional environment/adapter roles.
func (c HostConfig) PluginActivationPaths() []string {
	paths := []string{}
	if c.Git != nil {
		paths = append(paths, c.Git.ActivationPath)
	}
	if c.Events != nil {
		paths = append(paths, c.Events.ActivationPath)
	}
	if c.Runtime != nil {
		paths = append(paths, c.Runtime.ActivationPath)
		if c.Runtime.Environment != nil {
			paths = append(paths, c.Runtime.Environment.ActivationPath)
		}
		if c.Runtime.AgentAdapter != nil {
			paths = append(paths, c.Runtime.AgentAdapter.ActivationPath)
		}
	}
	return paths
}
