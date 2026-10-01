package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPrincipalRepairRevokesOldBeforeNewAuthorityAndRecovers(t *testing.T) {
	s, dir, _, prior := repairStoreFixture(t)
	ctx := context.Background()
	defer s.Close()
	req := PrincipalRepairRequest{Key: "principal-repair-once", UUID: renameTestUUID, TokenSHA256: strings.Repeat("c", 64)}
	ev := PrincipalRepairEvidence{Project: prior.Project, Branch: prior.Branch, PolicySHA256: prior.PolicySHA256,
		AssignedTip: prior.AssignedTip, RefPresent: true,
		GuestKeyStatus: "matching", GuestKeySHA256: strings.Repeat("d", 64),
		OldFingerprint: prior.CredentialFingerprint, OldActive: true, InstanceUUID: prior.InstanceUUID,
		IncusProject: prior.IncusProject, InstanceName: prior.InstanceName,
		IncusUUID:  "11111111-1111-4111-8111-111111111111",
		Generation: "22222222-2222-4222-8222-222222222222", ImageFingerprint: prior.ImageFingerprint,
		ExpiresAt: time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)}
	observe := func(context.Context) error { return nil }
	if _, err := s.BeginPrincipalRepair(ctx, req, ev, observe); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired token admitted: %v", err)
	}
	ev.ExpiresAt = time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
	op, err := s.BeginPrincipalRepair(ctx, req, ev, observe)
	if err != nil || op.Phase != "guarded" {
		t.Fatalf("begin: %+v %v", op, err)
	}
	if replay, e := s.BeginPrincipalRepair(ctx, req, ev, observe); e != nil || replay.ID != op.ID {
		t.Fatalf("key replay: %+v %v", replay, e)
	}
	if _, active, e := s.ActiveWorkspaceInspect(ctx, req.UUID); e != nil || !active {
		t.Fatalf("repair did not guard session: %v %v", active, e)
	}
	ev.NewFingerprint = strings.Repeat("a", 64)
	raw, _ := json.Marshal(ev)
	if err := s.AdvanceOperation(ctx, op.ID, "running", "key-ready", false, raw, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RotatePrincipalRepair(ctx, op.ID); err != nil {
		t.Fatalf("atomic rotation: %v", err)
	}
	if s.IsGitPrincipalActive(ctx, ev.OldFingerprint) || !s.IsGitPrincipalActive(ctx, ev.NewFingerprint) {
		t.Fatal("old principal was not disabled before replacement became active")
	}
	if got, registered, e := s.SessionGitPrincipal(ctx, req.UUID); e != nil || !registered || got != ev.NewFingerprint {
		t.Fatalf("current principal not selected: %q %v %v", got, registered, e)
	}
	if err := s.RotatePrincipalRepair(ctx, op.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("second key registration accepted: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	unfinished, err := s.UnfinishedPrincipalRepairs(ctx)
	if err != nil || len(unfinished) != 1 || unfinished[0].Phase != "authority-rotated" || !unfinished[0].Committed {
		t.Fatalf("restart lost committed rotation: %+v %v", unfinished, err)
	}
	if err := s.CompletePrincipalRepair(ctx, op.ID, func(context.Context, PrincipalRepairEvidence) error { return nil }); !errors.Is(err, ErrConflict) {
		t.Fatalf("completed before native key proof: %v", err)
	}
	if err := s.AdvanceOperation(ctx, op.ID, "running", "guest-key-ready", true, raw, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CompletePrincipalRepair(ctx, op.ID, func(_ context.Context, got PrincipalRepairEvidence) error {
		if got.NewFingerprint != ev.NewFingerprint || got.OldFingerprint != ev.OldFingerprint {
			return ErrConflict
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, active, e := s.ActiveWorkspaceInspect(ctx, req.UUID); e != nil || active {
		t.Fatalf("completed repair retained guard: %v %v", active, e)
	}
	if s.IsGitPrincipalActive(ctx, ev.OldFingerprint) == true || !s.IsGitPrincipalActive(ctx, ev.NewFingerprint) {
		t.Fatal("restart changed principal authority")
	}
}

func TestPrincipalRepairAdmitsMissingRegistrationWithoutPretendingOldAuthority(t *testing.T) {
	s, _, _, prior := repairStoreFixture(t)
	defer s.Close()
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM git_principals WHERE session_uuid=? AND role='session'`, renameTestUUID); err != nil {
		t.Fatal(err)
	}
	ev := PrincipalRepairEvidence{Project: prior.Project, Branch: prior.Branch, PolicySHA256: prior.PolicySHA256,
		AssignedTip: prior.AssignedTip, RefPresent: true,
		GuestKeyStatus: "missing",
		InstanceUUID:   prior.InstanceUUID, IncusProject: prior.IncusProject, InstanceName: prior.InstanceName,
		IncusUUID: "11111111-1111-4111-8111-111111111111", Generation: "22222222-2222-4222-8222-222222222222",
		ImageFingerprint: prior.ImageFingerprint, ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}
	req := PrincipalRepairRequest{Key: "missing-principal", UUID: renameTestUUID, TokenSHA256: strings.Repeat("c", 64)}
	op, err := s.BeginPrincipalRepair(ctx, req, ev, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	ev.NewFingerprint = strings.Repeat("a", 64)
	raw, _ := json.Marshal(ev)
	if err := s.AdvanceOperation(ctx, op.ID, "running", "key-ready", false, raw, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RotatePrincipalRepair(ctx, op.ID); err != nil || !s.IsGitPrincipalActive(ctx, ev.NewFingerprint) {
		t.Fatalf("missing registration repair: %v", err)
	}
}
