//go:build wasip1

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

//go:wasmimport p_broker_v1 call
//go:noescape
func brokerCall(requestPtr, requestLen, replyPtr, replyCapacity uint32) int32

func main() {
	var request struct {
		Event struct {
			Kind   string            `json:"kind"`
			Fields map[string]string `json:"fields"`
		} `json:"event"`
	}
	if json.NewDecoder(os.Stdin).Decode(&request) != nil {
		os.Exit(2)
	}
	switch request.Event.Kind {
	case "session.condition_changed":
		switch request.Event.Fields["condition"] {
		case "ready":
			_, fsErr := os.ReadFile("/etc/passwd")
			_, socketErr := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
			_, _, processErr := syscall.StartProcess("/bin/sh", []string{"/bin/sh", "-c", "true"}, &syscall.ProcAttr{})
			if fsErr == nil || !errors.Is(socketErr, syscall.ENOSYS) || !errors.Is(processErr, syscall.ENOSYS) || os.Getenv("P_PLUGIN_PROBE_SECRET") != "" {
				os.Exit(3)
			}
		case "creating":
			large := make([]byte, 128<<20)
			for i := 0; i < len(large); i += 65536 {
				large[i] = 1
			}
			fmt.Print(len(large))
		case "starting":
			call := []byte(`{"schema":"p.broker/v1","method":"event.file.append","extra":true}`)
			reply := make([]byte, 256)
			brokerCall(uint32(uintptr(unsafe.Pointer(&call[0]))), uint32(len(call)), uint32(uintptr(unsafe.Pointer(&reply[0]))), uint32(len(reply)))
		case "missing":
			call := []byte(`{"schema":"p.broker/v1","method":"event.file.append"}`)
			brokerCall(uint32(uintptr(unsafe.Pointer(&call[0]))), uint32(len(call)), ^uint32(0), 256)
		case "stopped":
			fmt.Print(`{"schema":"p.command-result/v1","status":"appended"}`)
			return
		}
	case "session.attachment_changed":
		call := []byte(`{"schema":"p.broker/v1","method":"runtime.incus"}`)
		reply := make([]byte, 256)
		brokerCall(uint32(uintptr(unsafe.Pointer(&call[0]))), uint32(len(call)), uint32(uintptr(unsafe.Pointer(&reply[0]))), uint32(len(reply)))
		if request.Event.Fields["count"] == "2" {
			brokerCall(uint32(uintptr(unsafe.Pointer(&call[0]))), uint32(len(call)), uint32(uintptr(unsafe.Pointer(&reply[0]))), uint32(len(reply)))
		}
	case "session.unattended_changed":
		brokerCall(^uint32(0), 100, 0, 256)
	case "session.policy_changed":
		if request.Event.Fields["policy_condition"] == "current" {
			fmt.Print(strings.Repeat("x", 5000))
		} else {
			fmt.Fprint(os.Stderr, strings.Repeat("x", 5000))
		}
	case "operation.progress":
		for {
		} // the host context must interrupt this guest computation
	}
	fmt.Print(`{"schema":"p.command-result/v1","status":"skipped"}`)
}
