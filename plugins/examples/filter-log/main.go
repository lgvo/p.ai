//go:build wasip1

// A standalone example: build with GOOS=wasip1 GOARCH=wasm. The output module
// and adjacent plugin.json form a flat P package without rebuilding P core.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"unsafe"
)

//go:wasmimport p_broker_v1 call
//go:noescape
func brokerCall(requestPtr, requestLen, replyPtr, replyCapacity uint32) int32

type event struct {
	Kind   string            `json:"kind"`
	Fields map[string]string `json:"fields"`
}

func main() {
	var request struct {
		Schema string `json:"schema"`
		Kind   string `json:"kind"`
		Event  event  `json:"event"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil || request.Schema != "p.command/v1" || request.Kind != "event.handle" {
		os.Exit(2)
	}
	status := "skipped"
	if request.Event.Kind == "session.condition_changed" && request.Event.Fields["condition"] == "ready" {
		call := []byte(`{"schema":"p.broker/v1","method":"event.file.append"}`)
		reply := make([]byte, 256)
		n := brokerCall(uint32(uintptr(unsafe.Pointer(&call[0]))), uint32(len(call)), uint32(uintptr(unsafe.Pointer(&reply[0]))), uint32(len(reply)))
		if n < 0 || n > int32(len(reply)) || string(reply[:n]) != `{"schema":"p.broker/v1","status":"ok"}` {
			os.Exit(3)
		}
		status = "appended"
	}
	fmt.Printf("{\"schema\":\"p.command-result/v1\",\"status\":%q}\n", status)
}
