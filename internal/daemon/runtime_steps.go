package daemon

import (
	"context"
	"errors"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
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
		return state, control.Failed(api.ReasonRuntime, errors.New("creating runtime is not a stopped owned instance"))
	}
	return state, nil
}

func assembleStoppedRuntime(ctx context.Context, run runtimeCall) error {
	state, err := run(ctx, "runtime.assemble")
	if err != nil {
		return err
	}
	if !state.Exists || state.Status != "Stopped" {
		return control.Failed(api.ReasonRuntime, errors.New("assembly did not leave an owned stopped runtime"))
	}
	return nil
}

// runRuntime runs a runtime command; a failure the runtime does not already
// classify answers failed with reason runtime.
func runRuntime(ctx context.Context, selected plugin.Active, kind string, broker plugin.RuntimeBroker) (plugin.RuntimeState, error) {
	state, err := plugin.RunRuntime(ctx, selected, kind, broker)
	return state, control.Failed(api.ReasonRuntime, err)
}
