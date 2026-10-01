package runtimeincus

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestCreateRejectsCompetingUUIDBeforeEffectMarker(t *testing.T) {
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
			f := &fakeIncus{}
			b := fakeBackend(f)
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
			marked := false
			_, err := b.CreateWithGate(context.Background(), testSession(), func() error { marked = true; return nil })
			if err == nil || marked || len(f.mutations) != 0 {
				t.Fatalf("ambiguous absence reached marker/init: error=%v marked=%v mutations=%v", err, marked, f.mutations)
			}
		})
	}
}

func TestCreateProvesAbsenceThenRecordsOneInit(t *testing.T) {
	f := &fakeIncus{}
	b := fakeBackend(f)
	base := b.run
	proved := false
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		if len(argv) == 6 && argv[3] == "list" && argv[4] == "--format" {
			proved = true
		}
		return base(ctx, binary, argv, env)
	}
	marks := 0
	got, err := b.CreateWithGate(context.Background(), testSession(), func() error {
		if !proved || len(f.mutations) != 0 {
			t.Fatal("marker precedes absence proof")
		}
		marks++
		return nil
	})
	if err != nil || !got.Exists || marks != 1 || len(f.mutations) != 1 || f.mutations[0] != "init" {
		t.Fatalf("ordinary creation changed: %+v %v marks=%d mutations=%v", got, err, marks, f.mutations)
	}
}

func TestCreatePreflightFailureDoesNotReachInitAttemptGate(t *testing.T) {
	f := &fakeIncus{}
	b := fakeBackend(f)
	base := b.run
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		if len(argv) > 4 && argv[3] == "image" && argv[4] == "list" {
			return nil, errors.New("fixture pinned-image preflight unavailable")
		}
		return base(ctx, binary, argv, env)
	}
	marked := false
	if _, err := b.CreateWithGate(context.Background(), testSession(), func() error { marked = true; return nil }); err == nil || marked || len(f.mutations) != 0 {
		t.Fatalf("preflight crossed dispatch gate: %v marked=%v native=%v", err, marked, f.mutations)
	}
}

func TestCreateLostInitReplyNeverIssuesBlindSecondInitAndCanObserveDelayedInstance(t *testing.T) {
	f := &fakeIncus{}
	b := fakeBackend(f)
	base := b.run
	var issued, issuedEnv []string
	calls, marks := 0, 0
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		if len(argv) > 3 && argv[3] == "init" {
			calls++
			issued = append([]string(nil), argv...)
			issuedEnv = append([]string(nil), env...)
			return nil, errors.New("fixture unknown delayed init")
		}
		return base(ctx, binary, argv, env)
	}
	gate := func() error {
		if marks != 0 {
			return errors.New("durable init attempt already issued")
		}
		marks++
		return nil
	}
	if observed, err := b.CreateWithGate(context.Background(), testSession(), gate); err == nil || observed.Exists || calls != 1 || marks != 1 {
		t.Fatalf("unknown init: %+v %v calls=%d marks=%d", observed, err, calls, marks)
	}
	if _, err := b.CreateWithGate(context.Background(), testSession(), gate); err == nil || calls != 1 {
		t.Fatalf("blind second init: %v calls=%d", err, calls)
	}
	// The original request materializes later. Exact owned observation resumes
	// without another command or another gate crossing.
	if _, err := base(context.Background(), b.config.Binary, issued, issuedEnv); err != nil {
		t.Fatal(err)
	}
	if observed, err := b.CreateWithGate(context.Background(), testSession(), gate); err != nil || !observed.Exists || calls != 1 || marks != 1 {
		t.Fatalf("delayed owned recovery: %+v %v calls=%d marks=%d", observed, err, calls, marks)
	}
}
