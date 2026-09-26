package daemon

import (
	"context"
	"errors"

	"github.com/lgvo/p.ai/internal/plugin"
)

type runtimeCall func(context.Context, string) (plugin.RuntimeState, error)

// ensureStoppedRuntime asks the selected module to complete native Create for
// both absent and exact stopped instances. Create is idempotent and repairs an
// endpoint attachment interrupted after Incus init; the module's preliminary
// observation is the only one allowed to see that partial state.
func ensureStoppedRuntime(ctx context.Context, run runtimeCall) (plugin.RuntimeState, error) {
	state, err := run(ctx, "runtime.create")
	if err != nil {
		return state, err
	}
	if !state.Exists || state.Status != "Stopped" {
		return state, errors.New("creating runtime is not a stopped owned instance")
	}
	return state, nil
}

func assembleStoppedRuntime(ctx context.Context, run runtimeCall) error {
	state, err := run(ctx, "runtime.assemble")
	if err != nil {
		return err
	}
	if !state.Exists || state.Status != "Stopped" {
		return errors.New("assembly did not leave an owned stopped runtime")
	}
	return nil
}
