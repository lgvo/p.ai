package conformance

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

var sessionCases = []testCase{
	{"inspect reports absent when nothing is at the session's address", func(t *testing.T, f Fixture) {
		o := inspect(t, f, f.Session(SessionOptions{}))
		if o.State != runtimedriver.StateAbsent {
			t.Fatalf("Inspect of a session never created = %+v, want absent", o)
		}
	}},
	{"create calls the gate once before any effect and leaves a complete stopped runtime", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		g := &gate{before: func() {
			if o := inspect(t, f, s); o.State != runtimedriver.StateAbsent {
				t.Errorf("at the gate the runtime was already %s", o.State)
			}
			if n := f.Sessions().Initializations(s.Ref); n != 0 {
				t.Errorf("at the gate %d runtimes were already initialized", n)
			}
		}}
		o, err := f.Driver().Create(t.Context(), s, "", g.call)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if g.count() != 1 {
			t.Fatalf("Create called the gate %d times, want 1", g.count())
		}
		if o.State != runtimedriver.StateStopped || !o.Complete || o.Incarnation == "" {
			t.Fatalf("Create = %+v, want a complete stopped runtime with an incarnation", o)
		}
		if got := inspect(t, f, s); got.State != runtimedriver.StateStopped || !got.Complete || got.Incarnation != o.Incarnation {
			t.Fatalf("Inspect after Create = %+v, want %+v", got, o)
		}
		if n := f.Sessions().Initializations(s.Ref); n != 1 {
			t.Fatalf("Create initialized %d runtimes, want 1", n)
		}
	}},
	{"a session with a grant and a public-egress address is created complete and starts", func(t *testing.T, f Fixture) {
		opts := SessionOptions{Grant: true, PublicEgress: offersPublicEgress(f)}
		s := f.Session(opts)
		if o := created(t, f, s); !o.Complete {
			t.Fatalf("Create = %+v, want complete", o)
		}
		if o, err := f.Driver().Start(t.Context(), s); err != nil || !o.Host.Ready {
			t.Fatalf("Start = %+v, %v; want a ready host", o, err)
		}
	}},
	{"a failing gate leaves nothing", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		g := &gate{err: errGate}
		if _, err := f.Driver().Create(t.Context(), s, "", g.call); !errors.Is(err, errGate) {
			t.Fatalf("Create with a failing gate = %v, want the gate's error", err)
		}
		if o := inspect(t, f, s); o.State != runtimedriver.StateAbsent {
			t.Fatalf("after a failing gate the runtime is %s, want absent", o.State)
		}
		if n := f.Sessions().Initializations(s.Ref); n != 0 {
			t.Fatalf("after a failing gate %d runtimes were initialized", n)
		}
	}},
	{"create refuses a session whose runtime exists, without calling the gate", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		first := created(t, f, s)
		if _, err := f.Driver().Create(t.Context(), s, "", unexpectedGate(t)); err == nil {
			t.Fatal("a second Create of the same session succeeded")
		}
		if n := f.Sessions().Initializations(s.Ref); n != 1 {
			t.Fatalf("%d runtimes initialized, want 1", n)
		}
		if o := inspect(t, f, s); o.Incarnation != first.Incarnation {
			t.Fatalf("the runtime changed incarnation from %s to %s", first.Incarnation, o.Incarnation)
		}
	}},
	{"a runtime renamed away from its address blocks create", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		created(t, f, s)
		f.Sessions().RenameAway(s.Ref)
		if o := inspect(t, f, s); o.State != runtimedriver.StateAbsent {
			t.Fatalf("Inspect at the emptied address = %+v, want absent", o)
		}
		if _, err := f.Driver().Create(t.Context(), s, "", unexpectedGate(t)); err == nil {
			t.Fatal("Create succeeded while a renamed runtime carries the reference")
		}
		if n := f.Sessions().Initializations(s.Ref); n != 1 {
			t.Fatalf("%d runtimes initialized, want 1", n)
		}
	}},
	{"inspect reports a mismatch for a runtime that drifted from its spec", func(t *testing.T, f Fixture) {
		for _, d := range []struct {
			name  string
			drift Drift
		}{{"labels", DriftLabels}, {"image", DriftImage}, {"devices", DriftDevices}} {
			t.Run(d.name, func(t *testing.T) {
				s := f.Session(SessionOptions{})
				created(t, f, s)
				f.Sessions().Drift(s.Ref, d.drift)
				if _, err := f.Driver().Inspect(t.Context(), s); !errors.Is(err, runtimedriver.ErrMismatch) {
					t.Fatalf("Inspect of a runtime whose %s drifted = %v, want ErrMismatch", d.name, err)
				}
			})
		}
	}},
	{"start returns a running runtime whose host is ready, at the same incarnation", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		c := created(t, f, s)
		o, err := f.Driver().Start(t.Context(), s)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		if o.State != runtimedriver.StateRunning || !o.Host.Ready || o.Incarnation != c.Incarnation {
			t.Fatalf("Start = %+v, want running and ready at %s", o, c.Incarnation)
		}
		if got := inspect(t, f, s); got.State != runtimedriver.StateRunning || !got.Host.Ready || got.Incarnation != c.Incarnation {
			t.Fatalf("Inspect after Start = %+v", got)
		}
	}},
	{"stop leaves the runtime stopped at the same incarnation", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		r := running(t, f, s)
		o, err := f.Driver().Stop(t.Context(), s)
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if o.State != runtimedriver.StateStopped || o.Incarnation != r.Incarnation {
			t.Fatalf("Stop = %+v, want stopped at %s", o, r.Incarnation)
		}
		if got := inspect(t, f, s); got.State != runtimedriver.StateStopped || got.Incarnation != r.Incarnation {
			t.Fatalf("Inspect after Stop = %+v", got)
		}
	}},
	{"start and stop do nothing when the state holds", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		created(t, f, s)
		steps := []struct {
			name        string
			call        func() (runtimedriver.Observation, error)
			transitions int
		}{
			{"stop of a stopped runtime", func() (runtimedriver.Observation, error) { return f.Driver().Stop(t.Context(), s) }, 0},
			{"start", func() (runtimedriver.Observation, error) { return f.Driver().Start(t.Context(), s) }, 1},
			{"start of a running runtime", func() (runtimedriver.Observation, error) { return f.Driver().Start(t.Context(), s) }, 1},
			{"stop", func() (runtimedriver.Observation, error) { return f.Driver().Stop(t.Context(), s) }, 2},
			{"stop of a stopped runtime again", func() (runtimedriver.Observation, error) { return f.Driver().Stop(t.Context(), s) }, 2},
		}
		for _, step := range steps {
			if _, err := step.call(); err != nil {
				t.Fatalf("%s: %v", step.name, err)
			}
			if n := f.Sessions().Transitions(s.Ref); n != step.transitions {
				t.Fatalf("after %s the backend made %d transitions, want %d", step.name, n, step.transitions)
			}
		}
	}},
	{"start refuses a session without a runtime", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		if _, err := f.Driver().Start(t.Context(), s); err == nil {
			t.Fatal("Start of a session never created succeeded")
		}
		if n := f.Sessions().Initializations(s.Ref); n != 0 {
			t.Fatalf("Start initialized %d runtimes", n)
		}
	}},
	{"a host failure leaves the runtime stopped and start returns its diagnostic", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		created(t, f, s)
		const diagnostic = "the session command exited with status 3"
		f.Sessions().FailHostOnNextStart(s.Ref, diagnostic)
		_, err := f.Driver().Start(t.Context(), s)
		var hostErr *runtimedriver.HostError
		if !errors.As(err, &hostErr) || !strings.Contains(hostErr.Diagnostic, diagnostic) {
			t.Fatalf("Start with a failing host = %v, want a HostError telling %q", err, diagnostic)
		}
		if o := inspect(t, f, s); o.State != runtimedriver.StateStopped {
			t.Fatalf("after a host failure the runtime is %s, want stopped", o.State)
		}
		if o, err := f.Driver().Start(t.Context(), s); err != nil || !o.Host.Ready {
			t.Fatalf("the next Start = %+v, %v; want a ready host", o, err)
		}
	}},
	{"inspect reports a host that is not ready with its diagnostic", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		running(t, f, s)
		const diagnostic = "the session host is restarting"
		f.Sessions().UnreadyHost(s.Ref, diagnostic)
		o := inspect(t, f, s)
		if o.State != runtimedriver.StateRunning || o.Host.Ready || !strings.Contains(o.Host.Diagnostic, diagnostic) {
			t.Fatalf("Inspect = %+v, want running, not ready, telling %q", o, diagnostic)
		}
	}},
	{"a file written anywhere in the private root survives stop and start", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		running(t, f, s)
		const path = "/home/p/marker"
		data := []byte("written by the session outside its workspace\n")
		f.Sessions().WriteRootFile(s.Ref, path, data)
		if _, err := f.Driver().Stop(t.Context(), s); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if _, err := f.Driver().Start(t.Context(), s); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if got, found := f.Sessions().ReadRootFile(s.Ref, path); !found || !bytes.Equal(got, data) {
			t.Fatalf("after stop and start %s holds %q (found %t), want %q", path, got, found, data)
		}
	}},
	{"a session gets exactly its public-egress address, and none without one", func(t *testing.T, f Fixture) {
		if !offersPublicEgress(f) {
			t.Skip("the driver offers no public egress")
		}
		public := f.Session(SessionOptions{PublicEgress: true})
		none := f.Session(SessionOptions{})
		created(t, f, public)
		created(t, f, none)
		if got := f.Sessions().PublicAddress(public.Ref); got != public.Network.PublicIPv4 {
			t.Fatalf("the public session is attached to %q, want %q", got, public.Network.PublicIPv4)
		}
		if got := f.Sessions().PublicAddress(none.Ref); got != "" {
			t.Fatalf("the session without a network is attached to %q", got)
		}
	}},
}

func offersPublicEgress(f Fixture) bool {
	return slices.Contains(f.Driver().Describe().Networks, runtimedriver.NetworkPublicEgress)
}
