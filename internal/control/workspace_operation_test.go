package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestWorkspaceLossInspectDurableSessionGuardAndReopen(t *testing.T) {
	s, dir := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "team/app", json.RawMessage(`{"network":"none"}`)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"} {
		_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256) VALUES(?,'team/app',?,'established','{}',?)`, id, id, strings.Repeat("a", 64))
		if err != nil {
			t.Fatal(err)
		}
	}
	ev := WorkspaceInspectEvidence{InstanceUUID: "33333333-3333-3333-3333-333333333333", ImageFingerprint: strings.Repeat("a", 64), BaseFingerprint: strings.Repeat("b", 64),
		SourceIncusUUID: "44444444-4444-4444-4444-444444444444", SourceGeneration: "55555555-5555-5555-5555-555555555555", OriginalStatus: "Running"}
	req := WorkspaceInspectRequest{Key: "inspect-a", SessionUUID: "11111111-1111-1111-1111-111111111111"}
	op, err := s.BeginWorkspaceLossInspect(ctx, req, ev)
	if err != nil || op.Phase != "helper-intent" || op.ID == req.SessionUUID {
		t.Fatalf("durable helper intent: %+v %v", op, err)
	}
	if replay, err := s.BeginWorkspaceLossInspect(ctx, req, ev); err != nil || replay.ID != op.ID {
		t.Fatalf("idempotent replay: %+v %v", replay, err)
	}
	if _, err := s.BeginWorkspaceLossInspect(ctx, WorkspaceInspectRequest{Key: "inspect-other", SessionUUID: req.SessionUUID}, ev); !errors.Is(err, ErrConflict) {
		t.Fatalf("second active operation accepted: %v", err)
	}
	if _, err := s.BeginWorkspaceLossInspect(ctx, WorkspaceInspectRequest{Key: req.Key, SessionUUID: "22222222-2222-2222-2222-222222222222"}, ev); !errors.Is(err, ErrConflict) {
		t.Fatalf("key reused for another session: %v", err)
	}
	if _, err := s.BeginWorkspaceLossInspect(ctx, WorkspaceInspectRequest{Key: "inspect-b", SessionUUID: "22222222-2222-2222-2222-222222222222"}, ev); err != nil {
		t.Fatalf("independent session blocked: %v", err)
	}
	active, yes, err := s.ActiveWorkspaceInspect(ctx, req.SessionUUID)
	if err != nil || !yes || active.ID != op.ID {
		t.Fatalf("active guard missing: %+v %v %v", active, yes, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	active, yes, err = reopened.ActiveWorkspaceInspect(ctx, req.SessionUUID)
	if err != nil || !yes || active.ID != op.ID {
		t.Fatalf("restart lost guard: %+v %v %v", active, yes, err)
	}
	if err := reopened.AdvanceOperation(ctx, op.ID, "completed", "inspected", true, op.Evidence, ""); err != nil {
		t.Fatal(err)
	}
	if _, yes, err := reopened.ActiveWorkspaceInspect(ctx, req.SessionUUID); err != nil || yes {
		t.Fatalf("completed guard retained: %v %v", yes, err)
	}
}

func TestWorkspaceLossInspectRejectsInvalidEvidenceBeforeIntent(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	const incusUUID, generation = "44444444-4444-4444-4444-444444444444", "55555555-5555-5555-5555-555555555555"
	for _, ev := range []WorkspaceInspectEvidence{
		{InstanceUUID: "bad", ImageFingerprint: strings.Repeat("a", 64), BaseFingerprint: strings.Repeat("b", 64), SourceIncusUUID: incusUUID, SourceGeneration: generation, OriginalStatus: "Stopped"},
		{InstanceUUID: "33333333-3333-3333-3333-333333333333", ImageFingerprint: strings.Repeat("a", 64), BaseFingerprint: strings.Repeat("b", 64), SourceIncusUUID: incusUUID, SourceGeneration: generation, OriginalStatus: "Frozen"},
	} {
		if _, err := s.BeginWorkspaceLossInspect(ctx, WorkspaceInspectRequest{Key: "bad", SessionUUID: "11111111-1111-1111-1111-111111111111"}, ev); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid evidence accepted: %v", err)
		}
	}
}

func TestWorkspaceLossInspectDurableGuard(t *testing.T) {
	s, dir := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "team/app", json.RawMessage(`{"network":"none"}`)); err != nil {
		t.Fatal(err)
	}
	const sessionID = "11111111-1111-1111-1111-111111111111"
	if _, err := s.db.ExecContext(ctx, `INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256) VALUES(?,'team/app','main','established','{}',?)`, sessionID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	ev := WorkspaceInspectEvidence{InstanceUUID: "33333333-3333-3333-3333-333333333333", ImageFingerprint: strings.Repeat("a", 64), BaseFingerprint: strings.Repeat("b", 64),
		SourceIncusUUID: "44444444-4444-4444-4444-444444444444", SourceGeneration: "55555555-5555-5555-5555-555555555555", OriginalStatus: "Stopped"}
	req := WorkspaceInspectRequest{Key: "loss-1", SessionUUID: sessionID}
	if _, err := s.BeginWorkspaceLossInspect(ctx, req, WorkspaceInspectEvidence{InstanceUUID: ev.InstanceUUID, ImageFingerprint: ev.ImageFingerprint, BaseFingerprint: ev.BaseFingerprint, OriginalStatus: ev.OriginalStatus}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("loss accepted unbound native generation: %v", err)
	}
	op, err := s.BeginWorkspaceLossInspect(ctx, req, ev)
	if err != nil || op.Kind != "workspace.loss.inspect" || op.Phase != "helper-intent" {
		t.Fatalf("loss intent unavailable: %+v %v", op, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	active, yes, err := reopened.ActiveWorkspaceInspect(ctx, sessionID)
	if err != nil || !yes || active.Kind != "workspace.loss.inspect" || active.ID != op.ID {
		t.Fatalf("loss guard lost on restart: %+v %v %v", active, yes, err)
	}
	if err := reopened.AdvanceOperation(ctx, op.ID, "completed", "inspected", true, op.Evidence, ""); err != nil {
		t.Fatal(err)
	}
	_, err = reopened.db.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,created_at,updated_at)
	 VALUES(?,?,?,?,?,'{}',?,'running','helper-intent','now','now')`,
		"66666666-6666-6666-6666-666666666666", "orphan-loss", "workspace.loss.inspect", "missing/project",
		"77777777-7777-7777-7777-777777777777", strings.Repeat("a", 64))
	if err == nil {
		t.Fatal("admitted loss intent without established source")
	}
}
