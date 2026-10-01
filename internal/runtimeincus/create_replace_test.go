package runtimeincus

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestFailedCreateAbsenceRequiresRuntimeAndBuilderInventory(t *testing.T) {
	ctx := context.Background()
	creation := "22222222-2222-4222-8222-222222222222"
	for _, tc := range []struct {
		name        string
		native      []instanceJSON
		unavailable bool
		wantAbsent  bool
	}{
		{name: "absent", wantAbsent: true},
		{name: "sibling", native: []instanceJSON{{Name: "p-sibling", Type: "container", Status: "Running", Config: map[string]string{}, ExpandedConfig: map[string]string{}}}, wantAbsent: true},
		{name: "runtime", native: []instanceJSON{{Name: "p-" + testUUID, Type: "container", Status: "Stopped", Config: map[string]string{}, ExpandedConfig: map[string]string{}}}},
		{name: "builder-name", native: []instanceJSON{{Name: "p-builder-" + creation, Type: "container", Status: "Stopped", Config: map[string]string{}, ExpandedConfig: map[string]string{}}}},
		{name: "builder-renamed", native: []instanceJSON{{Name: "renamed", Type: "container", Status: "Stopped", Config: map[string]string{"user.p.builder_request_uuid": creation, "user.p.builder_project_path": "/source/repo.git"}, ExpandedConfig: map[string]string{"user.p.builder_request_uuid": creation, "user.p.builder_project_path": "/source/repo.git"}}}},
		{name: "builder-label-disagreement", native: []instanceJSON{{Name: "renamed", Type: "container", Status: "Stopped", Config: map[string]string{"user.p.builder_request_uuid": creation}, ExpandedConfig: map[string]string{}}}},
		{name: "unavailable", unavailable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeIncus{}
			b := fakeBackend(f)
			base := b.run
			b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
				if len(argv) > 3 && argv[3] == "project" {
					raw, e := base(ctx, binary, argv, env)
					if e != nil {
						return nil, e
					}
					var projects []projectJSON
					if e = json.Unmarshal(raw, &projects); e != nil {
						return nil, e
					}
					projects[0].Config["limits.containers"] = "4"
					return json.Marshal(projects)
				}
				if len(argv) > 3 && argv[3] == "list" && !slices.Contains(argv, "^p-"+testUUID+"$") {
					if tc.unavailable {
						return nil, errors.New("native inventory unavailable")
					}
					if tc.native == nil {
						return []byte(`[]`), nil
					}
					return json.Marshal(tc.native)
				}
				return base(ctx, binary, argv, env)
			}
			err := b.ConfirmFailedCreateEffectsAbsent(ctx, testSession(), creation)
			if (err == nil) != tc.wantAbsent {
				t.Fatalf("absence=%v: %v", tc.wantAbsent, err)
			}
			if len(f.mutations) != 0 {
				t.Fatalf("absence proof mutated native state: %v", f.mutations)
			}
		})
	}
}

func TestFailedCreateRechecksBuilderAndRuntimeAfterInitialAbsence(t *testing.T) {
	creation := "22222222-2222-4222-8222-222222222222"
	for _, tc := range []struct {
		name   string
		config map[string]string
		status string
	}{
		{name: "renamed-runtime", config: map[string]string{"user.p.session_uuid": testUUID}, status: "Stopped"},
		{name: "builder", config: map[string]string{"user.p.builder_request_uuid": creation}, status: "Stopped"},
		{name: "opaque", config: map[string]string{}, status: "Unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeIncus{}
			b := fakeBackend(f)
			base := b.run
			reads := 0
			b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
				if len(argv) > 3 && argv[3] == "list" && !slices.Contains(argv, "^p-"+testUUID+"$") {
					reads++
					if reads == 1 {
						return []byte(`[]`), nil
					}
					return json.Marshal([]instanceJSON{{Name: "renamed", Type: "container", Status: tc.status, Config: tc.config, ExpandedConfig: map[string]string{}}})
				}
				return base(ctx, binary, argv, env)
			}
			if e := b.ConfirmFailedCreateEffectsAbsent(context.Background(), testSession(), creation); e == nil {
				t.Fatal("changed second inventory admitted")
			}
			if len(f.mutations) != 0 {
				t.Fatal("absence mutated native state")
			}
		})
	}
}
