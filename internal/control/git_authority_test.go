package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGitPrincipalAssignmentGuardAndRestart(t *testing.T) {
	store, dir := openTestStore(t)
	ctx := context.Background()
	if err := store.CreateProject(ctx, "team/app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	op, session, err := store.ReserveSession(ctx, ReserveSessionRequest{Key: "git", Project: "team/app", Branch: "work", Choice: "new", Source: "refs/heads/main"})
	if err != nil {
		t.Fatal(err)
	}
	host, agent := strings.Repeat("a", 64), strings.Repeat("b", 64)
	if fingerprint, registered, err := store.SessionGitPrincipal(ctx, session.UUID); err != nil || registered || fingerprint != "" {
		t.Fatalf("unregistered session principal: %q %t %v", fingerprint, registered, err)
	}
	if err := store.RegisterGitPrincipal(ctx, host, "host", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterGitPrincipal(ctx, agent, "session", "team/app", session.UUID); err != nil {
		t.Fatal(err)
	}
	if fingerprint, registered, err := store.SessionGitPrincipal(ctx, session.UUID); err != nil || !registered || fingerprint != agent {
		t.Fatalf("registered session principal: %q %t %v", fingerprint, registered, err)
	}
	if _, release, err := store.AcquireGitLease(ctx, agent, "team/app", "receive"); !errors.Is(err, ErrGitDenied) {
		if release != nil {
			release()
		}
		t.Fatalf("creating session gained write before commit: %v", err)
	}
	if err := store.AdvanceOperation(ctx, op.ID, "running", "principals-ready", true, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, release, err := store.AcquireGitLease(ctx, agent, "team/app", "receive"); !errors.Is(err, ErrGitDenied) {
		if release != nil {
			release()
		}
		t.Fatalf("write before verified workspace: %v", err)
	}
	if err := store.AdvanceOperation(ctx, op.ID, "running", "workspace-ready", true, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, release, err := store.AcquireGitLease(ctx, host, "team/app", "receive"); !errors.Is(err, ErrGitDenied) {
		if release != nil {
			release()
		}
		t.Fatalf("host write: %v", err)
	}
	if _, release, err := store.AcquireGitLease(ctx, host, "team/app", "upload"); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
	ref, release, err := store.AcquireGitLease(ctx, agent, "team/app", "receive")
	if err != nil || ref != "refs/heads/work" {
		t.Fatalf("assigned write: %q %v", ref, err)
	}
	guardDone := make(chan error, 1)
	go func() { guardDone <- store.SetGitRefGuard(ctx, "team/app", "work", op.ID, true) }()
	select {
	case err := <-guardDone:
		t.Fatalf("guard overtook active receive: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	release()
	if err := <-guardDone; err != nil {
		t.Fatal(err)
	}
	if _, release, err := store.AcquireGitLease(ctx, agent, "team/app", "receive"); !errors.Is(err, ErrGitDenied) {
		if release != nil {
			release()
		}
		t.Fatalf("guarded write: %v", err)
	}
	if _, release, err := store.AcquireGitLease(ctx, agent, "team/app", "upload"); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if _, release, err := restarted.AcquireGitLease(ctx, agent, "team/app", "receive"); !errors.Is(err, ErrGitDenied) {
		if release != nil {
			release()
		}
		t.Fatalf("guard lost on restart: %v", err)
	}
	if err := restarted.SetGitRefGuard(ctx, "team/app", "work", op.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := restarted.RevokeGitPrincipal(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if fingerprint, registered, err := restarted.SessionGitPrincipal(ctx, session.UUID); err != nil || !registered || fingerprint != agent {
		t.Fatalf("revoked session principal lost identity: %q %t %v", fingerprint, registered, err)
	}
	if restarted.IsGitPrincipalActive(ctx, agent) {
		t.Fatal("revoked key remains active")
	}
	if _, release, err := restarted.AcquireGitLease(ctx, agent, "team/app", "receive"); !errors.Is(err, ErrGitDenied) {
		if release != nil {
			release()
		}
		t.Fatalf("revoked write: %v", err)
	}
}

func TestBlankUnbornGrantSurvivesEstablishmentAndConsumesAtHook(t *testing.T) {
	store, dir := openTestStore(t)
	ctx := context.Background()
	if err := store.CreateProject(ctx, "blank", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	op, session, err := store.ReserveSession(ctx, ReserveSessionRequest{Key: "blank", Project: "blank", Branch: "main", Choice: "blank"})
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("c", 64)
	if err := store.RegisterGitPrincipal(ctx, key, "session", "blank", session.UUID); err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceOperation(ctx, op.ID, "running", "principals-ready", true, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE sessions SET registry_state='established' WHERE uuid=?`, session.UUID); err != nil {
		t.Fatal(err)
	}
	if _, release, err := store.AcquireGitLease(ctx, key, "blank", "receive"); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
	claimed, err := store.ConsumeGitUnbornGrant(ctx, key, "blank")
	if err != nil || !claimed {
		t.Fatalf("established blank session cannot create main: %v %v", claimed, err)
	}
	claimed, err = store.ConsumeGitUnbornGrant(ctx, key, "blank")
	if err != nil || claimed {
		t.Fatalf("unborn grant reusable: %v %v", claimed, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	claimed, err = restarted.ConsumeGitUnbornGrant(ctx, key, "blank")
	if err != nil || claimed {
		t.Fatalf("consumed grant restored after restart: %v %v", claimed, err)
	}
}

func TestCancelledGuardAndRevocationDoNotOvertakeReceive(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	op, session, err := store.ReserveSession(ctx, ReserveSessionRequest{Key: "guard-cancel", Project: "app", Branch: "work", Choice: "new", Source: "refs/heads/main"})
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("d", 64)
	if err := store.RegisterGitPrincipal(ctx, key, "session", "app", session.UUID); err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceOperation(ctx, op.ID, "running", "workspace-ready", true, nil, ""); err != nil {
		t.Fatal(err)
	}
	_, release, err := store.AcquireGitLease(ctx, key, "app", "receive")
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(context.Context) error{
		func(c context.Context) error { return store.SetGitRefGuard(c, "app", "work", op.ID, true) },
		func(c context.Context) error { return store.RevokeGitPrincipal(c, key) },
	} {
		deadline, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
		err := mutate(deadline)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			release()
			t.Fatalf("mutation ignored cancellation: %v", err)
		}
	}
	release()
	// Pending canceled writers may briefly drain, but neither may mutate.
	ref, release, err := store.AcquireGitLease(ctx, key, "app", "receive")
	if err != nil || ref != "refs/heads/work" {
		if release != nil {
			release()
		}
		t.Fatalf("canceled mutation changed authority: %q %v", ref, err)
	}
	release()
}
