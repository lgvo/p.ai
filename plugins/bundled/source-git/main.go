//go:build wasip1

// Build with GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -o git.wasm
// ./plugins/bundled/source-git. Stage only plugin.json, git.wasm, and p-git-ssh.
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
	Schema   string   `json:"schema"`
	Kind     string   `json:"kind"`
	Scope    string   `json:"scope"`
	Project  string   `json:"project"`
	Service  string   `json:"service,omitempty"`
	Ceilings *budgets `json:"ceilings,omitempty"`
}

type budgets struct {
	MaxInputBytes int64 `json:"max_input_bytes"`
	MaxDurationMS int64 `json:"max_duration_ms"`
}

type brokerRequest struct {
	Schema string `json:"schema"`
	Method string `json:"method"`
	Scope  string `json:"scope"`
}

type brokerReply struct {
	Schema string `json:"schema"`
	Status string `json:"status"`
}

func decode(data []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func call(scope, method string) error {
	request, err := json.Marshal(brokerRequest{Schema: "p.broker/v1", Method: method, Scope: scope})
	if err != nil {
		return err
	}
	buffer := make([]byte, 4096)
	n := brokerCall(uint32(uintptr(unsafe.Pointer(&request[0]))), uint32(len(request)), uint32(uintptr(unsafe.Pointer(&buffer[0]))), uint32(len(buffer)))
	if n < 0 || n > int32(len(buffer)) {
		return errors.New("broker refused operation")
	}
	var reply brokerReply
	if err := decode(buffer[:n], &reply); err != nil || reply.Schema != "p.broker/v1" || reply.Status != "ok" {
		return errors.New("invalid broker reply")
	}
	return nil
}

func run() error {
	input, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
	if err != nil || len(input) > 4096 {
		return errors.New("invalid command input")
	}
	var request command
	if err := decode(input, &request); err != nil || request.Schema != "p.command/v1" || request.Scope == "" || len(request.Scope) > 64 || request.Project == "" {
		return errors.New("invalid source command")
	}
	switch request.Kind {
	case "git.transport.plan":
		if (request.Service != "upload" && request.Service != "receive") || request.Ceilings == nil || request.Ceilings.MaxInputBytes < 1 || request.Ceilings.MaxDurationMS < 1 {
			return errors.New("invalid transport request")
		}
		return call(request.Scope, "git.transport.prepare")
	default:
		return errors.New("unsupported source command")
	}
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
