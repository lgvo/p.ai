package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testSelection() CreationSelection {
	return CreationSelection{RuntimeID: "runtime", RuntimeSHA256: strings.Repeat("a", 64), HostID: "host", HostSHA256: strings.Repeat("b", 64), SourceID: "source", SourceSHA256: strings.Repeat("c", 64)}
}

func TestCreationSelectionBindsOptionalAgentPackage(t *testing.T) {
	selection := testSelection()
	if !selection.Valid() {
		t.Fatal("base selection invalid")
	}
	selection.AgentID = "org.p.codex-adapter"
	if selection.Valid() {
		t.Fatal("agent id without digest accepted")
	}
	selection.AgentSHA256 = strings.Repeat("d", 64)
	if !selection.Valid() {
		t.Fatal("pinned agent selection refused")
	}
	selection.AgentID = ""
	if selection.Valid() {
		t.Fatal("agent digest without identity accepted")
	}
}

func TestBlankProjectIntentAndAtomicBootstrap(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	req := BlankProjectRequest{Key: "blank-1", Project: "team/app"}
	op, err := s.BeginBlankProject(ctx, req, json.RawMessage(`{"network":"none"}`), strings.Repeat("d", 64), testSelection())
	if err != nil {
		t.Fatal(err)
	}
	if op.Phase != "repo-pending" || op.SessionUUID == "" {
		t.Fatal("durable intent missing")
	}
	if _, err = s.GetSession(ctx, op.SessionUUID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session exposed before Git verify: %v", err)
	}
	if active, err := s.HasActiveProject(ctx, req.Project); err != nil || active {
		t.Fatal("project exposed before Git verify", err)
	}
	duplicate, err := s.BeginBlankProject(ctx, req, json.RawMessage(`{"network":"public-egress"}`), strings.Repeat("e", 64), CreationSelection{})
	if err != nil || duplicate.ID != op.ID || string(duplicate.Evidence) != string(op.Evidence) {
		t.Fatalf("duplicate reselected policy/image: %v", err)
	}
	if _, err = s.BeginBlankProject(ctx, BlankProjectRequest{Key: req.Key, Project: "other"}, json.RawMessage(`{}`), strings.Repeat("d", 64), testSelection()); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed request accepted: %v", err)
	}
	session, err := s.CommitBlankProject(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if session.UUID != op.SessionUUID || session.Branch != "main" {
		t.Fatal("bootstrap identity changed")
	}
	if _, err = s.CommitBlankProject(ctx, op.ID); err != nil {
		t.Fatalf("commit replay failed: %v", err)
	}
	var grants int
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM git_unborn_grants WHERE session_uuid=? AND state='pending'`, session.UUID).Scan(&grants); err != nil || grants != 1 {
		t.Fatal("grant not atomic", err)
	}
	if err = s.CompleteCreation(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	result, err := s.GetOperation(ctx, op.ID)
	if err != nil || result.Status != "completed" || result.Phase != "established" {
		t.Fatal("operation not completed", err)
	}
	final, err := s.GetSession(ctx, session.UUID)
	if err != nil || final.Registry != "established" {
		t.Fatal("session not established atomically", err)
	}
}

func TestCapturedSourceIsNotReobservedOnDuplicate(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "team/app", json.RawMessage(`{"network":"none"}`)); err != nil {
		t.Fatal(err)
	}
	captured := strings.Repeat("a", 40)
	calls := 0
	capture := func(context.Context, ReserveSessionRequest) (string, bool, error) {
		calls++
		return captured, false, nil
	}
	req := ReserveSessionRequest{Key: "create-1", Project: "team/app", Branch: "feature/x", Choice: "new", Source: "refs/heads/main"}
	op, session, err := s.BeginSessionCreate(ctx, req, strings.Repeat("d", 64), testSelection(), capture)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || op.Phase != "source-ready" || session.Registry != "creating" {
		t.Fatal("capture reservation failed")
	}
	captured = strings.Repeat("b", 40)
	replay, again, err := s.BeginSessionCreate(ctx, req, strings.Repeat("e", 64), CreationSelection{}, func(context.Context, ReserveSessionRequest) (string, bool, error) {
		t.Fatal("duplicate observed moved source")
		return "", false, nil
	})
	if err != nil || replay.ID != op.ID || again.UUID != session.UUID {
		t.Fatalf("duplicate changed identity: %v", err)
	}
	var ev CreationEvidence
	if err = json.Unmarshal(replay.Evidence, &ev); err != nil || ev.CapturedOID != strings.Repeat("a", 40) || !ev.RefCASIntent {
		t.Fatal("captured CAS intent changed", err)
	}
	req.Branch = "feature/y"
	if _, _, err = s.BeginSessionCreate(ctx, req, strings.Repeat("d", 64), testSelection(), capture); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed key input accepted: %v", err)
	}
	if calls != 1 {
		t.Fatal("changed request reached Git capture")
	}
}

func TestSourceObservationFailureLeavesNoReservation(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	req := ReserveSessionRequest{Key: "fail-capture", Project: "app", Branch: "work", Choice: "new", Source: "refs/heads/main"}
	if _, _, err := s.BeginSessionCreate(ctx, req, strings.Repeat("d", 64), testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
		return "", false, errors.New("source unavailable")
	}); err == nil {
		t.Fatal("capture error ignored")
	}
	_, sessions, operations, err := s.Count(ctx)
	if err != nil || sessions != 0 || operations != 0 {
		t.Fatalf("failed capture reserved identity: sessions=%d ops=%d %v", sessions, operations, err)
	}
}

func TestProjectCreateBootstrapReceiveOnlyAfterWorkspaceReady(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	op, err := s.BeginBlankProject(ctx, BlankProjectRequest{Key: "bootstrap", Project: "blank"}, json.RawMessage(`{"network":"none"}`), strings.Repeat("d", 64), testSelection())
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.CommitBlankProject(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := strings.Repeat("e", 64)
	if err = s.RegisterGitPrincipal(ctx, fingerprint, "session", session.Project, session.UUID); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"branch-assigned", "principals-ready", "assembly-ready"} {
		if phase != "branch-assigned" {
			if err = s.AdvanceOperation(ctx, op.ID, "running", phase, true, op.Evidence, ""); err != nil {
				t.Fatal(err)
			}
		}
		if _, release, e := s.AcquireGitLease(ctx, fingerprint, "blank", "receive"); !errors.Is(e, ErrGitDenied) {
			if release != nil {
				release()
			}
			t.Fatalf("bootstrap received too early at %s: %v", phase, e)
		}
	}
	if err = s.AdvanceOperation(ctx, op.ID, "running", "workspace-ready", true, op.Evidence, ""); err != nil {
		t.Fatal(err)
	}
	ref, release, err := s.AcquireGitLease(ctx, fingerprint, "blank", "receive")
	if err != nil || ref != "refs/heads/main" {
		t.Fatalf("bootstrap receive unavailable: %q %v", ref, err)
	}
	release()
	claimed, err := s.ConsumeGitUnbornGrant(ctx, fingerprint, "blank")
	if err != nil || !claimed {
		t.Fatal("bootstrap grant not usable", err)
	}
}

func TestCreationCompletionRollbackAndExactReplay(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	op, err := s.BeginBlankProject(ctx, BlankProjectRequest{Key: "commit-window", Project: "app"}, json.RawMessage(`{"network":"none"}`), strings.Repeat("d", 64), testSelection())
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.CommitBlankProject(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `CREATE TRIGGER deny_completion BEFORE UPDATE OF status ON operations BEGIN SELECT RAISE(ABORT,'injected completion failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteCreation(ctx, op.ID); err == nil {
		t.Fatal("injected completion succeeded")
	}
	current, err := s.GetSession(ctx, session.UUID)
	if err != nil || current.Registry != "creating" {
		t.Fatalf("registry advanced without operation: %+v %v", current, err)
	}
	currentOp, err := s.GetOperation(ctx, op.ID)
	if err != nil || currentOp.Status != "running" {
		t.Fatalf("operation changed despite rollback: %+v %v", currentOp, err)
	}
	if _, err = s.db.ExecContext(ctx, `DROP TRIGGER deny_completion`); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteCreation(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteCreation(ctx, op.ID); err != nil {
		t.Fatalf("completed replay failed: %v", err)
	}
}

func TestRevokedPrincipalIsNotReactivatedByCreationRetry(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	_, session, err := s.ReserveSession(ctx, ReserveSessionRequest{Key: "p", Project: "app", Branch: "work", Choice: "existing"})
	if err != nil {
		t.Fatal(err)
	}
	fp := strings.Repeat("f", 64)
	if err = s.EnsureSessionGitPrincipal(ctx, fp, "app", session.UUID); err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeGitPrincipal(ctx, fp); err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureSessionGitPrincipal(ctx, fp, "app", session.UUID); !errors.Is(err, ErrGitDenied) {
		t.Fatalf("retry reactivated revoked principal: %v", err)
	}
}
