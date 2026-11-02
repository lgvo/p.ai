package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDeleteEnvironmentImageExactRemovesOnlyTheReviewedGeneration(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "team/a", json.RawMessage(`{"network":"none"}`)); err != nil {
		t.Fatal(err)
	}
	a := testEnvironmentImage("team/a")
	a.LogicalSize = 1024
	if err := s.PutEnvironmentImage(ctx, a); err != nil {
		t.Fatal(err)
	}
	a, _, _ = s.GetEnvironmentImage(ctx, a.Project, a.Key)
	if err := s.DeleteEnvironmentImageExact(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetEnvironmentImage(ctx, a.Project, a.Key); err != nil || found {
		t.Fatalf("reviewed generation remained indexed: found=%v err=%v", found, err)
	}
	if err := s.PutEnvironmentImage(ctx, a); err != nil {
		t.Fatal(err)
	}
	newer, _, _ := s.GetEnvironmentImage(ctx, a.Project, a.Key)
	if newer.CreatedAt == a.CreatedAt {
		// SQLite timestamps have nanosecond precision; explicit mutation below
		// still proves exact generation comparison if this clock ties.
		newer.LogicalSize++
		if err := s.PutEnvironmentImage(ctx, newer); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteEnvironmentImageExact(ctx, a); !errors.Is(err, ErrConflict) {
		t.Fatalf("old reviewed claim removed newer index generation: %v", err)
	}
}

func TestEnvironmentLastUseOnlyOnEstablishedExactGeneration(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "team/a", json.RawMessage(`{"network":"none"}`)); err != nil {
		t.Fatal(err)
	}
	e := testEnvironmentImage("team/a")
	e.LogicalSize = 2048
	if err := s.PutEnvironmentImage(ctx, e); err != nil {
		t.Fatal(err)
	}
	e, _, _ = s.GetEnvironmentImage(ctx, e.Project, e.Key)
	if e.LastUsedAt != "" {
		t.Fatal("cache insertion counted as use")
	}
	sid, opid := "550e8400-e29b-41d4-a716-446655440001", "550e8400-e29b-41d4-a716-446655440002"
	if _, err := s.db.ExecContext(ctx, `INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256)
	 VALUES(?,?,'feature','creating','{}','fixture')`, sid, e.Project); err != nil {
		t.Fatal(err)
	}
	ev := CreationEvidence{EnvironmentState: &EnvironmentState{Key: e.Key, Fingerprint: e.Fingerprint}}
	raw, _ := json.Marshal(ev)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,evidence_json,created_at,updated_at)
	 VALUES(?,'use-exact','session.create',?,?,'{}','fixture','running','workspace-ready',?,'2026-09-24T00:00:00Z','2026-09-24T00:00:00Z')`, opid, e.Project, sid, string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteCreation(ctx, opid); err != nil {
		t.Fatal(err)
	}
	used, _, _ := s.GetEnvironmentImage(ctx, e.Project, e.Key)
	if used.LastUsedAt == "" || used.CreatedAt != e.CreatedAt {
		t.Fatalf("established use not recorded: %+v", used)
	}
	if err := s.PutEnvironmentImage(ctx, e); err != nil {
		t.Fatal(err)
	}
	usedAgain, _, _ := s.GetEnvironmentImage(ctx, e.Project, e.Key)
	if usedAgain.LastUsedAt != used.LastUsedAt || usedAgain.CreatedAt != e.CreatedAt {
		t.Fatal("same image acceptance reset creation/use metadata")
	}
	replaced := e
	replaced.Fingerprint = strings.Repeat("f", 64)
	if err := s.PutEnvironmentImage(ctx, replaced); !errors.Is(err, ErrConflict) {
		t.Fatalf("different image silently overwrote accepted generation: %v", err)
	}
	if err := s.ForgetEnvironmentImage(ctx, e.Project, e.Key, e.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if err := s.PutEnvironmentImage(ctx, replaced); err != nil {
		t.Fatal(err)
	}
	newGeneration, _, _ := s.GetEnvironmentImage(ctx, e.Project, e.Key)
	if newGeneration.LastUsedAt != "" || newGeneration.Fingerprint != replaced.Fingerprint {
		t.Fatal("new image inherited old accepted use")
	}
}

func TestUnresolvedEnvironmentPublicationBlocksSecondSameKey(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "team/a", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("a", 64)
	opid := "550e8400-e29b-41d4-a716-446655440004"
	evidence := `{"environment_state":{"key":"` + key + `","builder_request_uuid":"` + opid + `","properties":{"p.environment_key":"` + key + `"}}}`
	if _, err := s.db.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,request_json,request_sha256,status,phase,evidence_json,created_at,updated_at)
	 VALUES(?,'first-publish','session.create','team/a','{}','fixture','blocked','environment-publishing',?,'now','now')`, opid, evidence); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.PendingEnvironmentPublication(ctx, "team/a", key, ""); err != nil || !pending {
		t.Fatalf("unresolved publication hidden: %v %v", pending, err)
	}
	if pending, err := s.PendingEnvironmentPublication(ctx, "team/a", key, opid); err != nil || pending {
		t.Fatalf("own exact reconciliation blocked: %v %v", pending, err)
	}
	if pending, err := s.PendingEnvironmentPublication(ctx, "team/a", strings.Repeat("b", 64), ""); err != nil || pending {
		t.Fatalf("unrelated key blocked: %v %v", pending, err)
	}
	if err := s.AdvanceOperation(ctx, opid, "completed", "established", true, json.RawMessage(evidence), ""); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.PendingEnvironmentPublication(ctx, "team/a", key, ""); err != nil || pending {
		t.Fatalf("resolved publication still blocked: %v %v", pending, err)
	}
}

func TestRepairBuilderIntentExcludesSameKeyPublisherAcrossRestart(t *testing.T) {
	s, dir, req, ev := repairStoreFixture(t)
	ctx := context.Background()
	key := strings.Repeat("c", 64)
	id := "550e8400-e29b-41d4-a716-446655440005"
	evidence := `{"environment_key":"` + key + `"}`
	if _, err := s.db.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,evidence_json,created_at,updated_at)
	 VALUES(?,'repair-builder-intent','session.repair',?,?,'{}','fixture','blocked','image-builder-running',?,'now','now')`, id, ev.Project, req.UUID, evidence); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if pending, err := s.PendingEnvironmentPublication(ctx, ev.Project, key, ""); err != nil || !pending {
		t.Fatalf("restart forgot repair builder reservation: %v %v", pending, err)
	}
	if pending, err := s.PendingEnvironmentPublication(ctx, ev.Project, key, id); err != nil || pending {
		t.Fatalf("repair's own reconciliation blocked: %v %v", pending, err)
	}
	if err := s.AdvanceOperation(ctx, id, "blocked", "image-builder-absent", true, json.RawMessage(evidence), ""); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.PendingEnvironmentPublication(ctx, ev.Project, key, ""); err != nil || !pending {
		t.Fatalf("unaccepted published image lost reservation: %v %v", pending, err)
	}
	if err := s.AdvanceOperation(ctx, id, "completed", "image-ready", true, json.RawMessage(evidence), ""); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.PendingEnvironmentPublication(ctx, ev.Project, key, ""); err != nil || pending {
		t.Fatalf("accepted repair retained reservation: %v %v", pending, err)
	}
}
