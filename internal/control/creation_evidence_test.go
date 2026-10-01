package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCreationInitAttemptCannotBeErasedByStaleRetryAcrossRestart(t *testing.T) {
	s, dir := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{"network":"none"}`)); err != nil {
		t.Fatal(err)
	}
	req := ReserveSessionRequest{Key: "tracked", Project: "app", Branch: "work", Choice: "existing"}
	op, _, err := s.BeginSessionCreate(ctx, req, strings.Repeat("a", 64), testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
		return strings.Repeat("b", 40), true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AdvanceOperation(ctx, op.ID, "blocked", "principals-ready", true, op.Evidence, ""); err != nil {
		t.Fatal(err)
	}
	// Deterministic interleaving: Retry reads blocked/not-attempted, exact replay
	// worker durably crosses native gate, Retry then writes its stale snapshot.
	stale, err := s.GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := Evidence(stale)
	if err != nil {
		t.Fatal(err)
	}
	ev.RuntimeInitState = "attempted"
	attempted, _ := json.Marshal(ev)
	if err = s.AdvanceOperation(ctx, op.ID, "running", "principals-ready", true, attempted, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.AdvanceOperation(ctx, stale.ID, "running", stale.Phase, stale.Committed, stale.Evidence, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Retry erased dispatch marker: %v", err)
	}
	s.Close()
	s, err = testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	durable, err := s.GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	ev, err = Evidence(durable)
	if err != nil || ev.RuntimeInitState != "attempted" || ReplaceableCreationPhase(durable, ev) {
		t.Fatal("restart lost uncertain init provenance")
	}
	if err = s.AdvanceOperation(ctx, op.ID, "blocked", durable.Phase, true, stale.Evidence, "stale worker failure"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale failure erased marker")
	}
}

func TestCreationCleanupCompletionAndIdentityCannotBeRewound(t *testing.T) {
	old := Operation{ID: "11111111-1111-4111-8111-111111111111", SessionUUID: "550e8400-e29b-41d4-a716-446655440000"}
	c := replacementCleanupIntent(t, old, strings.Repeat("a", 64))
	ev := CreationEvidence{RuntimeInitState: "not-attempted", ReplacementCleanup: c}
	pending, _ := json.Marshal(ev)
	c.Completed = true
	completed, _ := json.Marshal(ev)
	if err := monotonicCreationEvidence("replacement-cleanup", pending, completed); err != nil {
		t.Fatal(err)
	}
	if err := monotonicCreationEvidence("source-ready", completed, pending); !errors.Is(err, ErrConflict) {
		t.Fatal("stale Retry erased completed cleanup")
	}
	c.KeyFingerprint = strings.Repeat("f", 64)
	changed, _ := json.Marshal(ev)
	if err := monotonicCreationEvidence("source-ready", completed, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("approved cleanup identity changed")
	}
	// A historical principals-ready record can never gain false positive proof.
	legacy, _ := json.Marshal(CreationEvidence{})
	tracked, _ := json.Marshal(CreationEvidence{RuntimeInitState: "not-attempted"})
	if err := monotonicCreationEvidence("principals-ready", legacy, tracked); !errors.Is(err, ErrConflict) {
		t.Fatal("legacy ambiguity relabeled safe")
	}
}
