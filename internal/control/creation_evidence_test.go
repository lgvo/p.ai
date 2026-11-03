package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
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
	if err = s.AdvanceOperation(ctx, op.ID, "blocked", "principals-ready", true, op.Evidence, "internal", ""); err != nil {
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
	if err = s.AdvanceOperation(ctx, op.ID, "running", "principals-ready", true, attempted, "", ""); err != nil {
		t.Fatal(err)
	}
	if err = s.AdvanceOperation(ctx, stale.ID, "running", stale.Phase, stale.Committed, stale.Evidence, "", ""); !errors.Is(err, ErrConflict) {
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
	if err = s.AdvanceOperation(ctx, op.ID, "blocked", durable.Phase, true, stale.Evidence, "internal", "stale worker failure"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale failure erased marker")
	}
}

func TestCreationCleanupCompletionAndIdentityCannotBeRewound(t *testing.T) {
	old := api.Operation{ID: "11111111-1111-4111-8111-111111111111", SessionUUID: "550e8400-e29b-41d4-a716-446655440000"}
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

// A creation that recorded its new branch's creation as sent keeps that
// record: a stale writer cannot erase it, also after a restart, so recovery
// settles the creation before it is sent again.
func TestBranchCreationSentCannotBeErasedAcrossRestart(t *testing.T) {
	s, dir := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{"network":"none"}`)); err != nil {
		t.Fatal(err)
	}
	req := ReserveSessionRequest{Key: "sent", Project: "app", Branch: "work", Choice: "new", Source: "refs/heads/main"}
	stale, _, err := s.BeginSessionCreate(ctx, req, strings.Repeat("a", 64), testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
		return strings.Repeat("b", 40), false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := Evidence(stale)
	if err != nil {
		t.Fatal(err)
	}
	ev.BranchCreateIssued = true
	sent, _ := json.Marshal(ev)
	if err = s.AdvanceOperation(ctx, stale.ID, "running", stale.Phase, stale.Committed, sent, "", ""); err != nil {
		t.Fatal(err)
	}
	if err = s.AdvanceOperation(ctx, stale.ID, "blocked", stale.Phase, stale.Committed, stale.Evidence, "internal", "stale worker failure"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a stale writer erased the sent creation: %v", err)
	}
	s.Close()
	s, err = testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	durable, err := s.GetOperation(ctx, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ev, err = Evidence(durable); err != nil || !ev.BranchCreateIssued {
		t.Fatalf("the restart lost the sent creation: %+v %v", ev, err)
	}
	if err = s.AdvanceOperation(ctx, stale.ID, "running", stale.Phase, stale.Committed, stale.Evidence, "", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("a stale writer erased the sent creation after the restart: %v", err)
	}
}

func TestBuilderCyclesMoveForwardOnly(t *testing.T) {
	state := func(cycle uint64, s string) *CreationBuilderState {
		return &CreationBuilderState{Cycle: cycle, State: s}
	}
	cases := []struct {
		name      string
		old, next *CreationBuilderState
		want      error
	}{
		{"pinned", nil, state(0, "not-attempted"), nil},
		{"unpinned attempt", nil, state(1, "attempted"), ErrConflict},
		{"first attempt", state(0, "not-attempted"), state(1, "attempted"), nil},
		{"skipped cycle", state(0, "not-attempted"), state(2, "attempted"), ErrConflict},
		{"same attempt", state(1, "attempted"), state(1, "attempted"), nil},
		{"settled absent", state(1, "attempted"), state(1, "absent"), nil},
		{"stale writer erases attempt", state(1, "attempted"), state(0, "not-attempted"), ErrConflict},
		{"stale writer drops builder", state(1, "attempted"), nil, ErrConflict},
		{"absent rewound", state(1, "absent"), state(1, "attempted"), ErrConflict},
		{"next cycle after absent", state(1, "absent"), state(2, "attempted"), nil},
		{"next cycle while attempted", state(1, "attempted"), state(2, "attempted"), ErrConflict},
		{"next cycle settled at once", state(1, "absent"), state(2, "absent"), ErrConflict},
		{"older cycle", state(2, "absent"), state(1, "absent"), ErrConflict},
		{"owned is gone", state(1, "attempted"), state(1, "owned"), ErrInvalid},
		{"settled as not accepted", state(1, "attempted"), state(1, "abandoned"), nil},
		{"same abandonment", state(1, "abandoned"), state(1, "abandoned"), nil},
		{"abandoned leftovers deleted", state(1, "abandoned"), state(1, "absent"), nil},
		{"abandoned rewound to attempted", state(1, "abandoned"), state(1, "attempted"), ErrConflict},
		{"absent rewound to abandoned", state(1, "absent"), state(1, "abandoned"), ErrConflict},
		{"stale writer drops abandonment", state(1, "abandoned"), nil, ErrConflict},
		{"next cycle while abandoned", state(1, "abandoned"), state(2, "attempted"), ErrConflict},
		{"next cycle starts abandoned", state(1, "absent"), state(2, "abandoned"), ErrConflict},
		{"abandoned before any attempt", state(0, "not-attempted"), state(0, "abandoned"), ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old, _ := json.Marshal(CreationEvidence{EnvironmentBuilder: tc.old})
			next, _ := json.Marshal(CreationEvidence{EnvironmentBuilder: tc.next})
			if err := monotonicCreationEvidence("image-ready", old, next); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestBuildRefIsAUUIDDerivedFromTheOperationAndCycle(t *testing.T) {
	op := "11111111-1111-4111-8111-111111111111"
	// SHA-256("11111111-1111-4111-8111-111111111111/2") starts e1f77d69b676b4862ce8d91b7e6e4d41;
	// the version nibble becomes 4 and the variant bits 10.
	if got := BuildRef(op, 2); got != "e1f77d69-b676-4486-ace8-d91b7e6e4d41" {
		t.Fatalf("BuildRef = %s", got)
	}
	seen := map[string]bool{}
	for _, id := range []string{op, "22222222-2222-4222-8222-222222222222"} {
		for cycle := uint64(1); cycle <= 3; cycle++ {
			ref := BuildRef(id, cycle)
			if !validUUID(ref) || ref[14] != '4' || !strings.ContainsRune("89ab", rune(ref[19])) || seen[ref] || BuildRef(id, cycle) != ref {
				t.Fatalf("build ref %s of %s cycle %d", ref, id, cycle)
			}
			seen[ref] = true
		}
	}
}

func TestCreationEvidenceImageMatchesTheRuntimeImage(t *testing.T) {
	base, built := strings.Repeat("a", 64), strings.Repeat("b", 64)
	valid := func() CreationEvidence {
		return CreationEvidence{
			CapturedOID: strings.Repeat("c", 40), ImageFingerprint: base, PolicySHA256: strings.Repeat("d", 64), Selection: testSelection(),
			Environment:        &EnvironmentIntent{System: "x86_64-linux", BaseFingerprint: base, BuilderStoragePool: "default", BuilderPolicySHA256: strings.Repeat("e", 64), ImageBuilder: "incus-commit-image/1"},
			EnvironmentBuilder: &CreationBuilderState{State: "not-attempted"},
		}
	}
	withImage := func(selection, material string) func(*CreationEvidence) {
		return func(ev *CreationEvidence) {
			ev.ImageFingerprint = built
			ev.Image = &CreationImage{Ref: built, Built: true, Selection: selection, MaterialSHA256: material}
		}
	}
	cases := []struct {
		name   string
		change func(*CreationEvidence)
		valid  bool
	}{
		{"base until image-ready", func(*CreationEvidence) {}, true},
		{"no builders", func(ev *CreationEvidence) { ev.Environment, ev.EnvironmentBuilder = nil, nil }, true},
		{"devshell image", withImage("devshell", strings.Repeat("f", 64)), true},
		{"no-flake image", withImage("no-flake", ""), true},
		{"no-default image", withImage("no-default", ""), true},
		{"runtime image not the base", func(ev *CreationEvidence) { ev.ImageFingerprint = built }, false},
		{"runtime image not the commit image", func(ev *CreationEvidence) { withImage("no-flake", "")(ev); ev.ImageFingerprint = base }, false},
		{"malformed image ref", func(ev *CreationEvidence) {
			withImage("no-flake", "")(ev)
			ev.Image.Ref = "image"
			ev.ImageFingerprint = "image"
		}, false},
		{"unknown selection", withImage("default", ""), false},
		{"devshell without material", withImage("devshell", ""), false},
		{"base packages with material", withImage("no-default", strings.Repeat("f", 64)), false},
		{"image without builders", func(ev *CreationEvidence) {
			withImage("no-flake", "")(ev)
			ev.Environment, ev.EnvironmentBuilder = nil, nil
		}, false},
		{"no image builder", func(ev *CreationEvidence) { ev.Environment.ImageBuilder = "" }, false},
		{"long image builder", func(ev *CreationEvidence) { ev.Environment.ImageBuilder = strings.Repeat("v", 129) }, false},
		{"unprintable image builder", func(ev *CreationEvidence) { ev.Environment.ImageBuilder = "v\x00" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := valid()
			tc.change(&ev)
			raw, _ := json.Marshal(ev)
			if _, err := Evidence(api.Operation{Kind: "session.create", Evidence: raw}); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
}

func TestInitProvenanceIsRecordedOnlyBeforePrincipals(t *testing.T) {
	untracked, _ := json.Marshal(CreationEvidence{})
	tracked, _ := json.Marshal(CreationEvidence{RuntimeInitState: "not-attempted"})
	for phase, want := range map[string]error{"source-ready": nil, "branch-assigned": nil, "image-ready": nil, "principals-ready": ErrConflict, "runtime-created": ErrConflict} {
		if err := monotonicCreationEvidence(phase, untracked, tracked); !errors.Is(err, want) {
			t.Fatalf("%s: got %v, want %v", phase, err, want)
		}
	}
}
