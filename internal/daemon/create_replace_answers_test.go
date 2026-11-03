package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
)

// failedCreationFixture is a lifecycle over a real store holding one session
// creation that still runs.
func failedCreationFixture(t *testing.T) (*lifecycle, api.Operation) {
	t.Helper()
	store, err := control.OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	policy, _, err := control.ProjectPolicySnapshot(control.ProjectPolicy{Network: "none", FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/sh"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateProject(ctx, "app", policy); err != nil {
		t.Fatal(err)
	}
	selection := control.CreationSelection{RuntimeID: "runtime", RuntimeSHA256: strings.Repeat("a", 64), HostID: "host", HostSHA256: strings.Repeat("b", 64), SourceID: "source", SourceSHA256: strings.Repeat("c", 64)}
	op, _, err := store.BeginSessionCreate(ctx, control.ReserveSessionRequest{Key: "create-work", Project: "app", Branch: "work", Choice: "existing"}, strings.Repeat("d", 64), selection,
		func(context.Context, control.ReserveSessionRequest) (string, bool, error) {
			return strings.Repeat("e", 40), true, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return &lifecycle{ctx: ctx, store: store}, op
}

// replacementChoices is a replacement's new creation in failedCreationFixture.
var replacementChoices = api.CreationChoices{Project: "app", Branch: "work", Choice: "existing"}

// futureTag is a well-formed change tag that expires in d.
func futureTag(d time.Duration) string {
	return time.Now().Add(d).UTC().Format(api.ChangeTagTimeLayout) + "-" + strings.Repeat("1", 64)
}

func TestFailedCreationChangesNameTheRunningCreationThatHoldsTheSession(t *testing.T) {
	l, op := failedCreationFixture(t)
	ctx := context.Background()
	// The creation's worker holds the session for its operation.
	err := l.holdSession(ctx, op.ID, op.SessionUUID)
	if err != nil {
		t.Fatal(err)
	}
	defer l.releaseHolds(op.ID)
	_, err = l.CleanupCreation(ctx, op.SessionUUID, api.ChangeRequest{})
	wantRefusal(t, "cleanup's dry run", err, api.KindRetry, api.ReasonSessionHeld, api.TargetOperation(op.ID))
	_, err = l.ReplaceCreation(ctx, op.SessionUUID, replacementChoices, api.ChangeRequest{})
	wantRefusal(t, "replacement's dry run", err, api.KindRetry, api.ReasonSessionHeld, api.TargetOperation(op.ID))
	run := api.ChangeRequest{Key: "change", IfMatch: futureTag(time.Minute)}
	_, err = l.CleanupCreation(ctx, op.SessionUUID, run)
	wantRefusal(t, "cleanup's run", err, api.KindRetry, api.ReasonSessionHeld, api.TargetOperation(op.ID))
	_, err = l.ReplaceCreation(ctx, op.SessionUUID, replacementChoices, run)
	wantRefusal(t, "replacement's run", err, api.KindRetry, api.ReasonSessionHeld, api.TargetOperation(op.ID))
}

func TestFailedCreationRunsRefuseExpiredTagsAndReusedKeys(t *testing.T) {
	l, op := failedCreationFixture(t)
	ctx := context.Background()
	expired := api.ChangeRequest{Key: "change", IfMatch: futureTag(-time.Second)}
	_, err := l.CleanupCreation(ctx, op.SessionUUID, expired)
	wantRefusal(t, "cleanup with an expired tag", err, api.KindPreconditionFailed, api.ReasonExpired, "")
	_, err = l.ReplaceCreation(ctx, op.SessionUUID, replacementChoices, expired)
	wantRefusal(t, "replacement with an expired tag", err, api.KindPreconditionFailed, api.ReasonExpired, "")
	// The creation's own key is recorded for another request.
	reused := api.ChangeRequest{Key: op.Key, IfMatch: futureTag(time.Minute)}
	_, err = l.CleanupCreation(ctx, op.SessionUUID, reused)
	wantRefusal(t, "cleanup reusing the creation's key", err, api.KindInvalid, api.ReasonKeyReused, "")
	_, err = l.ReplaceCreation(ctx, op.SessionUUID, replacementChoices, reused)
	wantRefusal(t, "replacement reusing the creation's key", err, api.KindInvalid, api.ReasonKeyReused, "")
}

func TestStaleFailedCreationCleanupIsNotRetried(t *testing.T) {
	stale := api.Operation{ID: replacementOtherUUID, Kind: control.KindCreateCleanup, Status: "failed", Phase: "stale"}
	err := cleanupFoundStale(nil, context.Background(), stale)
	wantRefusal(t, "Retry of a stale cleanup", err, api.KindConflict, api.ReasonOperationNotRetryable, api.TargetOperation(stale.ID))
	stale.Status = "blocked"
	if err = cleanupFoundStale(nil, context.Background(), stale); err != nil {
		t.Fatalf("Retry of a blocked cleanup refused: %v", err)
	}
}
