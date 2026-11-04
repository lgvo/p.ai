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

func TestCreateReplaceAcceptedReplayWithoutMutationAuthority(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{"network":"none"}`)); err != nil {
		t.Fatal(err)
	}
	image := strings.Repeat("d", 64)
	old, session, err := s.BeginSessionCreate(ctx,
		ReserveSessionRequest{Key: "old", Project: "app", Branch: "work", Choice: "existing"},
		image, testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
			return strings.Repeat("a", 40), true, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AdvanceOperation(ctx, old.ID, "blocked", "principals-ready", true, old.Evidence, "internal", "fixture read-only preflight refusal"); err != nil {
		t.Fatal(err)
	}
	cleanup := replacementCleanupIntent(t, old, image)
	if err = s.RegisterGitPrincipal(ctx, cleanup.KeyFingerprint, "session", "app", old.SessionUUID); err != nil {
		t.Fatal(err)
	}
	tag := testTag(time.Minute)
	newRequest := ReserveSessionRequest{Key: "new", Project: "app", Branch: "work", Choice: "existing"}
	intent := CreateReplaceIntent{OldOperationID: old.ID, OldUUID: old.SessionUUID,
		OldEvidenceSHA256: digest(old.Evidence), OldPolicySHA256: session.PolicySHA256,
		NewPolicySHA256: session.PolicySHA256, IfMatch: tag, ChangeHash: testHash, Cleanup: cleanup, New: newRequest}
	fresh, err := s.ReplaceBlockedCreate(ctx, intent, image, testSelection(), nil,
		func(context.Context, []CapacityReservation) (CapacityObservation, error) {
			return CapacityObservation{Limit: 4, Occupied: map[string]bool{}}, nil
		}, func(context.Context) (string, error) { return strings.Repeat("b", 40), nil })
	if err != nil || fresh.Phase != "replacement-cleanup" {
		t.Fatalf("cleanup admission: %+v %v", fresh, err)
	}
	checkIntent(t, s, fresh, api.OperationIntent{Project: "app", Session: fresh.SessionUUID, Branch: "work", Choice: "existing",
		ReplacesSession: old.SessionUUID, ReplacesBranch: "work"})
	if _, err = s.GetSession(ctx, old.SessionUUID); !errors.Is(err, ErrNotFound) {
		t.Fatal("old session row survived handoff")
	}
	// Read-only accepted replay must work without mutation authority and without
	// the retired row. The VM additionally holds the daemon's old UUID lock.
	s.gitAuthority.Lock()
	defer s.gitAuthority.Unlock()
	deadline, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	got, found, err := s.ReplayCreateReplacement(deadline, old.SessionUUID, tag, newRequest)
	if err != nil || !found || got.ID != fresh.ID || got.Phase != "replacement-cleanup" {
		t.Fatalf("accepted replay depended on mutation authority: %+v %v %v", got, found, err)
	}
	otherBranch := newRequest
	otherBranch.Branch = "other"
	oldKey := newRequest
	oldKey.Key = "old"
	for _, tc := range []struct {
		uuid, tag string
		request   ReserveSessionRequest
	}{
		{old.SessionUUID, testTag(2 * time.Minute), newRequest},
		{fresh.SessionUUID, tag, newRequest},
		{old.SessionUUID, tag, oldKey},
		{old.SessionUUID, tag, otherBranch},
	} {
		if _, found, err = s.ReplayCreateReplacement(deadline, tc.uuid, tc.tag, tc.request); !errors.Is(err, ErrConflict) || found {
			t.Fatalf("different run became accepted replay: %+v %v %v", tc, found, err)
		}
	}
	unaccepted := newRequest
	unaccepted.Key = "unaccepted"
	if _, found, err = s.ReplayCreateReplacement(deadline, old.SessionUUID, tag, unaccepted); err != nil || found {
		t.Fatal("unknown run became accepted replay")
	}
	if got, err = s.GetOperation(ctx, fresh.ID); err != nil || got.Status != "running" || got.Phase != "replacement-cleanup" {
		t.Fatal("read-only replay changed cleanup")
	}
}
