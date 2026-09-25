package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRefRepairDurableGuardMarkerAndSingleUseAdmission(t *testing.T) {
	s, dir, _, prior := repairStoreFixture(t)
	ctx := context.Background()
	uuid := renameTestUUID
	lossID := "77777777-7777-4777-8777-777777777777"
	lossRaw, _ := json.Marshal(WorkspaceInspectEvidence{InstanceUUID: prior.InstanceUUID,
		ImageFingerprint: prior.ImageFingerprint, BaseFingerprint: prior.BaseFingerprint,
		SourceIncusUUID:  "11111111-1111-4111-8111-111111111111",
		SourceGeneration: "22222222-2222-4222-8222-222222222222", OriginalStatus: "Running",
		Result: json.RawMessage(`{"schema":"p.workspace-loss/v1"}`)})
	if _, err := s.db.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,committed,evidence_json,created_at,updated_at)
	 VALUES(?,'fixture-ref-loss','workspace.loss.inspect',?,?,'{}','fixture','completed','inspected',1,?,'2026','2026')`, lossID, prior.Project, uuid, lossRaw); err != nil {
		t.Fatal(err)
	}
	ev := RefRepairEvidence{Project: prior.Project, Branch: prior.Branch, Tip: prior.AssignedTip,
		PolicySHA256: prior.PolicySHA256, CredentialFingerprint: prior.CredentialFingerprint,
		InstanceUUID: prior.InstanceUUID, IncusProject: prior.IncusProject, InstanceName: prior.InstanceName,
		ImageFingerprint: prior.ImageFingerprint, IncusUUID: "11111111-1111-4111-8111-111111111111",
		Generation: "22222222-2222-4222-8222-222222222222", OriginalStatus: "Running",
		LossOperationID: lossID, LossFingerprint: strings.Repeat("a", 64), LossResultSHA256: digest(lossRaw),
		PreviewExpiresAt: time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)}
	req := RefRepairRequest{Key: "restore-ref-once", UUID: uuid, TokenSHA256: strings.Repeat("c", 64)}
	if _, err := s.BeginRefRepair(ctx, req, ev, func(context.Context) error { return nil }); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired confirmation admitted: %v", err)
	}
	ev.PreviewExpiresAt = time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `UPDATE operations SET phase='completed' WHERE id=?`, lossID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginRefRepair(ctx, req, ev, func(context.Context) error { return nil }); !errors.Is(err, ErrConflict) {
		t.Fatalf("nonterminal loss phase admitted: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE operations SET phase='inspected' WHERE id=?`, lossID); err != nil {
		t.Fatal(err)
	}
	reads := 0
	op, err := s.BeginRefRepair(ctx, req, ev, func(context.Context) error { reads++; return nil })
	if err != nil || op.Phase != "guarded" || reads != 1 {
		t.Fatalf("durable admission: %+v %v reads=%d", op, err, reads)
	}
	if replay, e := s.BeginRefRepair(ctx, req, ev, func(context.Context) error { return ErrConflict }); e != nil || replay.ID != op.ID {
		t.Fatalf("exact key replay reobserved: %+v %v", replay, e)
	}
	if _, active, e := s.ActiveWorkspaceInspect(ctx, uuid); e != nil || !active {
		t.Fatalf("ref repair did not block session mutation: %v %v", active, e)
	}
	called := 0
	if issued, err := s.RefRepairGuardedCreate(ctx, op.ID, func(context.Context, RefRepairEvidence) error { return nil }, func(context.Context, RefRepairEvidence) error { called++; return nil }); !errors.Is(err, ErrConflict) || issued || called != 0 {
		t.Fatalf("effect before quiescence admitted: %v issued=%v calls=%d", err, issued, called)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	unfinished, err := s.UnfinishedRefRepairs(ctx)
	if err != nil || len(unfinished) != 1 || unfinished[0].ID != op.ID {
		t.Fatalf("restart lost ref repair guard: %+v %v", unfinished, err)
	}
	if err := s.AdvanceOperation(ctx, op.ID, "running", "quiesced", false, op.Evidence, ""); err != nil {
		t.Fatal(err)
	}
	if issued, err := s.RefRepairGuardedCreate(ctx, op.ID, func(context.Context, RefRepairEvidence) error { return ErrConflict }, func(context.Context, RefRepairEvidence) error { called++; return nil }); !errors.Is(err, ErrConflict) || issued || called != 0 {
		t.Fatalf("stale pre-marker observation issued create: %v issued=%v calls=%d", err, issued, called)
	}
	if err := s.EndRefRepair(ctx, op.ID, false, func(context.Context, RefRepairEvidence) error { return nil }); err != nil {
		t.Fatalf("definite pre-effect sibling-ref change retained guard: %v", err)
	}
	if _, active, e := s.ActiveWorkspaceInspect(ctx, uuid); e != nil || active {
		t.Fatalf("pre-effect stale repair did not release session: %v %v", active, e)
	}
	if stale, e := s.GetOperation(ctx, op.ID); e != nil || stale.Status != "failed" || stale.Phase != "stale" || stale.Committed {
		t.Fatalf("pre-effect mismatch not terminal stale: %+v %v", stale, e)
	}
	req.Key = "restore-ref-uncertain"
	op, err = s.BeginRefRepair(ctx, req, ev, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("new preview after stale rollback refused: %v", err)
	}
	if err := s.AdvanceOperation(ctx, op.ID, "running", "quiesced", false, op.Evidence, ""); err != nil {
		t.Fatal(err)
	}
	if issued, err := s.RefRepairGuardedCreate(ctx, op.ID, func(context.Context, RefRepairEvidence) error { return nil }, func(_ context.Context, got RefRepairEvidence) error {
		called++
		if got.Tip != ev.Tip {
			return ErrConflict
		}
		return errors.New("native outcome unknown")
	}); !issued || err == nil || called != 1 {
		t.Fatalf("durable marker did not precede uncertain effect: %v issued=%v calls=%d", err, issued, called)
	}
	if issued, err := s.RefRepairGuardedCreate(ctx, op.ID, func(context.Context, RefRepairEvidence) error { return nil }, func(context.Context, RefRepairEvidence) error { called++; return nil }); !errors.Is(err, ErrConflict) || issued || called != 1 {
		t.Fatalf("uncertain effect was reissued: %v issued=%v calls=%d", err, issued, called)
	}
	if err := s.EndRefRepair(ctx, op.ID, false, func(context.Context, RefRepairEvidence) error { return nil }); !errors.Is(err, ErrConflict) {
		t.Fatalf("ambiguous ref attempt released guard: %v", err)
	}
	if err := s.AdvanceOperation(ctx, op.ID, "running", "source-restored", true, op.Evidence, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.EndRefRepair(ctx, op.ID, true, func(_ context.Context, got RefRepairEvidence) error {
		if got.Tip != ev.Tip {
			return ErrConflict
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, active, e := s.ActiveWorkspaceInspect(ctx, uuid); e != nil || active {
		t.Fatalf("completed repair retained guard: %v %v", active, e)
	}
}
