package conformance

import (
	"errors"
	"testing"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

var deletionCases = []testCase{
	{"delete refuses another incarnation without calling the gate", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		other := f.Session(SessionOptions{})
		at := created(t, f, s).Incarnation
		wrong := created(t, f, other).Incarnation
		if err := f.Driver().Delete(t.Context(), s.Ref, wrong, unexpectedGate(t)); err == nil {
			t.Fatal("Delete at another runtime's incarnation succeeded")
		}
		if o := inspect(t, f, s); o.State != runtimedriver.StateStopped || o.Incarnation != at {
			t.Fatalf("the refused Delete changed the runtime: %+v", o)
		}
	}},
	{"delete calls the gate once, before stopping a running runtime", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		at := running(t, f, s).Incarnation
		g := &gate{before: func() {
			if o := inspect(t, f, s); o.State != runtimedriver.StateRunning {
				t.Errorf("at the gate the runtime was already %s", o.State)
			}
		}}
		if err := f.Driver().Delete(t.Context(), s.Ref, at, g.call); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if g.count() != 1 {
			t.Fatalf("Delete called the gate %d times, want 1", g.count())
		}
		if f.Deletion().DeletedWhileRunning(s.Ref) {
			t.Fatal("Delete removed the runtime while it was running instead of stopping it first")
		}
	}},
	{"a failing gate leaves the runtime as it was", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		at := running(t, f, s).Incarnation
		g := &gate{err: errGate}
		if err := f.Driver().Delete(t.Context(), s.Ref, at, g.call); !errors.Is(err, errGate) {
			t.Fatalf("Delete with a failing gate = %v, want the gate's error", err)
		}
		if o := inspect(t, f, s); o.State != runtimedriver.StateRunning || o.Incarnation != at || !o.Host.Ready {
			t.Fatalf("after a failing gate the runtime is %+v, want it running as before", o)
		}
		if n := f.Sessions().Transitions(s.Ref); n != 1 {
			t.Fatalf("after a failing gate the backend made %d transitions, want only the start", n)
		}
	}},
	{"delete of a session without a runtime succeeds without calling the gate", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		if err := f.Driver().Delete(t.Context(), s.Ref, "", unexpectedGate(t)); err != nil {
			t.Fatalf("Delete of an absent runtime: %v", err)
		}
	}},
	{"after delete the reference is absent from inspect and inventory", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{Grant: true})
		at := created(t, f, s).Incarnation
		if err := f.Driver().Delete(t.Context(), s.Ref, at, func() error { return nil }); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if o := inspect(t, f, s); o.State != runtimedriver.StateAbsent {
			t.Fatalf("Inspect after Delete = %+v, want absent", o)
		}
		inv, err := f.Driver().Inventory(t.Context())
		if err != nil {
			t.Fatalf("Inventory: %v", err)
		}
		for _, o := range inv.Objects {
			if o.Ref == s.Ref {
				t.Fatalf("Inventory after Delete still holds %s carrying the reference", o.Name)
			}
		}
	}},
	{"delete removes the session's private storage", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		at := created(t, f, s).Incarnation
		file := runtimedriver.File{Type: runtimedriver.FileRegular, UID: 1000, GID: 1000, Mode: 0o644, Data: []byte("kept until delete\n")}
		if err := f.Driver().WriteWorkspace(t.Context(), target(s, at), "kept.txt", file, func() error { return nil }); err != nil {
			t.Fatalf("WriteWorkspace: %v", err)
		}
		if err := f.Driver().Delete(t.Context(), s.Ref, at, func() error { return nil }); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		again := created(t, f, s).Incarnation
		if again == at {
			t.Fatalf("the re-created runtime kept the incarnation %s", at)
		}
		got, err := f.Driver().ReadWorkspace(t.Context(), target(s, again), "kept.txt", 1<<10)
		if err != nil || got.Type != runtimedriver.FileAbsent {
			t.Fatalf("the re-created runtime reads %+v, %v; want the file absent", got, err)
		}
	}},
	{"a delayed delete stays unknown until it lands, then settles applied", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		at := created(t, f, s).Incarnation
		land := f.Deletion().DelayDelete()
		g := &gate{}
		if err := f.Driver().Delete(t.Context(), s.Ref, at, g.call); !errors.Is(err, runtimedriver.ErrUnknownOutcome) || g.count() != 1 {
			t.Fatalf("delayed Delete = %v after %d gate calls, want ErrUnknownOutcome after 1", err, g.count())
		}
		effect := runtimedriver.Effect{Kind: runtimedriver.EffectDelete, Target: target(s, at)}
		wantSettled(t, f, effect, runtimedriver.Unknown)
		land()
		if o := inspect(t, f, s); o.State != runtimedriver.StateAbsent {
			t.Fatalf("after the delete landed Inspect = %+v, want absent", o)
		}
		wantSettled(t, f, effect, runtimedriver.Applied)
	}},
	{"a completed delete settles applied", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		at := running(t, f, s).Incarnation
		if err := f.Driver().Delete(t.Context(), s.Ref, at, func() error { return nil }); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		wantSettled(t, f, runtimedriver.Effect{Kind: runtimedriver.EffectDelete, Target: target(s, at)}, runtimedriver.Applied)
	}},
}
