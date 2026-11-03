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

const GitCommandSchema = "p.command/v1"
const GitResultSchema = "p.command-result/v1"
const GitBrokerSchema = "p.broker/v1"

type GitRef struct {
	Ref string `json:"ref"`
	OID string `json:"oid"`
}

type GitTransportPlan struct {
	MaxInputBytes int64 `json:"max_input_bytes"`
	MaxDurationMS int64 `json:"max_duration_ms"`
}

type GitCommand struct {
	Schema   string            `json:"schema"`
	Kind     string            `json:"kind"`
	Scope    string            `json:"scope"`
	Project  string            `json:"project"`
	Service  string            `json:"service,omitempty"`
	Ceilings *GitTransportPlan `json:"ceilings,omitempty"`
}

type GitResult struct {
	Schema  string `json:"schema"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type GitBrokerRequest struct {
	Schema        string `json:"schema"`
	Method        string `json:"method"`
	Scope         string `json:"scope"`
	MaxInputBytes *int64 `json:"max_input_bytes,omitempty"`
	MaxDurationMS *int64 `json:"max_duration_ms,omitempty"`
}

type GitBrokerResponse struct {
	Schema string `json:"schema"`
	Status string `json:"status"`
}

type GitOutcome struct {
	Plan GitTransportPlan
}

const GitCoreInputCeiling int64 = 1 << 30
const GitCoreDurationMS int64 = 600000

// RunSourceGit runs a package-authored operation sequence. The returned plan
// comes only from broker requests within core's ceilings, never from module
// output.
func RunSourceGit(parent context.Context, selected Active, command GitCommand) (GitOutcome, error) {
	var outcome GitOutcome
	release, leaseErr := LeasePackage(selected.Package.Path)
	if leaseErr != nil {
		return outcome, leaseErr
	}
	defer release()
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	select {
	case wasiSlots <- struct{}{}:
		defer func() { <-wasiSlots }()
	case <-ctx.Done():
		return outcome, errors.New("source-git execution timed out")
	}
	m := selected.Package.Manifest
	if m.Capability != "source-git" || m.Runtime.Kind != "wasi-command" || !slices.Equal(selected.Grants, []string{"git.project"}) {
		return outcome, errors.New("unsupported source-git capability")
	}
	if err := validateConfig(m, selected.Config); err != nil {
		return outcome, err
	}
	if command.Schema != "" || command.Scope != "" || command.Project == "" || len(command.Project) > 255 {
		return outcome, errors.New("invalid source-git command")
	}
	if command.Kind != "git.transport.plan" {
		return outcome, errors.New("unknown source-git method")
	}
	if (command.Service != "upload" && command.Service != "receive") || command.Ceilings == nil || command.Ceilings.MaxInputBytes <= 0 || command.Ceilings.MaxInputBytes > GitCoreInputCeiling || command.Ceilings.MaxDurationMS <= 0 || command.Ceilings.MaxDurationMS > GitCoreDurationMS {
		return outcome, errors.New("invalid transport request")
	}
	moduleBytes, unavailable := selectedModule(ctx, selected)
	switch unavailable {
	case moduleUnreadable, moduleChanged:
		return outcome, errors.New("source-git package changed")
	case moduleMissing:
		return outcome, errors.New("source-git module is missing")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return outcome, err
	}
	command.Schema, command.Scope = GitCommandSchema, hex.EncodeToString(nonce[:])
	input, err := json.Marshal(command)
	if err != nil || len(input) > maxCommandBytes {
		return outcome, errors.New("source-git command exceeds limit")
	}
	runtime, err := newSandbox(ctx)
	if err != nil {
		return outcome, errors.New("WASI initialization failed")
	}
	defer runtime.Close(ctx)
	var brokerFailure error
	callCount, prepared := 0, false
	plan := GitTransportPlan{MaxInputBytes: GitCoreInputCeiling, MaxDurationMS: GitCoreDurationMS}
	mod := runtime.NewHostModuleBuilder("p_broker_v1")
	mod.NewFunctionBuilder().WithFunc(func(callCtx context.Context, module api.Module, reqPtr, reqLen, replyPtr, replyCap uint32) int32 {
		callCount++
		if callCtx.Err() != nil || callCount > 16 || reqLen == 0 || reqLen > maxCommandBytes || replyCap > maxCommandBytes {
			brokerFailure = errors.New("source-git broker bounds exceeded")
			return -1
		}
		memory := guestMemory(module)
		if memory == nil {
			brokerFailure = errors.New("source-git memory missing")
			return -1
		}
		raw, ok := memory.Read(reqPtr, reqLen)
		if !ok {
			brokerFailure = errors.New("source-git broker pointer invalid")
			return -1
		}
		var req GitBrokerRequest
		if strictJSON(raw, &req) != nil || req.Schema != GitBrokerSchema || req.Scope != command.Scope {
			brokerFailure = errors.New("source-git broker request refused")
			return -1
		}
		reply := GitBrokerResponse{Schema: GitBrokerSchema, Status: "ok"}
		switch req.Method {
		case "git.transport.prepare":
			if prepared || req.MaxInputBytes != nil && (*req.MaxInputBytes <= 0 || *req.MaxInputBytes > command.Ceilings.MaxInputBytes) || req.MaxDurationMS != nil && (*req.MaxDurationMS <= 0 || *req.MaxDurationMS > command.Ceilings.MaxDurationMS) {
				brokerFailure = errors.New("transport plan refused")
				return -1
			}
			if req.MaxInputBytes != nil {
				plan.MaxInputBytes = *req.MaxInputBytes
			}
			if req.MaxDurationMS != nil {
				plan.MaxDurationMS = *req.MaxDurationMS
			}
			prepared = true
		default:
			brokerFailure = errors.New("unknown source-git broker method")
			return -1
		}
		encoded, _ := json.Marshal(reply)
		if uint32(len(encoded)) > replyCap {
			brokerFailure = errors.New("source-git broker reply exceeds capacity")
			return -1
		}
		if _, ok := memory.Read(replyPtr, uint32(len(encoded))); !ok {
			brokerFailure = errors.New("source-git reply pointer invalid")
			return -1
		}
		memory.Write(replyPtr, encoded)
		return int32(len(encoded))
	}).Export("call")
	if _, err := mod.Instantiate(ctx); err != nil {
		return outcome, errors.New("source-git broker initialization failed")
	}
	output, failed := runModule(ctx, runtime, moduleBytes, input)
	if brokerFailure != nil {
		return outcome, brokerFailure
	}
	switch failed {
	case runTimedOut:
		return outcome, errors.New("source-git execution timed out")
	case runOutputExceeded:
		return outcome, errors.New("source-git output exceeded limit")
	case runFailed:
		return outcome, errors.New("source-git execution failed")
	}
	var result GitResult
	if strictJSON(bytes.TrimSpace(output), &result) != nil || result.Schema != GitResultSchema || len(result.Message) > 256 {
		return outcome, errors.New("invalid source-git result")
	}
	if result.Status == "refused" {
		return outcome, errors.New("source-git operation refused")
	}
	if result.Status != "ready" {
		return outcome, errors.New("invalid source-git status")
	}
	if !prepared {
		return outcome, errors.New("source-git transport plan missing")
	}
	outcome.Plan = plan
	return outcome, nil
}
