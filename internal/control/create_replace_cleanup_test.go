package control

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func replacementCleanupIntent(t *testing.T, old Operation, image string) *CreateReplacementCleanup {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(key.Marshal())
	identity := func(inode uint64, mode uint32, links uint64) ReplacementLocalIdentity {
		return ReplacementLocalIdentity{Device: 1, Inode: inode, Mode: mode, Links: links}
	}
	return &CreateReplacementCleanup{OldUUID: old.SessionUUID, OldOperationID: old.ID, OldImageFingerprint: image, KeyFingerprint: hex.EncodeToString(sum[:]),
		KeyDirectory: identity(1, 040700, 2), Key: identity(2, 0100600, 1), EndpointPrefix: identity(3, 040700, 3), EndpointDirectory: identity(4, 040755, 2), GitSocket: identity(5, 0140666, 1), SessionSocket: identity(6, 0140666, 1)}
}

func TestReplacePrincipalsReadyAtomicCleanupReplayRestart(t *testing.T) {
	for _, negative := range []string{"", "principal-changed", "inactive", "extra-principal", "cleanup-unreviewed", "cleanup-image", "late-builder", "environment", "init-attempted", "legacy-init", "rollback", "retry-during-proof"} {
		t.Run(negative, func(t *testing.T) {
			s, dir := openTestStore(t)
			ctx := context.Background()
			if err := s.CreateProject(ctx, "app", json.RawMessage(`{"network":"none"}`)); err != nil {
				t.Fatal(err)
			}
			req := ReserveSessionRequest{Key: "old", Project: "app", Branch: "work", Choice: "existing"}
			image := strings.Repeat("d", 64)
			old, session, err := s.BeginSessionCreate(ctx, req, image, testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
				return strings.Repeat("a", 40), true, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			c := replacementCleanupIntent(t, old, image)
			if err = s.RegisterGitPrincipal(ctx, c.KeyFingerprint, "session", "app", old.SessionUUID); err != nil {
				t.Fatal(err)
			}
			ev, _ := Evidence(old)
			if negative == "init-attempted" {
				ev.RuntimeInitState = "attempted"
			}

			if negative == "late-builder" {
				ev.BuilderTreeOID = strings.Repeat("c", 40)
			}
			if negative == "environment" {
				ev.Environment = &EnvironmentIntent{ModuleID: "env", ModuleSHA256: image, ConfigSHA256: image, System: "x86_64-linux", BaseFingerprint: image, BuilderStoragePool: "default", BuilderPolicySHA256: image}
			}
			raw, _ := json.Marshal(ev)
			if err = s.AdvanceOperation(ctx, old.ID, "blocked", "principals-ready", true, raw, "native init refused before effect"); err != nil {
				t.Fatal(err)
			}
			if negative == "legacy-init" {
				// Fixture a historical record written before dispatch tracking existed.
				ev.RuntimeInitState = ""
				raw, _ = json.Marshal(ev)
				if _, err = s.db.ExecContext(ctx, `UPDATE operations SET evidence_json=? WHERE id=?`, string(raw), old.ID); err != nil {
					t.Fatal(err)
				}
			}
			if negative == "init-attempted" || negative == "legacy-init" {
				if err = s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = testScopedOpenStore(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				durable, e := s.GetOperation(ctx, old.ID)
				if e != nil {
					t.Fatal(e)
				}
				durableEv, e := Evidence(durable)
				if e != nil || durableEv.RuntimeInitState != ev.RuntimeInitState {
					t.Fatal("restart changed init provenance")
				}
			}
			intent := CreateReplaceIntent{OldOperationID: old.ID, OldUUID: old.SessionUUID, OldEvidenceSHA256: digest(raw), OldPolicySHA256: session.PolicySHA256, NewPolicySHA256: session.PolicySHA256, TokenSHA256: strings.Repeat("f", 64), ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), Cleanup: c, New: ReserveSessionRequest{Key: "new", Project: "app", Branch: "work", Choice: "existing"}}
			switch negative {
			case "principal-changed":
				c.KeyFingerprint = strings.Repeat("e", 64)
			case "inactive":
				s.db.ExecContext(ctx, `UPDATE git_principals SET active=0 WHERE session_uuid=?`, old.SessionUUID)
			case "extra-principal":
				s.db.ExecContext(ctx, `INSERT INTO git_principals(fingerprint,role,project_path,session_uuid,active) VALUES(?,'session','app',?,0)`, strings.Repeat("e", 64), old.SessionUUID)
			case "cleanup-unreviewed":
				intent.Cleanup = nil
			case "cleanup-image":
				c.OldImageFingerprint = strings.Repeat("e", 64)
			case "rollback":
				s.db.ExecContext(ctx, `CREATE TRIGGER reject_new_op BEFORE INSERT ON operations BEGIN SELECT RAISE(ABORT,'fixture rollback'); END`)
			}
			if negative == "extra-principal" || negative == "inactive" || negative == "principal-changed" {
				if e := s.CheckCreateReplacementPrincipal(ctx, old.SessionUUID, "app", c.KeyFingerprint); e == nil {
					t.Fatal("unsafe principal preview admitted")
				}
			}
			observe := func(context.Context, []CapacityReservation) (CapacityObservation, error) {
				return CapacityObservation{Limit: 4, Occupied: map[string]bool{}}, nil
			}
			verify := func(context.Context) (string, error) {
				if negative == "retry-during-proof" {
					if e := s.AdvanceOperation(ctx, old.ID, "running", "principals-ready", true, raw, ""); e != nil {
						t.Fatal(e)
					}
				}
				return strings.Repeat("b", 40), nil
			}
			fresh, err := s.ReplaceBlockedCreate(ctx, intent, image, testSelection(), nil, observe, verify)
			if negative != "" {
				if err == nil {
					t.Fatal("unsafe admission succeeded")
				}
				if _, e := s.GetSession(ctx, old.SessionUUID); e != nil {
					t.Fatal("refusal retired old session")
				}
				var count int
				if e := s.db.QueryRowContext(ctx, `SELECT count(*) FROM git_principals WHERE session_uuid=?`, old.SessionUUID).Scan(&count); e != nil || count < 1 {
					t.Fatal("refusal lost principal")
				}
				if _, e := s.GetOperationByKey(ctx, "new"); !errors.Is(e, ErrNotFound) {
					t.Fatal("refusal admitted new request")
				}
				return
			}
			if err != nil || fresh.Phase != "replacement-cleanup" || fresh.Committed {
				t.Fatalf("handoff: %+v %v", fresh, err)
			}
			if _, found, e := s.SessionGitPrincipal(ctx, old.SessionUUID); e != nil || found {
				t.Fatal("old Git authority survived atomic handoff")
			}
			if s.IsGitPrincipalActive(ctx, c.KeyFingerprint) {
				t.Fatal("retired old key still authorized")
			}
			if got, e := s.GetOperation(ctx, old.ID); e != nil || got.Status != "superseded" {
				t.Fatal("old op not superseded")
			}
			// Kill/reopen while approved cleanup has not committed completion. The new
			// immutable request retains old identity and cannot be mistaken for ready.
			if err = s.AdvanceOperation(ctx, fresh.ID, "blocked", "replacement-cleanup", false, fresh.Evidence, "fixture interrupted local cleanup"); err != nil {
				t.Fatal(err)
			}
			s.Close()
			s, err = testScopedOpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			replay, err := s.ReplaceBlockedCreate(ctx, intent, image, testSelection(), nil, observe, func(context.Context) (string, error) {
				t.Fatal("accepted replay reobserved old identity")
				return "", nil
			})
			saved, e := Evidence(replay)
			if err != nil || e != nil || replay.ID != fresh.ID || replay.SessionUUID != fresh.SessionUUID || replay.Phase != "replacement-cleanup" || saved.ReplacementCleanup == nil || *saved.ReplacementCleanup != *c {
				t.Fatalf("cleanup checkpoint forgotten: %+v %v", replay, err)
			}
			if e = s.AdvanceOperation(ctx, old.ID, "running", "principals-ready", true, raw, ""); !errors.Is(e, ErrConflict) {
				t.Fatalf("old Retry: %v", e)
			}
			if e = s.AdvanceOperation(ctx, fresh.ID, "running", replay.Phase, replay.Committed, replay.Evidence, ""); e != nil {
				t.Fatalf("cleanup Retry: %v", e)
			}
			saved.ReplacementCleanup.Completed = true
			completedEvidence, _ := json.Marshal(saved)
			if e = s.AdvanceOperation(ctx, fresh.ID, "running", "source-ready", false, completedEvidence, ""); e != nil {
				t.Fatal(e)
			}
			if e = s.AdvanceOperation(ctx, fresh.ID, "running", replay.Phase, false, replay.Evidence, ""); !errors.Is(e, ErrConflict) {
				t.Fatal("stale Retry rewound durable cleanup completion")
			}
			durable, e := s.GetOperation(ctx, fresh.ID)
			if e != nil {
				t.Fatal(e)
			}
			finalEv, e := Evidence(durable)
			if e != nil || durable.Phase != "source-ready" || !finalEv.ReplacementCleanup.Completed {
				t.Fatal("cleanup completion not retained")
			}
			if _, e = s.GetSession(ctx, old.SessionUUID); !errors.Is(e, ErrNotFound) {
				t.Fatal("old row resurrected")
			}
			var count int
			if e = s.db.QueryRowContext(ctx, `SELECT count(*) FROM operations WHERE kind='session.create'`).Scan(&count); e != nil || count != 2 {
				t.Fatal("recovery created request chain")
			}
		})
	}
}
