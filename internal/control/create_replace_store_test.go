package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestReplaceBlockedExistingCreateAtomicReplayAndRefusal(t *testing.T) {
	s, dir := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "team/app", json.RawMessage(`{"network":"none"}`)); err != nil {
		t.Fatal(err)
	}
	oldTip, newTip := strings.Repeat("a", 40), strings.Repeat("b", 40)
	oldReq := ReserveSessionRequest{Key: "old-create", Project: "team/app", Branch: "work", Choice: "existing"}
	old, session, err := s.BeginSessionCreate(ctx, oldReq, strings.Repeat("d", 64), testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) { return oldTip, true, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AdvanceOperation(ctx, old.ID, "blocked", "source-ready", false, old.Evidence, "branch advanced"); err != nil {
		t.Fatal(err)
	}
	newReq := ReserveSessionRequest{Key: "changed-create", Project: "team/app", Branch: "work", Choice: "existing"}
	intent := CreateReplaceIntent{OldOperationID: old.ID, OldUUID: session.UUID, OldEvidenceSHA256: digest(old.Evidence), OldPolicySHA256: session.PolicySHA256,
		NewPolicySHA256: session.PolicySHA256, TokenSHA256: strings.Repeat("f", 64), ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), New: newReq}
	observe := func(context.Context, []CapacityReservation) (CapacityObservation, error) {
		return CapacityObservation{Limit: 4, PhysicalCount: 0, Occupied: map[string]bool{}}, nil
	}
	verified := false
	check := func(context.Context) (string, error) { verified = true; return newTip, nil }
	bad := intent
	bad.OldEvidenceSHA256 = strings.Repeat("0", 64)
	if _, e := s.ReplaceBlockedCreate(ctx, bad, strings.Repeat("d", 64), testSelection(), nil, observe, check); !errors.Is(e, ErrConflict) {
		t.Fatalf("changed old evidence admitted: %v", e)
	}
	bad = intent
	bad.NewPolicySHA256 = strings.Repeat("0", 64)
	if _, e := s.ReplaceBlockedCreate(ctx, bad, strings.Repeat("d", 64), testSelection(), nil, observe, check); !errors.Is(e, ErrConflict) {
		t.Fatalf("changed current policy admitted: %v", e)
	}
	bad = intent
	bad.ExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	if _, e := s.ReplaceBlockedCreate(ctx, bad, strings.Repeat("d", 64), testSelection(), nil, observe, check); !errors.Is(e, ErrConflict) {
		t.Fatalf("expired token admitted: %v", e)
	}
	if _, e := s.GetSession(ctx, session.UUID); e != nil {
		t.Fatalf("failed admission removed old session: %v", e)
	}
	verified = false
	fresh, e := s.ReplaceBlockedCreate(ctx, intent, strings.Repeat("d", 64), testSelection(), nil, observe, check)
	if e != nil || !verified || fresh.ID == old.ID || fresh.SessionUUID == session.UUID || fresh.Phase != "source-ready" {
		t.Fatalf("new immutable create: %+v %v", fresh, e)
	}
	if _, e = s.GetSession(ctx, session.UUID); !errors.Is(e, ErrNotFound) {
		t.Fatalf("old reservation retained: %v", e)
	}
	if got, e := s.GetOperation(ctx, old.ID); e != nil || got.Status != "superseded" {
		t.Fatalf("old request not superseded: %+v %v", got, e)
	}
	if got, e := s.GetSession(ctx, fresh.SessionUUID); e != nil || got.Branch != "work" || got.PolicySHA256 != session.PolicySHA256 {
		t.Fatalf("new reservation missing: %+v %v", got, e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = testScopedOpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if replay, e := s.ReplaceBlockedCreate(ctx, intent, strings.Repeat("d", 64), testSelection(), nil, observe, func(context.Context) (string, error) { t.Fatal("replay observed native/Git"); return "", nil }); e != nil || replay.ID != fresh.ID {
		t.Fatalf("restart replay: %+v %v", replay, e)
	}
	bad = intent
	bad.TokenSHA256 = strings.Repeat("e", 64)
	if _, e := s.ReplaceBlockedCreate(ctx, bad, strings.Repeat("d", 64), testSelection(), nil, observe, check); !errors.Is(e, ErrConflict) {
		t.Fatalf("wrong token replay: %v", e)
	}
}

func TestReplaceBlockedCreateRefusesProvisionalBuilderAndPrincipal(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if e := s.CreateProject(ctx, "team/app", json.RawMessage(`{"network":"none"}`)); e != nil {
		t.Fatal(e)
	}
	oldReq := ReserveSessionRequest{Key: "builder-old", Project: "team/app", Branch: "work", Choice: "existing"}
	old, session, e := s.BeginSessionCreate(ctx, oldReq, strings.Repeat("d", 64), testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
		return strings.Repeat("a", 40), true, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	ev, _ := Evidence(old)
	ev.BuilderTreeOID = strings.Repeat("c", 40)
	raw, _ := json.Marshal(ev)
	if e = s.AdvanceOperation(ctx, old.ID, "blocked", "branch-assigned", true, raw, "builder failed"); e != nil {
		t.Fatal(e)
	}
	intent := CreateReplaceIntent{OldOperationID: old.ID, OldUUID: session.UUID, OldEvidenceSHA256: digest(raw), OldPolicySHA256: session.PolicySHA256, NewPolicySHA256: session.PolicySHA256,
		TokenSHA256: strings.Repeat("f", 64), ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), New: ReserveSessionRequest{Key: "builder-new", Project: "team/app", Branch: "work", Choice: "existing"}}
	observe := func(context.Context, []CapacityReservation) (CapacityObservation, error) {
		return CapacityObservation{Limit: 4, Occupied: map[string]bool{}}, nil
	}
	if _, e = s.ReplaceBlockedCreate(ctx, intent, strings.Repeat("d", 64), testSelection(), nil, observe, func(context.Context) (string, error) { return strings.Repeat("b", 40), nil }); !errors.Is(e, ErrConflict) {
		t.Fatalf("builder intent admitted: %v", e)
	}
	ev.BuilderTreeOID = ""
	raw, _ = json.Marshal(ev)
	if e = s.AdvanceOperation(ctx, old.ID, "blocked", "branch-assigned", true, raw, "before key"); e != nil {
		t.Fatal(e)
	}
	intent.OldEvidenceSHA256 = digest(raw)
	if e = s.RegisterGitPrincipal(ctx, strings.Repeat("e", 64), "session", session.Project, session.UUID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReplaceBlockedCreate(ctx, intent, strings.Repeat("d", 64), testSelection(), nil, observe, func(context.Context) (string, error) { return strings.Repeat("b", 40), nil }); !errors.Is(e, ErrConflict) {
		t.Fatalf("registered principal admitted: %v", e)
	}
}

func TestReplaceBlockedCreateSQLiteRefusalsPreserveReservation(t *testing.T) {
	for _, name := range []string{"new-branch", "later-phase", "retried", "ref-cas", "environment-effect", "foreign-assignment", "ref-guard", "native-unknown", "unchanged", "capacity", "rollback", "retry-during-proof"} {
		t.Run(name, func(t *testing.T) {
			s, _ := openTestStore(t)
			ctx := context.Background()
			if e := s.CreateProject(ctx, "team/app", json.RawMessage(`{"network":"none"}`)); e != nil {
				t.Fatal(e)
			}
			oldReq := ReserveSessionRequest{Key: "old", Project: "team/app", Branch: "work", Choice: "existing"}
			if name == "new-branch" {
				oldReq.Choice = "new"
				oldReq.Source = "refs/heads/main"
			}
			old, session, e := s.BeginSessionCreate(ctx, oldReq, strings.Repeat("d", 64), testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
				return strings.Repeat("a", 40), oldReq.Choice == "existing", nil
			})
			if e != nil {
				t.Fatal(e)
			}
			ev, _ := Evidence(old)
			phase, status, committed := "source-ready", "blocked", false
			switch name {
			case "later-phase":
				phase, committed = "runtime-created", true
			case "retried":
				status = "running"
			case "ref-cas":
				ev.RefCASIntent = true
			case "environment-effect":
				ev.EnvironmentState = &EnvironmentState{}
			}
			raw, _ := json.Marshal(ev)
			if e = s.AdvanceOperation(ctx, old.ID, status, phase, committed, raw, ""); e != nil {
				t.Fatal(e)
			}
			intent := CreateReplaceIntent{OldOperationID: old.ID, OldUUID: session.UUID, OldEvidenceSHA256: digest(raw), OldPolicySHA256: session.PolicySHA256, NewPolicySHA256: session.PolicySHA256, TokenSHA256: strings.Repeat("f", 64), ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), New: ReserveSessionRequest{Key: "new", Project: "team/app", Branch: "work", Choice: "existing"}}
			switch name {
			case "foreign-assignment":
				if _, e = s.db.ExecContext(ctx, `UPDATE sessions SET branch='foreign' WHERE uuid=?`, session.UUID); e != nil {
					t.Fatal(e)
				}
			case "ref-guard":
				if _, e = s.db.ExecContext(ctx, `INSERT INTO git_ref_guards(project_path,branch,operation_id) VALUES(?,?,?)`, session.Project, session.Branch, old.ID); e != nil {
					t.Fatal(e)
				}
			case "rollback":
				if _, e = s.db.ExecContext(ctx, `CREATE TRIGGER fail_replacement BEFORE INSERT ON operations BEGIN SELECT RAISE(ABORT,'test rollback'); END`); e != nil {
					t.Fatal(e)
				}
			}
			observe := func(context.Context, []CapacityReservation) (CapacityObservation, error) {
				limit := 4
				count := 0
				if name == "capacity" {
					limit, count = 2, 2
				}
				return CapacityObservation{Limit: limit, PhysicalCount: count, Occupied: map[string]bool{}}, nil
			}
			verify := func(context.Context) (string, error) {
				if name == "retry-during-proof" {
					if e := s.AdvanceOperation(ctx, old.ID, "running", phase, committed, raw, ""); e != nil {
						t.Fatal(e)
					}
					status = "running"
				}
				if name == "native-unknown" {
					return "", errors.New("native unavailable")
				}
				if name == "unchanged" {
					return ev.CapturedOID, nil
				}
				return strings.Repeat("b", 40), nil
			}
			if _, e = s.ReplaceBlockedCreate(ctx, intent, strings.Repeat("d", 64), testSelection(), nil, observe, verify); e == nil {
				t.Fatal("unsafe replacement admitted")
			}
			if got, e := s.GetOperation(ctx, old.ID); e != nil || got.Status != status {
				t.Fatalf("old request changed: %+v %v", got, e)
			}
			if _, e := s.GetSession(ctx, session.UUID); e != nil {
				t.Fatalf("old reservation removed: %v", e)
			}
			if _, e := s.GetOperationByKey(ctx, "new"); !errors.Is(e, ErrNotFound) {
				t.Fatalf("new request survived refusal: %v", e)
			}
		})
	}
}

