package runtimeincus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
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

// exactDiscardIncus is f with the instance at discardIncusUUID and
// discardGeneration, or at a new generation once the exec has ended when
// replacedAfterExec is set. Incus lists an exec operation on the instance as
// running while execRunning says so; stop and delete record whether it still
// was.
type exactDiscardIncus struct {
	f                 *fakeIncus
	execRunning       func() bool
	replacedAfterExec bool
	effects           []string
}

func (x *exactDiscardIncus) run(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
	if len(argv) > 3 {
		switch argv[3] {
		case "operation":
			if !x.execRunning() {
				return []byte(`[]`), nil
			}
			return []byte(`[{"id":"8e09a6c6-0000-4000-8000-000000000000","description":"Executing command","status":"Running","status_code":103,"resources":{"instances":["/1.0/instances/p-` + testUUID + `?project=user-1000"]}}]`), nil
		case "list":
			raw, err := x.f.run(ctx, binary, argv, env)
			if err != nil {
				return nil, err
			}
			var instances []instanceJSON
			if err := json.Unmarshal(raw, &instances); err != nil {
				return nil, err
			}
			generation := discardGeneration
			if x.replacedAfterExec && !x.execRunning() {
				generation = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
			}
			for i := range instances {
				for _, c := range []map[string]string{instances[i].Config, instances[i].ExpandedConfig} {
					c["volatile.uuid"] = discardIncusUUID
					c["volatile.uuid.generation"] = generation
				}
			}
			return json.Marshal(instances)
		case "stop", "delete":
			x.effects = append(x.effects, fmt.Sprintf("%s with exec running=%t", argv[3], x.execRunning()))
		}
	}
	return x.f.run(ctx, binary, argv, env)
}

const (
	discardIncusUUID  = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	discardGeneration = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

// An exec whose incus client was killed before it connected stays running
// in Incus for up to 10 s, whatever the instance's state.
func TestDiscardWaitsForAnAbandonedExecToEnd(t *testing.T) {
	for _, c := range []struct {
		effect, status string
		call           func(*Backend, func() error) error
	}{
		{"stop", "Running", func(b *Backend, mark func() error) error {
			return b.StopForDiscardExact(context.Background(), testSession(), discardIncusUUID, discardGeneration, mark)
		}},
		{"delete", "Stopped", func(b *Backend, mark func() error) error {
			return b.DeleteForDiscardExact(context.Background(), testSession(), discardIncusUUID, discardGeneration, mark)
		}},
	} {
		t.Run(c.effect, func(t *testing.T) {
			ends := time.Now().Add(2 * time.Second)
			x := &exactDiscardIncus{f: &fakeIncus{instance: true, status: c.status}, execRunning: func() bool { return time.Now().Before(ends) }}
			b := fakeBackend(x.f)
			b.run = x.run
			if err := c.call(b, func() error { return nil }); err != nil {
				t.Fatalf("%s blocked on an exec that ended within Incus's 10 s: %v", c.effect, err)
			}
			if want := []string{c.effect + " with exec running=false"}; !slices.Equal(x.effects, want) {
				t.Fatalf("Incus effects %v, want %v", x.effects, want)
			}
		})
	}
}

// An operation P waited for can have changed the instance, so removal acts
// only on the generation it finds after the wait.
func TestDiscardRefusesAGenerationReplacedWhileItWaited(t *testing.T) {
	for _, c := range []struct {
		effect, status string
		call           func(*Backend, func() error) error
	}{
		{"stop", "Running", func(b *Backend, mark func() error) error {
			return b.StopForDiscardExact(context.Background(), testSession(), discardIncusUUID, discardGeneration, mark)
		}},
		{"delete", "Stopped", func(b *Backend, mark func() error) error {
			return b.DeleteForDiscardExact(context.Background(), testSession(), discardIncusUUID, discardGeneration, mark)
		}},
	} {
		t.Run(c.effect, func(t *testing.T) {
			ends := time.Now().Add(time.Second)
			x := &exactDiscardIncus{f: &fakeIncus{instance: true, status: c.status}, execRunning: func() bool { return time.Now().Before(ends) }, replacedAfterExec: true}
			b := fakeBackend(x.f)
			b.run = x.run
			marked := false
			err := c.call(b, func() error { marked = true; return nil })
			if err == nil || !strings.Contains(err.Error(), "generation or state changed") || marked || len(x.effects) != 0 {
				t.Fatalf("%s reached a generation replaced while it waited: marked=%t effects=%v err=%v", c.effect, marked, x.effects, err)
			}
		})
	}
}

func TestDiscardBlocksOnAnIncusOperationThatOutlastsTheWait(t *testing.T) {
	x := &exactDiscardIncus{f: &fakeIncus{instance: true, status: "Stopped"}, execRunning: func() bool { return true }}
	b := fakeBackend(x.f)
	b.run = x.run
	marked := false
	began := time.Now()
	err := b.DeleteForDiscardExact(context.Background(), testSession(), discardIncusUUID, discardGeneration, func() error { marked = true; return nil })
	if err == nil || !strings.Contains(err.Error(), "discard source has active Incus operation") || marked || len(x.effects) != 0 {
		t.Fatalf("delete went on beside a running operation: marked=%t effects=%v err=%v", marked, x.effects, err)
	}
	if elapsed := time.Since(began); elapsed <= 10*time.Second || elapsed > 20*time.Second {
		t.Fatalf("blocked after %s, not after a wait longer than Incus's 10 s", elapsed)
	}
}
