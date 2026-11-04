package eventhandler

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/driverwire"
)

// pair is a connected pair of Unix stream sockets: P's end and the
// handler's.
func pair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	local, remote, err := driverwire.SocketPair("test")
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.FileConn(remote)
	remote.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { local.Close(); c.Close() })
	return local, c.(*net.UnixConn)
}

// frame is one JSON-RPC message as the fake reads it.
type frame struct {
	ID     *uint64         `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// fakeHandler answers the hello on conn as a handler of version answers,
// then answers the first call with reply, a frame without its id, which it
// fills in: the end of a handler that misbehaves.
func fakeHandler(t *testing.T, conn *net.UnixConn, version string, reply func(id uint64) string) {
	t.Helper()
	go func() {
		r := bufio.NewReaderSize(conn, 64<<10)
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				return
			}
			var f frame
			if json.Unmarshal(line, &f) != nil || f.ID == nil {
				continue
			}
			var answer string
			if f.Method == "hello" {
				answer = `{"jsonrpc":"2.0","id":` + itoa(*f.ID) + `,"result":{"protocol":"` + version + `","capabilities":{"id":"example.handler","version":"1.0.0"}}}`
			} else {
				answer = reply(*f.ID)
			}
			if _, err := conn.Write([]byte(answer + "\n")); err != nil {
				return
			}
		}
	}()
}

func itoa(n uint64) string { b, _ := json.Marshal(n); return string(b) }

func connect(t *testing.T, reply func(id uint64) string) *Client {
	t.Helper()
	local, remote := pair(t)
	fakeHandler(t, remote, Version, reply)
	c, err := Connect(t.Context(), local, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func result(status string) func(uint64) string {
	return func(id uint64) string {
		return `{"jsonrpc":"2.0","id":` + itoa(id) + `,"result":{"status":"` + status + `"}}`
	}
}

func TestHandleReturnsWhatTheHandlerDid(t *testing.T) {
	for _, status := range []Status{Handled, Skipped} {
		c := connect(t, result(string(status)))
		if got, err := c.Handle(t.Context(), ready()); err != nil || got != status {
			t.Fatalf("Handle = %q, %v; want %q", got, err, status)
		}
		if c.Identity() != (Identity{ID: "example.handler", Version: "1.0.0"}) {
			t.Fatalf("the handler answered the hello as %+v", c.Identity())
		}
	}
}

// A reply that is not a handled or skipped status is refused, as is a
// frame over the bound, which ends the connection.
func TestHandleRefusesAMalformedOrOversizedReply(t *testing.T) {
	for _, c := range []struct {
		name  string
		reply func(id uint64) string
		want  string
	}{
		{"another status", result("appended"), `answered status "appended"`},
		{"an unknown field", func(id uint64) string {
			return `{"jsonrpc":"2.0","id":` + itoa(id) + `,"result":{"status":"handled","note":"x"}}`
		}, "malformed answer"},
		{"no result object", func(id uint64) string {
			return `{"jsonrpc":"2.0","id":` + itoa(id) + `,"result":"handled"}`
		}, "malformed answer"},
		{"a frame that is no JSON", func(uint64) string { return `{"jsonrpc":` }, "unavailable"},
		{"a frame over the bound", func(id uint64) string {
			return `{"jsonrpc":"2.0","id":` + itoa(id) + `,"result":{"status":"` + strings.Repeat("x", driverwire.MaxFrameBytes) + `"}}`
		}, "unavailable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			client := connect(t, c.reply)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			if status, err := client.Handle(ctx, ready()); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Handle = %q, %v; want a refusal %q", status, err, c.want)
			}
		})
	}
}

// An event P has not validated is never sent.
func TestHandleSendsOnlyValidEvents(t *testing.T) {
	sent := make(chan struct{}, 1)
	c := connect(t, func(id uint64) string { sent <- struct{}{}; return result("handled")(id) })
	e := ready()
	e.Fields = map[string]string{"condition": "secret diagnostic"}
	if _, err := c.Handle(t.Context(), e); err == nil {
		t.Fatal("an invalid event was handled")
	}
	select {
	case <-sent:
		t.Fatal("an invalid event was sent")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestConnectRefusesAHandlerOfAnotherVersion(t *testing.T) {
	local, remote := pair(t)
	fakeHandler(t, remote, "p.event-handler/v0", nil)
	if _, err := Connect(t.Context(), local, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "p.event-handler/v0") {
		t.Fatalf("Connect to a handler of another version = %v", err)
	}
}

// serve runs Serve as identity, with open, on the handler's end of a pair
// and returns P's end, raw.
func serve(t *testing.T, open Open) (*bufio.Reader, *net.UnixConn, <-chan error) {
	t.Helper()
	local, remote := pair(t)
	done := make(chan error, 1)
	go func() {
		done <- Serve(context.Background(), remote, Identity{ID: "example.handler", Version: "1.0.0"}, open)
	}()
	return bufio.NewReader(local), local, done
}

func call(t *testing.T, r *bufio.Reader, w *net.UnixConn, request string) map[string]any {
	t.Helper()
	if _, err := w.Write([]byte(request + "\n")); err != nil {
		t.Fatal(err)
	}
	w.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("no answer to %s: %v", request, err)
	}
	var answer map[string]any
	if err := json.Unmarshal(line, &answer); err != nil {
		t.Fatal(err)
	}
	return answer
}

func errorCode(answer map[string]any) float64 {
	e, _ := answer["error"].(map[string]any)
	code, _ := e["code"].(float64)
	return code
}

func handles(status Status) Open {
	return func(json.RawMessage) (Handler, error) {
		return func(context.Context, Event) (Status, error) { return status, nil }, nil
	}
}

func TestServeAnswersTheHelloAndHandlesEvents(t *testing.T) {
	r, w, _ := serve(t, handles(Handled))
	answer := call(t, r, w, `{"jsonrpc":"2.0","id":1,"method":"hello","params":{"protocol":"p.event-handler/v1","config":{},"core":{"capability":"event-handler"}}}`)
	got, _ := json.Marshal(answer["result"])
	if string(got) != `{"capabilities":{"id":"example.handler","version":"1.0.0"},"protocol":"p.event-handler/v1"}` {
		t.Fatalf("the hello was answered %s", got)
	}
	event, _ := json.Marshal(ready())
	answer = call(t, r, w, `{"jsonrpc":"2.0","id":2,"method":"event.handle","params":`+string(event)+`}`)
	if got, _ := json.Marshal(answer["result"]); string(got) != `{"status":"handled"}` {
		t.Fatalf("event.handle was answered %v", answer)
	}
}

func TestServeRefuses(t *testing.T) {
	hello := `{"jsonrpc":"2.0","id":1,"method":"hello","params":{"protocol":"p.event-handler/v1","config":{},"core":{"capability":"event-handler"}}}`
	event, _ := json.Marshal(ready())
	for _, c := range []struct {
		name     string
		open     Open
		requests []string
		code     float64 // of the last request's answer
	}{
		{"another version", handles(Handled), []string{strings.Replace(hello, "v1", "v0", 1)}, -32001},
		{"another capability", handles(Handled), []string{strings.Replace(hello, `"capability":"event-handler"`, `"capability":"agent-adapter"`, 1)}, -32000},
		{"a config the handler refuses", func(json.RawMessage) (Handler, error) { return nil, errors.New("bad config") }, []string{hello}, -32000},
		{"an event before the hello", handles(Handled), []string{`{"jsonrpc":"2.0","id":1,"method":"event.handle","params":` + string(event) + `}`}, -32002},
		{"an invalid event", handles(Handled), []string{hello, `{"jsonrpc":"2.0","id":2,"method":"event.handle","params":` + strings.Replace(string(event), "ready", "secret", 1) + `}`}, -32000},
		{"an event with an unknown field", handles(Handled), []string{hello, `{"jsonrpc":"2.0","id":2,"method":"event.handle","params":` + strings.Replace(string(event), `"schema"`, `"secret":"x","schema"`, 1) + `}`}, -32602},
		{"another method", handles(Handled), []string{hello, `{"jsonrpc":"2.0","id":2,"method":"event.file.append","params":{}}`}, -32601},
		{"a status that is neither", handles("appended"), []string{hello, `{"jsonrpc":"2.0","id":2,"method":"event.handle","params":` + string(event) + `}`}, -32000},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, w, _ := serve(t, c.open)
			var answer map[string]any
			for _, request := range c.requests {
				answer = call(t, r, w, request)
			}
			if got := errorCode(answer); got != c.code {
				t.Fatalf("answered %v, want error code %v", answer, c.code)
			}
		})
	}
}
