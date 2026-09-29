package main

import (
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAPIArgumentsUseStandardInstance(t *testing.T) {
	t.Setenv("P_SOCKET", "")
	socket, method, params, err := apiArguments([]string{"system.health"})
	if err != nil || socket != "/var/lib/p/control.sock" || method != "system.health" || string(params) != `{"v":1}` {
		t.Fatalf("default API arguments: %q %q %s %v", socket, method, params, err)
	}
}

func TestAPIArgumentsRejectMissingMethod(t *testing.T) {
	t.Setenv("P_SOCKET", "/tmp/configured.sock")
	for _, args := range [][]string{nil, {""}, {"/tmp/explicit.sock"}, {"", "system.health"}, {"/tmp/explicit.sock", ""}, {"/tmp/explicit.sock", "system.health", `{}`, "extra"}} {
		if socket, _, _, err := apiArguments(args); err == nil || socket != "" {
			t.Errorf("%v selected %q instead of refusing missing or extra arguments: %v", args, socket, err)
		}
	}
}

// Verify routing and framing through each command, including snapshot flags
// without a positional socket and legacy explicit arguments overriding P_SOCKET.
func TestClientCommandsUseConfiguredSocket(t *testing.T) {
	for _, test := range []struct {
		name, method string
		explicit     bool
		args         func(string) []string
	}{
		{name: "api", method: "system.health", args: func(string) []string { return []string{"api", "system.health"} }},
		{name: "api params", method: "system.health", args: func(string) []string { return []string{"api", "system.health", `{"v":1}`} }},
		{name: "api explicit", method: "system.health", explicit: true, args: func(s string) []string { return []string{"api", s, "system.health"} }},
		{name: "api explicit params", method: "system.health", explicit: true, args: func(s string) []string { return []string{"api", s, "system.health", `{"v":1}`} }},
		{name: "tui snapshot", method: "system.capabilities", args: func(string) []string { return []string{"tui", "--snapshot", "--width", "80", "--height", "24"} }},
		{name: "tui explicit", method: "system.capabilities", explicit: true, args: func(s string) []string { return []string{"tui", s, "--snapshot"} }},
		{name: "attach", method: "session.attach", args: func(string) []string { return []string{"attach", "fixture-session"} }},
		{name: "attach explicit", method: "session.attach", explicit: true, args: func(s string) []string { return []string{"attach", s, "fixture-session"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "control.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if test.explicit {
				t.Setenv("P_SOCKET", "invalid-environment-override")
			} else {
				t.Setenv("P_SOCKET", socket)
			}
			served := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					served <- err
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				var request struct {
					JSONRPC string          `json:"jsonrpc"`
					ID      int             `json:"id"`
					Method  string          `json:"method"`
					Params  json.RawMessage `json:"params"`
				}
				if err := json.NewDecoder(conn).Decode(&request); err != nil {
					served <- err
					return
				}
				if request.JSONRPC != "2.0" || request.Method != test.method {
					served <- fmt.Errorf("unexpected request: %+v", request)
					return
				}
				reply := map[string]any{"jsonrpc": "2.0", "id": request.ID}
				if test.method == "session.attach" {
					var params struct {
						V    int    `json:"v"`
						UUID string `json:"uuid"`
					}
					if err := json.Unmarshal(request.Params, &params); err != nil || params.V != 1 || params.UUID != "fixture-session" {
						served <- fmt.Errorf("wrong attach params: %s", request.Params)
						return
					}
					reply["error"] = map[string]any{"code": -32003, "kind": "busy", "message": "fixture refused attach"}
				} else {
					if string(request.Params) != `{"v":1}` {
						served <- fmt.Errorf("wrong params: %s", request.Params)
						return
					}
					reply["result"] = map[string]any{"v": 1, "available": []string{}}
				}
				served <- json.NewEncoder(conn).Encode(reply)
			}()
			err = run(test.args(socket))
			if test.method == "session.attach" {
				if err == nil || !strings.Contains(err.Error(), "fixture refused attach") {
					t.Fatalf("attach did not reach configured instance: %v", err)
				}
			} else if err != nil {
				t.Fatalf("command failed: %v", err)
			}
			select {
			case err := <-served:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("command did not reach configured socket")
			}
		})
	}
}

func TestClientCommandsRefuseInvalidOverride(t *testing.T) {
	t.Setenv("P_SOCKET", "relative.sock")
	for _, args := range [][]string{{"api", "system.health"}, {"tui", "--snapshot"}, {"attach", "fixture-session"}} {
		if err := run(args); err == nil || !strings.Contains(err.Error(), "absolute path") {
			t.Errorf("%v did not refuse invalid override: %v", args, err)
		}
	}
}
