package control

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

func testDiscardEvidence() DiscardEvidence {
	return DiscardEvidence{
		Action:  "discard",
		Project: "app", Branch: "main", AssignedTip: strings.Repeat("a", 40),
		PolicySHA256: strings.Repeat("b", 64), IncusProject: "user-1000",
		InstanceName:     "p-550e8400-e29b-41d4-a716-446655440000",
		InstanceUUID:     "11111111-1111-4111-8111-111111111111",
		ImageFingerprint: strings.Repeat("c", 64), BaseFingerprint: strings.Repeat("d", 64),
		IncusUUID: "22222222-2222-4222-8222-222222222222", Generation: "33333333-3333-4333-8333-333333333333",
		PRefsDigest: strings.Repeat("1", 64),
	}
}

func TestDiscardExpiredAtTransactionalAdmissionLeavesNoGuard(t *testing.T) {
	s, ev, first := setupDiscardStore(t)
	if err := s.FailStaleDiscard(context.Background(), first.ID, "first review stale"); err != nil {
		t.Fatal(err)
	}
	_, err := s.BeginDiscard(context.Background(), DiscardRequest{Key: "expired-discard", UUID: "550e8400-e29b-41d4-a716-446655440000", IfMatch: testTag(-time.Second)}, ev, testHash, noRecheck)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expired confirmation accepted: %v", err)
	}
	if _, err := s.GetOperationByKey(context.Background(), "expired-discard"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired intent persisted: %v", err)
	}
}

