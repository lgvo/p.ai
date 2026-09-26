package plugin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

const RuntimeCommandSchema = "p.command/v1"
const RuntimeBrokerSchema = "p.broker/v1"

var runtimeSlots = make(chan struct{}, 2)

// RuntimeState is a native backend observation. Module output never supplies
// this value; the broker reads it from Incus after every native operation.
// AttachSpec contains only daemon-selected fixed execution data. Socket is
// available solely on the private helper connection, never in a public spec.
type AttachSpec struct {
	Project  string   `json:"project"`
	Instance string   `json:"instance"`
	Argv     []string `json:"argv"`
}
type RuntimeAttachmentBroker interface {
	Attach(context.Context) (RuntimeState, error)
}

type RuntimeState struct {
	AttachSpec          *AttachSpec `json:"-"`
	Exists              bool        `json:"exists"`
	Status              string      `json:"status,omitempty"`
	HostUnit            string      `json:"host_unit,omitempty"`
	HostReady           bool        `json:"host_ready,omitempty"`
	Diagnostic          string      `json:"diagnostic,omitempty"`
	DiagnosticAvailable bool        `json:"diagnostic_available,omitempty"`
}

// RuntimeBroker is already bound by core to one session and one configured
// Incus project. A plugin cannot supply a resource name or native argument.
type RuntimeBroker interface {
	Inspect(context.Context) (RuntimeState, error)
	Create(context.Context) (RuntimeState, error)
	Start(context.Context) (RuntimeState, error)
	Stop(context.Context) (RuntimeState, error)
	Delete(context.Context) (RuntimeState, error)
	Assemble(context.Context) (RuntimeState, error)
	ObserveHost(context.Context) (RuntimeState, error)
}

type runtimeCreatedInspector interface {
	InspectCreated(context.Context) (RuntimeState, error)
}

type RuntimeCommand struct {
	Schema string `json:"schema"`
	Kind   string `json:"kind"`
	Scope  string `json:"scope"`
}

type RuntimeBrokerCall struct {
	Schema string `json:"schema"`
	Method string `json:"method"`
	Scope  string `json:"scope"`
}

type RuntimeBrokerReply struct {
	Schema              string `json:"schema"`
	Status              string `json:"status"`
	Exists              bool   `json:"exists"`
	State               string `json:"state,omitempty"`
	HostUnit            string `json:"host_unit,omitempty"`
	HostReady           bool   `json:"host_ready,omitempty"`
	Diagnostic          string `json:"diagnostic,omitempty"`
	DiagnosticAvailable bool   `json:"diagnostic_available,omitempty"`
}

