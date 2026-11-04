package control

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lgvo/p.ai/internal/api"
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
	if err = s.AdvanceOperation(ctx, old.ID, "blocked", "source-ready", false, old.Evidence, "internal", "branch advanced"); err != nil {
		t.Fatal(err)
	}
	newReq := ReserveSessionRequest{Key: "changed-create", Project: "team/app", Branch: "work", Choice: "existing"}
	intent := CreateReplaceIntent{OldOperationID: old.ID, OldUUID: session.UUID, OldEvidenceSHA256: digest(old.Evidence), OldPolicySHA256: session.PolicySHA256,
		NewPolicySHA256: session.PolicySHA256, IfMatch: testTag(time.Minute), ChangeHash: testHash, New: newReq}
	observe := func(context.Context, []CapacityReservation) (CapacityObservation, error) {
		return CapacityObservation{Limit: 4, PhysicalCount: 0, Occupied: map[string]bool{}}, nil
	}
	verified := false
	check := func(context.Context) (string, error) { verified = true; return newTip, nil }
	bad := intent
	bad.OldEvidenceSHA256 = strings.Repeat("0", 64)
	if _, e := s.ReplaceBlockedCreate(ctx, bad, strings.Repeat("d", 64), testSelection(), nil, observe, check); !errors.Is(e, ErrConflict) || !answers(e, api.KindPreconditionFailed, api.ReasonChanged) {
		t.Fatalf("changed old evidence admitted: %v", e)
	}
	bad = intent
	bad.NewPolicySHA256 = strings.Repeat("0", 64)
	if _, e := s.ReplaceBlockedCreate(ctx, bad, strings.Repeat("d", 64), testSelection(), nil, observe, check); !errors.Is(e, ErrConflict) || !answers(e, api.KindPreconditionFailed, api.ReasonChanged) {
		t.Fatalf("changed current policy admitted: %v", e)
	}
	bad = intent
	bad.IfMatch = testTag(-time.Second)
	if _, e := s.ReplaceBlockedCreate(ctx, bad, strings.Repeat("d", 64), testSelection(), nil, observe, check); !errors.Is(e, ErrConflict) || !answers(e, api.KindPreconditionFailed, api.ReasonExpired) {
		t.Fatalf("expired tag admitted: %v", e)
	}
	// A tag that expires while the replacement waits for the store's
	// connection is refused inside its transaction.
	bad = intent
	expiry := time.Now().Add(2 * time.Second).Truncate(time.Second)
	bad.IfMatch = changeTag(expiry, testHash)
	if _, e := s.ReplaceBlockedCreate(ctx, bad, strings.Repeat("d", 64), testSelection(), nil, observe, func(ctx context.Context) (string, error) {
		held, e := s.beginJournal(ctx)
		if e != nil {
			return "", e
		}
		go func() { time.Sleep(time.Until(expiry) + 50*time.Millisecond); held.Rollback() }()
		return newTip, nil
	}); !answers(e, api.KindPreconditionFailed, api.ReasonExpired) {
		t.Fatalf("a tag that expired before the transaction answered %v", e)
	}
	if _, e := s.GetSession(ctx, session.UUID); e != nil {
		t.Fatalf("failed admission removed old session: %v", e)
	}
	holdStatusState(t, s, session.UUID)
	verified = false
	fresh, e := s.ReplaceBlockedCreate(ctx, intent, strings.Repeat("d", 64), testSelection(), nil, observe, check)
	if e != nil || !verified || fresh.ID == old.ID || fresh.SessionUUID == session.UUID || fresh.Phase != "source-ready" {
		t.Fatalf("new immutable create: %+v %v", fresh, e)
	}
	if _, e = s.GetSession(ctx, session.UUID); !errors.Is(e, ErrNotFound) {
		t.Fatalf("old reservation retained: %v", e)
	}
	requireStatusStateReleased(t, s, session.UUID)
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
	bad.IfMatch = testTag(2 * time.Minute)
	if _, e := s.ReplaceBlockedCreate(ctx, bad, strings.Repeat("d", 64), testSelection(), nil, observe, check); !errors.Is(e, ErrConflict) || !answers(e, api.KindInvalid, api.ReasonKeyReused) {
		t.Fatalf("wrong tag replay: %v", e)
	}
	if _, found, e := s.ReplayCreateReplacement(ctx, session.UUID, bad.IfMatch, intent.New); found || !answers(e, api.KindInvalid, api.ReasonKeyReused) {
		t.Fatalf("replay with another tag: %v %v", found, e)
	}
	var recorded string
	if e = s.db.QueryRowContext(ctx, `SELECT change_hash FROM operations WHERE id=?`, fresh.ID).Scan(&recorded); e != nil || recorded != testHash {
		t.Fatalf("the replacement's creation recorded change hash %q: %v", recorded, e)
	}
}

