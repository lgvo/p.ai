package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
)

// These tests state which durable operations Retry and Recover schedule and
// how a removing session's condition follows its removal operation. The
// lifecycle's worker slots stay closed, so scheduled work waits in the queue
// where the test reads it; no worker runs.

var dispatchKinds = []string{
	"project.create", "session.create", "project.delete",
	"workspace.loss.inspect",
	"session.discard", "session.delete", "session.create.cleanup",
	"session.rename", "project.retained.rename", "project.retained.delete",
	"session.repair", "session.repair.prepare",
	"session.ref.repair", "session.principal.repair", "session.record.repair",
}

var dispatchStatuses = []string{"running", "blocked", "unknown", "failed", "completed", "superseded"}

type journalFixture struct {
	store *control.Store
	raw   *sql.DB
	seq   int
}

// newJournalFixture opens a store whose journal accepts rows of every kind
// without the sessions, sources and reviews their real requests require.
func newJournalFixture(t *testing.T) *journalFixture {
	t.Helper()
	state := filepath.Join(t.TempDir(), "state")
	store, err := control.OpenStore(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err = store.CreateProject(ctx, "app", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", filepath.Join(state, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	rows, err := raw.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='trigger' AND tbl_name IN ('operations','sessions') AND sql LIKE '%BEFORE INSERT%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	rows.Close()
	for _, name := range names {
		if _, err = raw.ExecContext(ctx, `DROP TRIGGER `+name); err != nil {
			t.Fatal(err)
		}
	}
	return &journalFixture{store: store, raw: raw}
}

// operation records one journal row in creation order and returns its ID.
// Rows keep the shape the store's own transition rules accept: a project
// deletion is committed in ensure-absent, a failed-create cleanup carries its
// local-cleanup evidence, and a workspace loss inspection carries evidence. An
// empty phase selects that shape.
func (f *journalFixture) operation(t *testing.T, kind, status, phase, session string, committed bool) string {
	t.Helper()
	project := "app"
	if kind == "project.delete" {
		// One active deletion per project.
		project = fmt.Sprintf("app-%d", f.seq+1)
	}
	return f.operationIn(t, project, kind, status, phase, session, committed)
}

func (f *journalFixture) operationIn(t *testing.T, project, kind, status, phase, session string, committed bool) string {
	t.Helper()
	return f.operationWith(t, project, kind, status, phase, session, committed, "{}", nil)
}

func (f *journalFixture) operationWith(t *testing.T, project, kind, status, phase, session string, committed bool, request string, evidence any) string {
	t.Helper()
	f.seq++
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", f.seq)
	created := fmt.Sprintf("2026-01-01T%02d:%02d:%02dZ", f.seq/3600, f.seq/60%60, f.seq%60)
	switch {
	case phase != "":
	case kind == "project.delete":
		phase, committed = "ensure-absent", true
	case kind == "session.create.cleanup":
		phase, evidence = "local-cleanup", "{}"
	case kind == "workspace.loss.inspect":
		phase, evidence = "forged", "{}"
	default:
		phase = "forged"
	}
	if _, err := f.raw.Exec(`INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,committed,evidence_json,created_at,updated_at)
	 VALUES(?,?,?,?,NULLIF(?,''),?,'x',?,?,?,?,?,?)`, id, "key-"+id, kind, project, session, request, status, phase, committed, evidence, created, created); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *journalFixture) session(t *testing.T, uuid, project, registry string) {
	t.Helper()
	if _, err := f.raw.Exec(`INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256) VALUES(?,?,?,?,'{}','x')`,
		uuid, project, "b-"+uuid[len(uuid)-8:], registry); err != nil {
		t.Fatal(err)
	}
}

func sessionUUID(n int) string { return fmt.Sprintf("10000000-0000-4000-8000-%012d", n) }

func closedLifecycle(ctx context.Context, store *control.Store) *lifecycle {
	return &lifecycle{ctx: ctx, store: store, working: map[string]bool{}, queueSlots: make(chan struct{})}
}

func (l *lifecycle) queued() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.pending...)
}

