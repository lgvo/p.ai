package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

type removingSessionListLife struct {
	statusLife
	before          func(context.Context, string) error
	inspectErr      error
	requireCreation bool
}

func (l removingSessionListLife) InspectSession(ctx context.Context, id string) (SessionView, error) {
	if l.before != nil {
		if err := l.before(ctx, id); err != nil {
			return SessionView{}, err
		}
	}
	if l.inspectErr != nil {
		return SessionView{}, l.inspectErr
	}
	if l.requireCreation {
		if _, err := l.store.CreationForSession(ctx, id); err != nil {
			return SessionView{}, err
		}
	}
	return l.statusLife.InspectSession(ctx, id)
}

func committedListRemoval(t *testing.T) (*Store, Operation, []string) {
	t.Helper()
	s, ev, op := setupDiscardStore(t)
	ctx := context.Background()
	if err := s.SetGitRefGuard(ctx, ev.Project, ev.Branch, op.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceOperation(ctx, op.ID, "running", "validated", false, op.Evidence, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitDiscard(ctx, op.ID, func(context.Context, DiscardEvidence) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceOperation(ctx, op.ID, "running", "secrets-absent", true, op.Evidence, ""); err != nil {
		t.Fatal(err)
	}
	ids := []string{op.SessionUUID, "650e8400-e29b-41d4-a716-446655440000", "750e8400-e29b-41d4-a716-446655440000"}
	for i, id := range ids[1:] {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256)
			VALUES(?,'app',?,'established','{}',?)`, id, fmt.Sprintf("sibling-%d", i), ev.PolicySHA256); err != nil {
			t.Fatal(err)
		}
	}
	return s, op, ids
}

func completeListRemoval(ctx context.Context, s *Store, op Operation) error {
	return s.CompleteDiscard(ctx, op.ID, func(context.Context, string, string, string) error { return nil })
}

func TestListSessionsKeepsCompleteSnapshotAndRawCursor(t *testing.T) {
	s, op, ids := committedListRemoval(t)
	ctx := context.Background()
	page, next, err := s.ListSessions(ctx, "", 1)
	if err != nil || len(page) != 1 || next != ids[0] {
		t.Fatalf("snapshot page: %+v %q %v", page, next, err)
	}
	if page[0].UUID != ids[0] || page[0].Project != "app" || page[0].Branch != "main" || page[0].Registry != "removing" || string(page[0].Policy) != "{}" || page[0].PolicySHA256 != testDiscardEvidence().PolicySHA256 {
		t.Fatalf("incomplete snapshot: %+v", page[0])
	}
	if err := completeListRemoval(ctx, s, op); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, ids[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed snapshot member survived: %v", err)
	}
	siblings, later, err := s.ListSessions(ctx, next, 2)
	if err != nil || len(siblings) != 2 || siblings[0].UUID != ids[1] || siblings[1].UUID != ids[2] || later != "" {
		t.Fatalf("retained siblings after snapshot removal: %+v %q %v", siblings, later, err)
	}
}

func TestSessionListConcurrentCompletionSkipsOnlyAbsentRecord(t *testing.T) {
	for _, limit := range []int{1, 2} {
		t.Run(fmt.Sprintf("limit-%d", limit), func(t *testing.T) {
			s, op, ids := committedListRemoval(t)
			removed := false
			life := removingSessionListLife{statusLife: statusLife{s}, before: func(ctx context.Context, id string) error {
				if id == ids[0] {
					removed = true
					// Deterministically finish the real durable removal between
					// Store.ListSessions and the lifecycle's record re-read.
					return completeListRemoval(ctx, s, op)
				}
				return nil
			}}
			h := StateHandlerWithLifecycle(s, nil, nil, life)
			result, rpcErr := h(context.Background(), "session.list", json.RawMessage(fmt.Sprintf(`{"v":1,"limit":%d}`, limit)))
			if rpcErr != nil || !removed {
				t.Fatalf("concurrent completion failed list: %+v %v", result, rpcErr)
			}
			page := result.(map[string]any)
			views := page["sessions"].([]SessionView)
			if len(views) != limit-1 || page["next"] != ids[limit-1] {
				t.Fatalf("filtered page lost raw cursor: %+v", page)
			}
			if limit == 2 && (views[0].UUID != ids[1] || views[0].Condition != "stopped") {
				t.Fatalf("retained sibling missing: %+v", views)
			}
			result, rpcErr = h(context.Background(), "session.list", json.RawMessage(fmt.Sprintf(`{"v":1,"limit":2,"after":%q}`, page["next"])))
			if rpcErr != nil {
				t.Fatal(rpcErr)
			}
			following := result.(map[string]any)["sessions"].([]SessionView)
			if len(following) != 3-limit || following[len(following)-1].UUID != ids[2] {
				t.Fatalf("following page lost surviving record: %+v", following)
			}
		})
	}
}

func TestSessionListPreservesSurvivingMissingAuthorityAndOtherErrors(t *testing.T) {
	for _, missingAuthority := range []bool{true, false} {
		t.Run(fmt.Sprintf("missing-creation-%t", missingAuthority), func(t *testing.T) {
			s, _, ids := committedListRemoval(t)
			life := removingSessionListLife{statusLife: statusLife{s}, requireCreation: missingAuthority}
			if !missingAuthority {
				life.inspectErr = ErrConflict
			}
			h := StateHandlerWithLifecycle(s, nil, nil, life)
			result, rpcErr := h(context.Background(), "session.list", json.RawMessage(`{"v":1,"limit":1}`))
			if rpcErr == nil || result != nil {
				t.Fatalf("surviving record error hidden: %+v %+v", result, rpcErr)
			}
			if _, err := s.GetSession(context.Background(), ids[0]); err != nil {
				t.Fatalf("authority failure removed session: %v", err)
			}
			if missingAuthority && rpcErr.Kind != "unavailable" || !missingAuthority && rpcErr.Kind != "busy" {
				t.Fatalf("error classification changed: %+v", rpcErr)
			}
		})
	}
}

func TestSessionListDoesNotHideNonNotFoundErrorAfterRemoval(t *testing.T) {
	s, op, ids := committedListRemoval(t)
	life := removingSessionListLife{statusLife: statusLife{s}, inspectErr: ErrConflict,
		before: func(ctx context.Context, id string) error { return completeListRemoval(ctx, s, op) }}
	h := StateHandlerWithLifecycle(s, nil, nil, life)
	result, rpcErr := h(context.Background(), "session.list", json.RawMessage(`{"v":1,"limit":1}`))
	if result != nil || rpcErr == nil || rpcErr.Kind != "busy" {
		t.Fatalf("non-not-found error hidden by absent record: %+v %+v", result, rpcErr)
	}
	if _, err := s.GetSession(context.Background(), ids[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("fault did not complete concurrent removal: %v", err)
	}
}

func TestSessionListPreservesStatusStorageError(t *testing.T) {
	s, id := statusFixture(t)
	if _, err := s.db.Exec(`DROP TABLE session_unattended`); err != nil {
		t.Fatal(err)
	}
	h := StateHandlerWithLifecycle(s, nil, nil, statusLife{s})
	result, rpcErr := h(context.Background(), "session.list", json.RawMessage(`{"v":1,"limit":1}`))
	if result != nil || rpcErr == nil || rpcErr.Kind != "unavailable" {
		t.Fatalf("status storage failure hidden: %+v %+v", result, rpcErr)
	}
	if _, err := s.GetSession(context.Background(), id); err != nil {
		t.Fatalf("status failure removed surviving record: %v", err)
	}
}