// answers reports whether err answers kind with reason.
func answers(err error, kind api.ErrorKind, reason string) bool {
	answer := answerError(context.Background(), err)
	return answer.Kind() == kind && answer.Reason() == reason
}

func TestReplaceBlockedCreateRefusesProvisionalBuilderAndPrincipal(t *testing.T) {
	for _, name := range []string{"", "builder-attempted", "builder-abandoned", "builder-absent", "principal"} {
		t.Run(name, func(t *testing.T) {
			s, _ := openTestStore(t)
			ctx := context.Background()
			if e := s.CreateProject(ctx, "team/app", json.RawMessage(`{"network":"none"}`)); e != nil {
				t.Fatal(e)
			}
			base := strings.Repeat("d", 64)
			env := &EnvironmentIntent{System: "x86_64-linux", BaseFingerprint: base, BuilderStoragePool: "default", BuilderPolicySHA256: base, ImageBuilder: "incus-commit-image/1"}
			oldReq := ReserveSessionRequest{Key: "builder-old", Project: "team/app", Branch: "work", Choice: "existing"}
			old, session, e := s.BeginSessionCreate(ctx, oldReq, base, testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
				return strings.Repeat("a", 40), true, nil
			}, env)
			if e != nil {
				t.Fatal(e)
			}
			ev, _ := Evidence(old)
			states := map[string][]string{"builder-attempted": {"attempted"}, "builder-abandoned": {"attempted", "abandoned"}, "builder-absent": {"attempted", "absent"}}[name]
			for _, state := range states {
				ev.EnvironmentBuilder = &CreationBuilderState{Cycle: 1, State: state}
				raw, _ := json.Marshal(ev)
				if e = s.AdvanceOperation(ctx, old.ID, "blocked", "branch-assigned", true, raw, "internal", "builder failed"); e != nil {
					t.Fatal(e)
				}
			}
			raw, _ := json.Marshal(ev)
			if e = s.AdvanceOperation(ctx, old.ID, "blocked", "branch-assigned", true, raw, "internal", "before key"); e != nil {
				t.Fatal(e)
			}
			if name == "principal" {
				if e = s.RegisterGitPrincipal(ctx, strings.Repeat("e", 64), "session", session.Project, session.UUID); e != nil {
					t.Fatal(e)
				}
			}
			intent := CreateReplaceIntent{OldOperationID: old.ID, OldUUID: session.UUID, OldEvidenceSHA256: digest(raw), OldPolicySHA256: session.PolicySHA256, NewPolicySHA256: session.PolicySHA256,
				IfMatch: testTag(time.Minute), ChangeHash: testHash, New: ReserveSessionRequest{Key: "builder-new", Project: "team/app", Branch: "work", Choice: "existing"}}
			observe := func(context.Context, []CapacityReservation) (CapacityObservation, error) {
				return CapacityObservation{Limit: 4, Occupied: map[string]bool{}}, nil
			}
			_, e = s.ReplaceBlockedCreate(ctx, intent, base, testSelection(), nil, observe, func(context.Context) (string, error) { return strings.Repeat("b", 40), nil })
			if name == "" && e != nil {
				t.Fatalf("pinned builder that never ran refused: %v", e)
			}
			if name != "" && !errors.Is(e, ErrConflict) {
				t.Fatalf("%s admitted: %v", name, e)
			}
		})
	}
}

