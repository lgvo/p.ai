package runtimeincus

import (
	"context"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// shortSettle makes Settle's two observations follow each other at once.
func shortSettle(t *testing.T) {
	interval := settleInterval
	settleInterval = time.Millisecond
	t.Cleanup(func() { settleInterval = interval })
}

func settled(t *testing.T, w *incusWorld, e runtimedriver.Effect) runtimedriver.Settlement {
	t.Helper()
	got, _ := w.driver().Settle(context.Background(), e)
	return got
}

func TestSettleOfACreateOrStopAtAnotherIncarnationIsUnknown(t *testing.T) {
	shortSettle(t)
	w := newIncusWorld(t)
	s, at := createdRuntime(t, w)
	other := runtimedriver.Target{Session: s, At: "11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222"}
	for _, kind := range []runtimedriver.EffectKind{runtimedriver.EffectCreate, runtimedriver.EffectStop} {
		if got := settled(t, w, runtimedriver.Effect{Kind: kind, Target: other}); got != runtimedriver.Unknown {
			t.Errorf("Settle(%s) at another incarnation = %v", kind, got)
		}
		if got := settled(t, w, runtimedriver.Effect{Kind: kind, Target: runtimedriver.Target{Session: s, At: at}}); got != runtimedriver.Applied {
			t.Errorf("Settle(%s) at the runtime's incarnation = %v", kind, got)
		}
	}
}

func TestSettleOfACreateNeedsTheSameIncarnationOnBothObservations(t *testing.T) {
	shortSettle(t)
	w := newIncusWorld(t)
	s, _ := createdRuntime(t, w)
	restored := false
	// Incus restores the runtime, a new generation, between the two
	// observations.
	w.after = func(args []string) error {
		if args[0] == "operation" && !restored {
			restored = true
			w.mu.Lock()
			w.instance(testRuntime).config["volatile.uuid.generation"] = w.newUUID()
			w.mu.Unlock()
		}
		return nil
	}
	if got := settled(t, w, runtimedriver.Effect{Kind: runtimedriver.EffectCreate, Target: runtimedriver.Target{Session: s}}); got != runtimedriver.Unknown {
		t.Fatalf("Settle(create) across a restore = %v", got)
	}
}

func TestSettleOfADeleteIsUnknownWhileAnythingCarriesOrActsOnTheReference(t *testing.T) {
	shortSettle(t)
	w := newIncusWorld(t)
	s, at := createdRuntime(t, w)
	effect := runtimedriver.Effect{Kind: runtimedriver.EffectDelete, Target: runtimedriver.Target{Session: s, At: at}}
	w.change(testRuntime, func(in *worldInstance) { in.name = "renamed-by-hand" })
	if got := settled(t, w, effect); got != runtimedriver.Unknown {
		t.Fatalf("Settle(delete) of a renamed runtime = %v", got)
	}
	w.mu.Lock()
	w.instances = nil
	w.operations = []worldOperation{{StatusCode: 103, Resources: map[string][]string{"instances": {"/1.0/instances/" + testRuntime + "?project=user-1000"}}}}
	w.mu.Unlock()
	if got := settled(t, w, effect); got != runtimedriver.Unknown {
		t.Fatalf("Settle(delete) with an operation on the runtime's name = %v", got)
	}
	w.mu.Lock()
	w.operations = nil
	w.mu.Unlock()
	if got := settled(t, w, effect); got != runtimedriver.Applied {
		t.Fatalf("Settle(delete) once nothing carries or acts on the reference = %v", got)
	}
}
