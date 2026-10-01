//go:build wasip1

// Build: GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -o
// plugins/bundled/environment-nix/environment.wasm ./plugins/bundled/environment-nix
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
func broker(requestPtr, requestLen, replyPtr, replyCapacity uint32) int32

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
	Schema string `json:"schema"`
	Status string `json:"status"`
}

func strict(raw []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func run() error {
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
	if err != nil || len(raw) > 4096 {
		return errors.New("environment command exceeds limit")
	}
	var cmd command
	if strict(raw, &cmd) != nil || cmd.Schema != "p.command/v1" || len(cmd.Scope) != 32 || cmd.Kind != "environment.resolve" && cmd.Kind != "environment.realize" {
		return errors.New("invalid environment command")
	}
	call, _ := json.Marshal(request{Schema: "p.broker/v1", Method: cmd.Kind, Scope: cmd.Scope})
	response := make([]byte, 4096)
	n := broker(uint32(uintptr(unsafe.Pointer(&call[0]))), uint32(len(call)), uint32(uintptr(unsafe.Pointer(&response[0]))), uint32(len(response)))
	if n < 0 || n > int32(len(response)) {
		return errors.New("environment broker refused")
	}
	var accepted reply
	if strict(response[:n], &accepted) != nil || accepted.Schema != "p.broker/v1" || accepted.Status != "ok" {
		return errors.New("invalid environment broker reply")
	}
	_, err = fmt.Fprintln(os.Stdout, `{"schema":"p.command-result/v1","status":"ready"}`)
	return err
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
