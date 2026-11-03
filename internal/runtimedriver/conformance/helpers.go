package conformance

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// waitLimit bounds every wait of the suite, so that a driver that never
// answers fails its case instead of hanging the run.
const waitLimit = 10 * time.Second

// gate counts its calls and runs before, when set, on each one; it fails
// with err when err is set.
type gate struct {
	calls  atomic.Int32
	before func()
	err    error
}

func (g *gate) call() error {
	g.calls.Add(1)
	if g.before != nil {
		g.before()
	}
	return g.err
}

func (g *gate) count() int { return int(g.calls.Load()) }

// unexpectedGate is a gate for a call that must refuse before its first
// effect.
func unexpectedGate(t *testing.T) runtimedriver.Gate {
	return func() error {
		t.Error("the gate was called for a call that had to be refused")
		return nil
	}
}

var errGate = errors.New("the gate refused")

// created creates the session's runtime and returns its observation.
func created(t *testing.T, f Fixture, s runtimedriver.Session) runtimedriver.Observation {
	t.Helper()
	o, err := f.Driver().Create(t.Context(), s, "", func() error { return nil })
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return o
}

// running creates and starts the session's runtime and returns the
// observation of the start.
func running(t *testing.T, f Fixture, s runtimedriver.Session) runtimedriver.Observation {
	t.Helper()
	created(t, f, s)
	o, err := f.Driver().Start(t.Context(), s)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return o
}

func inspect(t *testing.T, f Fixture, s runtimedriver.Session) runtimedriver.Observation {
	t.Helper()
	o, err := f.Driver().Inspect(t.Context(), s)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	return o
}

func settle(t *testing.T, f Fixture, e runtimedriver.Effect) runtimedriver.Settlement {
	t.Helper()
	got, err := f.Driver().Settle(t.Context(), e)
	if err != nil && got != runtimedriver.Unknown {
		t.Fatalf("Settle answered %v with an error: %v", got, err)
	}
	return got
}

// wantSettled checks that the effect settles as want, before and after a
// driver restart: the answer comes from native state only.
func wantSettled(t *testing.T, f Fixture, e runtimedriver.Effect, want runtimedriver.Settlement) {
	t.Helper()
	if got := settle(t, f, e); got != want {
		t.Fatalf("Settle(%s) = %v, want %v", e.Kind, got, want)
	}
	f.Restart()
	if got := settle(t, f, e); got != want {
		t.Fatalf("after a restart, Settle(%s) = %v, want %v", e.Kind, got, want)
	}
}

func target(s runtimedriver.Session, at runtimedriver.Incarnation) runtimedriver.Target {
	return runtimedriver.Target{Session: s, At: at}
}

// within waits for ch to close or deliver, failing the case after waitLimit.
func within[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(waitLimit):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}

// async runs call in its own goroutine and delivers its error.
func async(call func() error) <-chan error {
	done := make(chan error, 1)
	go func() { done <- call() }()
	return done
}
