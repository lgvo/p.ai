//go:build wasip1

// This standalone conformance fixture uses only stdlib and the public ABI.
// Stage plugin.json and git.wasm after building this package for wasip1/wasm.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"unsafe"
)

//go:wasmimport p_broker_v1 call
//go:noescape
func broker(request, length, reply, capacity uint32) int32

type command struct {
	Schema         string `json:"schema"`
	Kind           string `json:"kind"`
	Scope          string `json:"scope"`
	Project        string `json:"project"`
	Branch         string `json:"branch,omitempty"`
	InitialHead    string `json:"initial_head,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	Service        string `json:"service,omitempty"`
	OriginRef      string `json:"origin_ref,omitempty"`
	SourceRef      string `json:"source_ref,omitempty"`
	DestinationRef string `json:"destination_ref,omitempty"`
	CommitOID      string `json:"commit_oid,omitempty"`
	Ceilings       *struct {
		MaxInputBytes int64 `json:"max_input_bytes"`
		MaxDurationMS int64 `json:"max_duration_ms"`
	} `json:"ceilings,omitempty"`
}

type response struct {
	Schema string `json:"schema"`
	Status string `json:"status"`
	Exists *bool  `json:"exists,omitempty"`
	Head   string `json:"head,omitempty"`
	Refs   []struct {
		Ref string `json:"ref"`
		OID string `json:"oid"`
	} `json:"refs,omitempty"`
	Exhausted    *bool `json:"exhausted,omitempty"`
	PageComplete *bool `json:"page_complete,omitempty"`
}

func strict(data []byte, value any) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil {
		return false
	}
	var tail any
	return d.Decode(&tail) == io.EOF
}

func invoke(scope, method string, fields map[string]any) (response, bool) {
	fields["schema"], fields["method"], fields["scope"] = "p.broker/v1", method, scope
	request, err := json.Marshal(fields)
	var result response
	if err != nil {
		return result, false
	}
	reply := make([]byte, 4096)
	n := broker(uint32(uintptr(unsafe.Pointer(&request[0]))), uint32(len(request)), uint32(uintptr(unsafe.Pointer(&reply[0]))), uint32(len(reply)))
	if n < 0 || n > int32(len(reply)) {
		return result, false
	}
	valid := strict(reply[:n], &result) && result.Schema == "p.broker/v1" && result.Status == "ok"
	return result, valid
}

func execute(request command) bool {
	switch request.Kind {
	case "git.project.ensure":
		if request.InitialHead != "refs/heads/main" || request.Limit != 0 || request.Service != "" || request.Ceilings != nil {
			return false
		}
		state, ok := invoke(request.Scope, "git.repo.inspect", map[string]any{})
		if !ok || state.Exists == nil {
			return false
		}
		if *state.Exists {
			return state.Head == request.InitialHead
		}
		if _, ok = invoke(request.Scope, "git.repo.init", map[string]any{}); !ok {
			return false
		}
		_, ok = invoke(request.Scope, "git.repo.set-head", map[string]any{})
		return ok
	case "git.refs.list":
		if request.Limit < 1 || request.Limit > 8 || request.InitialHead != "" || request.Service != "" || request.Ceilings != nil {
			return false
		}
		for range request.Limit {
			page, ok := invoke(request.Scope, "git.refs.next", map[string]any{"page_size": 1})
			if !ok || page.PageComplete == nil || page.Exhausted == nil {
				return false
			}
			if *page.PageComplete {
				return true
			}
		}
		return false
	case "git.transport.plan":
		if request.InitialHead != "" || request.Limit != 0 || (request.Service != "upload" && request.Service != "receive") || request.Ceilings == nil || request.Ceilings.MaxInputBytes < 1 || request.Ceilings.MaxDurationMS < 1 {
			return false
		}
		fields := map[string]any{}
		if request.Service == "receive" {
			fields["max_input_bytes"] = min(request.Ceilings.MaxInputBytes, int64(64*1024))
		}
		_, ok := invoke(request.Scope, "git.transport.prepare", fields)
		return ok
	case "git.origin.observe":
		if request.OriginRef != "" || request.CommitOID != "" || request.InitialHead != "" || request.Limit != 0 || request.Service != "" || request.Ceilings != nil {
			return false
		}
		_, ok := invoke(request.Scope, "git.origin.observe", map[string]any{})
		return ok
	case "git.origin.fetch":
		if request.OriginRef == "" || request.CommitOID == "" || request.InitialHead != "" || request.Limit != 0 || request.Service != "" || request.Ceilings != nil {
			return false
		}
		_, ok := invoke(request.Scope, "git.origin.fetch", map[string]any{})
		return ok
	case "git.origin.publish":
		if request.SourceRef == "" || request.DestinationRef == "" || request.CommitOID == "" || request.OriginRef != "" || request.InitialHead != "" || request.Limit != 0 || request.Service != "" || request.Ceilings != nil {
			return false
		}
		_, ok := invoke(request.Scope, "git.origin.publish", map[string]any{})
		return ok
	case "git.branch.delete":
		if request.Branch == "" {
			return false
		}
		// Deliberately ignore an uncertain first broker error and ask again.
		// Core must enforce one attempted effect per selected invocation.
		_, first := invoke(request.Scope, "git.branch.delete", map[string]any{})
		_, second := invoke(request.Scope, "git.branch.delete", map[string]any{})
		return first && second
	}
	return false
}

func main() {
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
	var request command
	if err == nil && len(data) <= 4096 && strict(data, &request) && request.Schema == "p.command/v1" && request.Scope != "" && len(request.Scope) <= 64 && request.Project != "" && execute(request) {
		fmt.Println(`{"schema":"p.command-result/v1","status":"ready"}`)
		return
	}
	fmt.Println(`{"schema":"p.command-result/v1","status":"refused","message":"alternate source operation refused"}`)
}
