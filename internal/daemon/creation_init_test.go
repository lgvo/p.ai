package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
)

func TestCreationInitGateDurableBeforeDispatchAndNoBlindRetry(t *testing.T) {
	raw, _ := json.Marshal(control.CreationEvidence{ImageFingerprint: strings.Repeat("a", 64), RuntimeInitState: "not-attempted"})
	op := control.Operation{Kind: "session.create", Phase: "principals-ready", Committed: true, Evidence: raw}
	refused := errors.New("fixture SQLite persistence failed")
	if err := recordCreationInitAttempt(context.Background(), &op, func(context.Context, json.RawMessage) error { return refused }); !errors.Is(err, refused) {
		t.Fatal(err)
	}
	ev, _ := control.Evidence(op)
	if ev.RuntimeInitState != "not-attempted" {
		t.Fatal("failed marker claimed dispatch")
	}
	var durable json.RawMessage
	if err := recordCreationInitAttempt(context.Background(), &op, func(_ context.Context, raw json.RawMessage) error {
		durable = append(json.RawMessage(nil), raw...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Crash immediately after durable marker, before/during unknown native init.
	op.Evidence = durable
	ev, _ = control.Evidence(op)
	if ev.RuntimeInitState != "attempted" {
		t.Fatal("durable init marker missing")
	}
	if control.ReplaceableCreationPhase(op, ev) {
		t.Fatal("ambiguous native attempt became replacement-eligible")
	}
	if err := recordCreationInitAttempt(context.Background(), &op, func(context.Context, json.RawMessage) error {
		t.Fatal("blind retry wrote another init intent")
		return nil
	}); err == nil {
		t.Fatal("blind second init admitted")
	}
}

func TestCreationInitLegacyOnlyEarlyCheckpointCanProveNoAttempt(t *testing.T) {
	for _, phase := range []string{"source-ready", "branch-assigned", "environment-ready", "principals-ready", "runtime-created", "future-phase"} {
		raw, _ := json.Marshal(control.CreationEvidence{ImageFingerprint: strings.Repeat("a", 64)})
		op := control.Operation{Kind: "session.create", Phase: phase, Evidence: raw, Committed: phase != "source-ready"}
		if err := prepareCreationInitState(&op); err != nil {
			t.Fatal(err)
		}
		ev, _ := control.Evidence(op)
		early := phase == "source-ready" || phase == "branch-assigned" || phase == "environment-ready"
		if (ev.RuntimeInitState == "not-attempted") != early {
			t.Fatalf("legacy phase %s fabricated dispatch provenance", phase)
		}
		if !early && recordCreationInitAttempt(context.Background(), &op, func(context.Context, json.RawMessage) error {
			t.Fatal("unknown provenance reached durable marker")
			return nil
		}) == nil {
			t.Fatal("legacy unknown init admitted")
		}
	}
}
