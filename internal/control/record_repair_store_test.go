package control

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRecordRepairBothAbsentGuardRevocationAndRestart(t *testing.T) {
	s, dir, _, prior := repairStoreFixture(t)
	ctx := context.Background()
	defer s.Close()
	ev := RecordRepairEvidence{Project: prior.Project, Branch: prior.Branch,
		Principal: prior.CredentialFingerprint, PrincipalActive: true,
		ExternalAuthority: "none_registered",
		PolicySHA256:      prior.PolicySHA256, InstanceUUID: prior.InstanceUUID,
		IncusProject: prior.IncusProject, InstanceName: prior.InstanceName,
		ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}
	req := RecordRepairRequest{Key: "unrecoverable-one", UUID: renameTestUUID, TokenSHA256: strings.Repeat("a", 64)}
	refAbsent, runtimeAbsent := true, true
	observe := func(context.Context) error {
		if !refAbsent || !runtimeAbsent {
			return ErrConflict
		}
		return nil
	}
	wrongName := ev
	wrongName.InstanceName = "p-sibling"
	if _, err := s.BeginRecordRepair(ctx, req, wrongName, observe); !errors.Is(err, ErrInvalid) {
		t.Fatalf("noncanonical native locator admitted: %v", err)
	}
	refAbsent = false
	if _, err := s.BeginRecordRepair(ctx, req, ev, observe); !errors.Is(err, ErrConflict) {
		t.Fatalf("present ref admitted: %v", err)
	}
	refAbsent, runtimeAbsent = true, false
	if _, err := s.BeginRecordRepair(ctx, req, ev, observe); !errors.Is(err, ErrConflict) {
		t.Fatalf("present runtime admitted: %v", err)
	}
	runtimeAbsent = true
	op, err := s.BeginRecordRepair(ctx, req, ev, observe)
	if err != nil || op.Phase != "guarded" {
		t.Fatalf("begin: %+v %v", op, err)
	}
	if active, e := s.HasActiveSessionOperation(ctx, req.UUID); e != nil || !active {
		t.Fatalf("durable active operation missing from preview gate: %v %v", active, e)
	}
	if replay, e := s.BeginRecordRepair(ctx, req, ev, observe); e != nil || replay.ID != op.ID {
		t.Fatalf("idempotent replay: %+v %v", replay, e)
	}
	refAbsent = false
	if err := s.CommitRecordRepair(ctx, op.ID, observe); !errors.Is(err, ErrConflict) {
		t.Fatalf("reappeared ref crossed commit: %v", err)
	}
	if !s.IsGitPrincipalActive(ctx, ev.Principal) {
		t.Fatal("failed commit disabled Git authority")
	}
	refAbsent = true
	if err := s.CommitRecordRepair(ctx, op.ID, observe); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if s.IsGitPrincipalActive(ctx, ev.Principal) {
		t.Fatal("old Git authority survived committed cleanup")
	}
	if got, e := s.GetSession(ctx, req.UUID); e != nil || got.Registry != "removing" {
		t.Fatalf("durable removal marker: %+v %v", got, e)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	unfinished, err := s.UnfinishedRecordRepairs(ctx)
	if err != nil || len(unfinished) != 1 || unfinished[0].Phase != "authority-disabled" || !unfinished[0].Committed {
		t.Fatalf("restart lost committed guard: %+v %v", unfinished, err)
	}
	if err := s.AdvanceOperation(ctx, op.ID, "running", "secrets-absent", true, op.Evidence, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE git_principals SET active=1 WHERE fingerprint=?`, ev.Principal); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRecordRepair(ctx, op.ID, observe); !errors.Is(err, ErrConflict) {
		t.Fatalf("row deleted while Git principal remained active: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE git_principals SET active=0 WHERE fingerprint=?`, ev.Principal); err != nil {
		t.Fatal(err)
	}
	runtimeAbsent = false
	if err := s.CompleteRecordRepair(ctx, op.ID, observe); !errors.Is(err, ErrConflict) {
		t.Fatalf("reappeared runtime crossed final boundary: %v", err)
	}
	runtimeAbsent = true
	if err := s.CompleteRecordRepair(ctx, op.ID, observe); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, err := s.GetSession(ctx, req.UUID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unrecoverable row survived cleanup: %v", err)
	}
	if s.IsGitPrincipalActive(ctx, ev.Principal) {
		t.Fatal("completed cleanup restored old authority")
	}
}
