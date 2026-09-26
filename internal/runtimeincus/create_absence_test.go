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
