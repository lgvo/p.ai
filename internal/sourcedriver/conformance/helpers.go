package conformance

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/sourcedriver"
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
func unexpectedGate(t *testing.T) sourcedriver.Gate {
	return func() error {
		t.Error("the gate was called for a call that had to be refused")
		return nil
	}
}

var errGate = errors.New("the gate refused")

// repository creates the project's repository through the driver.
func repository(t *testing.T, f Fixture, project string) sourcedriver.Repository {
	t.Helper()
	r, err := f.Driver().CreateRepository(t.Context(), project)
	if err != nil {
		t.Fatalf("CreateRepository(%q): %v", project, err)
	}
	return r
}
