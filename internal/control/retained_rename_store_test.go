package control

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRetainedRenameGuardsBothNamesAndSurvivesRestart(t *testing.T) {
	s, dir := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	r := RetainedRenameRequest{Key: "retained-once", Project: "app", OldBranch: "saved", NewBranch: "archive", ExpectedOldTip: strings.Repeat("a", 40)}
	calls := 0
	op, err := s.BeginRetainedRename(ctx, r, func(context.Context) error { calls++; return nil })
	if err != nil || op.Phase != "reserved" || calls != 1 {
		t.Fatalf("begin: %+v %v calls=%d", op, err, calls)
	}
	for _, branch := range []string{r.OldBranch, r.NewBranch} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256) VALUES(?,? ,?,'established','{}',?)`, "550e8400-e29b-41d4-a716-446655440000", r.Project, branch, strings.Repeat("b", 64)); err == nil {
			t.Fatalf("assigned guarded branch %s", branch)
		}
	}
	if again, err := s.BeginRetainedRename(ctx, r, func(context.Context) error { calls++; return nil }); err != nil || again.ID != op.ID || calls != 1 {
		t.Fatalf("replay: %+v %v calls=%d", again, err, calls)
	}
	changed := r
	changed.NewBranch = "other"
	if _, err := s.BeginRetainedRename(ctx, changed, func(context.Context) error { return nil }); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed key accepted: %v", err)
	}
	if err := s.TransitionRetainedRename(ctx, op.ID, "reserved", "new-ref-create-issued", true, func(context.Context, RetainedRenameRequest) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	unfinished, err := reopened.UnfinishedRetainedRenames(ctx)
	if err != nil || len(unfinished) != 1 || unfinished[0].Phase != "new-ref-create-issued" || !unfinished[0].Committed {
		t.Fatalf("recovery: %+v %v", unfinished, err)
	}
	for _, pair := range [][2]string{{"new-ref-create-issued", "new-ref-created"}, {"new-ref-created", "old-ref-delete-issued"}, {"old-ref-delete-issued", "old-ref-deleted"}, {"old-ref-deleted", "completed"}} {
		if err := reopened.TransitionRetainedRename(ctx, op.ID, pair[0], pair[1], true, func(_ context.Context, saved RetainedRenameRequest) error {
			if saved != r {
				t.Fatal("request changed")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	completed, err := reopened.GetOperation(ctx, op.ID)
	if err != nil || completed.Status != "completed" || completed.Phase != "completed" {
		t.Fatalf("complete: %+v %v", completed, err)
	}
	for _, branch := range []string{r.OldBranch, r.NewBranch} {
		var count int
		if err := reopened.db.QueryRowContext(ctx, `SELECT count(*) FROM git_ref_guards WHERE project_path=? AND branch=?`, r.Project, branch).Scan(&count); err != nil || count != 0 {
			t.Fatalf("guard %s survived: %d %v", branch, count, err)
		}
	}
}

func TestRetainedRenameStaleBeforeEffectReleasesButIssuedCannot(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	r := RetainedRenameRequest{Key: "stale", Project: "app", OldBranch: "saved", NewBranch: "archive", ExpectedOldTip: strings.Repeat("a", 40)}
	op, err := s.BeginRetainedRename(ctx, r, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TransitionRetainedRename(ctx, op.ID, "reserved", "new-ref-create-issued", true, func(context.Context, RetainedRenameRequest) error { return ErrConflict }); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale observed: %v", err)
	}
	if err := s.FailRetainedRenamePreEffect(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	failed, err := s.GetOperation(ctx, op.ID)
	if err != nil || failed.Status != "failed" || failed.Committed {
		t.Fatalf("failed: %+v %v", failed, err)
	}
	r.Key = "issued"
	op, err = s.BeginRetainedRename(ctx, r, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TransitionRetainedRename(ctx, op.ID, "reserved", "new-ref-create-issued", true, func(context.Context, RetainedRenameRequest) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.FailRetainedRenamePreEffect(ctx, op.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("issued intent rolled back: %v", err)
	}
}
