package control

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

func TestRPCFramingVersionAndUnavailableMethods(t *testing.T) {
	store, _ := openTestStore(t)
	client, server := net.Pipe()
	defer client.Close()
	go ServeConn(context.Background(), server, NewHandler(HandlerConfig{Store: store}))
	reader := bufio.NewReader(client)
	requests := []struct {
		line string
		kind string
	}{
		{`{"jsonrpc":"2.0","id":1,"method":"system.hello","params":{"v":1}}`, ""},
		{`{"jsonrpc":"1.0","id":2,"method":"system.hello","params":{"v":1}}`, "unsupported_version"},
		{`{"jsonrpc":"2.0","id":3,"method":"system.hello","params":{"v":2}}`, "invalid_params"},
		{`{"jsonrpc":"2.0","id":4,"method":"session.create","params":{"v":1}}`, "unavailable"},
		{`{"jsonrpc":"2.0","id":5,"method":"unknown","params":{"v":1}}`, "method_not_found"},
		{`{"jsonrpc":"2.0","id":6,"id":7,"method":"system.hello","params":{"v":1}}`, "invalid_request"},
		{`{"jsonrpc":"2.0","id":true,"method":"system.hello","params":{"v":1}}`, "invalid_request"},
		{`{"jsonrpc":"2.0","id":8,"method":"system.hello","params":{"v":0,"v":1}}`, "invalid_params"},
		{`{"jsonrpc":"2.0",`, "parse_error"},
	}
	for _, test := range requests {
		if _, err := client.Write([]byte(test.line + "\n")); err != nil {
			t.Fatal(err)
		}
		client.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			Error  *api.RPCError   `json:"error"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(line, &response); err != nil {
			t.Fatal(err)
		}
		if test.kind == "" {
			if response.Error != nil || !strings.Contains(string(response.Result), `"instance_id"`) {
				t.Fatalf("hello failed: %s", line)
			}
		} else if response.Error == nil || response.Error.Kind != test.kind {
			t.Fatalf("wanted %s, got %s", test.kind, line)
		}
	}
}

func TestRPCDaemonShutdownClosesExistingConnection(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ServeConn(ctx, server, func(context.Context, string, json.RawMessage) (any, *api.RPCError) { return nil, nil })
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("existing connection survived daemon cancellation")
	}
}

func TestRPCCancellationAndBoundedFrame(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	started := make(chan struct{})
	handler := func(ctx context.Context, method string, _ json.RawMessage) (any, *api.RPCError) {
		close(started)
		<-ctx.Done()
		return nil, internalRPC(ctx.Err())
	}
	go ServeConn(context.Background(), server, handler)
	reader := bufio.NewReader(client)
	if _, err := client.Write([]byte(`{"jsonrpc":"2.0","id":"\u0077ait","method":"system.hello","params":{"v":1}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	if _, err := client.Write([]byte(`{"jsonrpc":"2.0","method":"rpc.cancel","params":{"v":1,"id":"wait"}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := reader.ReadBytes('\n')
	if err != nil || !strings.Contains(string(line), `"kind":"cancelled"`) {
		t.Fatalf("cancel result: %s %v", line, err)
	}
	client.Close()

	oversizeClient, oversizeServer := net.Pipe()
	defer oversizeClient.Close()
	go ServeConn(context.Background(), oversizeServer, handler)
	go oversizeClient.Write([]byte(strings.Repeat("x", api.MaxFrameBytes+2) + "\n"))
	oversizeClient.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err = bufio.NewReader(oversizeClient).ReadBytes('\n')
	if err != nil || !strings.Contains(string(line), `"parse_error"`) {
		t.Fatalf("oversize result: %s %v", line, err)
	}
}

func TestRPCRejectsInvalidHandlerEnvelopes(t *testing.T) {
	for _, test := range []struct {
		name    string
		handler Handler
	}{
		{"neither", func(context.Context, string, json.RawMessage) (any, *api.RPCError) { return nil, nil }},
		{"both", func(context.Context, string, json.RawMessage) (any, *api.RPCError) {
			return map[string]any{"v": 1}, errorRPC(-32602, "invalid_params", "bad")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			go ServeConn(context.Background(), server, test.handler)
			if _, err := client.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"system.hello","params":{"v":1}}` + "\n")); err != nil {
				t.Fatal(err)
			}
			client.SetReadDeadline(time.Now().Add(2 * time.Second))
			line, err := bufio.NewReader(client).ReadBytes('\n')
			if err != nil || !strings.Contains(string(line), `"kind":"internal"`) || strings.Contains(string(line), `"result"`) {
				t.Fatalf("invalid result envelope was accepted: %s %v", line, err)
			}
		})
	}
}

func TestRPCHandlerPanicAnswersInternalAndConnectionKeepsServing(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go ServeConn(context.Background(), server, func(_ context.Context, method string, _ json.RawMessage) (any, *api.RPCError) {
		if method == "session.inspect" {
			panic("handler bug")
		}
		return map[string]any{"v": 1}, nil
	})
	reader := bufio.NewReader(client)
	exchange := func(request string) (response struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *api.RPCError   `json:"error"`
	}) {
		t.Helper()
		if _, err := client.Write([]byte(request + "\n")); err != nil {
			t.Fatal(err)
		}
		client.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(line, &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	failed := exchange(`{"jsonrpc":"2.0","id":"broken","method":"session.inspect","params":{"v":1}}`)
	if string(failed.ID) != `"broken"` || failed.Result != nil || failed.Error == nil || failed.Error.Code != -32603 || failed.Error.Kind != "internal" {
		t.Fatalf("panicking handler answered %s %s %+v", failed.ID, failed.Result, failed.Error)
	}
	next := exchange(`{"jsonrpc":"2.0","id":2,"method":"system.health","params":{"v":1}}`)
	if string(next.ID) != "2" || string(next.Result) != `{"v":1}` || next.Error != nil {
		t.Fatalf("request after the panic answered %s %s %+v", next.ID, next.Result, next.Error)
	}
}

func TestRPCConnectionCloseSurvivesPanickingRelease(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		ServeConn(context.Background(), server, func(ctx context.Context, _ string, _ json.RawMessage) (any, *api.RPCError) {
			if err := ctx.Value(connectionKey{}).(*Connection).Own(func() { panic("release bug") }); err != nil {
				return nil, lifecycleRPC(err)
			}
			return map[string]any{"v": 1}, nil
		})
		close(done)
	}()
	if _, err := client.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"attachment.claim","params":{"v":1}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if line, err := bufio.NewReader(client).ReadBytes('\n'); err != nil || !strings.Contains(string(line), `"result"`) {
		t.Fatalf("claim: %s %v", line, err)
	}
	client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("connection teardown did not finish after its release panicked")
	}
}

func TestClientCallsPrivateSocket(t *testing.T) {
	store, dir := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, store, dir) }()
	socket := dir + "/control.sock"
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.Dial("unix", socket)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	callCtx, callCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer callCancel()
	response, err := api.Call(callCtx, socket, "system.health", json.RawMessage(`{"v":1}`))
	if err != nil || !strings.Contains(string(response), `"control_state":"ready"`) {
		t.Fatalf("client health: %s %v", response, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not stop")
	}
}
