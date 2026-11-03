package main

import (
	"encoding/json"
	"fmt"
	"github.com/lgvo/p.ai/internal/api"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAPIArgumentsUseStandardInstance(t *testing.T) {
	t.Setenv("P_SOCKET", "")
	socket, method, params, err := apiArguments([]string{"system.health"})
	if err != nil || socket != "/var/lib/p/control.sock" || method != "system.health" || string(params) != `{}` {
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

// fixtureSession is the session UUID the attach commands name.
const fixtureSession = "00000000-0000-4000-8000-000000000001"

// Verify routing and framing through each command, including snapshot flags
// without a positional socket and legacy explicit arguments overriding P_SOCKET.
func TestClientCommandsUseConfiguredSocket(t *testing.T) {
	for _, test := range []struct {
		name, method string
		explicit     bool
		args         func(string) []string
	}{
		{name: "api", method: "system.health", args: func(string) []string { return []string{"api", "system.health"} }},
		{name: "api params", method: "system.health", args: func(string) []string { return []string{"api", "system.health", `{}`} }},
		{name: "api explicit", method: "system.health", explicit: true, args: func(s string) []string { return []string{"api", s, "system.health"} }},
		{name: "api explicit params", method: "system.health", explicit: true, args: func(s string) []string { return []string{"api", s, "system.health", `{}`} }},
		{name: "tui snapshot", method: "system.hello", args: func(string) []string { return []string{"tui", "--snapshot", "--width", "80", "--height", "24"} }},
		{name: "tui explicit", method: "system.hello", explicit: true, args: func(s string) []string { return []string{"tui", s, "--snapshot"} }},
		{name: "attach", method: "session.attach", args: func(string) []string { return []string{"attach", fixtureSession} }},
		{name: "attach explicit", method: "session.attach", explicit: true, args: func(s string) []string { return []string{"attach", s, fixtureSession} }},
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
				decoder, encoder := json.NewDecoder(conn), json.NewEncoder(conn)
				// Every connection agrees the protocol first; the snapshot then
				// asks for the hello again to read the capabilities.
				for agreed := false; ; agreed = true {
					var request struct {
						JSONRPC string          `json:"jsonrpc"`
						ID      int             `json:"id"`
						Method  string          `json:"method"`
						Params  json.RawMessage `json:"params"`
					}
					if err := decoder.Decode(&request); err != nil {
						served <- err
						return
					}
					if request.JSONRPC != "2.0" || !agreed && request.Method != "system.hello" || agreed && request.Method != test.method {
						served <- fmt.Errorf("unexpected request: %+v", request)
						return
					}
					reply := map[string]any{"jsonrpc": "2.0", "id": request.ID}
					switch {
					case request.Method == "system.hello":
						if string(request.Params) != `{"protocol":"`+api.Protocol+`"}` {
							served <- fmt.Errorf("wrong hello params: %s", request.Params)
							return
						}
						reply["result"] = api.HelloResult{Protocol: api.Protocol, Capabilities: []api.Capability{}}
					case test.method == "session.attach":
						if string(request.Params) != `{"uuid":"`+fixtureSession+`"}` {
							served <- fmt.Errorf("wrong attach params: %s", request.Params)
							return
						}
						reply["error"] = map[string]any{"code": -32011, "message": "conflict", "data": map[string]any{"reason": "attachment_active"}}
					default:
						if string(request.Params) != `{}` {
							served <- fmt.Errorf("wrong params: %s", request.Params)
							return
						}
						reply["result"] = api.HealthResult{ControlState: "ready"}
					}
					if err := encoder.Encode(reply); err != nil || agreed {
						served <- err
						return
					}
				}
			}()
			err = run(test.args(socket))
			if test.method == "session.attach" {
				if err == nil || err.Error() != "Detach every terminal from this session first." {
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

func TestAPIDeadlineCoversAWaitAndItsConnection(t *testing.T) {
	for _, test := range []struct {
		method, params string
		want           time.Duration
	}{
		{"operation.wait", `{"id":"op","timeout":20}`, 25 * time.Second},
		{"operation.wait", `{"id":"op","timeout":25}`, 30 * time.Second},
		{"operation.wait", `{"id":"op","timeout":99}`, 10 * time.Second},
		{"operation.inspect", `{"id":"op"}`, 10 * time.Second},
	} {
		if got := apiDeadline(test.method, json.RawMessage(test.params)); got != test.want {
			t.Errorf("%s %s: %s, want %s", test.method, test.params, got, test.want)
		}
	}
}
