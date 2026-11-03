package runtimeincus

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// Create proves the whole project free of the session's identity before its
// gate: an instance under another name that carries the session's labels, in
// its own or its expanded config, one whose labels cannot be read, or a
// listing that fails refuses it with no effect.
func TestCreateRefusesBeforeItsGateUnlessTheProjectIsFreeOfTheSession(t *testing.T) {
	for _, tc := range []struct {
		name             string
		config, expanded map[string]string
		unavailable      bool
	}{
		{name: "renamed", config: map[string]string{"user.p.session_uuid": testUUID}, expanded: map[string]string{}},
		{name: "expanded-label", config: map[string]string{}, expanded: map[string]string{"user.p.session_uuid": testUUID}},
		{name: "opaque", config: nil, expanded: map[string]string{}},
		{name: "unavailable", unavailable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newIncusWorld(t)
			b := w.backend()
			base := b.run
			b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
				if len(argv) == 6 && argv[3] == "list" && argv[4] == "--format" {
					if tc.unavailable {
						return nil, errors.New("inventory unavailable")
					}
					return json.Marshal([]instanceJSON{{Name: "externally-renamed", Type: "container", Status: "Stopped", Config: tc.config, ExpandedConfig: tc.expanded}})
				}
				return base(ctx, binary, argv, env)
			}
			gated := false
			_, err := NewDriver(b).Create(context.Background(), testSessionSpec(t), "", func() error { gated = true; return nil })
			if err == nil || gated || len(w.mutations()) != 0 {
				t.Fatalf("ambiguous absence reached the gate or Incus: error=%v gated=%v mutations=%v", err, gated, w.mutations())
			}
		})
	}
}

func TestCreatePreflightFailureNeverReachesItsGate(t *testing.T) {
	w := newIncusWorld(t)
	w.before = func(args []string) error {
		if slices.Equal(args[:2], []string{"image", "list"}) {
			return errors.New("pinned image listing unavailable")
		}
		return nil
	}
	gated := false
	if _, err := w.driver().Create(context.Background(), testSessionSpec(t), "", func() error { gated = true; return nil }); err == nil || gated || len(w.mutations()) != 0 {
		t.Fatalf("preflight crossed the gate: %v gated=%v effects=%v", err, gated, w.mutations())
	}
}

// A lost init request is an unknown outcome, and the init is never sent
// again blindly: a second Create reaches only the gate, which core refuses
// once it recorded an attempt. When the delayed init lands, Create with no
// incarnation never adopts the runtime; Create at its incarnation completes
// it without another init or gate.
func TestCreateAfterALostInitNeverInitsAgainAndCompletesTheDelayedRuntime(t *testing.T) {
	ctx := context.Background()
	w := newIncusWorld(t)
	var delayed []string
	w.before = func(args []string) error {
		if args[0] == "init" {
			delayed = slices.Clone(args)
			return errors.New("Incus connection lost")
		}
		return nil
	}
	marks := 0
	gate := func() error {
		if marks != 0 {
			return errors.New("durable init attempt already issued")
		}
		marks++
		return nil
	}
	d := w.driver()
	s := testSessionSpec(t)
	if _, err := d.Create(ctx, s, "", gate); !errors.Is(err, runtimedriver.ErrUnknownOutcome) || marks != 1 {
		t.Fatalf("lost init: %v marks=%d", err, marks)
	}
	if _, err := d.Create(ctx, s, "", gate); err == nil || w.count("init") != 1 {
		t.Fatalf("blind second init: %v inits=%d", err, w.count("init"))
	}
	w.before = nil
	w.mu.Lock()
	_, landed := w.command(delayed)
	w.mu.Unlock()
	if landed != nil {
		t.Fatal(landed)
	}
	if _, err := d.Create(ctx, s, "", gate); !errors.Is(err, runtimedriver.ErrMismatch) || w.count("init") != 1 || marks != 1 {
		t.Fatalf("delayed runtime adopted: %v inits=%d marks=%d", err, w.count("init"), marks)
	}
	observed, err := d.Inspect(ctx, s)
	if err != nil || observed.State != runtimedriver.StateStopped || observed.Complete {
		t.Fatalf("delayed runtime: %+v %v", observed, err)
	}
	got, err := d.Create(ctx, s, observed.Incarnation, nil)
	if err != nil || !got.Complete || w.count("init") != 1 || marks != 1 {
		t.Fatalf("completing the delayed runtime: %+v %v inits=%d marks=%d", got, err, w.count("init"), marks)
	}
}

// An init whose reply is lost after Incus applied it leaves a runtime the
// same Create observes and completes.
func TestCreateCompletesARuntimeWhoseInitReplyWasLost(t *testing.T) {
	w := newIncusWorld(t)
	w.after = func(args []string) error {
		if args[0] == "init" {
			return errors.New("connection dropped")
		}
		return nil
	}
	got, err := w.driver().Create(context.Background(), testSessionSpec(t), "", func() error { return nil })
	if err != nil || got.State != runtimedriver.StateStopped || !got.Complete || w.count("init") != 1 {
		t.Fatalf("create after a lost reply: %+v %v inits=%d", got, err, w.count("init"))
	}
}
