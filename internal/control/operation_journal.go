package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

// The kinds of durable operation the journal records.
const (
	KindProjectCreate  = "project.create"
	KindSessionCreate  = "session.create"
	KindProjectDelete  = "project.delete"
	KindSessionDiscard = "session.discard"
	KindSessionDelete  = "session.delete"
	KindCreateCleanup  = "session.create.cleanup"
	KindSessionRename  = "session.rename"
	KindRetainedRename = "project.retained.rename"
	KindRetainedDelete = "project.retained.delete"
	KindSessionStart   = "session.start"
	KindSessionStop    = "session.stop"
	KindOriginChange   = "origin.change"
	KindOriginPublish  = "origin.publish"
)

// UnfinishedOperations lists every running or blocked operation in the order
// the journal accepted them.
func (s *Store) UnfinishedOperations(ctx context.Context) ([]api.Operation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM operations WHERE status IN ('running','blocked') ORDER BY seq`)
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
	ops := make([]api.Operation, 0, len(ids))
	for _, id := range ids {
		op, err := s.GetOperation(ctx, id)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
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
		return false, KeyReused()
	}
	return true, nil
}

// recorded returns the operation that an identical earlier request of kind
// recorded under key; replayed is false when key is unused. A key recorded
// for another request, or retired with its project, is reused.
func (s *Store) recorded(ctx context.Context, key, kind string, request []byte) (op api.Operation, replayed bool, err error) {
	if err = s.checkRetiredKey(ctx, key); err != nil {
		return api.Operation{}, false, err
	}
	same, err := keyRecordsRequest(ctx, s.db, key, kind, request)
	if err != nil || !same {
		return api.Operation{}, false, err
	}
	op, err = s.GetOperationByKey(ctx, key)
	return op, err == nil, err
}

// insertForProject records new operation r of r.Project in a journal
// transaction of its own, which also checks that the project is active and
// that check, if any, holds. An identical request recorded meanwhile under
// r.Key is returned instead.
func (s *Store) insertForProject(ctx context.Context, r operationRecord, check func(queryRower) error) (api.Operation, error) {
	j, err := s.beginJournal(ctx)
	if err != nil {
		return api.Operation{}, err
	}
	defer j.Rollback()
	if same, err := keyRecordsRequest(ctx, j, r.Key, r.Kind, r.Request); err != nil || same {
		if err != nil {
			return api.Operation{}, err
		}
		return getOperationTx(ctx, j.Tx, r.Key)
	}
	var registry string
	err = j.QueryRowContext(ctx, `SELECT registry_state FROM projects WHERE path=?`, r.Project).Scan(&registry)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Operation{}, NotFound(api.ReasonProject, api.TargetProject(r.Project))
	}
	if err != nil {
		return api.Operation{}, err
	}
	if registry != "active" {
		return api.Operation{}, Conflict(api.ReasonProjectDeleting, api.TargetProject(r.Project))
	}
	if check != nil {
		if err = check(j); err != nil {
			return api.Operation{}, err
		}
	}
	if err = j.insert(ctx, r); err != nil {
		return api.Operation{}, classifyWrite(err)
	}
	if err = j.commit(ctx); err != nil {
		return api.Operation{}, err
	}
	return r.operation(), nil
}

// operationRecord is a new journal row; every operation starts running.
type operationRecord struct {
	ID, Key, Kind, Project string
	Session                string // empty records no session
	Request                []byte
	Intent                 api.OperationIntent
	Phase                  string
	Committed              bool
	Evidence               []byte // nil records no evidence
	// ChangeHash is the change hash of a risky change's run: the exact change
	// it runs. The journal keeps it out of every reply.
	ChangeHash string
	Now        string
}

// operation is the operation that r records, as the journal returns it.
func (r operationRecord) operation() api.Operation {
	return api.Operation{ID: r.ID, Key: r.Key, Kind: r.Kind, Project: r.Project, SessionUUID: r.Session, Intent: r.Intent,
		Request: json.RawMessage(r.Request), Status: "running", Phase: r.Phase, Committed: r.Committed,
		Evidence: json.RawMessage(r.Evidence), CreatedAt: r.Now, UpdatedAt: r.Now}
}

// JournalObserver hears of the journal's status changes. The daemon sets it
// once with ObserveJournal, before it serves.
type JournalObserver interface {
	// Ending runs just before the commit that takes operation id out of
	// running. The daemon releases the sessions the operation's worker holds
	// there: once the end is recorded nothing of the operation holds them,
	// and until it is recorded the journal refuses work that would compete.
	Ending(id string)
	// Changed runs after the commit of each status change of op, its
	// acceptance included; from is the status before, empty for a new
	// operation.
	Changed(op api.Operation, from string)
}

// ObserveJournal sets the observer of the journal's status changes.
func (s *Store) ObserveJournal(o JournalObserver) { s.observer = o }

// journalTx is a transaction that writes the journal. Every write of an
// operation's row goes through insert or update, so commit reports each
// status change to the observer: the one write path that ends operations,
// emits their progress and wakes their waiters.
type journalTx struct {
	*sql.Tx
	store   *Store
	changes []statusChange
}

// statusChange is one change of an operation's status in a journalTx, with
// the operation as the transaction leaves it.
type statusChange struct {
	id, from, to string
	op           api.Operation
}

func (s *Store) beginJournal(ctx context.Context) (*journalTx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &journalTx{Tx: tx, store: s}, nil
}

// insert records new operation r, running; it is the journal's only insert.
func (j *journalTx) insert(ctx context.Context, r operationRecord) error {
	var session any
	if r.Session != "" {
		session = r.Session
	}
	intent, err := json.Marshal(r.Intent)
	if err != nil {
		return err
	}
	_, err = j.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,intent_json,change_hash,status,phase,committed,evidence_json,created_at,updated_at)
	 VALUES(?,?,?,?,?,?,?,?,?,'running',?,?,?,?,?)`, r.ID, r.Key, r.Kind, r.Project, session, string(r.Request), digest(r.Request), string(intent), r.ChangeHash, r.Phase, r.Committed, nullableJSON(r.Evidence), r.Now, r.Now)
	if err == nil {
		j.changes = append(j.changes, statusChange{id: r.ID, to: "running"})
	}
	return err
}

