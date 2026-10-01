package control

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCompletedCreationReplayAfterSessionRemoval(t *testing.T) {
	for _, action := range []string{"discard", "delete"} {
		t.Run(action, func(t *testing.T) {
			s, dir := openTestStore(t)
			ctx := context.Background()
			if err := s.CreateProject(ctx, "app", []byte(`{}`)); err != nil {
				t.Fatal(err)
			}
			req := ReserveSessionRequest{Key: "create-replay", Project: "app", Branch: "work", Choice: "existing"}
			create, session, err := s.BeginSessionCreate(ctx, req, strings.Repeat("d", 64), testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
				return strings.Repeat("a", 40), true, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err = s.CompleteCreation(ctx, create.ID); err != nil {
				t.Fatal(err)
			}
			replay := func(request ReserveSessionRequest) (Operation, Session, error) {
				return s.BeginSessionCreateCapturedWithCapacity(ctx, request, "", CreationSelection{}, func(context.Context, ReserveSessionRequest) (CapturedSource, error) {
					t.Fatal("replay recaptured source")
					return CapturedSource{}, nil
				}, func(context.Context, []CapacityReservation) (CapacityObservation, error) {
					t.Fatal("replay performed capacity admission")
					return CapacityObservation{}, nil
				})
			}
			before, live, err := replay(req)
			if err != nil || before.ID != create.ID || live.UUID != session.UUID || live.Registry != "established" {
				t.Fatalf("live replay changed: %+v %+v %v", before, live, err)
			}
			ev := testDiscardEvidence()
			ev.Action = action
			ev.Branch = session.Branch
			ev.PolicySHA256 = session.PolicySHA256
			ev.InstanceName = "p-" + session.UUID
			ev.ImageFingerprint = strings.Repeat("d", 64)
			removeReq := DiscardRequest{Key: "remove", UUID: session.UUID, TokenSHA256: strings.Repeat("5", 64)}
			var remove Operation
			if action == "delete" {
				ev.DeleteReviewSHA256 = strings.Repeat("6", 64)
				remove, err = s.BeginDelete(ctx, removeReq, ev)
			} else {
				remove, err = s.BeginDiscard(ctx, removeReq, ev)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = s.SetGitRefGuard(ctx, session.Project, session.Branch, remove.ID, true); err != nil {
				t.Fatal(err)
			}
			if err = s.AdvanceOperation(ctx, remove.ID, "running", "validated", false, remove.Evidence, ""); err != nil {
				t.Fatal(err)
			}
			verify := func(context.Context, DiscardEvidence) error { return nil }
			phase := "secrets-absent"
			if action == "delete" {
				err = s.CommitDelete(ctx, remove.ID, verify)
				phase = "branch-absent"
			} else {
				err = s.CommitDiscard(ctx, remove.ID, verify)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = s.AdvanceOperation(ctx, remove.ID, "running", phase, true, remove.Evidence, ""); err != nil {
				t.Fatal(err)
			}
			complete := func(context.Context, string, string, string) error { return nil }
			if action == "delete" {
				err = s.CompleteDelete(ctx, remove.ID, complete)
			} else {
				err = s.CompleteDiscard(ctx, remove.ID, complete)
			}
			if err != nil {
				t.Fatal(err)
			}
			for restart := 0; restart < 2; restart++ {
				if restart == 1 {
					if err = s.Close(); err != nil {
						t.Fatal(err)
					}
					s, err = testScopedOpenStore(dir)
					if err != nil {
						t.Fatal(err)
					}
					defer s.Close()
				}
				got, missing, err := replay(req)
				if err != nil || got.ID != create.ID || got.Status != "completed" || got.SessionUUID != session.UUID || missing.UUID != "" || string(got.Evidence) != string(before.Evidence) {
					t.Fatalf("completed replay failed: %+v %+v %v", got, missing, err)
				}
				if _, err = s.GetSession(ctx, session.UUID); !errors.Is(err, ErrNotFound) {
					t.Fatalf("replay resurrected removed session: %v", err)
				}
				_, sessions, operations, err := s.Count(ctx)
				if err != nil || sessions != 0 || operations != 2 {
					t.Fatalf("replay changed records: sessions%d operations%d %v", sessions, operations, err)
				}
				changed := req
				changed.Branch = "different"
				if _, _, err = replay(changed); !errors.Is(err, ErrConflict) {
					t.Fatalf("changed completed request accepted: %v", err)
				}
				changed = req
				changed.Key = removeReq.Key
				if _, _, err = replay(changed); !errors.Is(err, ErrConflict) {
					t.Fatalf("other operation kind replayed: %v", err)
				}
			}
			// Reusing the old branch creates a distinct identity; replay must
			// still report the old completed request rather than adopt it.
			newReq := req
			newReq.Key = "new-identity"
			if action == "delete" {
				newReq.Choice, newReq.Source = "new", "refs/heads/seed"
			}
			_, replacement, err := s.BeginSessionCreate(ctx, newReq, strings.Repeat("d", 64), testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
				return strings.Repeat("a", 40), action == "discard", nil
			})
			if err != nil || replacement.UUID == session.UUID {
				t.Fatalf("new branch identity unavailable: %+v %v", replacement, err)
			}
			got, missing, err := replay(req)
			if err != nil || got.ID != create.ID || got.SessionUUID != session.UUID || missing.UUID != "" {
				t.Fatalf("old replay adopted replacement session: %+v %+v %v", got, missing, err)
			}
			if _, err = s.GetSession(ctx, replacement.UUID); err != nil {
				t.Fatalf("old replay changed replacement session: %v", err)
			}
		})
	}
}
func TestUnfinishedCreationReplayRequiresSessionIdentity(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	req := ReserveSessionRequest{Key: "unfinished", Project: "app", Branch: "work", Choice: "existing"}
	op, session, err := s.BeginSessionCreate(ctx, req, strings.Repeat("d", 64), testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
		return strings.Repeat("a", 40), true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate inconsistent/externally removed unfinished identity. Replay must
	// expose the missing authority and must never reserve another session.
	if _, err = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE uuid=?`, session.UUID); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.BeginSessionCreate(ctx, req, "", CreationSelection{}, func(context.Context, ReserveSessionRequest) (string, bool, error) {
		t.Fatal("missing identity recaptured source")
		return "", false, nil
	})
	if !errors.Is(err, ErrNotFound) || got.ID != op.ID {
		t.Fatalf("unfinished missing identity hidden: %+v %v", got, err)
	}
}
