//go:build wasip1

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
	_ = call(command.Scope, "git.repo.inspect")
	_ = call(command.Scope, "git.repo.set-head")
	fmt.Println(`{"schema":"p.command-result/v1","status":"ready"}`)
}
