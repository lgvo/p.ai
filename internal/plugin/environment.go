package plugin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/tetratelabs/wazero/api"
)

const EnvironmentCommandSchema = "p.command/v1"
const EnvironmentBrokerSchema = "p.broker/v1"

var environmentSlots = make(chan struct{}, 2)

// EnvironmentBroker is an operation-scoped, core-owned effect. Each method
// has no arguments: builder identity, system, source and selection are bound
// before the WASI command starts. An implementation keeps native values
// pending until RunEnvironment returns successfully.
type EnvironmentBroker interface {
	Resolve(context.Context) error
	Realize(context.Context) error
}

type environmentCommand struct {
	Schema string `json:"schema"`
	Kind   string `json:"kind"`
	Scope  string `json:"scope"`
}
type environmentCall struct {
	Schema string `json:"schema"`
	Method string `json:"method"`
	Scope  string `json:"scope"`
}
type environmentResult struct {
	Schema string `json:"schema"`
	Status string `json:"status"`
}

// RunEnvironment accepts only a ready/refused policy decision around one
// native broker effect. It returns no Nix selection, path or activation data;
// those remain in the core-owned adapter after a valid ready result.
func RunEnvironment(parent context.Context, selected Active, kind string, broker EnvironmentBroker) error {
	release, leaseErr := LeasePackage(selected.Package.Path)
	if leaseErr != nil {
		return leaseErr
	}
	defer release()
	ctx, cancel := context.WithTimeout(parent, 7*time.Minute)
	defer cancel()
	select {
	case environmentSlots <- struct{}{}:
		defer func() { <-environmentSlots }()
	case <-ctx.Done():
		return errors.New("environment WASI queue timed out")
	}
	m := selected.Package.Manifest
	if m.Capability != "environment" || m.Placement != "host" || m.Runtime.Kind != "wasi-command" || !slices.Equal(selected.Grants, []string{"environment.nix"}) || broker == nil {
		return errors.New("unsupported environment capability")
	}
	if err := validateConfig(m, selected.Config); err != nil {
		return err
	}
	if kind != "environment.resolve" && kind != "environment.realize" {
		return errors.New("unknown environment command")
	}
	moduleBytes, unavailable := selectedModule(ctx, selected)
	switch unavailable {
	case moduleUnreadable, moduleChanged:
		return errors.New("environment package changed")
	case moduleMissing:
		return errors.New("environment module missing")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	command := environmentCommand{Schema: EnvironmentCommandSchema, Kind: kind, Scope: hex.EncodeToString(nonce[:])}
	input, err := json.Marshal(command)
	if err != nil || len(input) > maxCommandBytes {
		return errors.New("environment command exceeds limit")
	}
	rt, err := newSandbox(ctx)
	if err != nil {
		return errors.New("environment WASI initialization failed")
	}
	defer rt.Close(ctx)
	calls := 0
	var brokerFailure error
	host := rt.NewHostModuleBuilder("p_broker_v1")
	host.NewFunctionBuilder().WithFunc(func(callCtx context.Context, module api.Module, reqPtr, reqLen, replyPtr, replyCap uint32) int32 {
		calls++
		if callCtx.Err() != nil || calls > 1 || reqLen == 0 || reqLen > maxCommandBytes || replyCap > maxCommandBytes {
			brokerFailure = errors.New("environment broker bounds exceeded")
			return -1
		}
		memory := guestMemory(module)
		if memory == nil {
			brokerFailure = errors.New("environment module memory missing")
			return -1
		}
		raw, ok := memory.Read(reqPtr, reqLen)
		if !ok {
			brokerFailure = errors.New("environment broker request pointer invalid")
			return -1
		}
		var request environmentCall
		if strictJSON(raw, &request) != nil || request.Schema != EnvironmentBrokerSchema || request.Scope != command.Scope || request.Method != kind {
			brokerFailure = errors.New("environment broker request refused")
			return -1
		}
		if kind == "environment.resolve" {
			brokerFailure = broker.Resolve(callCtx)
		} else {
			brokerFailure = broker.Realize(callCtx)
		}
		if brokerFailure != nil {
			return -1
		}
		reply := []byte(`{"schema":"p.broker/v1","status":"ok"}`)
		if uint32(len(reply)) > replyCap || !memory.Write(replyPtr, reply) {
			brokerFailure = errors.New("environment broker reply pointer invalid")
			return -1
		}
		return int32(len(reply))
	}).Export("call")
	if _, err := host.Instantiate(ctx); err != nil {
		return errors.New("environment broker initialization failed")
	}
	output, failed := runModule(ctx, rt, moduleBytes, input)
	if brokerFailure != nil {
		return brokerFailure
	}
	switch failed {
	case runTimedOut:
		return errors.New("environment WASI timed out; native state requires inspection")
	case runOutputExceeded:
		return errors.New("environment WASI output exceeded limit")
	case runFailed:
		return errors.New("environment WASI execution failed")
	}
	var result environmentResult
	if strictJSON(bytes.TrimSpace(output), &result) != nil || result.Schema != WASIResultSchema {
		return errors.New("environment WASI result invalid")
	}
	if result.Status == "refused" {
		return errors.New("environment WASI refused")
	}
	if result.Status != "ready" || calls != 1 {
		return errors.New("environment WASI skipped required broker operation")
	}
	return nil
}
