package runtimeincus

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestProjectDeletionInventoryRefusesUnreviewedNativeAndCache(t *testing.T) {
	f := &fakeIncus{}
	b := fakeBackend(f)
	project := "team/app"
	instances := []instanceJSON{}
	images := []builderImageJSON{}
	unavailable := false
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		switch {
		case slices.Equal(argv[3:], []string{"list", "--format", "json"}):
			if unavailable {
				return nil, errors.New("native unavailable")
			}
			return json.Marshal(instances)
		case slices.Equal(argv[3:], []string{"image", "list", "--format", "json"}):
			return json.Marshal(images)
		default:
			return f.run(ctx, binary, argv, env)
		}
	}
	check := func(names, fps map[string]bool) error {
		return b.CheckProjectDeletionInventory(context.Background(), project, names, fps)
	}
	if err := check(nil, nil); err != nil {
		t.Fatal(err)
	}
	instances = []instanceJSON{{Name: "foreign", Type: "container", Config: map[string]string{"user.p.project_path": "other"}, ExpandedConfig: map[string]string{}}}
	if err := check(nil, nil); err != nil {
		t.Fatal("unrelated resource rejected", err)
	}
	instances[0].ExpandedConfig["user.p.project_path"] = project
	if err := check(nil, nil); err == nil {
		t.Fatal("unindexed/expanded-owned runtime forgotten")
	}
	if err := check(map[string]bool{"foreign": true}, nil); err != nil {
		t.Fatal("reviewed resource refused", err)
	}
	instances[0].ExpandedConfig = map[string]string{"user.p.builder_project_path": project}
	if err := check(nil, nil); err == nil {
		t.Fatal("unreviewed builder forgotten")
	}
	instances = nil
	images = []builderImageJSON{{Fingerprint: strings.Repeat("a", 64), Properties: map[string]string{"p.project_path": project}}}
	if err := check(nil, nil); err == nil {
		t.Fatal("unindexed cache forgotten")
	}
	if err := check(nil, map[string]bool{strings.Repeat("a", 64): true}); err != nil {
		t.Fatal(err)
	}
	unavailable = true
	if err := check(nil, nil); err == nil {
		t.Fatal("unreachable treated absent")
	}
	if len(f.mutations) != 0 {
		t.Fatal("read inventory mutated native authority")
	}
}
