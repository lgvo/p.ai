//go:build wasip1

// Build: GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -o
// plugins/bundled/runtime-incus/runtime.wasm ./plugins/bundled/runtime-incus
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"unsafe"
)

//go:wasmimport p_broker_v1 call
//go:noescape
func brokerCall(requestPtr, requestLen, replyPtr, replyCapacity uint32) int32

type command struct {
	Schema string `json:"schema"`
	Kind   string `json:"kind"`
	Scope  string `json:"scope"`
}
type request struct {
	Schema string `json:"schema"`
	Method string `json:"method"`
	Scope  string `json:"scope"`
}
type reply struct {
	Schema              string `json:"schema"`
	Status              string `json:"status"`
	Exists              bool   `json:"exists"`
	State               string `json:"state,omitempty"`
	HostUnit            string `json:"host_unit,omitempty"`
	HostReady           bool   `json:"host_ready,omitempty"`
	Diagnostic          string `json:"diagnostic,omitempty"`
	DiagnosticAvailable bool   `json:"diagnostic_available,omitempty"`
}

func strict(data []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func call(scope, method string) (reply, error) {
	var result reply
	raw, _ := json.Marshal(request{Schema: "p.broker/v1", Method: method, Scope: scope})
	buffer := make([]byte, 4096)
	n := brokerCall(uint32(uintptr(unsafe.Pointer(&raw[0]))), uint32(len(raw)), uint32(uintptr(unsafe.Pointer(&buffer[0]))), uint32(len(buffer)))
	if n < 0 || n > int32(len(buffer)) {
		return result, errors.New("runtime broker refused")
	}
	if err := strict(buffer[:n], &result); err != nil || result.Schema != "p.broker/v1" || result.Status != "ok" {
		return result, errors.New("invalid runtime broker reply")
	}
	return result, nil
}

func run() error {
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
	if err != nil || len(raw) > 4096 {
		return errors.New("runtime command input exceeds limit")
	}
	var cmd command
	if err := strict(raw, &cmd); err != nil || cmd.Schema != "p.command/v1" || cmd.Scope == "" || len(cmd.Scope) > 64 {
		return errors.New("invalid runtime command")
	}
	before, err := call(cmd.Scope, "runtime.inspect")
	if err != nil {
		return err
	}
	switch cmd.Kind {
	case "runtime.inspect":
		return nil
	case "runtime.create":
		if before.Exists && before.State != "Stopped" {
			return errors.New("existing runtime must be stopped before create repair")
		}
	case "runtime.start":
		if !before.Exists {
			return errors.New("runtime missing")
		}
		if before.State == "Running" {
			return nil
		}
	case "runtime.stop":
		if !before.Exists {
			return errors.New("runtime missing")
		}
		if before.State == "Stopped" {
			return nil
		}
	case "runtime.delete":
		if !before.Exists {
			return nil
		}
	case "runtime.assemble":
		if !before.Exists || before.State != "Stopped" {
			return errors.New("assembly requires stopped instance")
		}
	case "runtime.observe-host", "runtime.attach":
		// Native broker obtains systemd state and bounded diagnostics.
	default:
		return errors.New("unknown runtime command")
	}
	_, err = call(cmd.Scope, cmd.Kind)
	return err
}

func main() {
	if err := run(); err != nil {
		message := err.Error()
		if len(message) > 256 {
			message = message[:256]
		}
		result, _ := json.Marshal(struct {
			Schema  string `json:"schema"`
			Status  string `json:"status"`
			Message string `json:"message"`
		}{"p.command-result/v1", "refused", message})
		fmt.Println(string(result))
		return
	}
	fmt.Println(`{"schema":"p.command-result/v1","status":"ready"}`)
}
