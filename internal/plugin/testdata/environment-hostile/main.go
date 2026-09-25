//go:build wasip1

package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

//go:wasmimport p_broker_v1 call
//go:noescape
func broker(requestPtr, requestLen, replyPtr, replyCapacity uint32) int32

var mode = "wrong-scope"

func main() {
	raw, _ := io.ReadAll(os.Stdin)
	var cmd struct {
		Kind  string `json:"kind"`
		Scope string `json:"scope"`
	}
	_ = json.Unmarshal(raw, &cmd)
	method, scope := cmd.Kind, cmd.Scope
	if mode == "wrong-scope" {
		scope = "foreign"
	}
	if mode == "wrong-kind" {
		method = "environment.realize"
	}
	request := map[string]string{"schema": "p.broker/v1", "method": method, "scope": scope}
	if mode == "extra-field" {
		request["path"] = "/tmp/host"
	}
	if mode == "no-call" {
		_, _ = os.Stdout.WriteString(`{"schema":"p.command-result/v1","status":"ready"}`)
		return
	}
	if mode == "ambient" {
		_, fileErr := os.ReadFile("/etc/passwd")
		_, dirErr := os.ReadDir("/")
		_, socketErr := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
		_, _, processErr := syscall.StartProcess("/bin/sh", []string{"/bin/sh", "-c", "true"}, &syscall.ProcAttr{})
		if fileErr == nil || dirErr == nil || !errors.Is(socketErr, syscall.ENOSYS) || !errors.Is(processErr, syscall.ENOSYS) || os.Getenv("P_ENV_WASI_SECRET") != "" || len(os.Args) > 1 {
			os.Exit(3)
		}
	}
	encoded, _ := json.Marshal(request)
	reply := make([]byte, 4096)
	call := func() {
		_ = broker(uint32(uintptr(unsafe.Pointer(&encoded[0]))), uint32(len(encoded)), uint32(uintptr(unsafe.Pointer(&reply[0]))), uint32(len(reply)))
	}
	if mode == "bad-pointer" {
		_ = broker(^uint32(0), 128, 0, 256)
		_, _ = os.Stdout.WriteString(`{"schema":"p.command-result/v1","status":"ready"}`)
		return
	}
	call()
	if mode == "repeat" {
		call()
	}
	if mode == "refuse-after" {
		_, _ = os.Stdout.WriteString(`{"schema":"p.command-result/v1","status":"refused"}`)
		return
	}
	if mode == "extra-result" {
		_, _ = os.Stdout.WriteString(`{"schema":"p.command-result/v1","status":"ready","path":"/nix/store/forged"}`)
		return
	}
	if mode == "oversize-result" {
		_, _ = os.Stdout.WriteString(strings.Repeat("x", 4097))
		return
	}
	_, _ = os.Stdout.WriteString(`{"schema":"p.command-result/v1","status":"ready"}`)
}
