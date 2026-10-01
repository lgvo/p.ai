package control

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func retainedDeleteFixture(t *testing.T) (*Store, string, RetainedDeleteRequest, RetainedDeleteEvidence) {
	t.Helper()
	s, dir := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	r := RetainedDeleteRequest{Key: "delete-retained", Project: "app", Branch: "saved", TokenSHA256: strings.Repeat("a", 64)}
	e := RetainedDeleteEvidence{Project: "app", Branch: "saved", Tip: strings.Repeat("b", 40), ReviewSHA256: strings.Repeat("c", 64), InstanceUUID: "11111111-1111-4111-8111-111111111111", PreviewExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}
	return s, dir, r, e
}

func TestRetainedDeleteGuardedEffectAndRestart(t *testing.T) {
	s, dir, r, e := retainedDeleteFixture(t)
	ctx := context.Background()
	verified := 0
	verify := func(_ context.Context, got RetainedDeleteEvidence) error {
		verified++
		if got != e {
			t.Fatal("review evidence changed")
		}
		return nil
	}
	op, err := s.BeginRetainedDelete(ctx, r, e, verify)
	if err != nil || op.Phase != "guarded" || verified != 1 {
		t.Fatalf("begin: %+v %v", op, err)
	}
	var owner string
	if err := s.db.QueryRowContext(ctx, `SELECT operation_id FROM git_ref_guards WHERE project_path='app' AND branch='saved'`).Scan(&owner); err != nil || owner != op.ID {
		t.Fatalf("guard: %q %v", owner, err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256) VALUES('550e8400-e29b-41d4-a716-446655440000','app','saved','established','{}',?)`, strings.Repeat("d", 64)); err == nil {
		t.Fatal("guarded branch assigned")
	}
	if replay, err := s.BeginRetainedDelete(ctx, r, e, verify); err != nil || replay.ID != op.ID || verified != 1 {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	effectCalls := 0
	if err := s.IssueRetainedDelete(ctx, op.ID, verify, func() error { effectCalls++; return errors.New("uncertain Git outcome") }); err == nil || effectCalls != 1 {
		t.Fatalf("effect: %d %v", effectCalls, err)
	}
	if err := s.IssueRetainedDelete(ctx, op.ID, verify, func() error { effectCalls++; return nil }); !errors.Is(err, ErrConflict) || effectCalls != 1 {
		t.Fatalf("reissued uncertain effect: %d %v", effectCalls, err)
	}
	if err := s.FailRetainedDeleteStale(ctx, op.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("issued intent rolled back: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	unfinished, err := reopened.UnfinishedRetainedDeletes(ctx)
	if err != nil || len(unfinished) != 1 || unfinished[0].Phase != "ref-delete-issued" || !unfinished[0].Committed {
		t.Fatalf("restart: %+v %v", unfinished, err)
	}
	if err := reopened.CompleteRetainedDelete(ctx, op.ID, verify); err != nil {
		t.Fatal(err)
	}
	completed, err := reopened.GetOperation(ctx, op.ID)
	if err != nil || completed.Status != "completed" || completed.Phase != "completed" {
		t.Fatalf("complete: %+v %v", completed, err)
	}
	var count int
	if err := reopened.db.QueryRowContext(ctx, `SELECT count(*) FROM git_ref_guards WHERE project_path='app' AND branch='saved'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("guard survived: %d %v", count, err)
	}
}

func TestRetainedDeleteStaleAndExpiredReviewNeverIssue(t *testing.T) {
	s, _, r, e := retainedDeleteFixture(t)
	ctx := context.Background()
	e.PreviewExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	if _, err := s.BeginRetainedDelete(ctx, r, e, func(context.Context, RetainedDeleteEvidence) error { return nil }); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired accepted: %v", err)
	}
	e.PreviewExpiresAt = time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
	op, err := s.BeginRetainedDelete(ctx, r, e, func(context.Context, RetainedDeleteEvidence) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	effect := false
	if err := s.IssueRetainedDelete(ctx, op.ID, func(context.Context, RetainedDeleteEvidence) error { return ErrConflict }, func() error { effect = true; return nil }); !errors.Is(err, ErrConflict) || effect {
		t.Fatalf("stale issued effect: %v %t", err, effect)
	}
	if err := s.FailRetainedDeleteStale(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	failed, err := s.GetOperation(ctx, op.ID)
	if err != nil || failed.Status != "failed" || failed.Phase != "stale" || failed.Committed {
		t.Fatalf("stale: %+v %v", failed, err)
	}
	r.Key = "assigned"
	if _, err := s.db.ExecContext(ctx, `INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256) VALUES('550e8400-e29b-41d4-a716-446655440000','app','saved','established','{}',?)`, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginRetainedDelete(ctx, r, e, func(context.Context, RetainedDeleteEvidence) error { return nil }); !errors.Is(err, ErrConflict) {
		t.Fatalf("assigned branch accepted: %v", err)
	}
}