// RunRuntime executes a replaceable WASI runtime policy. The effect surface is
// closed over the core-selected session; native Incus remains authoritative.
func RunRuntime(parent context.Context, selected Active, kind string, broker RuntimeBroker) (RuntimeState, error) {
	var zero RuntimeState
	ctx, cancel := context.WithTimeout(parent, 120*time.Second)
	defer cancel()
	select {
	case runtimeSlots <- struct{}{}:
		defer func() { <-runtimeSlots }()
	case <-ctx.Done():
		return zero, errors.New("runtime plugin timed out")
	}
	m := selected.Package.Manifest
	if m.Capability != "runtime" || m.Runtime.Kind != "wasi-command" || !slices.Equal(selected.Grants, []string{"runtime.incus"}) || broker == nil {
		return zero, errors.New("unsupported runtime capability")
	}
	if err := validateConfig(m, selected.Config); err != nil {
		return zero, err
	}
	switch kind {
	case "runtime.inspect", "runtime.create", "runtime.start", "runtime.stop", "runtime.delete", "runtime.assemble", "runtime.observe-host", "runtime.attach":
	default:
		return zero, errors.New("unknown runtime command")
	}
	current, retained, err := packageSnapshot(ctx, selected.Package.Path, map[string]bool{m.Runtime.Entry: true})
	if err != nil || current.SHA256 != selected.Package.SHA256 || !reflect.DeepEqual(current.Manifest, m) {
		return zero, errors.New("runtime package changed")
	}
	moduleBytes := retained[m.Runtime.Entry]
	if len(moduleBytes) == 0 {
		return zero, errors.New("runtime module missing")
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return zero, err
	}
	cmd := RuntimeCommand{Schema: RuntimeCommandSchema, Kind: kind, Scope: hex.EncodeToString(nonce[:])}
	input, err := json.Marshal(cmd)
	if err != nil || len(input) > maxCommandBytes {
		return zero, errors.New("runtime command exceeds limit")
	}
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigInterpreter().WithMemoryLimitPages(256).WithCloseOnContextDone(true))
	defer rt.Close(ctx)
	if _, err = wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		return zero, errors.New("runtime WASI initialization failed")
	}
	calls, mutations, hostReads := 0, 0, 0
	inspected := false
	var brokerFailure error
	host := rt.NewHostModuleBuilder("p_broker_v1")
	host.NewFunctionBuilder().WithFunc(func(callCtx context.Context, module api.Module, reqPtr, reqLen, replyPtr, replyCap uint32) int32 {
		calls++
		if callCtx.Err() != nil || calls > 4 || reqLen == 0 || reqLen > maxCommandBytes || replyCap > maxCommandBytes {
			brokerFailure = errors.New("runtime broker bounds exceeded")
			return -1
		}
		memory := module.ExportedMemory("memory")
		if memory == nil {
			brokerFailure = errors.New("runtime module memory missing")
			return -1
		}
		raw, ok := memory.Read(reqPtr, reqLen)
		if !ok {
			brokerFailure = errors.New("runtime broker pointer invalid")
			return -1
		}
		var request RuntimeBrokerCall
		if strictJSON(raw, &request) != nil || request.Schema != RuntimeBrokerSchema || request.Scope != cmd.Scope {
			brokerFailure = errors.New("runtime broker request refused")
			return -1
		}
		if request.Method != "runtime.inspect" && request.Method != "runtime.observe-host" && (request.Method != kind || mutations > 0 || !inspected) {
			brokerFailure = errors.New("runtime broker method refused")
			return -1
		}
		var state RuntimeState
		var e error
		switch request.Method {
		case "runtime.inspect":
			inspected = true
			if kind == "runtime.create" {
				if pending, ok := broker.(runtimeCreatedInspector); ok {
					state, e = pending.InspectCreated(callCtx)
				} else {
					state, e = broker.Inspect(callCtx)
				}
			} else {
				state, e = broker.Inspect(callCtx)
			}
		case "runtime.create":
			mutations++
			state, e = broker.Create(callCtx)
		case "runtime.start":
			mutations++
			state, e = broker.Start(callCtx)
		case "runtime.stop":
			mutations++
			state, e = broker.Stop(callCtx)
		case "runtime.delete":
			mutations++
			state, e = broker.Delete(callCtx)
		case "runtime.assemble":
			mutations++
			state, e = broker.Assemble(callCtx)
		case "runtime.attach":
			mutations++
			attachment, ok := broker.(RuntimeAttachmentBroker)
			if !ok {
				brokerFailure = errors.New("runtime attachment unavailable")
				return -1
			}
			state, e = attachment.Attach(callCtx)
		case "runtime.observe-host":
			hostReads++
			state, e = broker.ObserveHost(callCtx)
		default:
			brokerFailure = errors.New("runtime broker method unknown")
			return -1
		}
		if e != nil {
			brokerFailure = e
			return -1
		}
		encoded, _ := json.Marshal(RuntimeBrokerReply{Schema: RuntimeBrokerSchema, Status: "ok", Exists: state.Exists, State: state.Status, HostUnit: state.HostUnit, HostReady: state.HostReady, Diagnostic: state.Diagnostic, DiagnosticAvailable: state.DiagnosticAvailable})
		if uint32(len(encoded)) > replyCap {
			brokerFailure = errors.New("runtime broker reply too large")
			return -1
		}
		if _, ok := memory.Read(replyPtr, uint32(len(encoded))); !ok {
			brokerFailure = errors.New("runtime reply pointer invalid")
			return -1
		}
		memory.Write(replyPtr, encoded)
		return int32(len(encoded))
	}).Export("call")
	if _, err = host.Instantiate(ctx); err != nil {
		return zero, errors.New("runtime broker initialization failed")
	}
	var output, diagnostic boundedOutput
	_, err = rt.InstantiateWithConfig(ctx, moduleBytes, wazero.NewModuleConfig().WithStdin(bytes.NewReader(append(input, '\n'))).WithStdout(&output).WithStderr(&diagnostic))
	if brokerFailure != nil {
		return zero, brokerFailure
	}
	if ctx.Err() != nil {
		return zero, errors.New("runtime plugin timed out; native state must be re-inspected")
	}
	if output.overflow || diagnostic.overflow {
		return zero, errors.New("runtime plugin output exceeded limit")
	}
	if err != nil {
		var exit *sys.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 0 {
			return zero, errors.New("runtime plugin execution failed")
		}
	}
	var result CommandResult
	if strictJSON(bytes.TrimSpace(output.Bytes()), &result) != nil || result.Schema != WASIResultSchema || len(result.Message) > 256 {
		return zero, errors.New("runtime plugin result invalid")
	}
	if result.Status == "refused" {
		return zero, fmt.Errorf("runtime plugin refused: %s", result.Message)
	}
	if result.Status != "ready" || result.Message != "" {
		return zero, errors.New("runtime plugin result invalid")
	}
	if calls == 0 || kind != "runtime.observe-host" && !inspected || mutations > 1 || kind == "runtime.inspect" && mutations != 0 || (kind == "runtime.assemble" || kind == "runtime.attach") && mutations != 1 || kind == "runtime.observe-host" && (hostReads == 0 || mutations != 0) {
		return zero, errors.New("runtime plugin skipped required observation or operation")
	}
	// Always ask Incus again. A plugin cannot synthesize success or state.
	currentState, e := broker.Inspect(ctx)
	if e != nil {
		return zero, e
	}
	if kind == "runtime.observe-host" {
		return broker.ObserveHost(ctx)
	}
	if kind == "runtime.attach" {
		return broker.(RuntimeAttachmentBroker).Attach(ctx)
	}
	switch kind {
	case "runtime.create":
		if !currentState.Exists {
			return zero, errors.New("runtime create postcondition failed")
		}
	case "runtime.start":
		if !currentState.Exists || currentState.Status != "Running" {
			return zero, errors.New("runtime start postcondition failed")
		}
	case "runtime.stop":
		if !currentState.Exists || currentState.Status != "Stopped" {
			return zero, errors.New("runtime stop postcondition failed")
		}
	case "runtime.delete":
		if currentState.Exists {
			return zero, errors.New("runtime delete postcondition failed")
		}
	case "runtime.assemble":
		if !currentState.Exists || currentState.Status != "Stopped" {
			return zero, errors.New("runtime assemble postcondition failed")
		}
	}
	return currentState, nil
}