func TestReplaceBranchAssignedCreateTransfersPublicSlotAndRetiresOldKey(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if e := s.CreateProject(ctx, "team/app", json.RawMessage(`{"network":"public-egress"}`)); e != nil {
		t.Fatal(e)
	}
	req := ReserveSessionRequest{Key: "old", Project: "team/app", Branch: "work", Choice: "existing"}
	old, session, e := s.BeginSessionCreate(ctx, req, strings.Repeat("d", 64), testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
		return strings.Repeat("a", 40), true, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	slot, e := s.PublicAddressSlot(ctx, session.UUID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.AdvanceOperation(ctx, old.ID, "blocked", "branch-assigned", true, old.Evidence, ""); e != nil {
		t.Fatal(e)
	}
	intent := CreateReplaceIntent{OldOperationID: old.ID, OldUUID: session.UUID, OldEvidenceSHA256: digest(old.Evidence), OldPolicySHA256: session.PolicySHA256, NewPolicySHA256: session.PolicySHA256, TokenSHA256: strings.Repeat("f", 64), ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), New: ReserveSessionRequest{Key: "new", Project: "team/app", Branch: "work", Choice: "existing"}}
	// Exactly the old reservation and helper fit. Ordinary admission would need a third slot.
	observe := func(context.Context, []CapacityReservation) (CapacityObservation, error) {
		return CapacityObservation{Limit: 2, Occupied: map[string]bool{}}, nil
	}
	fresh, e := s.ReplaceBlockedCreate(ctx, intent, strings.Repeat("d", 64), testSelection(), nil, observe, func(context.Context) (string, error) { return strings.Repeat("b", 40), nil })
	if e != nil {
		t.Fatal(e)
	}
	if got, e := s.PublicAddressSlot(ctx, fresh.SessionUUID); e != nil || got != slot {
		t.Fatalf("public address not transferred: %d %v", got, e)
	}
	if _, e := s.PublicAddressSlot(ctx, session.UUID); !errors.Is(e, ErrNotFound) {
		t.Fatalf("old address leaked: %v", e)
	}
	var count int
	if e = s.db.QueryRowContext(ctx, `SELECT count(*) FROM session_public_addresses`).Scan(&count); e != nil || count != 1 {
		t.Fatalf("address count: %d %v", count, e)
	}
	if replay, _, e := s.BeginSessionCreate(ctx, req, strings.Repeat("d", 64), testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
		t.Fatal("old replay recaptured source")
		return "", false, nil
	}); e != nil || replay.ID != old.ID || replay.Status != "superseded" {
		t.Fatalf("original exact replay: %+v %v", replay, e)
	}
	if e = s.AdvanceOperation(ctx, old.ID, "running", "branch-assigned", true, old.Evidence, ""); !errors.Is(e, ErrConflict) {
		t.Fatalf("superseded request resurrected: %v", e)
	}
}

