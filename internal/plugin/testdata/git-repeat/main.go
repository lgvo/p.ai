//go:build wasip1

// This adversarial fixture attempts two effects even after an uncertain reply.
// It is separate from the alternate adapter's unsupported-method conformance.
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
	Schema    string `json:"schema"`
	Kind      string `json:"kind"`
	Scope     string `json:"scope"`
	Project   string `json:"project"`
	Branch    string `json:"branch"`
	CommitOID string `json:"commit_oid"`
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

func invoke(scope, method string) bool {
	request, err := json.Marshal(map[string]string{"schema": "p.broker/v1", "method": method, "scope": scope})
	if err != nil {
		return false
	}
	reply := make([]byte, 4096)
	n := broker(uint32(uintptr(unsafe.Pointer(&request[0]))), uint32(len(request)), uint32(uintptr(unsafe.Pointer(&reply[0]))), uint32(len(reply)))
	if n < 0 || n > int32(len(reply)) {
		return false
	}
	var result struct {
		Schema string `json:"schema"`
		Status string `json:"status"`
	}
	return strict(reply[:n], &result) && result.Schema == "p.broker/v1" && result.Status == "ok"
}

func main() {
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
	var request command
	if err == nil && len(data) <= 4096 && strict(data, &request) && request.Schema == "p.command/v1" &&
		request.Scope != "" && len(request.Scope) <= 64 && request.Project != "" &&
		request.Branch != "" && request.CommitOID != "" &&
		(request.Kind == "git.branch.create" || request.Kind == "git.branch.delete") {
		first := invoke(request.Scope, request.Kind)
		second := invoke(request.Scope, request.Kind)
		if first && second {
			fmt.Println(`{"schema":"p.command-result/v1","status":"ready"}`)
			return
		}
	}
	fmt.Println(`{"schema":"p.command-result/v1","status":"refused","message":"repeated source effect refused"}`)
}