func TestRetrySchedulesUnfinishedKnownOperations(t *testing.T) {
	type outcome struct {
		err    error
		status string
		queued bool
	}
	expect := func(status string) outcome {
		switch status {
		case "superseded":
			return outcome{control.ErrConflict, status, false}
		case "completed":
			return outcome{nil, status, false}
		case "blocked":
			return outcome{nil, "running", true}
		default:
			return outcome{nil, status, true}
		}
	}
	f := newJournalFixture(t)
	type row struct {
		name, id string
		want     outcome
	}
	var rows []row
	n := 0
	for _, kind := range dispatchKinds {
		for _, status := range dispatchStatuses {
			n++
			rows = append(rows, row{kind + " " + status, f.operation(t, kind, status, "", sessionUUID(n), false), expect(status)})
		}
	}
	n++
	rows = append(rows,
		row{"unknown kind", f.operation(t, "origin.change", "blocked", "", "", false), outcome{control.ErrInvalid, "blocked", false}},
		row{"cleaned workspace loss inspection", f.operation(t, "workspace.loss.inspect", "failed", "cleaned", sessionUUID(n), false), outcome{control.ErrConflict, "failed", false}},
		row{"stale failed-create cleanup", f.operation(t, "session.create.cleanup", "failed", "stale", sessionUUID(n+1), false), outcome{control.ErrConflict, "failed", false}},
	)
	inspected := sessionUUID(n + 2)
	f.operation(t, "workspace.loss.inspect", "running", "", inspected, false)
	rows = append(rows, row{"creation of an inspected session", f.operation(t, "session.create", "failed", "", inspected, false), outcome{control.ErrConflict, "failed", false}})

	for _, test := range rows {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			l := closedLifecycle(ctx, f.store)
			_, err := l.Retry(ctx, test.id)
			if !errors.Is(err, test.want.err) || (err == nil) != (test.want.err == nil) {
				t.Fatalf("answer %v, want %v", err, test.want.err)
			}
			got, err := f.store.GetOperation(ctx, test.id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != test.want.status {
				t.Errorf("status %s, want %s", got.Status, test.want.status)
			}
			if queued := reflect.DeepEqual(l.queued(), []string{test.id}); queued != test.want.queued || !queued && len(l.queued()) != 0 {
				t.Errorf("queued %q, want queued=%v", l.queued(), test.want.queued)
			}
		})
	}
}

func TestRecoverSchedulesUnfinishedOperationsInKindOrder(t *testing.T) {
	f := newJournalFixture(t)
	ids := map[string]string{}
	n := 0
	// Insert kinds in reverse, so a kind's position in the queue comes from
	// recovery's kind order rather than from the journal's row order.
	for i := len(dispatchKinds) - 1; i >= 0; i-- {
		kind := dispatchKinds[i]
		for _, status := range dispatchStatuses {
			n++
			ids[kind+" "+status] = f.operation(t, kind, status, "", sessionUUID(n), false)
		}
	}
	// A blocked early local creation stays dormant until an explicit Retry; a
	// later one is resumed. Both are newer than every row above.
	request, _ := json.Marshal(control.ReserveSessionRequest{Key: "early", Project: "app", Branch: "work", Choice: "new", Source: "refs/heads/main"})
	evidence, _ := json.Marshal(control.CreationEvidence{CapturedOID: strings.Repeat("a", 40), ImageFingerprint: strings.Repeat("d", 64), RefCASIntent: true, RuntimeInitState: "not-attempted"})
	f.operationWith(t, "app", "session.create", "blocked", "source-ready", sessionUUID(n+1), false, string(request), string(evidence))
	later := f.operationWith(t, "app", "session.create", "blocked", "runtime-created", sessionUUID(n+2), true, string(request), string(evidence))
	unfinished := func(kinds ...string) []string {
		var want []string
		for _, kind := range kinds {
			for _, status := range []string{"running", "blocked", "unknown"} {
				want = append(want, ids[kind+" "+status])
			}
		}
		return want
	}
	var want []string
	want = append(want, unfinished("session.create", "project.create")...)
	want = append(want, later)
	want = append(want, unfinished("project.delete", "workspace.loss.inspect",
		"session.create.cleanup", "session.delete", "session.discard", "session.rename",
		"project.retained.rename", "project.retained.delete", "session.repair", "session.repair.prepare",
		"session.ref.repair", "session.principal.repair", "session.record.repair")...)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := closedLifecycle(ctx, f.store)
	if err := l.Recover(); err != nil {
		t.Fatal(err)
	}
	if got := l.queued(); !reflect.DeepEqual(got, want) {
		var names []string
		for _, id := range got {
			for name, candidate := range ids {
				if candidate == id {
					names = append(names, name)
				}
			}
		}
		t.Fatalf("recovery queue:\n got %s\nwant %d operations", strings.Join(names, ", "), len(want))
	}
}

