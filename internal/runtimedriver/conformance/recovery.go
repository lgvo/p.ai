package conformance

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

var recoveryCases = []testCase{
	{"a create that died after initializing settles applied and completes at its incarnation", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{Grant: true})
		diedInCreate(t, f, s, AfterInit)
		o := inspect(t, f, s)
		if o.State != runtimedriver.StateStopped || o.Complete || o.Incarnation == "" {
			t.Fatalf("Inspect after the driver died = %+v, want an incomplete stopped runtime with an incarnation", o)
		}
		wantSettled(t, f, createEffect(s), runtimedriver.Applied)
		completedAt(t, f, s, o.Incarnation)
	}},
	{"a create that died between two guest writes completes at its incarnation", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		diedInCreate(t, f, s, BetweenGuestWrites)
		wantSettled(t, f, createEffect(s), runtimedriver.Applied)
		completedAt(t, f, s, inspect(t, f, s).Incarnation)
	}},
	{"an uncertain guest write found identical is kept and the create completes", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		diedInCreate(t, f, s, DuringGuestWrite)
		wantSettled(t, f, createEffect(s), runtimedriver.Applied)
		completedAt(t, f, s, inspect(t, f, s).Incarnation)
	}},
	{"an uncertain guest write found different is refused, never initializing again", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		diedInCreate(t, f, s, DuringPartialGuestWrite)
		wantSettled(t, f, createEffect(s), runtimedriver.Applied)
		at := inspect(t, f, s).Incarnation
		if o, err := f.Driver().Create(t.Context(), s, at, nil); err == nil {
			t.Fatalf("Create at %s over a different guest file = %+v, want a refusal", at, o)
		}
		if n := f.Sessions().Initializations(s.Ref); n != 1 {
			t.Fatalf("%d runtimes initialized, want 1", n)
		}
		if o := inspect(t, f, s); o.Incarnation != at {
			t.Fatalf("the runtime changed incarnation from %s to %s", at, o.Incarnation)
		}
	}},
	{"create at an incarnation never initializes a runtime", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		other := f.Session(SessionOptions{})
		at := created(t, f, other).Incarnation
		if _, err := f.Driver().Create(t.Context(), s, at, nil); err == nil {
			t.Fatal("Create at an incarnation succeeded for a session without a runtime")
		}
		if o := inspect(t, f, s); o.State != runtimedriver.StateAbsent {
			t.Fatalf("Create at an incarnation left the runtime %s", o.State)
		}
		if n := f.Sessions().Initializations(s.Ref); n != 0 {
			t.Fatalf("Create at an incarnation initialized %d runtimes", n)
		}
	}},
	{"create at an incarnation refuses another incarnation", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		other := f.Session(SessionOptions{})
		created(t, f, s)
		at := created(t, f, other).Incarnation
		if _, err := f.Driver().Create(t.Context(), s, at, nil); err == nil {
			t.Fatal("Create at another runtime's incarnation succeeded")
		}
	}},
	{"create at an incarnation refuses a running runtime", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		at := running(t, f, s).Incarnation
		if _, err := f.Driver().Create(t.Context(), s, at, nil); err == nil {
			t.Fatal("Create at the incarnation of a running runtime succeeded")
		}
		if o := inspect(t, f, s); o.State != runtimedriver.StateRunning || o.Incarnation != at {
			t.Fatalf("the refused Create changed the runtime: %+v", o)
		}
	}},
	{"create at the incarnation of a complete runtime changes nothing", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		c := created(t, f, s)
		o, err := f.Driver().Create(t.Context(), s, c.Incarnation, nil)
		if err != nil || !o.Complete || o.State != runtimedriver.StateStopped || o.Incarnation != c.Incarnation {
			t.Fatalf("Create at %s = %+v, %v; want the same complete stopped runtime", c.Incarnation, o, err)
		}
		if n := f.Sessions().Initializations(s.Ref); n != 1 {
			t.Fatalf("%d runtimes initialized, want 1", n)
		}
	}},
	{"create at an incarnation refuses a guest file that differs from the spec, changing nothing", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		c := created(t, f, s)
		for _, change := range []struct {
			name string
			edit func(*runtimedriver.Session)
		}{
			{"command", func(s *runtimedriver.Session) { s.Command = append(slices.Clone(s.Command), "--changed") }},
			{"Git key", func(s *runtimedriver.Session) { s.GitKey = append(slices.Clone(s.GitKey), "changed\n"...) }},
		} {
			changed := s
			change.edit(&changed)
			if o, err := f.Driver().Create(t.Context(), changed, c.Incarnation, nil); err == nil {
				t.Fatalf("Create at %s with another %s = %+v, want a refusal", c.Incarnation, change.name, o)
			}
		}
		if o := inspect(t, f, s); o != c {
			t.Fatalf("after the refused Creates Inspect = %+v, want %+v", o, c)
		}
		completedAt(t, f, s, c.Incarnation)
	}},
	{"a delayed create stays unknown until it lands, then settles applied", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		land := f.Recovery().DelayCreate()
		g := &gate{}
		if _, err := f.Driver().Create(t.Context(), s, "", g.call); !errors.Is(err, runtimedriver.ErrUnknownOutcome) || g.count() != 1 {
			t.Fatalf("delayed Create = %v after %d gate calls, want ErrUnknownOutcome after 1", err, g.count())
		}
		if o := inspect(t, f, s); o.State != runtimedriver.StateAbsent {
			t.Fatalf("before the create landed the runtime was %s, want absent", o.State)
		}
		wantSettled(t, f, createEffect(s), runtimedriver.Unknown)
		land()
		if o := inspect(t, f, s); o.State != runtimedriver.StateStopped || o.Complete {
			t.Fatalf("after the create landed Inspect = %+v, want an incomplete stopped runtime", o)
		}
		wantSettled(t, f, createEffect(s), runtimedriver.Applied)
		completedAt(t, f, s, inspect(t, f, s).Incarnation)
	}},
	{"settle needs nothing in flight on the runtime", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		created(t, f, s)
		release := f.Recovery().Hold(s.Ref)
		if got := settle(t, f, createEffect(s)); got != runtimedriver.Unknown {
			t.Fatalf("Settle(create) with an operation in flight = %v, want unknown", got)
		}
		release()
		if got := settle(t, f, createEffect(s)); got != runtimedriver.Applied {
			t.Fatalf("Settle(create) once nothing is in flight = %v, want applied", got)
		}
	}},
	{"settle of a create needs the runtime exactly as specified", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		created(t, f, s)
		f.Sessions().Drift(s.Ref, DriftLabels)
		if got, _ := f.Driver().Settle(t.Context(), createEffect(s)); got == runtimedriver.Applied {
			t.Fatal("Settle(create) of a drifted runtime = applied")
		}
	}},
	{"start and stop settle applied once their state holds", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		at := running(t, f, s).Incarnation
		wantSettled(t, f, runtimedriver.Effect{Kind: runtimedriver.EffectStart, Target: target(s, at)}, runtimedriver.Applied)
		if _, err := f.Driver().Stop(t.Context(), s); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		wantSettled(t, f, runtimedriver.Effect{Kind: runtimedriver.EffectStop, Target: target(s, at)}, runtimedriver.Applied)
	}},
}

