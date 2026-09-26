package control

import (
	"context"
	"encoding/json"
	"errors"
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
	if err = s.AdvanceOperation(ctx, old.ID, "blocked", "principals-ready", true, old.Evidence, "fixture read-only preflight refusal"); err != nil {
		t.Fatal(err)
	}
	cleanup := replacementCleanupIntent(t, old, image)
	if err = s.RegisterGitPrincipal(ctx, cleanup.KeyFingerprint, "session", "app", old.SessionUUID); err != nil {
		t.Fatal(err)
	}
	tokenSHA256 := strings.Repeat("f", 64)
	intent := CreateReplaceIntent{OldOperationID: old.ID, OldUUID: old.SessionUUID,
		OldEvidenceSHA256: digest(old.Evidence), OldPolicySHA256: session.PolicySHA256,
		NewPolicySHA256: session.PolicySHA256, TokenSHA256: tokenSHA256, Cleanup: cleanup,
		ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano),
		New:       ReserveSessionRequest{Key: "new", Project: "app", Branch: "work", Choice: "existing"}}
	fresh, err := s.ReplaceBlockedCreate(ctx, intent, image, testSelection(), nil,
		func(context.Context, []CapacityReservation) (CapacityObservation, error) {
			return CapacityObservation{Limit: 4, Occupied: map[string]bool{}}, nil
		}, func(context.Context) (string, error) { return strings.Repeat("b", 40), nil })
	if err != nil || fresh.Phase != "replacement-cleanup" {
		t.Fatalf("cleanup admission: %+v %v", fresh, err)
	}
	if _, err = s.GetSession(ctx, old.SessionUUID); !errors.Is(err, ErrNotFound) {
		t.Fatal("old session row survived handoff")
	}
	// Read-only accepted replay must work without mutation authority and without
	// the retired row. The VM additionally holds the daemon's old UUID lock.
	s.gitAuthority.Lock()
	defer s.gitAuthority.Unlock()
	deadline, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	got, found, err := s.ReplayCreateReplacement(deadline, "new", old.SessionUUID, tokenSHA256)
	if err != nil || !found || got.ID != fresh.ID || got.Phase != "replacement-cleanup" {
		t.Fatalf("accepted replay depended on mutation authority: %+v %v %v", got, found, err)
	}
	for _, tc := range []struct{ uuid, key, token string }{
		{old.SessionUUID, "new", strings.Repeat("e", 64)},
		{fresh.SessionUUID, "new", tokenSHA256},
		{old.SessionUUID, "old", tokenSHA256},
	} {
		if _, found, err = s.ReplayCreateReplacement(deadline, tc.key, tc.uuid, tc.token); !errors.Is(err, ErrConflict) || found {
			t.Fatalf("different confirmation became accepted replay: %+v %v %v", tc, found, err)
		}
	}
	if _, found, err = s.ReplayCreateReplacement(deadline, "unaccepted", old.SessionUUID, tokenSHA256); err != nil || found {
		t.Fatal("unknown confirmation became accepted replay")
	}
	if got, err = s.GetOperation(ctx, fresh.ID); err != nil || got.Status != "running" || got.Phase != "replacement-cleanup" {
		t.Fatal("read-only replay changed cleanup")
	}
}
