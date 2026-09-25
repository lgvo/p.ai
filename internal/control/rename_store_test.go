package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const renameTestUUID = "550e8400-e29b-41d4-a716-446655440000"

func renameStoreFixture(t *testing.T) (*Store, string, RenameRequest, RenameEvidence) {
	t.Helper()
	s, dir := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	policy, err := s.ProjectPolicySHA(ctx, "app")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256)
	 VALUES(?,'app','main','established','{}',?)`, renameTestUUID, policy); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterGitPrincipal(ctx, strings.Repeat("f", 64), "session", "app", renameTestUUID); err != nil {
		t.Fatal(err)
	}
	r := RenameRequest{Key: "rename-once", UUID: renameTestUUID, NewBranch: "renamed", ExpectedOldTip: strings.Repeat("a", 40)}
	e := RenameEvidence{Project: "app", OldBranch: "main", NewBranch: "renamed", OldTip: r.ExpectedOldTip,
		PolicySHA256: policy, InstanceUUID: "11111111-1111-4111-8111-111111111111",
		ImageFingerprint: strings.Repeat("b", 64), BaseFingerprint: strings.Repeat("c", 64),
		IncusUUID: "22222222-2222-4222-8222-222222222222", Generation: "33333333-3333-4333-8333-333333333333", OriginalStatus: "Running"}
	return s, dir, r, e
}

func renameAdvance(t *testing.T, s *Store, op Operation, ev RenameEvidence, phase string, committed bool) Operation {
	t.Helper()
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceOperation(context.Background(), op.ID, "running", phase, committed, raw, ""); err != nil {
		t.Fatal(err)
	}
	op, err = s.GetOperation(context.Background(), op.ID)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func TestRenameReservesBothNamesAndReplaysOnlyExactRequest(t *testing.T) {
	s, _, r, ev := renameStoreFixture(t)
	ctx := context.Background()
	observations := 0
	observe := func(context.Context) error { observations++; return nil }
	op, err := s.BeginRename(ctx, r, ev, observe)
	if err != nil || op.Phase != "reserved" || op.Committed {
		t.Fatalf("begin: %+v %v", op, err)
	}
	if observations != 1 {
		t.Fatal("ref observation omitted")
	}
	for _, name := range []string{"main", "renamed"} {
		var owner string
		if err := s.db.QueryRowContext(ctx, `SELECT operation_id FROM git_ref_guards WHERE project_path='app' AND branch=?`, name).Scan(&owner); err != nil || owner != op.ID {
			t.Fatalf("guard %s: %q %v", name, owner, err)
		}
	}
	if _, err := s.PublicationSource(ctx, PublicationRequest{Project: "app", Kind: "retained", Source: "renamed",
		SourceOID: r.ExpectedOldTip, DestinationRef: "refs/heads/renamed", ExpectedOriginURL: "ssh://host/repo"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("unassigned but guarded new ref published as retained: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256)
	 VALUES('77777777-7777-4777-8777-777777777777','app','renamed','established','{}',?)`, ev.PolicySHA256); err == nil {
		t.Fatal("reserved name accepted for concurrent creation")
	}
	if replay, err := s.BeginRename(ctx, r, ev, observe); err != nil || replay.ID != op.ID || observations != 1 {
		t.Fatalf("replay changed intent or repeated observation: %+v %v", replay, err)
	}
	r.NewBranch = "different"
	if _, err := s.BeginRename(ctx, r, ev, observe); !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrConflict) {
		t.Fatalf("changed key request accepted: %v", err)
	}
}

func TestRenamePostCommitForwardSurvivesSQLiteRestart(t *testing.T) {
	s, dir, r, ev := renameStoreFixture(t)
	ctx := context.Background()
	op, err := s.BeginRename(ctx, r, ev, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	ev.WorkspaceTip, ev.ConfigAfterSHA256 = strings.Repeat("d", 40), strings.Repeat("e", 64)
	op = renameAdvance(t, s, op, ev, "new-ref-created", true)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	unfinished, err := reopened.UnfinishedRenames(ctx)
	if err != nil || len(unfinished) != 1 || unfinished[0].ID != op.ID || !unfinished[0].Committed {
		t.Fatalf("committed rename lost at restart: %+v %v", unfinished, err)
	}
	op = renameAdvance(t, reopened, op, ev, "workspace-renamed", true)
	if err := reopened.UpdateRenameAssignment(ctx, op.ID, func(_ context.Context, got RenameEvidence) error {
		if got != ev {
			t.Fatal("assignment lost captured evidence")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	session, err := reopened.GetSession(ctx, r.UUID)
	if err != nil || session.Branch != "renamed" || session.Registry != "established" {
		t.Fatalf("assignment: %+v %v", session, err)
	}
	active, err := reopened.IsSessionGitPrincipalActive(ctx, r.UUID)
	if err != nil || !active {
		t.Fatalf("principal changed during rename: %v %v", active, err)
	}
	op = renameAdvance(t, reopened, op, ev, "source-restored", true)
	if err := reopened.CompleteRename(ctx, op.ID, func(_ context.Context, got RenameEvidence) error {
		if got != ev {
			t.Fatal("completion lost captured evidence")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	completed, err := reopened.GetOperation(ctx, op.ID)
	if err != nil || completed.Status != "completed" || completed.Phase != "completed" || !completed.Committed {
		t.Fatalf("completed rename: %+v %v", completed, err)
	}
	for _, branch := range []string{"main", "renamed"} {
		var count int
		if err := reopened.db.QueryRowContext(ctx, `SELECT count(*) FROM git_ref_guards WHERE project_path='app' AND branch=?`, branch).Scan(&count); err != nil || count != 0 {
			t.Fatalf("guard %s survived completion: %d %v", branch, count, err)
		}
	}
}

func TestRenamePrecommitFailureReleasesGuardsButCreateIssuedRetainsThem(t *testing.T) {
	s, _, r, ev := renameStoreFixture(t)
	ctx := context.Background()
	op, err := s.BeginRename(ctx, r, ev, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FailRenamePrecommit(ctx, op.ID, "stale old tip"); err != nil {
		t.Fatal(err)
	}
	failed, err := s.GetOperation(ctx, op.ID)
	if err != nil || failed.Status != "failed" || failed.Committed {
		t.Fatalf("precommit rollback: %+v %v", failed, err)
	}
	r.Key = "rename-unknown"
	op, err = s.BeginRename(ctx, r, ev, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	op = renameAdvance(t, s, op, ev, "prepare-issued", false)
	if err := s.FailRenamePrecommit(ctx, op.ID, "backup POST timed out"); !errors.Is(err, ErrConflict) {
		t.Fatalf("uncertain backup write released reservation: %v", err)
	}
	op = renameAdvance(t, s, op, ev, "new-ref-create-issued", false)
	if err := s.FailRenamePrecommit(ctx, op.ID, "no immediate new ref"); !errors.Is(err, ErrConflict) {
		t.Fatalf("uncertain Git effect released reservation: %v", err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM git_ref_guards WHERE operation_id=?`, op.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("uncertain guard count: %d %v", count, err)
	}
}
