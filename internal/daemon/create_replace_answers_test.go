package daemon

import (
	"context"
	"errors"
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

// wantRefusal fails unless err is a refusal of kind with reason and target.
func TestFailedCreationReviewsNameTheRunningCreationThatHoldsTheSession(t *testing.T) {
	l, op := failedCreationFixture(t)
	ctx := context.Background()
	// The creation's worker holds the session for its operation.
	err := l.holdSession(ctx, op.ID, op.SessionUUID)
	if err != nil {
		t.Fatal(err)
	}
	defer l.releaseHolds(op.ID)
	_, err = l.PreviewCreateCleanup(ctx, op.SessionUUID)
	wantRefusal(t, "cleanup review", err, api.KindRetry, api.ReasonSessionHeld, api.TargetOperation(op.ID))
	_, err = l.PreviewCreateReplace(ctx, op.SessionUUID, control.ReserveSessionRequest{Key: "replace", Project: "app", Branch: "work", Choice: "existing"})
	wantRefusal(t, "replacement review", err, api.KindRetry, api.ReasonSessionHeld, api.TargetOperation(op.ID))
	token := strings.Repeat("1", 32)
	_, err = l.ConfirmCreateCleanup(ctx, op.SessionUUID, "cleanup", token)
	wantRefusal(t, "cleanup confirmation", err, api.KindRetry, api.ReasonSessionHeld, api.TargetOperation(op.ID))
	_, err = l.ConfirmCreateReplace(ctx, op.SessionUUID, "replace", token)
	wantRefusal(t, "replacement confirmation", err, api.KindRetry, api.ReasonSessionHeld, api.TargetOperation(op.ID))
}

func TestFailedCreationConfirmationsRefuseStaleTokensAndReusedKeys(t *testing.T) {
	l, op := failedCreationFixture(t)
	ctx := context.Background()
	token := strings.Repeat("1", 32)
	_, err := l.ConfirmCreateCleanup(ctx, op.SessionUUID, "cleanup", token)
	wantRefusal(t, "cleanup with a token nobody issued", err, api.KindPreconditionFailed, api.ReasonExpired, "")
	_, err = l.ConfirmCreateReplace(ctx, op.SessionUUID, "replace", token)
	wantRefusal(t, "replacement with a token nobody issued", err, api.KindPreconditionFailed, api.ReasonExpired, "")

	l.createCleanupPreviews = map[string]createCleanupPreviewState{
		token: {Preview: api.CreateCleanupPreview{UUID: replacementOtherUUID}, Expiry: time.Now().Add(time.Minute)}}
	_, err = l.ConfirmCreateCleanup(ctx, op.SessionUUID, "cleanup", token)
	wantRefusal(t, "cleanup with another session's token", err, api.KindPreconditionFailed, api.ReasonChanged, "")
	l.createCleanupPreviews[token] = createCleanupPreviewState{Preview: api.CreateCleanupPreview{UUID: op.SessionUUID}, Expiry: time.Now().Add(-time.Second)}
	_, err = l.ConfirmCreateCleanup(ctx, op.SessionUUID, "cleanup", token)
	wantRefusal(t, "cleanup with an expired token", err, api.KindPreconditionFailed, api.ReasonExpired, "")

	l.createReplacePreviews = map[string]createReplacePreviewState{
		token: {Preview: api.CreateReplacePreview{OldUUID: replacementOtherUUID, NewRequest: control.ReserveSessionRequest{Key: "replace"}}, Expiry: time.Now().Add(time.Minute)}}
	_, err = l.ConfirmCreateReplace(ctx, op.SessionUUID, "replace", token)
	wantRefusal(t, "replacement with another session's token", err, api.KindPreconditionFailed, api.ReasonChanged, "")
	l.createReplacePreviews[token] = createReplacePreviewState{Preview: api.CreateReplacePreview{OldUUID: op.SessionUUID, NewRequest: control.ReserveSessionRequest{Key: "replace"}}, Expiry: time.Now().Add(-time.Second)}
	_, err = l.ConfirmCreateReplace(ctx, op.SessionUUID, "replace", token)
	wantRefusal(t, "replacement with an expired token", err, api.KindPreconditionFailed, api.ReasonExpired, "")

	// The creation's own key is recorded for another request.
	_, err = l.ConfirmCreateCleanup(ctx, op.SessionUUID, op.Key, token)
	wantRefusal(t, "cleanup reusing the creation's key", err, api.KindInvalid, api.ReasonKeyReused, "")
	_, err = l.ConfirmCreateReplace(ctx, op.SessionUUID, op.Key, token)
	wantRefusal(t, "replacement reusing the creation's key", err, api.KindInvalid, api.ReasonKeyReused, "")
}

func TestFailedCreationReviewsAreBounded(t *testing.T) {
	reviews := map[string]time.Time{}
	expiry := func(t time.Time) time.Time { return t }
	for i := 0; i < 128; i++ {
		if err := storeReview(reviews, strings.Repeat("0", 31)+string(rune('a'+i%26))+strings.Repeat("x", i/26), time.Now().Add(time.Minute), expiry); err != nil {
			t.Fatalf("review %d: %v", i, err)
		}
	}
	err := storeReview(reviews, "one-more", time.Now().Add(time.Minute), expiry)
	wantRefusal(t, "a review beyond the limit", err, api.KindRetry, api.ReasonTooManyPreviews, "")
	reviews["expired"] = time.Now().Add(-time.Second)
	delete(reviews, strings.Repeat("0", 31)+"a")
	if err = storeReview(reviews, "after-expiry", time.Now().Add(time.Minute), expiry); err != nil {
		t.Fatalf("an expired review kept its slot: %v", err)
	}
	err = boundedReview(replacementOldUUID, strings.Repeat("x", api.MaxFrameBytes))
	wantRefusal(t, "an oversized review", err, api.KindConflict, api.ReasonBoundExceeded, api.TargetSession(replacementOldUUID))
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

func TestReplacementConfirmationAnswersChangedFactsAndKeepsFailures(t *testing.T) {
	wantRefusal(t, "a changed review", confirmChanged(control.ErrConflict), api.KindPreconditionFailed, api.ReasonChanged, "")
	wantRefusal(t, "an ineligible review", confirmChanged(nil), api.KindPreconditionFailed, api.ReasonChanged, "")
	wantRefusal(t, "a vanished session", confirmChanged(control.NotFound(api.ReasonSession, api.TargetSession(replacementOldUUID))), api.KindPreconditionFailed, api.ReasonChanged, "")
	if err := confirmChanged(control.ErrConflict); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("changed review lost its conflict cause: %v", err)
	}
	failed := control.Failed(api.ReasonGit, errors.New("git unavailable"))
	wantRefusal(t, "a Git failure", confirmChanged(failed), api.KindFailed, api.ReasonGit, "")
	if err := confirmChanged(context.Canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation became %v", err)
	}
}
