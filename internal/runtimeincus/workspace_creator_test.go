package runtimeincus

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestAssembledCreationSourceRejectsUnknownOrCompetingNativeInventory(t *testing.T) {
	const creator = "22222222-2222-4222-8222-222222222222"
	const native = "33333333-3333-4333-8333-333333333333"
	const generation = "44444444-4444-4444-8444-444444444444"
	for _, name := range []string{"owned-stopped", "running", "generation", "uuid", "renamed", "competing", "builder", "unknown", "unreachable", "mount", "security", "inventory-changed"} {
		t.Run(name, func(t *testing.T) {
			f := &fakeIncus{instance: true, status: "Stopped"}
			if name == "running" {
				f.status = "Running"
			}
			if name == "mount" {
				f.unsafeExpandedDevice = true
			}
			if name == "security" {
				f.unsafeExpandedConfig = true
			}
			b := fakeBackend(f)
			b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
				raw, err := f.run(ctx, binary, argv, env)
				if err != nil {
					return nil, err
				}
				if len(argv) <= 3 {
					return raw, nil
				}
				if argv[3] == "project" {
					var projects []projectJSON
					_ = json.Unmarshal(raw, &projects)
					projects[0].Config["limits.containers"] = "4"
					return json.Marshal(projects)
				}
				if argv[3] != "list" {
					return raw, nil
				}
				inventory := !slices.Contains(argv, "^p-"+testUUID+"$")
				if inventory && name == "unreachable" {
					return nil, errors.New("inventory unavailable")
				}
				var instances []instanceJSON
				_ = json.Unmarshal(raw, &instances)
				for _, c := range []map[string]string{instances[0].Config, instances[0].ExpandedConfig} {
					c["volatile.uuid"], c["volatile.uuid.generation"] = native, generation
					if name == "uuid" {
						c["volatile.uuid"] = creator
					}
					if name == "generation" || inventory && name == "inventory-changed" {
						c["volatile.uuid.generation"] = creator
					}
				}
				if inventory {
					switch name {
					case "renamed":
						instances[0].Name = "renamed"
					case "competing", "builder", "unknown":
						other := instanceJSON{Name: "other", Type: "container", Status: "Stopped", Config: map[string]string{}, ExpandedConfig: map[string]string{}}
						if name == "competing" {
							other.ExpandedConfig["user.p.session_uuid"] = testUUID
						}
						if name == "builder" {
							other.Config["user.p.builder_request_uuid"] = creator
						}
						if name == "unknown" {
							other.Status = "Unknown"
						}
						instances = append(instances, other)
					}
				}
				return json.Marshal(instances)
			}
			err := b.CheckAssembledCreationSource(context.Background(), testSession(), creator, native, generation)
			if (err == nil) != (name == "owned-stopped") {
				t.Fatalf("native proof=%v", err)
			}
			if len(f.mutations) != 0 {
				t.Fatalf("read-only proof mutated native resources: %v", f.mutations)
			}
		})
	}
}
