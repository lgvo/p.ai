package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func repairStoreFixture(t *testing.T) (*Store, string, RepairRequest, RepairEvidence) {
	t.Helper()
	s, dir, _, renamed := renameStoreFixture(t)
	req := RepairRequest{Key: "repair-once", UUID: renameTestUUID, TokenSHA256: strings.Repeat("9", 64)}
	ev := RepairEvidence{Project: "app", Branch: "main", AssignedTip: renamed.OldTip, PolicySHA256: renamed.PolicySHA256,
		CredentialFingerprint: strings.Repeat("f", 64), IncusProject: "user-1000", InstanceName: "p-" + renameTestUUID,
		InstanceUUID: renamed.InstanceUUID, ImageFingerprint: renamed.ImageFingerprint, BaseFingerprint: renamed.BaseFingerprint,
		PreviewExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}
	return s, dir, req, ev
}

func repairCapacity(_ context.Context, reservations []CapacityReservation) (CapacityObservation, error) {
	return CapacityObservation{Limit: 4, PhysicalCount: 0, Occupied: map[string]bool{}}, nil
}

func TestRepairSQLiteStaleTokenExactReplayAndNoEffectRollback(t *testing.T) {
	s, _, req, ev := repairStoreFixture(t)
	ctx := context.Background()
	reads := 0
	observe := func(context.Context) error { reads++; return nil }
	ev.PreviewExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	if _, err := s.BeginRepair(ctx, req, ev, observe, repairCapacity); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired review admitted: %v", err)
	}
	if reads != 1 {
		t.Fatalf("observation count %d", reads)
	}
	ev.PreviewExpiresAt = time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
	changed := func(context.Context) error { return ErrConflict }
	if _, err := s.BeginRepair(ctx, req, ev, changed, repairCapacity); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed ref admitted: %v", err)
	}
	op, err := s.BeginRepair(ctx, req, ev, observe, repairCapacity)
	if err != nil || op.Phase != "reserved" {
		t.Fatalf("begin: %+v %v", op, err)
	}
	if replay, e := s.BeginRepair(ctx, req, ev, changed, repairCapacity); e != nil || replay.ID != op.ID || reads != 2 {
		t.Fatalf("replay reobserved: %+v %v reads=%d", replay, e, reads)
	}
	if _, active, e := s.ActiveWorkspaceInspect(ctx, req.UUID); e != nil || !active {
		t.Fatalf("active guard missing: %v", e)
	}
	if err = s.FailRepairPreInit(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, active, e := s.ActiveWorkspaceInspect(ctx, req.UUID); e != nil || active {
		t.Fatalf("pre-effect guard retained: %v", e)
	}
	if replay, e := s.BeginRepair(ctx, req, ev, changed, repairCapacity); e != nil || replay.ID != op.ID || replay.Status != "failed" {
		t.Fatalf("failed key replay: %+v %v", replay, e)
	}
}

func TestRepairInitIssuedRecoveryTombstoneAndExactCompletion(t *testing.T) {
	s, dir, req, ev := repairStoreFixture(t)
	ctx := context.Background()
	op, err := s.BeginRepair(ctx, req, ev, func(context.Context) error { return nil }, repairCapacity)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ev)
	if err = s.AdvanceOperation(ctx, op.ID, "blocked", "init-issued", true, raw, "native admission unknown"); err != nil {
		t.Fatal(err)
	}
	if err = s.FailRepairPreInit(ctx, op.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("unknown init released guard: %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	unfinished, err := s.UnfinishedRepairs(ctx)
	if err != nil || len(unfinished) != 1 || unfinished[0].ID != op.ID || unfinished[0].Phase != "init-issued" {
		t.Fatalf("restart lost tombstone: %+v %v", unfinished, err)
	}
	if _, active, e := s.ActiveWorkspaceInspect(ctx, req.UUID); e != nil || !active {
		t.Fatalf("restart released guard: %v", e)
	}
	ev.IncusUUID = "22222222-2222-4222-8222-222222222222"
	ev.Generation = "33333333-3333-4333-8333-333333333333"
	raw, _ = json.Marshal(ev)
	if err = s.AdvanceOperation(ctx, op.ID, "running", "host-ready", true, raw, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteRepair(ctx, op.ID, func(_ context.Context, got RepairEvidence) error {
		if got.IncusUUID != ev.IncusUUID || got.Generation != ev.Generation {
			return ErrConflict
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	completed, err := s.GetOperation(ctx, op.ID)
	if err != nil || completed.Status != "completed" || completed.Phase != "completed" {
		t.Fatalf("completion: %+v %v", completed, err)
	}
	if _, active, e := s.ActiveWorkspaceInspect(ctx, req.UUID); e != nil || active {
		t.Fatalf("completion retained guard: %v", e)
	}
}

func TestRepairCapacityCountsMissingReservationAndPreservesHelperSlot(t *testing.T) {
	s, _, req, ev := repairStoreFixture(t)
	ctx := context.Background()
	nearFull := func(_ context.Context, res []CapacityReservation) (CapacityObservation, error) {
		if len(res) != 1 || res[0].SessionUUID != req.UUID {
			return CapacityObservation{}, ErrConflict
		}
		return CapacityObservation{Limit: 4, PhysicalCount: 3, Occupied: map[string]bool{}}, nil
	}
	if _, err := s.BeginRepair(ctx, req, ev, func(context.Context) error { return nil }, nearFull); !errors.Is(err, ErrConflict) {
		t.Fatalf("helper headroom lost: %v", err)
	}
	room := func(_ context.Context, res []CapacityReservation) (CapacityObservation, error) {
		return CapacityObservation{Limit: 4, PhysicalCount: 2, Occupied: map[string]bool{}}, nil
	}
	if _, err := s.BeginRepair(ctx, req, ev, func(context.Context) error { return nil }, room); err != nil {
		t.Fatalf("existing reservation counted twice: %v", err)
	}
}

func TestRepairPolicyDriftRejectsBeforeDurableIntentAndPreservesSibling(t *testing.T) {
	s, _, req, ev := repairStoreFixture(t)
	ctx := context.Background()
	sibling := "77777777-7777-4777-8777-777777777777"
	if _, err := s.db.ExecContext(ctx, `INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256) VALUES(?,'app','sibling','established','{}',?)`, sibling, ev.PolicySHA256); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE projects SET policy_sha256=? WHERE path='app'`, strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginRepair(ctx, req, ev, func(context.Context) error { return nil }, repairCapacity); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed current project policy admitted: %v", err)
	}
	if _, err := s.GetOperationByKey(ctx, req.Key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale plan left intent: %v", err)
	}
	kept, err := s.GetSession(ctx, sibling)
	if err != nil || kept.Branch != "sibling" || kept.Registry != "established" {
		t.Fatalf("sibling changed: %+v %v", kept, err)
	}
}
