package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

func failedNixCleanupFixture(t *testing.T) (*Store, string, api.Operation, CreateCleanupEvidence) {
	return failedNixCleanupFixtureAt(t, true)
}
func failedNixCleanupFixtureAt(t *testing.T, settled bool) (*Store, string, api.Operation, CreateCleanupEvidence) {
	t.Helper()
	s, dir := openTestStore(t)
	ctx := context.Background()
	if e := s.CreateProject(ctx, "app", json.RawMessage(`{"network":"none"}`)); e != nil {
		t.Fatal(e)
	}
	image := strings.Repeat("a", 64)
	env := &EnvironmentIntent{System: "x86_64-linux", BaseFingerprint: image, BuilderStoragePool: "default", BuilderPolicySHA256: image, ImageBuilder: "incus-commit-image/1"}
	req := ReserveSessionRequest{Key: "failed-create", Project: "app", Branch: "work", Choice: "existing"}
	old, session, e := s.BeginSessionCreate(ctx, req, image, testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
		return strings.Repeat("b", 40), true, nil
	}, env)
	if e != nil {
		t.Fatal(e)
	}
	ev, _ := Evidence(old)
	states := []string{"not-attempted"}
	if settled {
		states = []string{"attempted", "absent"}
	}
	for _, state := range states {
		cycle := uint64(1)
		if state == "not-attempted" {
			cycle = 0
		}
		ev.EnvironmentBuilder = &CreationBuilderState{Cycle: cycle, State: state}
		raw, _ := json.Marshal(ev)
		status := "running"
		if state == "absent" || state == "not-attempted" {
			status = "blocked"
		}
		if e = s.AdvanceOperation(ctx, old.ID, status, "branch-assigned", true, raw, testReason(status), "invalid immutable Nix selection"); e != nil {
			t.Fatal(e)
		}
		old.Evidence = raw
	}
	old.Status, old.Phase, old.Committed = "blocked", "branch-assigned", true
	review := api.CreateCleanupPreview{UUID: old.SessionUUID, OldOperationID: old.ID, OldRequest: req, OldPhase: old.Phase, OldEvidenceSHA256: digest(old.Evidence), PolicySHA256: session.PolicySHA256, AssignedBranch: api.CreateReplaceBranch{Ref: "refs/heads/work", Observed: true, Exists: true, OID: ev.CapturedOID}, ImageFingerprint: image, EnvironmentBuilder: (*api.CreationBuilderState)(ev.EnvironmentBuilder), Eligible: true, UnsafeReasons: []string{}, ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}
	return s, dir, old, CreateCleanupEvidence{Review: review, InstanceUUID: "11111111-1111-4111-8111-111111111111"}
}
func TestFailedCreateCleanupAtomicAuthorityReplayRecoveryAndSeparateCreate(t *testing.T) {
	s, dir, old, ev := failedNixCleanupFixture(t)
	ctx := context.Background()
	local := replacementCleanupIntent(t, old, ev.Review.ImageFingerprint)
	ev.Review.Provisional.Cleanup = local
	if e := s.RegisterGitPrincipal(ctx, local.KeyFingerprint, "session", "app", old.SessionUUID); e != nil {
		t.Fatal(e)
	}
	req := DiscardRequest{Key: "cleanup", UUID: old.SessionUUID, TokenSHA256: strings.Repeat("d", 64)}
	n := 0
	verify := func(context.Context) error { n++; return nil }
	cleaned, e := s.BeginCreateCleanup(ctx, req, ev, verify)
	if e != nil || n != 1 || cleaned.SessionUUID != old.SessionUUID || cleaned.Kind != "session.create.cleanup" || !cleaned.Committed {
		t.Fatalf("cleanup admission: %+v %v", cleaned, e)
	}
	checkIntent(t, s, cleaned, api.OperationIntent{Project: "app", Session: old.SessionUUID, Branch: ev.Review.OldRequest.Branch})
	if s.IsGitPrincipalActive(ctx, local.KeyFingerprint) {
		t.Fatal("old Git authority still active")
	}
	if session, e := s.GetSession(ctx, old.SessionUUID); e != nil || session.Registry != "removing" {
		t.Fatal("creation registry misrepresented")
	}
	if got, e := s.GetOperation(ctx, old.ID); e != nil || got.Status != "superseded" {
		t.Fatal("old worker not fenced")
	}
	if e = s.AdvanceOperation(ctx, old.ID, "running", old.Phase, true, old.Evidence, "", ""); !errors.Is(e, ErrConflict) {
		t.Fatal("superseded Retry acted")
	}
	if e = s.CompleteCreateCleanup(ctx, cleaned.ID, func(context.Context, CreateCleanupEvidence) error { return nil }); !errors.Is(e, ErrConflict) {
		t.Fatal("record dropped before local cleanup checkpoint")
	}
	// Crash with confirmed cleanup still pending; reopen one old UUID/op/key.
	if e = s.AdvanceOperation(ctx, cleaned.ID, "blocked", cleaned.Phase, true, cleaned.Evidence, "internal", "fixture interruption"); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = testScopedOpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	replay, e := s.BeginCreateCleanup(ctx, req, ev, func(context.Context) error { t.Fatal("replay re-observed old mutable resources"); return nil })
	if e != nil || replay.ID != cleaned.ID || replay.SessionUUID != old.SessionUUID {
		t.Fatal("cleanup replay changed identity")
	}
	reused := req
	reused.TokenSHA256 = strings.Repeat("e", 64)
	if _, e = s.BeginCreateCleanup(ctx, reused, ev, verify); !answers(e, api.KindInvalid, api.ReasonKeyReused) {
		t.Fatalf("cleanup key reused for another review answered %v", e)
	}
	var count int
	if e = s.db.QueryRowContext(ctx, `SELECT count(*) FROM operations WHERE session_uuid=? AND status IN ('running','blocked')`, old.SessionUUID).Scan(&count); e != nil || count != 1 {
		t.Fatal("active operation invariant violated")
	}
	var saved CreateCleanupEvidence
	if json.Unmarshal(replay.Evidence, &saved) != nil {
		t.Fatal("cleanup evidence missing")
	}
	changed := saved
	changed.Review.AssignedBranch.OID = strings.Repeat("f", 40)
	raw, _ := json.Marshal(changed)
	if e = s.AdvanceOperation(ctx, cleaned.ID, "running", "local-cleanup", true, raw, "", ""); !errors.Is(e, ErrConflict) {
		t.Fatal("approved identity changed")
	}
	saved.LocalComplete = true
	raw, _ = json.Marshal(saved)
	if e = s.AdvanceOperation(ctx, cleaned.ID, "running", "local-complete", true, raw, "", ""); e != nil {
		t.Fatal(e)
	}
	if e = s.AdvanceOperation(ctx, cleaned.ID, "running", "local-cleanup", true, replay.Evidence, "", ""); !errors.Is(e, ErrConflict) {
		t.Fatal("stale Retry rewound completed local cleanup")
	}
	if e = s.CompleteCreateCleanup(ctx, cleaned.ID, func(context.Context, CreateCleanupEvidence) error { return ErrConflict }); !errors.Is(e, ErrConflict) {
		t.Fatal("new native identity or stale ref admitted")
	}
	if _, e = s.GetSession(ctx, old.SessionUUID); e != nil {
		t.Fatal("refusal lost cleanup tombstone")
	}
	if e = s.CompleteCreateCleanup(ctx, cleaned.ID, func(_ context.Context, got CreateCleanupEvidence) error {
		if got.Review.AssignedBranch != ev.Review.AssignedBranch {
			t.Fatal("reviewed ref lost")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.GetSession(ctx, old.SessionUUID); !errors.Is(e, ErrNotFound) {
		t.Fatal("cleanup did not release old assignment")
	}
	if _, found, e := s.SessionGitPrincipal(ctx, old.SessionUUID); e != nil || found {
		t.Fatal("retired credential retained")
	}
	// Corrected source is a separate new Create; no new UUID is admitted by cleanup.
	newReq := ReserveSessionRequest{Key: "corrected-create", Project: "app", Branch: "work", Choice: "existing"}
	next, _, e := s.BeginSessionCreate(ctx, newReq, ev.Review.ImageFingerprint, testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
		return strings.Repeat("e", 40), true, nil
	})
	if e != nil || next.SessionUUID == old.SessionUUID {
		t.Fatal("separate corrected Create failed")
	}
	if prior, e := s.BeginCreateCleanup(ctx, req, ev, verify); e != nil || prior.ID != cleaned.ID || prior.Status != "completed" {
		t.Fatal("completed replay forgot operation")
	}
}
func TestFailedCreateCleanupRefusesUnsafeOrStaleSQLiteAuthority(t *testing.T) {
	for _, name := range []string{"evidence", "expired", "retry-during-proof", "native-unavailable", "builder-attempted", "builder-abandoned", "builder-without-environment", "session-init-attempted", "origin", "image", "later-workspace", "guard", "rollback"} {
		t.Run(name, func(t *testing.T) {
			s, _, old, ev := failedNixCleanupFixture(t)
			ctx := context.Background()
			req := DiscardRequest{Key: "cleanup", UUID: old.SessionUUID, TokenSHA256: strings.Repeat("d", 64)}
			creation, _ := Evidence(old)
			switch name {
			case "evidence":
				ev.Review.OldEvidenceSHA256 = strings.Repeat("0", 64)
			case "expired":
				ev.Review.ExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
			case "builder-attempted":
				creation.EnvironmentBuilder.State = "attempted"
			case "builder-abandoned":
				creation.EnvironmentBuilder.State = "abandoned"
			case "builder-without-environment":
				creation.Environment = nil
			case "session-init-attempted":
				creation.RuntimeInitState = "attempted"
			case "origin":
				creation.OriginURL = "ssh://origin.invalid/repo"
			case "image":
				creation.ImageFingerprint = strings.Repeat("e", 64)
				creation.Image = &CreationImage{Ref: creation.ImageFingerprint, Built: true, Selection: "no-flake"}
				ev.Review.ImageFingerprint = creation.ImageFingerprint
			case "later-workspace":
				ev.Review.OldPhase = "workspace-ready"
			case "guard":
				s.db.ExecContext(ctx, `INSERT INTO git_ref_guards VALUES('app','work',?)`, old.ID)
			case "rollback":
				s.db.ExecContext(ctx, `CREATE TRIGGER fail_cleanup BEFORE INSERT ON operations BEGIN SELECT RAISE(ABORT,'fixture rollback'); END`)
			}
			if name == "builder-attempted" || name == "builder-abandoned" || name == "builder-without-environment" || name == "session-init-attempted" || name == "origin" || name == "image" || name == "later-workspace" {
				raw, _ := json.Marshal(creation)
				ev.Review.OldEvidenceSHA256 = digest(raw)
				// Fixture historical/unsafe durable state without weakening real monotonic API.
				if _, e := s.db.ExecContext(ctx, `UPDATE operations SET evidence_json=?,phase=? WHERE id=?`, string(raw), ev.Review.OldPhase, old.ID); e != nil {
					t.Fatal(e)
				}
			}
			_, e := s.BeginCreateCleanup(ctx, req, ev, func(context.Context) error {
				if name == "native-unavailable" {
					return ErrConflict
				}
				if name == "retry-during-proof" {
					return s.AdvanceOperation(ctx, old.ID, "running", old.Phase, true, old.Evidence, "", "")
				}
				return nil
			})
			if e == nil {
				t.Fatal("unsafe cleanup admitted")
			}
			// The verify callback's own error and a failed write keep their
			// answers; every reviewed fact that no longer holds answers changed.
			if name != "native-unavailable" && name != "rollback" {
				reason := api.ReasonChanged
				if name == "expired" {
					reason = api.ReasonExpired
				}
				if !answers(e, api.KindPreconditionFailed, reason) {
					t.Fatalf("refusal answered %v, want precondition_failed %s", answerError(ctx, e), reason)
				}
			}
			if session, e := s.GetSession(ctx, old.SessionUUID); e != nil || session.Registry != "creating" {
				t.Fatal("refusal changed old registry")
			}
			if _, e = s.GetOperationByKey(ctx, "cleanup"); !errors.Is(e, ErrNotFound) {
				t.Fatal("refusal admitted cleanup")
			}
		})
	}
}
func TestFailedCreateCleanupUnavailableAfterConfirmationPreservesDisabledAuthority(t *testing.T) {
	s, dir, old, ev := failedNixCleanupFixture(t)
	ctx := context.Background()
	local := replacementCleanupIntent(t, old, ev.Review.ImageFingerprint)
	ev.Review.Provisional.Cleanup = local
	if e := s.RegisterGitPrincipal(ctx, local.KeyFingerprint, "session", "app", old.SessionUUID); e != nil {
		t.Fatal(e)
	}
	req := DiscardRequest{Key: "cleanup-unavailable", UUID: old.SessionUUID, TokenSHA256: strings.Repeat("d", 64)}
	op, e := s.BeginCreateCleanup(ctx, req, ev, func(context.Context) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	ev.LocalComplete = true
	raw, _ := json.Marshal(ev)
	if e = s.AdvanceOperation(ctx, op.ID, "running", "local-complete", true, raw, "", ""); e != nil {
		t.Fatal(e)
	}
	for _, phase := range []string{"local-cleanup", "cleaned", "unknown"} {
		if e = s.AdvanceOperation(ctx, op.ID, "running", phase, true, raw, "", ""); !errors.Is(e, ErrConflict) {
			t.Fatalf("checkpoint regressed to %s: %v", phase, e)
		}
	}
	unavailable := errors.New("fixture Incus unavailable")
	if e = s.CompleteCreateCleanup(ctx, op.ID, func(context.Context, CreateCleanupEvidence) error { return unavailable }); !errors.Is(e, unavailable) {
		t.Fatal(e)
	}
	if e = s.AdvanceOperation(ctx, op.ID, "blocked", "local-complete", true, raw, "internal", unavailable.Error()); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = testScopedOpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	session, e := s.GetSession(ctx, old.SessionUUID)
	if e != nil || session.Registry != "removing" {
		t.Fatal("incomplete cleanup forgot registry")
	}
	if s.IsGitPrincipalActive(ctx, local.KeyFingerprint) {
		t.Fatal("unreachable native authority restored old Git principal")
	}
	if e = s.CheckCreateReplacementTarget(ctx, "app", "work", old.SessionUUID); !errors.Is(e, ErrConflict) {
		t.Fatal("incomplete cleanup released ref guard")
	}
	pending, e := unfinishedOf(ctx, s, KindSessionDiscard, KindSessionDelete, KindCreateCleanup)
	if e != nil || len(pending) != 1 || pending[0].ID != op.ID || pending[0].Phase != "local-complete" || pending[0].Status != "blocked" {
		t.Fatal("restart lost forward cleanup")
	}
	if _, _, e = s.BeginSessionCreate(ctx, ReserveSessionRequest{Key: "too-early", Project: "app", Branch: "work", Choice: "existing"}, ev.Review.ImageFingerprint, testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
		return strings.Repeat("b", 40), true, nil
	}); !errors.Is(e, ErrConflict) {
		t.Fatal("uncertain cleanup admitted replacement")
	}
	replay, e := s.BeginCreateCleanup(ctx, req, ev, func(context.Context) error { t.Fatal("accepted replay retried authority inspection"); return nil })
	if e != nil || replay.ID != op.ID {
		t.Fatal("accepted replay changed intent")
	}
	if e = s.AdvanceOperation(ctx, op.ID, "running", "local-complete", true, raw, "", ""); e != nil {
		t.Fatal(e)
	}
	if e = s.CompleteCreateCleanup(ctx, op.ID, func(context.Context, CreateCleanupEvidence) error { return nil }); e != nil {
		t.Fatal("returned authority did not reconcile approved cleanup", e)
	}
}

func TestFailedCreateCleanupBuilderPreflightRetainsExactSQLiteRetryAndCleanup(t *testing.T) {
	for _, settled := range []bool{false, true} {
		name := "first-dispatch"
		if settled {
			name = "settled-prior-cycle"
		}
		t.Run(name, func(t *testing.T) {
			s, _, old, ev := failedNixCleanupFixtureAt(t, settled)
			ctx := context.Background()
			before, _ := Evidence(old)
			if !SafeCreateCleanupEvidence(ev.Review.OldRequest, before) {
				t.Fatal("affirmative pre-dispatch captured tree ineligible")
			}
			if e := s.AdvanceOperation(ctx, old.ID, "running", old.Phase, true, old.Evidence, "", ""); e != nil {
				t.Fatal(e)
			}
			// A native preflight rejection never invokes the dispatch gate. Its blocked
			// status update retains the original key/UUID/source and settled cycle.
			if e := s.AdvanceOperation(ctx, old.ID, "blocked", old.Phase, true, old.Evidence, "internal", "pinned image unavailable before init"); e != nil {
				t.Fatal(e)
			}
			blocked, e := s.GetOperation(ctx, old.ID)
			if e != nil || blocked.Key != old.Key || blocked.SessionUUID != old.SessionUUID || digest(blocked.Evidence) != ev.Review.OldEvidenceSHA256 {
				t.Fatal("preflight failure changed immutable intent")
			}
			request := DiscardRequest{Key: "cleanup-preflight", UUID: old.SessionUUID, TokenSHA256: strings.Repeat("d", 64)}
			cleanup, e := s.BeginCreateCleanup(ctx, request, ev, func(context.Context) error { return nil })
			if e != nil || cleanup.SessionUUID != old.SessionUUID {
				t.Fatal("preflight failure permanently stranded cleanup", e)
			}
		})
	}
}

func TestSupersededCreateReplayPrecedesContestedGitAuthority(t *testing.T) {
	for _, completed := range []bool{false, true} {
		state := "pending"
		if completed {
			state = "cleaned"
		}
		for _, name := range []string{"exact", "changed-request", "immutable-hash", "immutable-request", "wrong-kind", "origin-key"} {
			t.Run(state+"/"+name, func(t *testing.T) {
				s, _, old, ev := failedNixCleanupFixture(t)
				ctx := context.Background()
				cleanup, e := s.BeginCreateCleanup(ctx, DiscardRequest{Key: "cleanup-replay", UUID: old.SessionUUID, TokenSHA256: strings.Repeat("d", 64)}, ev, func(context.Context) error { return nil })
				if e != nil {
					t.Fatal(e)
				}
				if completed {
					ev.LocalComplete = true
					raw, _ := json.Marshal(ev)
					if e = s.AdvanceOperation(ctx, cleanup.ID, "running", "local-complete", true, raw, "", ""); e != nil {
						t.Fatal(e)
					}
					if e = s.CompleteCreateCleanup(ctx, cleanup.ID, func(context.Context, CreateCleanupEvidence) error { return nil }); e != nil {
						t.Fatal(e)
					}
				}
				req := ev.Review.OldRequest
				switch name {
				case "changed-request":
					req.Branch = "different"
				case "immutable-hash":
					if _, e = s.db.ExecContext(ctx, `UPDATE operations SET request_sha256=? WHERE id=?`, strings.Repeat("0", 64), old.ID); e == nil {
						t.Fatal("SQLite permitted immutable hash mutation")
					}
				case "immutable-request":
					if _, e = s.db.ExecContext(ctx, `UPDATE operations SET request_json=? WHERE id=?`, `{"key":"different"}`, old.ID); e == nil {
						t.Fatal("SQLite permitted immutable request mutation")
					}
				case "wrong-kind":
					req.Key = cleanup.Key
				case "origin-key":
					req.Key = "origin-key"
					if _, _, e = s.PrepareOriginChange(ctx, OriginChange{Key: req.Key, Project: req.Project, Kind: "remove"}); e != nil {
						t.Fatal(e)
					}
				}
				_, _, count, e := s.Count(ctx)
				if e != nil {
					t.Fatal(e)
				}
				// Model CompleteCreateCleanup's paused final ref proof: Git authority is
				// held, but no SQLite transaction or session/ref mutation is in progress.
				s.gitAuthority.Lock()
				defer s.gitAuthority.Unlock()
				call, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
				defer cancel()
				got, session, e := s.BeginSessionCreateCapturedWithCapacity(call, req, ev.Review.ImageFingerprint, testSelection(), func(context.Context, ReserveSessionRequest) (CapturedSource, error) {
					t.Fatal("replay captured Git source")
					return CapturedSource{}, nil
				}, func(context.Context, []CapacityReservation) (CapacityObservation, error) {
					t.Fatal("replay observed native admission")
					return CapacityObservation{}, nil
				})
				if name == "exact" || name == "immutable-hash" || name == "immutable-request" {
					if e != nil || got.ID != old.ID || got.Status != "superseded" || session.UUID != "" {
						t.Fatalf("read-only replay waited/refused contested authority: %+v %+v %v", got, session, e)
					}
				} else if !errors.Is(e, ErrConflict) {
					t.Fatalf("immutable replay conflict depended on Git authority: %v", e)
				}
				if _, _, after, e := s.Count(ctx); e != nil || after != count {
					t.Fatal("replay changed operation inventory")
				}
			})
		}
	}
}
