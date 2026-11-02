package plugin

import (
	"bytes"
	"context"
	"errors"
	"reflect"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// newSandbox returns the runtime every executable capability runs in. The
// caller adds its broker module and closes the runtime.
func newSandbox(ctx context.Context) (wazero.Runtime, error) {
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigInterpreter().WithMemoryLimitPages(256).WithCloseOnContextDone(true))
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		rt.Close(ctx)
		return nil, err
	}
	return rt, nil
}

type moduleFailure int

const (
	moduleUnreadable moduleFailure = iota + 1
	moduleChanged
	moduleMissing
)

func selectedModule(ctx context.Context, selected Active) ([]byte, moduleFailure) {
	m := selected.Package.Manifest
	current, retained, err := packageSnapshot(ctx, selected.Package.Path, map[string]bool{m.Runtime.Entry: true})
	if err != nil {
		return nil, moduleUnreadable
	}
	if current.SHA256 != selected.Package.SHA256 || !reflect.DeepEqual(current.Manifest, m) {
		return nil, moduleChanged
	}
	module := retained[m.Runtime.Entry]
	if len(module) == 0 {
		return nil, moduleMissing
	}
	return module, 0
}

// guestMemory returns the calling module's linear memory, or nil. Module.Memory
// can hold a typed nil when a module has no memory; WASI commands export their
// linear memory as "memory", and ExportedMemory returns a real nil when it is
// missing.
func guestMemory(module api.Module) api.Memory { return module.ExportedMemory("memory") }

type runFailure int

const (
	runTimedOut runFailure = iota + 1
	runOutputExceeded
	runFailed
)

// runModule runs a command module with one input line, collecting stdout and
// stderr up to maxCommandBytes each. A broker failure recorded during the run
// takes precedence over the run's own failure; callers check it first.
func runModule(ctx context.Context, rt wazero.Runtime, module, input []byte) ([]byte, runFailure) {
	var output, diagnostic boundedOutput
	config := wazero.NewModuleConfig().WithStdin(bytes.NewReader(append(input, '\n'))).WithStdout(&output).WithStderr(&diagnostic)
	_, err := rt.InstantiateWithConfig(ctx, module, config)
	if ctx.Err() != nil {
		return nil, runTimedOut
	}
	if output.overflow || diagnostic.overflow {
		return nil, runOutputExceeded
	}
	if err != nil {
		var exit *sys.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 0 {
			return nil, runFailed
		}
	}
	return output.Bytes(), 0
}
