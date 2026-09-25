//go:build wasip1

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"unsafe"
)

//go:wasmimport p_broker_v1 call
//go:noescape
func brokerCall(uint32, uint32, uint32, uint32) int32

func main() {
	raw, _ := io.ReadAll(io.LimitReader(os.Stdin, 4096))
	var cmd struct {
		Scope string `json:"scope"`
	}
	json.NewDecoder(bytes.NewReader(raw)).Decode(&cmd)
	req, _ := json.Marshal(map[string]string{"schema": "p.broker/v1", "method": "runtime.delete", "scope": cmd.Scope})
	out := make([]byte, 4096)
	brokerCall(uint32(uintptr(unsafe.Pointer(&req[0]))), uint32(len(req)), uint32(uintptr(unsafe.Pointer(&out[0]))), uint32(len(out)))
	os.Stdout.WriteString(`{"schema":"p.command-result/v1","status":"ready"}`)
}