// update runs query, an UPDATE of operation id's row, and records the status
// change it makes. It returns the number of rows the statement changed.
func (j *journalTx) update(ctx context.Context, id, query string, args ...any) (int64, error) {
	from, err := j.status(ctx, id)
	if err != nil {
		return 0, err
	}
	result, err := j.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	to, err := j.status(ctx, id)
	if err != nil {
		return 0, err
	}
	if from != to {
		j.changes = append(j.changes, statusChange{id: id, from: from, to: to})
	}
	return n, nil
}

// status is operation id's status in the transaction, empty for no row.
func (j *journalTx) status(ctx context.Context, id string) (string, error) {
	var status string
	err := j.QueryRowContext(ctx, `SELECT status FROM operations WHERE id=?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return status, err
}

// commit commits the transaction. Before it, the observer releases what the
// operations it ends hold; after it, the observer hears of every status
// change, with each operation as this transaction left it. journalMu keeps
// the reports of successive commits in their commit order.
func (j *journalTx) commit(ctx context.Context) error {
	observer := j.store.observer
	if observer == nil {
		return j.Commit()
	}
	for i, c := range j.changes {
		op, err := operationByIDTx(ctx, j.Tx, c.id)
		if err != nil {
			return err
		}
		j.changes[i].op = op
	}
	for _, c := range j.changes {
		if c.from == "running" && c.to != "running" {
			observer.Ending(c.id)
		}
	}
	j.store.journalMu.Lock()
	defer j.store.journalMu.Unlock()
	if err := j.Commit(); err != nil {
		return err
	}
	for _, c := range j.changes {
		observer.Changed(c.op, c.from)
	}
	return nil
}

// BlockAfterPanic records running operation id blocked with reason internal
// and diagnostic, after its work panicked. It changes nothing else: the
// operation keeps its phase, evidence and guards, and waits for Retry.
func (s *Store) BlockAfterPanic(ctx context.Context, id, diagnostic string) (api.Operation, error) {
	j, err := s.beginJournal(ctx)
	if err != nil {
		return api.Operation{}, err
	}
	defer j.Rollback()
	n, err := j.update(ctx, id, `UPDATE operations SET status='blocked',reason=?,diagnostic=?,updated_at=? WHERE id=? AND status='running'`,
		api.ReasonInternal, api.BoundDiagnostic(diagnostic), time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return api.Operation{}, err
	}
	if n != 1 {
		return api.Operation{}, ErrConflict
	}
	if err = j.commit(ctx); err != nil {
		return api.Operation{}, err
	}
	return s.GetOperation(ctx, id)
}
