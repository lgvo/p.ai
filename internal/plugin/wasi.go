package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/tetratelabs/wazero/api"
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
	release, leaseErr := LeasePackage(selected.Package.Path)
	if leaseErr != nil {
		return result, leaseErr
	}
	defer release()
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
	moduleBytes, unavailable := selectedModule(ctx, selected)
	switch {
	case unavailable == moduleUnreadable && ctx.Err() != nil:
		return result, errors.New("plugin execution timed out or was cancelled")
	case unavailable == moduleUnreadable:
		return result, errors.New("selected package digest changed or invalid")
	case unavailable == moduleChanged:
		return result, errors.New("selected package digest changed")
	case unavailable == moduleMissing:
		return result, errors.New("module entry missing")
	}
	input, err := json.Marshal(CommandRequest{Schema: WASIRequestSchema, Kind: "event.handle", Event: event})
	if err != nil || len(input) > maxCommandBytes {
		return result, errors.New("command input exceeds limit")
	}
	runtime, err := newSandbox(ctx)
	if err != nil {
		return result, errors.New("WASI runtime initialization failed")
	}
	defer runtime.Close(ctx)
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
			memory := guestMemory(module)
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
	output, failed := runModule(ctx, runtime, moduleBytes, input)
	if brokerFailure != nil {
		return result, brokerFailure
	}
	switch failed {
	case runTimedOut:
		return result, errors.New("plugin execution timed out or was cancelled")
	case runOutputExceeded:
		return result, errors.New("plugin output exceeds limit")
	case runFailed:
		return result, errors.New("plugin execution failed")
	}
	if err := strictJSON(bytes.TrimSpace(output), &result); err != nil || result.Schema != WASIResultSchema || (result.Status != "appended" && result.Status != "skipped") || len(result.Message) > 256 || (result.Status == "appended") != (callCount == 1) {
		return CommandResult{}, errors.New("invalid plugin result")
	}
	return result, nil
}
