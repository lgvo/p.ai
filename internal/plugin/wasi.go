package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

const (
	WASIRequestSchema = "p.command/v1"
	WASIResultSchema  = "p.command-result/v1"
	BrokerSchema      = "p.broker/v1"
	maxCommandBytes   = 4096
	maxBrokerCalls    = 1
)

var wasiSlots = make(chan struct{}, 4)

type CommandRequest struct {
	Schema string `json:"schema"`
	Kind   string `json:"kind"`
	Event  Event  `json:"event"`
}

type CommandResult struct {
	Schema  string `json:"schema"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// BrokerCall has a deliberately closed method set. Future capability methods
// require their own typed argument schemas and core operation-scope checks.
type BrokerCall struct {
	Schema string `json:"schema"`
	Method string `json:"method"`
}

type BrokerReply struct {
	Schema string `json:"schema"`
	Status string `json:"status"`
}

type boundedOutput struct {
	buffer   bytes.Buffer
	overflow bool
}

func (o *boundedOutput) Bytes() []byte { return o.buffer.Bytes() }
func (o *boundedOutput) Len() int      { return o.buffer.Len() }

func (o *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > maxCommandBytes-o.Len() {
		o.overflow = true
		return 0, errors.New("command output limit exceeded")
	}
	return o.buffer.Write(p)
}

// RunEvent executes an independently authored WASI event handler. The only
// effect it can request is append of the core-provided reduced event.
func RunEvent(parent context.Context, selected Active, event Event) (CommandResult, error) {
	var result CommandResult
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	select {
	case wasiSlots <- struct{}{}:
		defer func() { <-wasiSlots }()
	case <-ctx.Done():
		return result, errors.New("plugin execution timed out or was cancelled")
	}
	if err := event.Validate(); err != nil {
		return result, err
	}
	m := selected.Package.Manifest
	if m.Capability != "event-handler" || m.Runtime.Kind != "wasi-command" || !slices.Equal(selected.Grants, []string{"event.file.append"}) {
		return result, errors.New("handler lacks supported broker grant")
	}
	var config FileLogConfig
	if err := validateConfig(m, selected.Config); err != nil {
		return result, errors.New("invalid trusted event-handler config")
	}
	if err := strictJSON(selected.Config, &config); err != nil {
		return result, errors.New("invalid trusted event-handler config")
	}
	current, retained, err := packageSnapshot(ctx, selected.Package.Path, map[string]bool{m.Runtime.Entry: true})
	if err != nil {
		if ctx.Err() != nil {
			return result, errors.New("plugin execution timed out or was cancelled")
		}
		return result, errors.New("selected package digest changed or invalid")
	}
	if current.SHA256 != selected.Package.SHA256 || !reflect.DeepEqual(current.Manifest, m) {
		return result, errors.New("selected package digest changed")
	}
	moduleBytes := retained[m.Runtime.Entry]
	if len(moduleBytes) == 0 {
		return result, errors.New("module entry missing")
	}
	input, err := json.Marshal(CommandRequest{Schema: WASIRequestSchema, Kind: "event.handle", Event: event})
	if err != nil || len(input) > maxCommandBytes {
		return result, errors.New("command input exceeds limit")
	}
	runtime := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigInterpreter().WithMemoryLimitPages(256).WithCloseOnContextDone(true))
	defer runtime.Close(ctx)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		return result, errors.New("WASI runtime initialization failed")
	}
	var brokerFailure error
	callCount := 0
	broker := runtime.NewHostModuleBuilder("p_broker_v1")
	broker.NewFunctionBuilder().WithFunc(
		func(callCtx context.Context, module api.Module, reqPtr, reqLen, replyPtr, replyCap uint32) int32 {
			callCount++
			if callCtx.Err() != nil {
				brokerFailure = errors.New("broker call cancelled")
				return -1
			}
			if callCount > maxBrokerCalls || reqLen == 0 || reqLen > maxCommandBytes || replyCap > maxCommandBytes {
				brokerFailure = errors.New("broker request exceeds limit")
				return -1
			}
			// wazero's Module.Memory can hold a typed nil when a module has
			// no memory. WASI commands export their linear memory as "memory";
			// ExportedMemory returns a real nil for a missing export.
			memory := module.ExportedMemory("memory")
			if memory == nil {
				brokerFailure = errors.New("broker request outside module memory")
				return -1
			}
			request, ok := memory.Read(reqPtr, reqLen)
			if !ok {
				brokerFailure = errors.New("broker request outside module memory")
				return -1
			}
			var call BrokerCall
			if err := strictJSON(request, &call); err != nil || call.Schema != BrokerSchema || call.Method != "event.file.append" {
				brokerFailure = errors.New("broker method refused")
				return -1
			}
			reply, _ := json.Marshal(BrokerReply{Schema: BrokerSchema, Status: "ok"})
			if _, ok := memory.Read(replyPtr, uint32(len(reply))); uint32(len(reply)) > replyCap || !ok {
				brokerFailure = errors.New("broker response outside module memory")
				return -1
			}
			// The current operation scope is event dispatch; the event and path
			// come only from core and trusted config, never from guest memory.
			line, _ := json.Marshal(event)
			if err := appendFile(config, append(line, '\n')); err != nil {
				brokerFailure = errors.New("broker append failed")
				return -1
			}
			memory.Write(replyPtr, reply)
			return int32(len(reply))
		}).Export("call")
	if _, err := broker.Instantiate(ctx); err != nil {
		return result, errors.New("broker initialization failed")
	}
	var output, diagnostic boundedOutput
	moduleConfig := wazero.NewModuleConfig().WithStdin(bytes.NewReader(append(input, '\n'))).WithStdout(&output).WithStderr(&diagnostic)
	_, err = runtime.InstantiateWithConfig(ctx, moduleBytes, moduleConfig)
	if brokerFailure != nil {
		return result, brokerFailure
	}
	if ctx.Err() != nil {
		return result, errors.New("plugin execution timed out or was cancelled")
	}
	if output.overflow || diagnostic.overflow {
		return result, errors.New("plugin output exceeds limit")
	}
	if err != nil {
		var exit *sys.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 0 {
			return result, errors.New("plugin execution failed")
		}
	}
	if err := strictJSON(bytes.TrimSpace(output.Bytes()), &result); err != nil || result.Schema != WASIResultSchema || (result.Status != "appended" && result.Status != "skipped") || len(result.Message) > 256 || (result.Status == "appended") != (callCount == 1) {
		return CommandResult{}, errors.New("invalid plugin result")
	}
	return result, nil
}