func TestRemovingSessionConditionFollowsItsRemovalOperation(t *testing.T) {
	f := newJournalFixture(t)
	ctx := context.Background()
	if err := f.store.CreateProject(ctx, "gone", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.raw.Exec(`UPDATE projects SET registry_state='deleting' WHERE path='gone'`); err != nil {
		t.Fatal(err)
	}
	l := closedLifecycle(ctx, f.store)
	for i, test := range []struct {
		name, project, registry, kind string
		want                          string
		err                           error
	}{
		{"creating", "app", "creating", "", "creating", nil},
		{"established", "app", "established", "", "", nil},
		{"discard", "app", "removing", "session.discard", "discarding", nil},
		{"failed-create cleanup", "app", "removing", "session.create.cleanup", "discarding", nil},
		{"delete", "app", "removing", "session.delete", "deleting", nil},
		{"record repair", "app", "removing", "session.record.repair", "deleting", nil},
		{"project delete", "gone", "removing", "project.delete", "deleting", nil},
		{"another kind", "app", "removing", "session.rename", "", control.ErrNotFound},
		{"no operation", "app", "removing", "", "", control.ErrNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			uuid := sessionUUID(900 + i)
			f.session(t, uuid, test.project, test.registry)
			if test.kind != "" {
				session := uuid
				if test.kind == "project.delete" {
					session = ""
				}
				f.operationIn(t, test.project, test.kind, "running", "", session, true)
			}
			s, err := f.store.GetSession(ctx, uuid)
			if err != nil {
				t.Fatal(err)
			}
			got, err := l.registryCondition(ctx, s)
			if got != test.want || !errors.Is(err, test.err) || (err == nil) != (test.err == nil) {
				t.Fatalf("condition %q %v, want %q %v", got, err, test.want, test.err)
			}
		})
	}
}

func TestSchedulingHoldsUnfinishedCreationDuringWorkspaceInspection(t *testing.T) {
	f := newJournalFixture(t)
	inspected := sessionUUID(800)
	f.operation(t, "workspace.loss.inspect", "running", "", inspected, false)
	for _, test := range []struct {
		status string
		err    error
	}{
		{"failed", control.ErrConflict},
		{"completed", nil},
		{"superseded", nil},
	} {
		t.Run(test.status, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			l := closedLifecycle(ctx, f.store)
			id := f.operation(t, "session.create", test.status, "", inspected, false)
			err := l.enqueue(id)
			if !errors.Is(err, test.err) || (err == nil) != (test.err == nil) {
				t.Fatalf("answer %v, want %v", err, test.err)
			}
			if queued := l.queued(); (err == nil) != (len(queued) == 1 && queued[0] == id) {
				t.Fatalf("queued %q", queued)
			}
		})
	}
}
