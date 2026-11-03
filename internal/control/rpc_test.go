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

// helloLine opens a connection with the protocol this build speaks.
const helloLine = `{"jsonrpc":"2.0","id":"hello","method":"system.hello","params":{"protocol":"p.control/v2"}}`

// withHello answers system.hello as the daemon does and passes every other
// request to h.
func withHello(h Handler) Handler {
	return func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
		if method == api.SystemHello.Name {
			return api.HelloResult{Protocol: api.Protocol, Capabilities: []api.Capability{}}, nil
		}
		return h(ctx, method, params)
	}
}

// agree writes the hello and reads its answer.
func agree(t *testing.T, client net.Conn, reader *bufio.Reader) {
	t.Helper()
	if _, err := client.Write([]byte(helloLine + "\n")); err != nil {
		t.Fatal(err)
	}
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := reader.ReadBytes('\n')
	if err != nil || !strings.Contains(string(line), `"protocol":"p.control/v2"`) {
		t.Fatalf("hello: %s %v", line, err)
	}
}

func TestRPCFramingHelloAndUnavailableMethods(t *testing.T) {
	store, _ := openTestStore(t)
	client, server := net.Pipe()
	defer client.Close()
	go ServeConn(context.Background(), server, NewHandler(HandlerConfig{Store: store}))
	reader := bufio.NewReader(client)
	requests := []struct {
		line   string
		kind   string
		reason string
		result string
	}{
		{`{"jsonrpc":"2.0","id":1,"method":"system.health","params":{}}`, "invalid_request", api.ReasonHelloRequired, ""},
		{`{"jsonrpc":"2.0","id":2,"method":"system.hello","params":{"protocol":"p.control/v1"}}`, "unsupported_version", api.ReasonProtocol, ""},
		{`{"jsonrpc":"2.0","id":3,"method":"system.hello","params":{"protocol":"p.control/v2","v":1}}`, "invalid_params", "", ""},
		{`{"jsonrpc":"2.0","id":4,"method":"system.hello","params":{}}`, "invalid_params", "", ""},
		{`{"jsonrpc":"2.0","id":5,"method":"system.health","params":{}}`, "invalid_request", api.ReasonHelloRequired, ""},
		{`{"jsonrpc":"2.0","id":6,"method":"system.hello","params":{"protocol":"p.control/v2"}}`, "", "", `"instance_id"`},
		{`{"jsonrpc":"1.0","id":7,"method":"system.health","params":{}}`, "unsupported_version", "", ""},
		{`{"jsonrpc":"2.0","id":8,"method":"session.create","params":{}}`, "unavailable", "", ""},
		{`{"jsonrpc":"2.0","id":9,"method":"unknown","params":{}}`, "method_not_found", "", ""},
		{`{"jsonrpc":"2.0","id":10,"id":11,"method":"system.health","params":{}}`, "invalid_request", "", ""},
		{`{"jsonrpc":"2.0","id":true,"method":"system.health","params":{}}`, "invalid_request", "", ""},
		{`{"jsonrpc":"2.0","id":12,"method":"system.health","params":{"v":1}}`, "invalid_params", "", ""},
		{`{"jsonrpc":"2.0","id":13,"method":"system.health","params":{}}`, "", "", `"control_state":"ready"`},
		{`{"jsonrpc":"2.0","id":14,"method":"system.hello","params":{"protocol":"p.control/v2"}}`, "", "", `"capabilities":[]`},
		{`{"jsonrpc":"2.0",`, "parse_error", "", ""},
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
			if response.Error != nil || !strings.Contains(string(response.Result), test.result) {
				t.Fatalf("%s failed: %s", test.line, line)
			}
			continue
		}
		if response.Error == nil || response.Error.Kind != test.kind {
			t.Fatalf("%s: wanted %s, got %s", test.line, test.kind, line)
		}
		reason := ""
		if response.Error.Data != nil {
			reason = response.Error.Data.Reason
		}
		if reason != test.reason {
			t.Fatalf("%s: reason %q, want %q", test.line, reason, test.reason)
		}
		if test.kind == "unsupported_version" && test.reason != "" && strings.Join(response.Error.Data.Supported, ",") != api.Protocol {
			t.Fatalf("supported protocols: %s", line)
		}
	}
}

