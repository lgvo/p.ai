package control

import (
	"context"
	"errors"
)

// SessionRemovalKind identifies the committed authority ending this session.
// Project deletion owns the whole project even if a subordinate session
// operation still has a retained unfinished journal record.
func (s *Store) SessionRemovalKind(ctx context.Context, uuid string) (string, error) {
	if !validUUID(uuid) {
		return "", ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT o.kind FROM sessions s JOIN projects p ON p.path=s.project_path
 JOIN operations o ON o.project_path=s.project_path AND
 ((p.registry_state='active' AND o.session_uuid=s.uuid AND o.kind IN ('session.discard','session.delete','session.create.cleanup','session.record.repair'))
 OR (o.kind='project.delete' AND p.registry_state='deleting'))
 WHERE s.uuid=? AND s.registry_state='removing' AND o.committed=1 AND o.status IN ('running','blocked','unknown')
 ORDER BY CASE WHEN o.kind='project.delete' THEN 0 ELSE 1 END LIMIT 2`, uuid)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var kind string
		if err = rows.Scan(&kind); err != nil {
			return "", err
		}
		kinds = append(kinds, kind)
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if len(kinds) == 0 {
		return "", ErrNotFound
	}
	if len(kinds) > 1 && (kinds[0] != "project.delete" || kinds[1] == "project.delete") {
		return "", errors.Join(ErrConflict, errors.New("ambiguous session removal intent"))
	}
	return kinds[0], nil
}
