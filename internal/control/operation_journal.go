package control

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/lgvo/p.ai/internal/api"
)

// The kinds of durable operation the journal records.
const (
	KindProjectCreate        = "project.create"
	KindSessionCreate        = "session.create"
	KindProjectDelete        = "project.delete"
	KindWorkspaceLossInspect = "workspace.loss.inspect"
	KindSessionDiscard       = "session.discard"
	KindSessionDelete        = "session.delete"
	KindCreateCleanup        = "session.create.cleanup"
	KindSessionRename        = "session.rename"
	KindRetainedRename       = "project.retained.rename"
	KindRetainedDelete       = "project.retained.delete"
)

// unfinishedGroups lists unfinished operations in recovery order. Each group
// keeps the order its listing has always used: creation time, or insertion
// order (rowid).
var unfinishedGroups = []struct {
	kinds   []string
	orderBy string
}{
	{[]string{KindProjectCreate, KindSessionCreate}, "created_at"},
	{[]string{KindProjectDelete}, "created_at"},
	{[]string{KindWorkspaceLossInspect}, "created_at"},
	{[]string{KindSessionDiscard, KindSessionDelete, KindCreateCleanup}, "created_at"},
	{[]string{KindSessionRename}, "created_at"},
	{[]string{KindRetainedRename}, "rowid"},
	{[]string{KindRetainedDelete}, "rowid"},
}

// UnfinishedOperations lists every running, blocked or unknown operation in
// recovery order.
func (s *Store) UnfinishedOperations(ctx context.Context) ([]api.Operation, error) {
	var ops []api.Operation
	for _, group := range unfinishedGroups {
		args := make([]any, len(group.kinds))
		for i, kind := range group.kinds {
			args[i] = kind
		}
		marks := strings.TrimSuffix(strings.Repeat("?,", len(group.kinds)), ",")
		rows, err := s.db.QueryContext(ctx, `SELECT id FROM operations WHERE kind IN (`+marks+`) AND status IN ('running','blocked','unknown') ORDER BY `+group.orderBy, args...)
		if err != nil {
			return nil, err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err = rows.Close(); err != nil {
			return nil, err
		}
		for _, id := range ids {
			op, err := s.GetOperation(ctx, id)
			if err != nil {
				return nil, err
			}
			ops = append(ops, op)
		}
	}
	return ops, nil
}

// queryRower is the database or an open transaction.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// keyRecordsRequest reports whether key already recorded exactly this request
// of kind. Keys share one namespace: a key recorded for another kind or
// another request is a conflict.
func keyRecordsRequest(ctx context.Context, q queryRower, key, kind string, request []byte) (bool, error) {
	var recordedKind, recordedSHA string
	err := q.QueryRowContext(ctx, `SELECT kind,request_sha256 FROM operations WHERE idempotency_key=?`, key).Scan(&recordedKind, &recordedSHA)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if recordedKind != kind || recordedSHA != digest(request) {
		return false, ErrConflict
	}
	return true, nil
}

// execer is the database or an open transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// operationRecord is a new journal row; every operation starts running.
type operationRecord struct {
	ID, Key, Kind, Project string
	Session                string // empty records no session
	Request                []byte
	Phase                  string
	Committed              bool
	Evidence               []byte // nil records no evidence
	Now                    string
}

// insertOperation records a new operation; it is the journal's only insert.
func insertOperation(ctx context.Context, x execer, r operationRecord) error {
	var session any
	if r.Session != "" {
		session = r.Session
	}
	_, err := x.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,committed,evidence_json,created_at,updated_at)
	 VALUES(?,?,?,?,?,?,?,'running',?,?,?,?,?)`, r.ID, r.Key, r.Kind, r.Project, session, string(r.Request), digest(r.Request), r.Phase, r.Committed, nullableJSON(r.Evidence), r.Now, r.Now)
	return err
}