func TestRPCAnswersAHelloWrittenTogetherWithTheFirstRequest(t *testing.T) {
	store, _ := openTestStore(t)
	client, server := net.Pipe()
	defer client.Close()
	go ServeConn(context.Background(), server, NewHandler(HandlerConfig{Store: store}))
	go client.Write([]byte(`{"jsonrpc":"2.0","method":"system.health","params":{}}` + "\n" + helloLine + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"system.health","params":{}}` + "\n"))
	reader := bufio.NewReader(client)
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	// The notification before the hello is ignored: the first answer is the hello's.
	for _, want := range []string{`"id":"hello","result":{"protocol":"p.control/v2"`, `"id":2,"result":{"control_state":"ready"}`} {
		line, err := reader.ReadBytes('\n')
		if err != nil || !strings.Contains(string(line), want) {
			t.Fatalf("answer %s %v, want %s", line, err, want)
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
	go ServeConn(context.Background(), server, withHello(handler))
	reader := bufio.NewReader(client)
	agree(t, client, reader)
	if _, err := client.Write([]byte(`{"jsonrpc":"2.0","id":"\u0077ait","method":"system.health","params":{}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	if _, err := client.Write([]byte(`{"jsonrpc":"2.0","method":"rpc.cancel","params":{"id":"wait"}}` + "\n")); err != nil {
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
			return api.HealthResult{}, errorRPC(-32602, "invalid_params", "bad")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			go ServeConn(context.Background(), server, withHello(test.handler))
			reader := bufio.NewReader(client)
			agree(t, client, reader)
			if _, err := client.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"system.health","params":{}}` + "\n")); err != nil {
				t.Fatal(err)
			}
			client.SetReadDeadline(time.Now().Add(2 * time.Second))
			line, err := reader.ReadBytes('\n')
			if err != nil || !strings.Contains(string(line), `"kind":"internal"`) || strings.Contains(string(line), `"result"`) {
				t.Fatalf("invalid result envelope was accepted: %s %v", line, err)
			}
		})
	}
}

func TestRPCHandlerPanicAnswersInternalAndConnectionKeepsServing(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go ServeConn(context.Background(), server, withHello(func(_ context.Context, method string, _ json.RawMessage) (any, *api.RPCError) {
		if method == "session.inspect" {
			panic("handler bug")
		}
		return api.HealthResult{ControlState: "ready"}, nil
	}))
	reader := bufio.NewReader(client)
	agree(t, client, reader)
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
	failed := exchange(`{"jsonrpc":"2.0","id":"broken","method":"session.inspect","params":{}}`)
	if string(failed.ID) != `"broken"` || failed.Result != nil || failed.Error == nil || failed.Error.Code != -32603 || failed.Error.Kind != "internal" {
		t.Fatalf("panicking handler answered %s %s %+v", failed.ID, failed.Result, failed.Error)
	}
	next := exchange(`{"jsonrpc":"2.0","id":2,"method":"system.health","params":{}}`)
	if string(next.ID) != "2" || string(next.Result) != `{"control_state":"ready"}` || next.Error != nil {
		t.Fatalf("request after the panic answered %s %s %+v", next.ID, next.Result, next.Error)
	}
}

func TestRPCConnectionCloseSurvivesPanickingRelease(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		ServeConn(context.Background(), server, withHello(func(ctx context.Context, _ string, _ json.RawMessage) (any, *api.RPCError) {
			if err := ctx.Value(connectionKey{}).(*Connection).Own(func() { panic("release bug") }); err != nil {
				return nil, lifecycleRPC(err)
			}
			return api.AttachmentResult{}, nil
		}))
		close(done)
	}()
	reader := bufio.NewReader(client)
	agree(t, client, reader)
	if _, err := client.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"attachment.claim","params":{}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if line, err := reader.ReadBytes('\n'); err != nil || !strings.Contains(string(line), `"result"`) {
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
	conn, err := api.Dial(callCtx, socket)
	if err != nil {
		t.Fatal(err)
	}
	health, err := api.Do(callCtx, conn, api.SystemHealth, api.HealthRequest{})
	conn.Close()
	if err != nil || health.ControlState != "ready" || conn.Hello().Protocol != api.Protocol || len(conn.Hello().Capabilities) != 0 {
		t.Fatalf("client health: %+v %+v %v", health, conn.Hello(), err)
	}
	response, err := api.CallRaw(callCtx, socket, "system.health", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(string(response), `"control_state":"ready"`) {
		t.Fatalf("raw client health: %s %v", response, err)
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