func setupDiscardStore(t *testing.T) (*Store, DiscardEvidence, api.Operation) {
	t.Helper()
	s, _ := openTestStore(t)
	ctx := context.Background()
	ev := testDiscardEvidence()
	if err := s.CreateProject(ctx, ev.Project, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256)
	 VALUES(?,'app','main','established','{}',?)`, "550e8400-e29b-41d4-a716-446655440000", ev.PolicySHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RegisterGitPrincipal(ctx, strings.Repeat("f", 64), "session", ev.Project, "550e8400-e29b-41d4-a716-446655440000"); err != nil {
		t.Fatal(err)
	}
	op, err := s.BeginDiscard(ctx, DiscardRequest{Key: "discard-1", UUID: "550e8400-e29b-41d4-a716-446655440000", IfMatch: testTag(time.Minute)}, ev, testHash, noRecheck)
	if err != nil {
		t.Fatal(err)
	}
	checkIntent(t, s, op, api.OperationIntent{Project: ev.Project, Session: "550e8400-e29b-41d4-a716-446655440000", Branch: ev.Branch})
	return s, ev, op
}

func TestDiscardStaleRollbackReleasesOnlyOwnGuard(t *testing.T) {
	s, ev, op := setupDiscardStore(t)
	ctx := context.Background()
	if err := s.SetGitRefGuard(ctx, ev.Project, ev.Branch, op.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceOperation(ctx, op.ID, "running", "guarded", false, op.Evidence, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.FailStaleDiscard(ctx, op.ID, "content changed"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetOperation(ctx, op.ID)
	if err != nil || got.Status != "failed" || got.Phase != "stale" || got.Committed {
		t.Fatalf("stale state: %+v %v", got, err)
	}
	if _, err := s.GetSession(ctx, op.SessionUUID); err != nil {
		t.Fatal(err)
	}
	active, err := s.IsSessionGitPrincipalActive(ctx, op.SessionUUID)
	if err != nil || !active {
		t.Fatalf("principal changed on stale rollback: %t %v", active, err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM git_ref_guards WHERE project_path='app' AND branch='main'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale guard remains: %d %v", count, err)
	}
}

func TestDiscardCommitAndExactForwardCompletion(t *testing.T) {
	s, ev, op := setupDiscardStore(t)
	ctx := context.Background()
	holdStatusState(t, s, op.SessionUUID)
	if err := s.SetGitRefGuard(ctx, ev.Project, ev.Branch, op.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceOperation(ctx, op.ID, "running", "validated", false, op.Evidence, "", ""); err != nil {
		t.Fatal(err)
	}
	verify := func(_ context.Context, got DiscardEvidence) error {
		if got.PRefsDigest != ev.PRefsDigest {
			t.Fatal("fresh P refs digest changed")
		}
		return nil
	}
	if err := s.CommitDiscard(ctx, op.ID, verify); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitDiscard(ctx, op.ID, verify); err != nil {
		t.Fatalf("commit replay: %v", err)
	}
	got, err := s.GetOperation(ctx, op.ID)
	if err != nil || got.Phase != "removal-committed" || !got.Committed {
		t.Fatalf("commit state: %+v %v", got, err)
	}
	current, err := s.GetSession(ctx, op.SessionUUID)
	if err != nil || current.Registry != "removing" {
		t.Fatalf("session not disabled: %+v %v", current, err)
	}
	active, err := s.IsSessionGitPrincipalActive(ctx, op.SessionUUID)
	if err != nil || active {
		t.Fatalf("principal not disabled: %t %v", active, err)
	}
	if err := s.FailStaleDiscard(ctx, op.ID, "late stale"); !errors.Is(err, ErrConflict) {
		t.Fatalf("committed removal rolled back: %v", err)
	}
	if err := s.AdvanceOperation(ctx, op.ID, "blocked", "delete-issued", true, op.Evidence, "internal", "native outcome unknown"); err != nil {
		t.Fatal(err)
	}
	unresolved, err := unfinishedOf(ctx, s, KindSessionDiscard, KindSessionDelete, KindCreateCleanup)
	if err != nil || len(unresolved) != 1 || unresolved[0].Phase != "delete-issued" {
		t.Fatalf("restart tombstone missing: %+v %v", unresolved, err)
	}
	if err := s.AdvanceOperation(ctx, op.ID, "running", "secrets-absent", true, op.Evidence, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDiscard(ctx, op.ID, func(_ context.Context, project, branch, tip string) error {
		if project != ev.Project || branch != ev.Branch || tip != ev.AssignedTip {
			t.Fatalf("wrong guarded ref: %s %s %s", project, branch, tip)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, op.SessionUUID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("assignment survived: %v", err)
	}
	requireStatusStateReleased(t, s, op.SessionUUID)
	if err := s.CompleteDiscard(ctx, op.ID, func(context.Context, string, string, string) error {
		t.Fatal("completed replay rechecked ref")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDiscardChangedSiblingRefCannotCrossCommit(t *testing.T) {
	s, ev, op := setupDiscardStore(t)
	ctx := context.Background()
	if err := s.SetGitRefGuard(ctx, ev.Project, ev.Branch, op.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceOperation(ctx, op.ID, "running", "validated", false, op.Evidence, "", ""); err != nil {
		t.Fatal(err)
	}
	changed := errors.New("sibling P ref changed after analysis")
	if err := s.CommitDiscard(ctx, op.ID, func(context.Context, DiscardEvidence) error { return changed }); !errors.Is(err, changed) {
		t.Fatalf("changed sibling ref crossed commit: %v", err)
	}
	got, err := s.GetOperation(ctx, op.ID)
	if err != nil || got.Committed || got.Phase != "validated" {
		t.Fatalf("commit marker advanced: %+v %v", got, err)
	}
	current, err := s.GetSession(ctx, op.SessionUUID)
	if err != nil || current.Registry != "established" {
		t.Fatalf("assignment revoked on stale refs: %+v %v", current, err)
	}
	active, err := s.IsSessionGitPrincipalActive(ctx, op.SessionUUID)
	if err != nil || !active {
		t.Fatalf("principal disabled on stale refs: %t %v", active, err)
	}
}

func TestDeleteDurableCommitBranchCASConflictAndExactCompletion(t *testing.T) {
	s, ev, discarded := setupDiscardStore(t)
	ctx := context.Background()
	if err := s.FailStaleDiscard(ctx, discarded.ID, "first action not accepted"); err != nil {
		t.Fatal(err)
	}
	ev.Action, ev.DeleteReviewSHA256 = "delete", strings.Repeat("2", 64)
	request := DiscardRequest{Key: "delete-1", UUID: discarded.SessionUUID, IfMatch: testTag(time.Minute)}
	if _, err := s.BeginDiscard(ctx, request, ev, testHash, noRecheck); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong action accepted by Discard: %v", err)
	}
	op, err := s.BeginDelete(ctx, request, ev, testHash, noRecheck)
	if err != nil || op.Kind != "session.delete" {
		t.Fatalf("delete intent unavailable: %+v %v", op, err)
	}
	holdStatusState(t, s, op.SessionUUID)
	if _, err := s.BeginDiscard(ctx, request, testDiscardEvidence(), testHash, noRecheck); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-action key replay accepted: %v", err)
	}
	if err := s.SetGitRefGuard(ctx, ev.Project, ev.Branch, op.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceOperation(ctx, op.ID, "running", "validated", false, op.Evidence, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitDelete(ctx, op.ID, func(_ context.Context, got DiscardEvidence) error {
		if got.Action != "delete" || got.DeleteReviewSHA256 != ev.DeleteReviewSHA256 {
			t.Fatal("review binding changed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceOperation(ctx, op.ID, "blocked", "branch-delete-issued", true, op.Evidence, "internal", "CAS outcome unknown"); err != nil {
		t.Fatal(err)
	}
	unresolved, err := unfinishedOf(ctx, s, KindSessionDiscard, KindSessionDelete, KindCreateCleanup)
	if err != nil || len(unresolved) != 1 || unresolved[0].Phase != "branch-delete-issued" {
		t.Fatalf("delete recovery marker missing: %+v %v", unresolved, err)
	}
	if err := s.AdvanceOperation(ctx, op.ID, "running", "branch-absent", true, op.Evidence, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDelete(ctx, op.ID, func(context.Context, string, string, string) error { return ErrConflict }); !errors.Is(err, ErrConflict) {
		t.Fatalf("branch mismatch completed: %v", err)
	}
	current, err := s.GetSession(ctx, op.SessionUUID)
	if err != nil || current.Registry != "removing" {
		t.Fatalf("branch conflict released assignment: %+v %v", current, err)
	}
	if err := s.CompleteDelete(ctx, op.ID, func(_ context.Context, project, branch, tip string) error {
		if project != ev.Project || branch != ev.Branch || tip != ev.AssignedTip {
			t.Fatal("wrong branch CAS evidence")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession(ctx, op.SessionUUID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted assignment remains: %v", err)
	}
	requireStatusStateReleased(t, s, op.SessionUUID)
	got, err := s.GetOperation(ctx, op.ID)
	if err != nil || got.Status != "completed" || got.Phase != "deleted" || !got.Committed {
		t.Fatalf("delete completion unavailable: %+v %v", got, err)
	}
}

func TestDeleteAdmissionRequiresEstablishedSession(t *testing.T) {
	s, _ := openTestStore(t)
	_, err := s.db.ExecContext(context.Background(), `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,intent_json,status,phase,committed,created_at,updated_at)
	 VALUES('x','x','session.delete','app','550e8400-e29b-41d4-a716-446655440000','{}','x','{}','running','guard-pending',0,'2026','2026')`)
	if err == nil {
		t.Fatal("delete operation without established session bypassed admission guard")
	}
}

func TestRemovalDoesNotDependOnTheSessionGitPrincipal(t *testing.T) {
	const uuid = "550e8400-e29b-41d4-a716-446655440000"
	for _, action := range []string{"discard", "delete"} {
		for _, principal := range []string{"active", "revoked", "missing"} {
			t.Run(action+"/"+principal, func(t *testing.T) {
				s, _ := openTestStore(t)
				ctx := context.Background()
				ev := testDiscardEvidence()
				ev.Action = action
				if action == "delete" {
					ev.DeleteReviewSHA256 = strings.Repeat("9", 64)
				}
				if err := s.CreateProject(ctx, ev.Project, []byte(`{}`)); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.ExecContext(ctx, `INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256)
				 VALUES(?,'app','main','established','{}',?)`, uuid, ev.PolicySHA256); err != nil {
					t.Fatal(err)
				}
				if principal != "missing" {
					if err := s.RegisterGitPrincipal(ctx, strings.Repeat("f", 64), "session", ev.Project, uuid); err != nil {
						t.Fatal(err)
					}
				}
				if principal == "revoked" {
					if _, err := s.db.ExecContext(ctx, `UPDATE git_principals SET active=0 WHERE session_uuid=?`, uuid); err != nil {
						t.Fatal(err)
					}
				}
				req := DiscardRequest{Key: "remove", UUID: uuid, IfMatch: testTag(time.Minute)}
				begin, commit, complete, terminal := s.BeginDiscard, s.CommitDiscard, s.CompleteDiscard, "secrets-absent"
				if action == "delete" {
					begin, commit, complete, terminal = s.BeginDelete, s.CommitDelete, s.CompleteDelete, "branch-absent"
				}
				op, err := begin(ctx, req, ev, testHash, noRecheck)
				if err != nil {
					t.Fatal(err)
				}
				if err = s.SetGitRefGuard(ctx, ev.Project, ev.Branch, op.ID, true); err != nil {
					t.Fatal(err)
				}
				if err = s.AdvanceOperation(ctx, op.ID, "running", "validated", false, op.Evidence, "", ""); err != nil {
					t.Fatal(err)
				}
				if err = commit(ctx, op.ID, func(context.Context, DiscardEvidence) error { return nil }); err != nil {
					t.Fatalf("commit refused: %v", err)
				}
				if err = s.AdvanceOperation(ctx, op.ID, "running", terminal, true, op.Evidence, "", ""); err != nil {
					t.Fatal(err)
				}
				if err = complete(ctx, op.ID, func(context.Context, string, string, string) error { return nil }); err != nil {
					t.Fatalf("completion refused: %v", err)
				}
				if _, err = s.GetSession(ctx, uuid); !errors.Is(err, ErrNotFound) {
					t.Fatalf("session survived removal: %v", err)
				}
				var principals int
				if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM git_principals WHERE session_uuid=?`, uuid).Scan(&principals); err != nil || principals != 0 {
					t.Fatalf("session principal survived removal: %d %v", principals, err)
				}
			})
		}
	}
}

// A removal's run is recorded only after verify planned it again under the
// Git authority; a plan that changed records nothing.
func TestRemovalRunPlansAgainUnderTheGitAuthority(t *testing.T) {
	s, ev, first := setupDiscardStore(t)
	ctx := context.Background()
	if err := s.FailStaleDiscard(ctx, first.ID, "first plan stale"); err != nil {
		t.Fatal(err)
	}
	req := DiscardRequest{Key: "replanned", UUID: "550e8400-e29b-41d4-a716-446655440000", IfMatch: testTag(time.Minute)}
	held := false
	_, err := s.BeginDiscard(ctx, req, ev, testHash, func(context.Context) error {
		held = !s.gitAuthority.TryLock()
		if !held {
			s.gitAuthority.Unlock()
		}
		return Stale(api.ReasonChanged)
	})
	if !held || !answers(err, api.KindPreconditionFailed, api.ReasonChanged) {
		t.Fatalf("a changed plan answered %v (verify under the Git authority: %v)", err, held)
	}
	if _, err = s.GetOperationByKey(ctx, req.Key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a changed plan recorded its run: %v", err)
	}
}
