package control

import (
	"context"
	"errors"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
)

const startStopSession = "550e8400-e29b-41d4-a716-446655440000"

// startStopStore is a store with project app and its established session
// startStopSession on branch main.
func startStopStore(t *testing.T) *Store {
	t.Helper()
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s, `INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256)
	 VALUES('`+startStopSession+`','app','main','established','{}','x')`)
	return s
}

func startStopRows(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM operations WHERE session_uuid=? AND kind IN ('session.start','session.stop')`, startStopSession).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestStartAndStopRecordOneOperationAndKeepOnlyTheNewestFinished(t *testing.T) {
	s := startStopStore(t)
	ctx := context.Background()
	start, recorded, err := s.BeginSessionChange(ctx, KindSessionStart, startStopSession)
	if err != nil || !recorded || start.Kind != KindSessionStart || start.Status != "running" || start.Key != start.ID {
		t.Fatalf("start: %+v recorded=%v %v", start, recorded, err)
	}
	checkIntent(t, s, start, api.OperationIntent{Project: "app", Session: startStopSession, Branch: "main"})
	// A start while one is unfinished returns that one and records nothing.
	again, recorded, err := s.BeginSessionChange(ctx, KindSessionStart, startStopSession)
	if err != nil || recorded || again.ID != start.ID || startStopRows(t, s) != 1 {
		t.Fatalf("repeated start: %+v recorded=%v %v", again, recorded, err)
	}
	// A stop competes with the unfinished start.
	if _, _, err = s.BeginSessionChange(ctx, KindSessionStop, startStopSession); answerError(ctx, err).Kind() != api.KindRetry ||
		answerError(ctx, err).Target() != api.TargetOperation(start.ID) {
		t.Fatalf("stop during a start answered %v", err)
	}
	if err = s.AdvanceOperation(ctx, start.ID, "failed", "starting", false, nil, api.ReasonRuntime, "host did not become ready"); err != nil {
		t.Fatal(err)
	}
	latest, found, err := s.LatestSessionChange(ctx, startStopSession)
	if err != nil || !found || latest.ID != start.ID || latest.Diagnostic != "host did not become ready" {
		t.Fatalf("latest after the failed start: %+v %v %v", latest, found, err)
	}
	// Recording the next change deletes the finished one.
	stop, recorded, err := s.BeginSessionChange(ctx, KindSessionStop, startStopSession)
	if err != nil || !recorded || stop.Kind != KindSessionStop {
		t.Fatalf("stop: %+v recorded=%v %v", stop, recorded, err)
	}
	if _, err = s.GetOperation(ctx, start.ID); !errors.Is(err, ErrNotFound) || startStopRows(t, s) != 1 {
		t.Fatalf("the older finished start was kept: %v", err)
	}
	latest, _, _ = s.LatestSessionChange(ctx, startStopSession)
	if latest.ID != stop.ID {
		t.Fatalf("latest is %s, want the stop", latest.ID)
	}
}

func TestStartAndStopNeedAnEstablishedSessionFreeOfOtherWork(t *testing.T) {
	s := startStopStore(t)
	ctx := context.Background()
	if _, _, err := s.BeginSessionChange(ctx, KindSessionStart, "11111111-1111-4111-8111-111111111111"); answerError(ctx, err).Kind() != api.KindNotFound {
		t.Fatalf("unknown session answered %v", err)
	}
	holdSession(t, s, startStopSession, "blocked")
	if _, _, err := s.BeginSessionChange(ctx, KindSessionStart, startStopSession); answerError(ctx, err).Reason() != api.ReasonOperationBlocked {
		t.Fatalf("a session held by a blocked operation answered %v", err)
	}
	mustExec(t, s, `UPDATE operations SET status='completed' WHERE id='op-holder'`)
	mustExec(t, s, `UPDATE sessions SET registry_state='removing' WHERE uuid='`+startStopSession+`'`)
	if _, _, err := s.BeginSessionChange(ctx, KindSessionStop, startStopSession); answerError(ctx, err).Reason() != api.ReasonSessionNotEstablished {
		t.Fatalf("a session being removed answered %v", err)
	}
	if startStopRows(t, s) != 0 {
		t.Fatal("a refused start or stop recorded an operation")
	}
}
