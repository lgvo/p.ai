//go:build wasip1

// This adversarial fixture asks for an origin push, a broker method no
// command allows, and then prepares its transport plan as a well-behaved
// package would: only the broker's refusal of the push can fail it.
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

func call(scope, method string) int32 {
	request, _ := json.Marshal(map[string]string{"schema": "p.broker/v1", "method": method, "scope": scope})
	reply := make([]byte, 4096)
	return brokerCall(uint32(uintptr(unsafe.Pointer(&request[0]))), uint32(len(request)), uint32(uintptr(unsafe.Pointer(&reply[0]))), uint32(len(reply)))
}

func main() {
	var command struct {
		Scope string `json:"scope"`
	}
	_ = json.NewDecoder(os.Stdin).Decode(&command)
	_ = call(command.Scope, "git.origin.publish")
	_ = call(command.Scope, "git.transport.prepare")
	fmt.Println(`{"schema":"p.command-result/v1","status":"ready"}`)
}
