package runtimeincus

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func TestExactDiscardDeleteUnknownCannotTargetLateReplacement(t *testing.T) {
	f := &fakeIncus{instance: true, status: "Stopped"}
	b := fakeBackend(f)
	const oldUUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	const generation = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	currentUUID := oldUUID
	marked, attempted := false, 0
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		if len(argv) > 3 {
			switch argv[3] {
			case "operation":
				return []byte(`[]`), nil // delayed request has no OperationCreate yet
			case "list":
				raw, err := f.run(ctx, binary, argv, env)
				if err != nil {
					return nil, err
				}
				var instances []instanceJSON
				if err := json.Unmarshal(raw, &instances); err != nil {
					return nil, err
				}
				for i := range instances {
					for _, c := range []map[string]string{instances[i].Config, instances[i].ExpandedConfig} {
						c["volatile.uuid"] = currentUUID
						c["volatile.uuid.generation"] = generation
					}
				}
				return json.Marshal(instances)
			case "delete":
				if !marked {
					t.Fatal("name-targeted delete sent before durable marker")
				}
				attempted++
				return nil, errors.New("request canceled before Incus operation admission")
			}
		}
		return f.run(ctx, binary, argv, env)
	}
	mark := func() error { marked = true; return nil }
	if err := b.DeleteForDiscardExact(context.Background(), testSession(), oldUUID, generation, mark); err == nil || !marked || attempted != 1 {
		t.Fatalf("unknown delete was not retained: marked=%t attempts=%d err=%v", marked, attempted, err)
	}
	// The server can later admit that first request by name. A new generation
	// with the same P labels cannot authorize another request or completion.
	currentUUID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	marked = false
	if err := b.DeleteForDiscardExact(context.Background(), testSession(), oldUUID, generation, mark); err == nil || marked || attempted != 1 || slices.Contains(f.mutations, "delete") {
		t.Fatalf("same-name replacement reached delete: marked=%t attempts=%d mutations=%v err=%v", marked, attempted, f.mutations, err)
	}
}

func TestDiscardStopForceStopsARunningSourceAfterItsMarker(t *testing.T) {
	f := &fakeIncus{instance: true, status: "Running"}
	b := fakeBackend(f)
	const incusUUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	const generation = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	marked := false
	var stopArgs []string
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		if len(argv) > 3 {
			switch argv[3] {
			case "operation":
				return []byte(`[]`), nil
			case "list":
				raw, err := f.run(ctx, binary, argv, env)
				if err != nil {
					return nil, err
				}
				var instances []instanceJSON
				if err := json.Unmarshal(raw, &instances); err != nil {
					return nil, err
				}
				for i := range instances {
					for _, c := range []map[string]string{instances[i].Config, instances[i].ExpandedConfig} {
						c["volatile.uuid"] = incusUUID
						c["volatile.uuid.generation"] = generation
					}
				}
				return json.Marshal(instances)
			case "stop":
				if !marked {
					t.Fatal("stop sent before durable marker")
				}
				stopArgs = argv[3:]
			}
		}
		return f.run(ctx, binary, argv, env)
	}
	if err := b.StopForDiscardExact(context.Background(), testSession(), incusUUID, generation, func() error { marked = true; return nil }); err != nil {
		t.Fatalf("running source was not stopped: %v", err)
	}
	if !slices.Equal(stopArgs, []string{"stop", "p-" + testUUID, "--force"}) || f.status != "Stopped" {
		t.Fatalf("stop request %v left status %s", stopArgs, f.status)
	}
	marked = false
	if err := b.StopForDiscardExact(context.Background(), testSession(), incusUUID, generation, func() error { marked = true; return nil }); err != nil || marked {
		t.Fatalf("an already stopped source was stopped again: marked=%t err=%v", marked, err)
	}
}