func TestReplaceBlockedCreateCapturesChangedPolicyWithoutMovingRef(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if e := s.CreateProject(ctx, "team/app", json.RawMessage(`{"network":"none"}`)); e != nil {
		t.Fatal(e)
	}
	tip := strings.Repeat("a", 40)
	old, session, e := s.BeginSessionCreate(ctx, ReserveSessionRequest{Key: "old", Project: "team/app", Branch: "work", Choice: "existing"}, strings.Repeat("d", 64), testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) { return tip, true, nil })
	if e != nil {
		t.Fatal(e)
	}
	if e = s.AdvanceOperation(ctx, old.ID, "blocked", "source-ready", false, old.Evidence, ""); e != nil {
		t.Fatal(e)
	}
	if e = s.SetProjectPolicy(ctx, "team/app", json.RawMessage(`{"network":"none","command":["/bin/sh"]}`)); e != nil {
		t.Fatal(e)
	}
	policy, hash, e := s.ProjectPolicyRecord(ctx, "team/app")
	if e != nil || hash == session.PolicySHA256 {
		t.Fatalf("policy change: %s %v", hash, e)
	}
	intent := CreateReplaceIntent{OldOperationID: old.ID, OldUUID: session.UUID, OldEvidenceSHA256: digest(old.Evidence), OldPolicySHA256: session.PolicySHA256, NewPolicySHA256: hash, TokenSHA256: strings.Repeat("f", 64), ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), New: ReserveSessionRequest{Key: "new", Project: "team/app", Branch: "work", Choice: "existing"}}
	fresh, e := s.ReplaceBlockedCreate(ctx, intent, strings.Repeat("d", 64), testSelection(), nil, func(context.Context, []CapacityReservation) (CapacityObservation, error) {
		return CapacityObservation{Limit: 4, Occupied: map[string]bool{}}, nil
	}, func(context.Context) (string, error) { return tip, nil })
	if e != nil {
		t.Fatal(e)
	}
	ev, e := Evidence(fresh)
	if e != nil || ev.CapturedOID != tip || ev.PolicySHA256 != hash {
		t.Fatalf("new capture: %+v %v", ev, e)
	}
	newSession, e := s.GetSession(ctx, fresh.SessionUUID)
	if e != nil || string(newSession.Policy) != string(policy) || newSession.PolicySHA256 != hash {
		t.Fatalf("new policy snapshot: %+v %v", newSession, e)
	}
}
