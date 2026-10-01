package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/lgvo/p.ai/internal/plugin"
)

func TestRuntimeCreateAmbiguityReusesOwnedStoppedInstance(t *testing.T) {
	ctx := context.Background()
	exists, creates := false, 0
	run := func(_ context.Context, kind string) (plugin.RuntimeState, error) {
		switch kind {
		case "runtime.inspect":
			return plugin.RuntimeState{Exists: exists, Status: status(exists)}, nil
		case "runtime.create":
			creates++
			exists = true
			if creates == 1 {
				return plugin.RuntimeState{}, errors.New("injected lost create result")
			}
			return plugin.RuntimeState{Exists: true, Status: "Stopped"}, nil
		}
		return plugin.RuntimeState{}, errors.New("unexpected call")
	}
	if _, err := ensureStoppedRuntime(ctx, run); err == nil {
		t.Fatal("ambiguous create reported success")
	}
	state, err := ensureStoppedRuntime(ctx, run)
	if err != nil || !state.Exists || state.Status != "Stopped" || creates != 2 {
		t.Fatalf("retry duplicated runtime: state=%+v creates=%d err=%v", state, creates, err)
	}
}
func status(exists bool) string {
	if exists {
		return "Stopped"
	}
	return ""
}

func TestAssemblyFailurePreservesRuntimeForExactRetry(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	run := func(_ context.Context, kind string) (plugin.RuntimeState, error) {
		if kind != "runtime.assemble" {
			t.Fatalf("unexpected native mutation %s", kind)
		}
		attempts++
		if attempts == 1 {
			return plugin.RuntimeState{Exists: true, Status: "Stopped"}, errors.New("injected assembly failure")
		}
		return plugin.RuntimeState{Exists: true, Status: "Stopped"}, nil
	}
	if err := assembleStoppedRuntime(ctx, run); err == nil {
		t.Fatal("assembly failure ignored")
	}
	if err := assembleStoppedRuntime(ctx, run); err != nil || attempts != 2 {
		t.Fatalf("safe assembly retry failed: %d %v", attempts, err)
	}
}
