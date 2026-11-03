package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net"
	"os"
	"strconv"
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
	return func(ctx context.Context, method string, params json.RawMessage) (any, *api.Error) {
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
		{`{"jsonrpc":"2.0","id":3,"method":"system.hello","params":{"protocol":"p.control/v2","v":1}}`, "invalid", api.ReasonParam, ""},
		{`{"jsonrpc":"2.0","id":4,"method":"system.hello","params":{}}`, "invalid", api.ReasonParam, ""},
		{`{"jsonrpc":"2.0","id":5,"method":"system.health","params":{}}`, "invalid_request", api.ReasonHelloRequired, ""},
		{`{"jsonrpc":"2.0","id":6,"method":"system.hello","params":{"protocol":"p.control/v2"}}`, "", "", `"instance_id"`},
		{`{"jsonrpc":"1.0","id":7,"method":"system.health","params":{}}`, "unsupported_version", api.ReasonJSONRPC, ""},
		{`{"jsonrpc":"2.0","id":8,"method":"session.create","params":{}}`, "not_configured", api.ReasonRuntime, ""},
		{`{"jsonrpc":"2.0","id":9,"method":"unknown","params":{}}`, "method_not_found", "", ""},
		{`{"jsonrpc":"2.0","id":10,"id":11,"method":"system.health","params":{}}`, "invalid_request", api.ReasonEnvelope, ""},
		{`{"jsonrpc":"2.0","id":true,"method":"system.health","params":{}}`, "invalid_request", api.ReasonEnvelope, ""},
		{`{"jsonrpc":"2.0","id":12,"method":"system.health","params":{"v":1}}`, "invalid", api.ReasonParam, ""},
		{`{"jsonrpc":"2.0","id":15,"method":"system.health"}`, "", "", `"control_state":"ready"`},
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
			Error  *api.Error      `json:"error"`
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
		if response.Error == nil || string(response.Error.Kind()) != test.kind || response.Error.Code != api.ErrorKind(test.kind).Code() {
			t.Fatalf("%s: wanted %s, got %s", test.line, test.kind, line)
		}
		reason := ""
		if response.Error.Data != nil {
			reason = response.Error.Data.Reason
		}
		if reason != test.reason {
			t.Fatalf("%s: reason %q, want %q", test.line, reason, test.reason)
		}
		if test.reason == api.ReasonProtocol && strings.Join(response.Error.Data.Supported, ",") != api.Protocol {
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
		ServeConn(ctx, server, func(context.Context, string, json.RawMessage) (any, *api.Error) { return nil, nil })
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
	handler := func(ctx context.Context, method string, _ json.RawMessage) (any, *api.Error) {
		close(started)
		<-ctx.Done()
		return nil, answerError(ctx, ctx.Err())
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
	if err != nil || !strings.Contains(string(line), `"error":{"code":-32001,"message":"cancelled"}`) {
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
		{"neither", func(context.Context, string, json.RawMessage) (any, *api.Error) { return nil, nil }},
		{"both", func(context.Context, string, json.RawMessage) (any, *api.Error) {
			return api.HealthResult{}, api.InvalidParam("")
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
			if err != nil || !strings.Contains(string(line), `"error":{"code":-32603,"message":"internal","data":{"reason":"encoding"}}`) || strings.Contains(string(line), `"result"`) {
				t.Fatalf("invalid result envelope was accepted: %s %v", line, err)
			}
		})
	}
}

func TestRPCHandlerPanicAnswersInternalAndConnectionKeepsServing(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)
	client, server := net.Pipe()
	defer client.Close()
	go ServeConn(context.Background(), server, withHello(func(_ context.Context, method string, _ json.RawMessage) (any, *api.Error) {
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
		Error  *api.Error      `json:"error"`
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
	if string(failed.ID) != `"broken"` || failed.Result != nil || failed.Error == nil || failed.Error.Code != -32603 || failed.Error.Kind() != "internal" || failed.Error.Reason() != api.ReasonPanic {
		t.Fatalf("panicking handler answered %s %s %+v", failed.ID, failed.Result, failed.Error)
	}
	next := exchange(`{"jsonrpc":"2.0","id":2,"method":"system.health","params":{}}`)
	if string(next.ID) != "2" || string(next.Result) != `{"control_state":"ready"}` || next.Error != nil {
		t.Fatalf("request after the panic answered %s %s %+v", next.ID, next.Result, next.Error)
	}
	if !strings.Contains(logged.String(), `control request "session.inspect" id "broken" panicked: handler bug`) {
		t.Fatalf("the panic log does not name the request: %s", logged.String())
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

// serveTest serves h on a pipe after the hello and returns a function that
// writes one line and reads one answer.
func serveTest(t *testing.T, h Handler) (net.Conn, func(string) []byte) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close() })
	go ServeConn(context.Background(), server, withHello(h))
	reader := bufio.NewReader(client)
	agree(t, client, reader)
	return client, func(line string) []byte {
		t.Helper()
		if line != "" {
			if _, err := client.Write([]byte(line + "\n")); err != nil {
				t.Fatal(err)
			}
		}
		client.SetReadDeadline(time.Now().Add(5 * time.Second))
		answer, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		return answer
	}
}

func TestRPCRefusesADuplicateInFlightID(t *testing.T) {
	release := make(chan struct{})
	_, exchange := serveTest(t, func(ctx context.Context, _ string, _ json.RawMessage) (any, *api.Error) {
		<-release
		return api.HealthResult{ControlState: "ready"}, nil
	})
	first := `{"jsonrpc":"2.0","id":7,"method":"system.health","params":{}}`
	answer := exchange(first + "\n" + first)
	if want := `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"invalid_request","data":{"reason":"duplicate_id"}}}`; strings.TrimSpace(string(answer)) != want {
		t.Fatalf("duplicate answered %s, want %s", answer, want)
	}
	close(release)
	if answer = exchange(""); !strings.Contains(string(answer), `"id":7,"result"`) {
		t.Fatalf("first request answered %s", answer)
	}
}

func TestRPCAnswersRetryBeyondSixteenRequests(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 16)
	_, exchange := serveTest(t, func(ctx context.Context, _ string, _ json.RawMessage) (any, *api.Error) {
		started <- struct{}{}
		<-release
		return api.HealthResult{ControlState: "ready"}, nil
	})
	var lines []string
	for i := 1; i <= 16; i++ {
		lines = append(lines, `{"jsonrpc":"2.0","id":`+strconv.Itoa(i)+`,"method":"system.health","params":{}}`)
	}
	go func() {
		for range 16 {
			<-started
		}
	}()
	answer := exchange(strings.Join(lines, "\n") + "\n" + `{"jsonrpc":"2.0","id":17,"method":"system.health","params":{}}`)
	if want := `{"jsonrpc":"2.0","id":17,"error":{"code":-32010,"message":"retry","data":{"reason":"too_many_requests"}}}`; strings.TrimSpace(string(answer)) != want {
		t.Fatalf("17th request answered %s, want %s", answer, want)
	}
	close(release)
	for range 16 {
		exchange("")
	}
}

// TestRPCContextEndKeepsAReturnedResult checks that once a request's context
// ends, an error the handler returns answers as the context's (cancelled
// here), and a result the handler returned is still sent.
func TestRPCContextEndKeepsAReturnedResult(t *testing.T) {
	started := make(chan string, 2)
	_, exchange := serveTest(t, func(ctx context.Context, method string, _ json.RawMessage) (any, *api.Error) {
		started <- method
		<-ctx.Done()
		if method == "session.inspect" {
			// A handler that recorded its change returns it even though the
			// context it served ended.
			return api.HealthResult{ControlState: "ready"}, nil
		}
		return nil, api.InvalidParam("")
	})
	for _, test := range []struct{ id, method, want string }{
		{"w1", "system.health", `{"jsonrpc":"2.0","id":"w1","error":{"code":-32001,"message":"cancelled"}}`},
		{"w2", "session.inspect", `{"jsonrpc":"2.0","id":"w2","result":{"control_state":"ready"}}`},
	} {
		go func() {
			<-started
		}()
		answer := exchange(`{"jsonrpc":"2.0","id":"` + test.id + `","method":"` + test.method + `","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","method":"rpc.cancel","params":{"id":"` + test.id + `"}}`)
		if strings.TrimSpace(string(answer)) != test.want {
			t.Fatalf("%s answered %s, want %s", test.method, answer, test.want)
		}
	}
}

// TestConnectionMethodsNameTheUnknownParam checks that system.hello and
// rpc.cancel name an unknown member as the field at fault, as every
// registered method does.
func TestConnectionMethodsNameTheUnknownParam(t *testing.T) {
	_, exchange := serveTest(t, func(context.Context, string, json.RawMessage) (any, *api.Error) {
		return api.HealthResult{ControlState: "ready"}, nil
	})
	for _, test := range []struct{ line, want string }{
		{`{"jsonrpc":"2.0","id":1,"method":"system.hello","params":{"protocol":"p.control/v2","extra":1}}`,
			`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"invalid","data":{"reason":"param","field":"extra"}}}`},
		{`{"jsonrpc":"2.0","id":2,"method":"rpc.cancel","params":{"id":1,"extra":1}}`,
			`{"jsonrpc":"2.0","id":2,"error":{"code":-32602,"message":"invalid","data":{"reason":"param","field":"extra"}}}`},
		{`{"jsonrpc":"2.0","id":3,"method":"rpc.cancel","params":{"id":true}}`,
			`{"jsonrpc":"2.0","id":3,"error":{"code":-32602,"message":"invalid","data":{"reason":"param","field":"id"}}}`},
	} {
		if answer := strings.TrimSpace(string(exchange(test.line))); answer != test.want {
			t.Errorf("%s answered %s, want %s", test.line, answer, test.want)
		}
	}
}
