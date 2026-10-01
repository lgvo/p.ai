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

func emptyRelatedDigest() string {
	sum := sha256.Sum256(nil)
	return hex.EncodeToString(sum[:])
}

func collectionClaim(e EnvironmentImage) EnvironmentCollectionClaim {
	return EnvironmentCollectionClaim{
		Image: e, ImagePresent: true, ObservedSize: 1024,
		RelatedDigest: emptyRelatedDigest(), ConfirmationDigest: strings.Repeat("d", 64),
		ConfirmationExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano),
	}
}

func TestEnvironmentCollectionExactGenerationAndProjectGuard(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	for _, project := range []string{"team/a", "team/b", "team/c"} {
		if err := s.CreateProject(ctx, project, json.RawMessage(`{"network":"none"}`)); err != nil {
			t.Fatal(err)
		}
	}
	a := testEnvironmentImage("team/a")
	a.LogicalSize = 1024
	b := testEnvironmentImage("team/b")
	b.LogicalSize = 2048
	for _, e := range []EnvironmentImage{a, b} {
		if err := s.PutEnvironmentImage(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	a, _, _ = s.GetEnvironmentImage(ctx, a.Project, a.Key)
	b, _, _ = s.GetEnvironmentImage(ctx, b.Project, b.Key)
	claimA, claimB := collectionClaim(a), collectionClaim(b)
	opA, err := s.BeginEnvironmentCollection(ctx, "collect-a", claimA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginEnvironmentCollection(ctx, "collect-a", claimA); err != nil {
		t.Fatalf("exact durable replay failed: %v", err)
	}
	if _, err := s.BeginEnvironmentCollection(ctx, "collect-a-different", claimA); !errors.Is(err, ErrConflict) {
		t.Fatalf("second same-project collection accepted: %v", err)
	}
	if _, err := s.BeginEnvironmentCollection(ctx, "collect-b", claimB); err != nil {
		t.Fatalf("other project collection rejected: %v", err)
	}
	if _, _, err := s.ReserveSession(ctx, ReserveSessionRequest{Key: "blocked-create", Project: "team/a", Branch: "feature", Choice: "existing"}); !errors.Is(err, ErrConflict) {
		t.Fatal("new same-project creation bypassed durable guard")
	}
	if err := s.DeleteEnvironmentImageExact(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceOperation(ctx, opA.ID, "completed", "index-absent", true, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.PutEnvironmentImage(ctx, a); err != nil {
		t.Fatalf("completed collection retained write guard: %v", err)
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
		t.Fatalf("old accepted claim removed newer index generation: %v", err)
	}
}

func TestEnvironmentCollectionRejectsStaleOrExpiredBeforeGuard(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "team/a", json.RawMessage(`{"network":"none"}`)); err != nil {
		t.Fatal(err)
	}
	e := testEnvironmentImage("team/a")
	if err := s.PutEnvironmentImage(ctx, e); err != nil {
		t.Fatal(err)
	}
	e, _, _ = s.GetEnvironmentImage(ctx, e.Project, e.Key)
	stale := collectionClaim(e)
	stale.Image.LogicalSize++
	if _, err := s.BeginEnvironmentCollection(ctx, "stale-preview", stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale preview committed: %v", err)
	}
	expired := collectionClaim(e)
	expired.ConfirmationExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	if _, err := s.BeginEnvironmentCollection(ctx, "expired-preview", expired); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired preview committed: %v", err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM operations WHERE kind='environment.collect'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected preview left guard: count=%d err=%v", count, err)
	}
	if err := s.PutEnvironmentImage(ctx, e); err != nil {
		t.Fatalf("rejected preview blocked cache writes: %v", err)
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