func TestReplaceBlockedCreateSQLiteRefusalsPreserveReservation(t *testing.T) {
	for _, name := range []string{"origin-source", "later-phase", "retried", "ref-cas", "environment-effect", "foreign-assignment", "ref-guard", "native-unknown", "unchanged", "capacity", "rollback", "retry-during-proof"} {
		t.Run(name, func(t *testing.T) {
			s, _ := openTestStore(t)
			ctx := context.Background()
			if e := s.CreateProject(ctx, "team/app", json.RawMessage(`{"network":"none"}`)); e != nil {
				t.Fatal(e)
			}
			oldReq := ReserveSessionRequest{Key: "old", Project: "team/app", Branch: "work", Choice: "existing"}
			if name == "origin-source" {
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
			case "origin-source":
				ev.OriginURL = "ssh://origin.invalid/repo"
				ev.OriginRef = "refs/heads/main"
			case "later-phase":
				phase, committed = "runtime-created", true
			case "retried":
				status = "running"
			case "ref-cas":
				ev.RefCASIntent = true
			case "environment-effect":
				ev.Image = &CreationImage{Ref: strings.Repeat("e", 64), Selection: "no-flake"}
			}
			raw, _ := json.Marshal(ev)
			if e = s.AdvanceOperation(ctx, old.ID, status, phase, committed, raw, testReason(status), ""); e != nil {
				t.Fatal(e)
			}
			intent := CreateReplaceIntent{OldOperationID: old.ID, OldUUID: session.UUID, OldEvidenceSHA256: digest(raw), OldPolicySHA256: session.PolicySHA256, NewPolicySHA256: session.PolicySHA256, IfMatch: testTag(time.Minute), ChangeHash: testHash, New: ReserveSessionRequest{Key: "new", Project: "team/app", Branch: "work", Choice: "existing"}}
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
					if e := s.AdvanceOperation(ctx, old.ID, "running", phase, committed, raw, "", ""); e != nil {
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
	if e = s.AdvanceOperation(ctx, old.ID, "blocked", "branch-assigned", true, old.Evidence, "internal", ""); e != nil {
		t.Fatal(e)
	}
	intent := CreateReplaceIntent{OldOperationID: old.ID, OldUUID: session.UUID, OldEvidenceSHA256: digest(old.Evidence), OldPolicySHA256: session.PolicySHA256, NewPolicySHA256: session.PolicySHA256, IfMatch: testTag(time.Minute), ChangeHash: testHash, New: ReserveSessionRequest{Key: "new", Project: "team/app", Branch: "work", Choice: "existing"}}
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
	if e = s.AdvanceOperation(ctx, old.ID, "running", "branch-assigned", true, old.Evidence, "", ""); !errors.Is(e, ErrConflict) {
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
	if e = s.AdvanceOperation(ctx, old.ID, "blocked", "source-ready", false, old.Evidence, "internal", ""); e != nil {
		t.Fatal(e)
	}
	if e = s.SetProjectPolicy(ctx, "team/app", json.RawMessage(`{"network":"none","command":["/bin/sh"]}`)); e != nil {
		t.Fatal(e)
	}
	policy, hash, e := s.ProjectPolicyRecord(ctx, "team/app")
	if e != nil || hash == session.PolicySHA256 {
		t.Fatalf("policy change: %s %v", hash, e)
	}
	intent := CreateReplaceIntent{OldOperationID: old.ID, OldUUID: session.UUID, OldEvidenceSHA256: digest(old.Evidence), OldPolicySHA256: session.PolicySHA256, NewPolicySHA256: hash, IfMatch: testTag(time.Minute), ChangeHash: testHash, New: ReserveSessionRequest{Key: "new", Project: "team/app", Branch: "work", Choice: "existing"}}
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

func TestReplaceBlockedLocalNewCreateChoicesAndRestartReplay(t *testing.T) {
	for _, tc := range []struct {
		name, phase, branch, choice, source string
		tipChanged                          bool
	}{
		{name: "same-absent-target-new-source", phase: "source-ready", branch: "work", choice: "new", source: "refs/heads/main", tipChanged: true},
		{name: "different-absent-target", phase: "source-ready", branch: "other", choice: "new", source: "refs/heads/main"},
		{name: "preserved-created-target", phase: "branch-assigned", branch: "work", choice: "existing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, dir := openTestStore(t)
			ctx := context.Background()
			if e := s.CreateProject(ctx, "team/app", json.RawMessage(`{"network":"none"}`)); e != nil {
				t.Fatal(e)
			}
			oldReq := ReserveSessionRequest{Key: "old", Project: "team/app", Branch: "work", Choice: "new", Source: "refs/heads/main"}
			old, session, e := s.BeginSessionCreate(ctx, oldReq, strings.Repeat("d", 64), testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
				return strings.Repeat("a", 40), false, nil
			})
			if e != nil {
				t.Fatal(e)
			}
			if e = s.AdvanceOperation(ctx, old.ID, "blocked", tc.phase, tc.phase == "branch-assigned", old.Evidence, "internal", ""); e != nil {
				t.Fatal(e)
			}
			req := ReserveSessionRequest{Key: "replacement", Project: session.Project, Branch: tc.branch, Choice: tc.choice, Source: tc.source}
			intent := CreateReplaceIntent{OldOperationID: old.ID, OldUUID: session.UUID, OldEvidenceSHA256: digest(old.Evidence), OldPolicySHA256: session.PolicySHA256, NewPolicySHA256: session.PolicySHA256, IfMatch: testTag(time.Minute), ChangeHash: testHash, New: req}
			tip := strings.Repeat("a", 40)
			if tc.tipChanged {
				tip = strings.Repeat("b", 40)
			}
			observe := func(context.Context, []CapacityReservation) (CapacityObservation, error) {
				return CapacityObservation{Limit: 4, Occupied: map[string]bool{}}, nil
			}
			fresh, e := s.ReplaceBlockedCreate(ctx, intent, strings.Repeat("d", 64), testSelection(), nil, observe, func(context.Context) (string, error) { return tip, nil })
			if e != nil {
				t.Fatal(e)
			}
			ev, e := Evidence(fresh)
			if e != nil || ev.CapturedOID != tip || ev.BranchExisted != (tc.choice == "existing") || ev.RefCASIntent != (tc.choice == "new") || ev.SupersedesUUID != session.UUID {
				t.Fatalf("new immutable choice: %+v %v", ev, e)
			}
			if got, e := s.GetSession(ctx, fresh.SessionUUID); e != nil || got.Branch != tc.branch {
				t.Fatalf("new target assignment: %+v %v", got, e)
			}
			// The replaced creation held work; the replacement names its own
			// branch, choice and source, and none of the captured tips.
			checkIntent(t, s, fresh, api.OperationIntent{Project: "team/app", Session: fresh.SessionUUID, Branch: tc.branch, Choice: tc.choice, Source: tc.source,
				ReplacesSession: session.UUID, ReplacesBranch: "work"})
			if e = s.Close(); e != nil {
				t.Fatal(e)
			}
			s, e = testScopedOpenStore(dir)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			if replay, e := s.ReplaceBlockedCreate(ctx, intent, strings.Repeat("d", 64), testSelection(), nil, observe, func(context.Context) (string, error) { t.Fatal("replay observed source"); return "", nil }); e != nil || replay.ID != fresh.ID {
				t.Fatalf("restart replay: %+v %v", replay, e)
			}
			if got, _, e := s.BeginSessionCreate(ctx, oldReq, strings.Repeat("d", 64), testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
				t.Fatal("superseded replay recaptured")
				return "", false, nil
			}); e != nil || got.Status != "superseded" {
				t.Fatalf("old replay: %+v %v", got, e)
			}
		})
	}
}

func TestReplaceBlockedNewCreateRefusesOccupiedGuardedOrUnchangedTarget(t *testing.T) {
	for _, name := range []string{"occupied", "guarded", "unchanged", "origin-new", "foreign-existing", "stale-source", "image", "stale-during-proof"} {
		t.Run(name, func(t *testing.T) {
			s, _ := openTestStore(t)
			ctx := context.Background()
			if e := s.CreateProject(ctx, "app", json.RawMessage(`{"network":"none"}`)); e != nil {
				t.Fatal(e)
			}
			oldReq := ReserveSessionRequest{Key: "old", Project: "app", Branch: "work", Choice: "new", Source: "refs/heads/main"}
			old, session, e := s.BeginSessionCreate(ctx, oldReq, strings.Repeat("d", 64), testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
				return strings.Repeat("a", 40), false, nil
			})
			if e != nil {
				t.Fatal(e)
			}
			ev, _ := Evidence(old)
			if name == "image" {
				ev.Image = &CreationImage{Ref: strings.Repeat("e", 64), Selection: "no-flake"}
			}
			raw, _ := json.Marshal(ev)
			if e = s.AdvanceOperation(ctx, old.ID, "blocked", "source-ready", false, raw, "internal", ""); e != nil {
				t.Fatal(e)
			}
			req := ReserveSessionRequest{Key: "new", Project: "app", Branch: "other", Choice: "new", Source: "refs/heads/main"}
			if name == "unchanged" {
				req.Branch = oldReq.Branch
			}
			if name == "foreign-existing" {
				req.Choice = "existing"
				req.Source = ""
			}
			if name == "origin-new" {
				req.Source = ""
				req.OriginRef = "refs/heads/main"
				req.ExpectedCommitOID = strings.Repeat("b", 40)
			}
			if name == "occupied" {
				if _, _, e = s.BeginSessionCreate(ctx, ReserveSessionRequest{Key: "foreign", Project: "app", Branch: "other", Choice: "existing"}, strings.Repeat("d", 64), testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
					return strings.Repeat("c", 40), true, nil
				}); e != nil {
					t.Fatal(e)
				}
			}
			if name == "guarded" {
				if _, e = s.db.ExecContext(ctx, `INSERT INTO git_ref_guards(project_path,branch,operation_id) VALUES('app','other',?)`, old.ID); e != nil {
					t.Fatal(e)
				}
			}
			intent := CreateReplaceIntent{OldOperationID: old.ID, OldUUID: session.UUID, OldEvidenceSHA256: digest(raw), OldPolicySHA256: session.PolicySHA256, NewPolicySHA256: session.PolicySHA256, IfMatch: testTag(time.Minute), ChangeHash: testHash, New: req}
			verify := func(context.Context) (string, error) {
				if name == "stale-source" {
					return "", ErrConflict
				}
				if name == "stale-during-proof" {
					if _, e := s.db.ExecContext(ctx, `INSERT INTO git_ref_guards(project_path,branch,operation_id) VALUES('app','other',?)`, old.ID); e != nil {
						t.Fatal(e)
					}
				}
				return strings.Repeat("a", 40), nil
			}
			if _, e = s.ReplaceBlockedCreate(ctx, intent, strings.Repeat("d", 64), testSelection(), nil, func(context.Context, []CapacityReservation) (CapacityObservation, error) {
				return CapacityObservation{Limit: 4, Occupied: map[string]bool{}}, nil
			}, verify); e == nil {
				t.Fatal("unsafe replacement admitted")
			}
			if _, e = s.GetSession(ctx, session.UUID); e != nil {
				t.Fatal("old assignment removed", e)
			}
			if got, e := s.GetOperation(ctx, old.ID); e != nil || got.Status != "blocked" {
				t.Fatalf("old operation changed: %+v %v", got, e)
			}
		})
	}
}