func createEffect(s runtimedriver.Session) runtimedriver.Effect {
	return runtimedriver.Effect{Kind: runtimedriver.EffectCreate, Target: target(s, "")}
}

// diedInCreate runs a Create that dies at point and restarts the driver. The
// lost call must end with an error once the driver restarted.
func diedInCreate(t *testing.T, f Fixture, s runtimedriver.Session, point CreatePoint) {
	t.Helper()
	died := f.Recovery().DieInCreate(point)
	g := &gate{}
	dead := f.Driver()
	done := async(func() error {
		_, err := dead.Create(t.Context(), s, "", g.call)
		return err
	})
	select {
	case <-died:
	case err := <-done:
		t.Fatalf("Create returned %v without reaching the point where the driver dies", err)
	case <-time.After(waitLimit):
		t.Fatal("timed out waiting for the driver to die in Create")
	}
	f.Restart()
	if err := within(t, done, "the lost Create to return"); err == nil {
		t.Fatal("the Create the driver died in returned success")
	}
	if g.count() != 1 {
		t.Fatalf("the lost Create called the gate %d times, want 1", g.count())
	}
	if n := f.Sessions().Initializations(s.Ref); n != 1 {
		t.Fatalf("the lost Create initialized %d runtimes, want 1", n)
	}
}

// completedAt completes the session's runtime at its incarnation and checks
// that it is complete, never initialized again, and starts. Whether the
// runtime read as incomplete before is not checked after a death between or
// during guest writes: a backend may tell only missing devices.
func completedAt(t *testing.T, f Fixture, s runtimedriver.Session, at runtimedriver.Incarnation) {
	t.Helper()
	o, err := f.Driver().Create(t.Context(), s, at, nil)
	if err != nil {
		t.Fatalf("Create at %s: %v", at, err)
	}
	if o.State != runtimedriver.StateStopped || !o.Complete || o.Incarnation != at {
		t.Fatalf("Create at %s = %+v, want the complete stopped runtime at that incarnation", at, o)
	}
	if n := f.Sessions().Initializations(s.Ref); n != 1 {
		t.Fatalf("%d runtimes initialized, want 1", n)
	}
	if o := inspect(t, f, s); !o.Complete || o.Incarnation != at {
		t.Fatalf("Inspect of the completed runtime = %+v, want it complete at %s", o, at)
	}
	if o, err := f.Driver().Start(t.Context(), s); err != nil || !o.Host.Ready {
		t.Fatalf("Start of the completed runtime = %+v, %v; want a ready host", o, err)
	}
}
