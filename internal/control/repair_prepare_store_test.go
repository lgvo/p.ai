package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func repairPreparationFixture(t *testing.T) (*Store, string, string, RepairPreparation) {
	t.Helper()
	s, dir, _, old := repairStoreFixture(t)
	selected := CreationSelection{RuntimeID: "runtime", RuntimeSHA256: strings.Repeat("a", 64), HostID: "host", HostSHA256: strings.Repeat("b", 64), SourceID: "source", SourceSHA256: strings.Repeat("c", 64)}
	env := EnvironmentIntent{ModuleID: "nix", ModuleSHA256: strings.Repeat("d", 64), ConfigSHA256: strings.Repeat("e", 64), System: "x86_64-linux", BaseFingerprint: old.BaseFingerprint, BuilderStoragePool: "builders", BuilderPolicySHA256: strings.Repeat("f", 64)}
	ev := RepairPreparation{Project: old.Project, Branch: old.Branch, AssignedTip: old.AssignedTip,
		PolicySHA256: old.PolicySHA256, CredentialFingerprint: old.CredentialFingerprint,
		IncusProject: old.IncusProject, InstanceName: old.InstanceName, InstanceUUID: old.InstanceUUID,
		RecordedImageFingerprint: old.ImageFingerprint, RecordedSourceCommit: old.AssignedTip,
		Selection: selected, Environment: env}
	return s, dir, renameTestUUID, ev
}

