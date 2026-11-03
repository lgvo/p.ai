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
	Schema   string `json:"schema"`
	Kind     string `json:"kind"`
	Scope    string `json:"scope"`
	Project  string `json:"project"`
	Service  string `json:"service,omitempty"`
	Ceilings *struct {
		MaxInputBytes int64 `json:"max_input_bytes"`
		MaxDurationMS int64 `json:"max_duration_ms"`
	} `json:"ceilings,omitempty"`
}

type response struct {
	Schema string `json:"schema"`
	Status string `json:"status"`
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
	case "git.transport.plan":
		if (request.Service != "upload" && request.Service != "receive") || request.Ceilings == nil || request.Ceilings.MaxInputBytes < 1 || request.Ceilings.MaxDurationMS < 1 {
			return false
		}
		fields := map[string]any{}
		if request.Service == "receive" {
			fields["max_input_bytes"] = min(request.Ceilings.MaxInputBytes, int64(64*1024))
		}
		_, ok := invoke(request.Scope, "git.transport.prepare", fields)
		return ok
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