func TestRepairPreparationDigestBindsConfirmedImageAndAcceptedOverride(t *testing.T) {
	s, _, uuid, prep := repairPreparationFixture(t)
	defer s.Close()
	ctx := context.Background()
	prepared, err := s.BeginRepairPreparation(ctx, "prepare-for-confirm", uuid, prep, func(context.Context) error { return nil }, repairCapacity)
	if err != nil {
		t.Fatal(err)
	}
	prep.BuilderTreeOID = strings.Repeat("1", 40)
	prep.EnvironmentSelection = "devshell"
	prep.EnvironmentKey = strings.Repeat("2", 64)
	prep.CommittedInputsDigest = strings.Repeat("3", 64)
	prep.DerivationPath = "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-shell.drv"
	prep.SourceNarHash = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	prep.FlakePresent = true
	raw, _ := json.Marshal(prep)
	if err = s.AdvanceOperation(ctx, prepared.ID, "running", "builder-absent", true, raw, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteRepairPreparation(ctx, prepared.ID, func(context.Context, RepairPreparation) error { return nil }); err != nil {
		t.Fatal(err)
	}
	prepared, err = s.GetOperation(ctx, prepared.ID)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(prepared.Evidence)
	req := RepairRequest{Key: "confirm-prepared", UUID: uuid, TokenSHA256: strings.Repeat("f", 64)}
	ev := RepairEvidence{Project: prep.Project, Branch: prep.Branch, AssignedTip: prep.AssignedTip,
		PolicySHA256: prep.PolicySHA256, CredentialFingerprint: prep.CredentialFingerprint,
		IncusProject: prep.IncusProject, InstanceName: prep.InstanceName, InstanceUUID: prep.InstanceUUID,
		ImageSourceCommit: prep.RecordedSourceCommit, BaseFingerprint: prep.Environment.BaseFingerprint,
		RecordedImageFingerprint: prep.RecordedImageFingerprint, PreparationOperationID: prepared.ID,
		PreparationSHA256: strings.Repeat("0", 64), EnvironmentKey: prep.EnvironmentKey,
		EnvironmentSelection: prep.EnvironmentSelection, EnvironmentSourceCommit: prep.AssignedTip,
		Environment: &prep.Environment, BuilderTreeOID: prep.BuilderTreeOID,
		PreviewExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}
	if _, err = s.BeginRepair(ctx, req, ev, func(context.Context) error { return nil }, repairCapacity); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong prepared evidence digest admitted: %v", err)
	}
	ev.PreparationSHA256 = hex.EncodeToString(sum[:])
	wrong := ev
	wrong.EnvironmentKey = strings.Repeat("6", 64)
	if _, err = s.BeginRepair(ctx, req, wrong, func(context.Context) error { return nil }, repairCapacity); !errors.Is(err, ErrConflict) {
		t.Fatalf("preparation key mismatch admitted: %v", err)
	}
	op, err := s.BeginRepair(ctx, req, ev, func(context.Context) error { return nil }, repairCapacity)
	if err != nil {
		t.Fatal(err)
	}
	if _, accepted, e := s.AcceptedRepairImage(ctx, uuid); e != nil || accepted {
		t.Fatalf("unconfirmed image override visible: %v %v", accepted, e)
	}
	ev.ImageFingerprint = strings.Repeat("4", 64)
	ev.EnvironmentState = &EnvironmentState{Key: ev.EnvironmentKey, MaterialDigest: strings.Repeat("5", 64), CaptureStorePath: "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-shell-env", BuilderRequest: op.ID, Fingerprint: ev.ImageFingerprint}
	ev.IncusUUID = "11111111-1111-4111-8111-111111111111"
	ev.Generation = "22222222-2222-4222-8222-222222222222"
	raw, _ = json.Marshal(ev)
	if err = s.AdvanceOperation(ctx, op.ID, "running", "host-ready", true, raw, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteRepair(ctx, op.ID, func(context.Context, RepairEvidence) error { return nil }); err != nil {
		t.Fatal(err)
	}
	got, accepted, err := s.AcceptedRepairImage(ctx, uuid)
	if err != nil || !accepted || got.ImageFingerprint != ev.ImageFingerprint || got.EnvironmentSourceCommit != ev.AssignedTip {
		t.Fatalf("accepted repair image not authoritative: %+v %v %v", got, accepted, err)
	}
	view := AcceptedRepairEnvironmentView(got)
	if view == nil || view.CommitOID != ev.AssignedTip || view.ImageFingerprint != ev.ImageFingerprint || view.Key != ev.EnvironmentKey {
		t.Fatalf("session status did not report accepted repaired image: %+v", view)
	}
	created, _ := json.Marshal(CreationEvidence{EnvironmentState: &EnvironmentState{Key: strings.Repeat("a", 64), Fingerprint: prep.RecordedImageFingerprint}})
	if _, err = s.db.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,committed,evidence_json,created_at,updated_at)
	 VALUES('33333333-3333-4333-8333-333333333333','fixture-create','session.create',? ,?,'{}','fixture','completed','completed',1,?,'2026','2026')`, ev.Project, uuid, string(created)); err != nil {
		t.Fatal(err)
	}
	related, _, err := s.ListRelatedEnvironmentSessions(ctx, ev.Project, ev.EnvironmentKey, ev.ImageFingerprint, "", 10)
	if err != nil || len(related) != 1 || related[0].UUID != uuid {
		t.Fatalf("repaired image omitted from collection loss preview: %+v %v", related, err)
	}
}

func TestRepairPreparationDurableGuardAndRestart(t *testing.T) {
	s, dir, uuid, ev := repairPreparationFixture(t)
	ctx := context.Background()
	reads := 0
	verify := func(context.Context) error { reads++; return nil }
	op, err := s.BeginRepairPreparation(ctx, "prepare-once", uuid, ev, verify, repairCapacity)
	if err != nil || op.Phase != "reserved" || reads != 1 {
		t.Fatalf("prepare admission: %+v %v reads=%d", op, err, reads)
	}
	if replay, e := s.BeginRepairPreparation(ctx, "prepare-once", uuid, ev, func(context.Context) error { return ErrConflict }, repairCapacity); e != nil || replay.ID != op.ID || reads != 1 {
		t.Fatalf("idempotent preparation replay: %+v %v", replay, e)
	}
	if _, active, e := s.ActiveWorkspaceInspect(ctx, uuid); e != nil || !active {
		t.Fatalf("preparation guard unavailable: %v", e)
	}
	if _, e := s.db.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,request_json,request_sha256,status,phase,committed,created_at,updated_at)
	 VALUES('44444444-4444-4444-8444-444444444444','collect-during-prepare','environment.collect',?,'{}','x','running','reserved',0,'2026','2026')`, ev.Project); e == nil {
		t.Fatal("cache collection admitted during builder preparation")
	}
	if err = s.AdvanceOperation(ctx, op.ID, "blocked", "builder-init-issued", true, op.Evidence, "unknown init"); err != nil {
		t.Fatal(err)
	}
	if err = s.FailRepairPreparationPreInit(ctx, op.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("uncertain builder admission released guard: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	unfinished, err := s.UnfinishedRepairPreparations(ctx)
	if err != nil || len(unfinished) != 1 || unfinished[0].ID != op.ID || unfinished[0].Phase != "builder-init-issued" {
		t.Fatalf("restart lost exact builder intent: %+v %v", unfinished, err)
	}
	if _, active, e := s.ActiveWorkspaceInspect(ctx, uuid); e != nil || !active {
		t.Fatalf("restart released guard: %v", e)
	}
}

func TestRepairPreparationCompletedIdentityIsEvidenceOnly(t *testing.T) {
	s, _, uuid, ev := repairPreparationFixture(t)
	defer s.Close()
	ctx := context.Background()
	op, err := s.BeginRepairPreparation(ctx, "prepare-complete", uuid, ev, func(context.Context) error { return nil }, repairCapacity)
	if err != nil {
		t.Fatal(err)
	}
	ev.BuilderTreeOID = strings.Repeat("1", 40)
	ev.EnvironmentSelection = "devshell"
	ev.EnvironmentKey = strings.Repeat("2", 64)
	ev.CommittedInputsDigest = strings.Repeat("3", 64)
	ev.DerivationPath = "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-shell.drv"
	ev.SourceNarHash = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	ev.FlakePresent = true
	raw, _ := json.Marshal(ev)
	if err = s.AdvanceOperation(ctx, op.ID, "running", "builder-absent", true, raw, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteRepairPreparation(ctx, op.ID, func(_ context.Context, got RepairPreparation) error {
		if got.EnvironmentKey != ev.EnvironmentKey || got.AssignedTip != ev.AssignedTip {
			return ErrConflict
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.CompletedRepairPreparation(ctx, op.ID, uuid)
	if err != nil || got.EnvironmentKey != ev.EnvironmentKey {
		t.Fatalf("completed identity: %+v %v", got, err)
	}
	if _, active, e := s.ActiveWorkspaceInspect(ctx, uuid); e != nil || active {
		t.Fatalf("completed preparation retained guard: %v", e)
	}
	if _, exists, e := s.AcceptedRepairImage(ctx, uuid); e != nil || exists {
		t.Fatalf("preparation changed accepted runtime image: %v %v", exists, e)
	}
}

func TestAcceptedRepairImageUsesDurableInsertionOrder(t *testing.T) {
	s, _, _, first := repairStoreFixture(t)
	defer s.Close()
	ctx := context.Background()
	first.EnvironmentState = &EnvironmentState{Key: strings.Repeat("a", 64), Fingerprint: first.ImageFingerprint}
	second := first
	second.ImageFingerprint = strings.Repeat("6", 64)
	second.EnvironmentState = &EnvironmentState{Key: strings.Repeat("b", 64), Fingerprint: second.ImageFingerprint}
	// The second insertion has an earlier wall-clock timestamp and a
	// lexically smaller ID. Neither orders the accepted repair generation.
	for _, row := range []struct {
		id, key, created string
		ev               RepairEvidence
	}{
		{"ffffffff-ffff-4fff-8fff-ffffffffffff", "older-repair-generation", "2026-09-26T00:00:00Z", first},
		{"00000000-0000-4000-8000-000000000001", "newer-repair-generation", "2026-09-25T00:00:00Z", second},
	} {
		raw, err := json.Marshal(row.ev)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,committed,evidence_json,created_at,updated_at)
		 VALUES(?,?, 'session.repair',?,?, '{}','fixture','completed','completed',1,?,?,?)`, row.id, row.key, first.Project, renameTestUUID, raw, row.created, row.created); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := s.AcceptedRepairImage(ctx, renameTestUUID)
	if err != nil || !ok || got.ImageFingerprint != second.ImageFingerprint {
		t.Fatalf("accepted image did not follow durable order: %+v %v %v", got, ok, err)
	}
	created, _ := json.Marshal(CreationEvidence{EnvironmentState: first.EnvironmentState})
	if _, err := s.db.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,committed,evidence_json,created_at,updated_at)
	 VALUES('33333333-3333-4333-8333-333333333333','fixture-order-create','session.create',?,?,'{}','fixture','completed','completed',1,?,'2026','2026')`, first.Project, renameTestUUID, created); err != nil {
		t.Fatal(err)
	}
	related, _, err := s.ListRelatedEnvironmentSessions(ctx, first.Project, second.EnvironmentState.Key, second.ImageFingerprint, "", 10)
	if err != nil || len(related) != 1 || related[0].UUID != renameTestUUID {
		t.Fatalf("collection relation used an older repair: %+v %v", related, err)
	}
}
