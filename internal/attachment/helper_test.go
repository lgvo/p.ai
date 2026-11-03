package attachment

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/errtext"
)

var testToken = strings.Repeat("a", 64)

// fakeDaemon is a control socket that agrees the protocol and answers the
// helper's attachment.claim with refusal, or with success after which the
// connection carries terminal frames.
type fakeDaemon struct {
	t       *testing.T
	socket  string
	claimed chan *api.FrameConn // the daemon's end of each claimed connection
	claims  chan api.AttachmentClaimRequest
	accepts chan struct{}
}

func newFakeDaemon(t *testing.T, refusal *api.Error, afterClaim []byte) *fakeDaemon {
	t.Helper()
	d := &fakeDaemon{t: t, socket: filepath.Join(t.TempDir(), "control.sock"), claimed: make(chan *api.FrameConn, 4),
		claims: make(chan api.AttachmentClaimRequest, 4), accepts: make(chan struct{}, 4)}
	listener, err := net.Listen("unix", d.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			d.accepts <- struct{}{}
			go d.serve(conn, refusal, afterClaim)
		}
	}()
	return d
}

func (d *fakeDaemon) serve(conn net.Conn, refusal *api.Error, afterClaim []byte) {
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			conn.Close()
			return
		}
		var req struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(line, &req) != nil {
			conn.Close()
			return
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case api.SystemHello.Name:
			reply["result"] = api.HelloResult{Protocol: api.Protocol, Capabilities: []api.Capability{}}
		case api.AttachmentClaim.Name:
			var claim api.AttachmentClaimRequest
			if json.Unmarshal(req.Params, &claim) != nil {
				conn.Close()
				return
			}
			d.claims <- claim
			if refusal != nil {
				reply["error"] = refusal
			} else {
				reply["result"] = api.AttachmentLaunch{}
			}
		default:
			conn.Close()
			return
		}
		raw, _ := json.Marshal(reply)
		raw = append(raw, '\n')
		if req.Method == api.AttachmentClaim.Name && refusal == nil {
			// Frames written right after the answer may reach the helper in
			// the same read as the answer.
			conn.Write(append(raw, afterClaim...))
			d.claimed <- api.NewFrameConn(conn, reader)
			return
		}
		conn.Write(raw)
	}
}

// helperRun is the helper running on one end of a carrier; client is the
// other end.
type helperRun struct {
	t      *testing.T
	client *api.FrameConn
	done   chan error
}

func startHelper(t *testing.T, init initiation) *helperRun {
	t.Helper()
	helperEnd, clientEnd := net.Pipe()
	t.Cleanup(func() { clientEnd.Close() })
	h := &helperRun{t: t, client: api.NewFrameConn(clientEnd, nil), done: make(chan error, 1)}
	go func() { defer helperEnd.Close(); h.done <- runHelper(api.NewFrameConn(helperEnd, nil)) }()
	if err := h.client.WriteJSON(api.FrameInit, init); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *helperRun) frame() api.Frame {
	h.t.Helper()
	h.client.SetReadDeadline(time.Now().Add(3 * time.Second))
	f, err := h.client.ReadFrame()
	if err != nil {
		h.t.Fatalf("client frame: %v", err)
	}
	return f
}

func (h *helperRun) result() error {
	h.t.Helper()
	select {
	case err := <-h.done:
		return err
	case <-time.After(3 * time.Second):
		h.t.Fatal("the helper did not end")
	}
	return nil
}

func receive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	panic("unreachable")
}

func daemonFrame(t *testing.T, c *api.FrameConn) api.Frame {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	f, err := c.ReadFrame()
	if err != nil {
		t.Fatalf("daemon frame: %v", err)
	}
	return f
}

// The helper claims the token at the client's size, and relays the client's
// input and resizes to the daemon and the daemon's output back, including
// output the daemon sent with the claim's answer, until the daemon's end.
func TestHelperRelaysTheClaimedTerminal(t *testing.T) {
	welcome := []byte{byte(api.FrameOutput), 0, 0, 0, 7, 'w', 'e', 'l', 'c', 'o', 'm', 'e'}
	d := newFakeDaemon(t, nil, welcome)
	h := startHelper(t, initiation{Socket: d.socket, Token: testToken, Width: 80, Height: 24})
	if claim := receive(t, d.claims, "the claim"); claim != (api.AttachmentClaimRequest{Token: testToken, Width: 80, Height: 24}) {
		t.Fatalf("claim %+v", claim)
	}
	daemon := receive(t, d.claimed, "the claimed connection")
	if f := h.frame(); f.Kind != api.FrameReady {
		t.Fatalf("first frame %+v, want ready", f)
	}
	if f := h.frame(); f.Kind != api.FrameOutput || string(f.Data) != "welcome" {
		t.Fatalf("output %+v", f)
	}
	if err := h.client.WriteFrame(api.FrameInput, []byte("echo\r")); err != nil {
		t.Fatal(err)
	}
	if f := daemonFrame(t, daemon); f.Kind != api.FrameInput || string(f.Data) != "echo\r" {
		t.Fatalf("input %+v", f)
	}
	if err := h.client.WriteJSON(api.FrameResize, api.TerminalSize{Width: 100, Height: 40}); err != nil {
		t.Fatal(err)
	}
	if f := daemonFrame(t, daemon); f.Kind != api.FrameResize || string(f.Data) != `{"width":100,"height":40}` {
		t.Fatalf("resize %+v", f)
	}
	if err := daemon.WriteFrame(api.FrameOutput, []byte("echo")); err != nil {
		t.Fatal(err)
	}
	if f := h.frame(); f.Kind != api.FrameOutput || string(f.Data) != "echo" {
		t.Fatalf("output %+v", f)
	}
	if err := daemon.WriteFrame(api.FrameEnd, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.result(); err != nil {
		t.Fatalf("a normal end: %v", err)
	}
	daemon.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := daemon.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Fatalf("the helper kept the daemon's connection: %v", err)
	}
}

// Losing the client ends the attachment: the helper closes its connection,
// and the daemon then closes the terminal.
func TestHelperClosesTheDaemonsConnectionWhenTheClientGoes(t *testing.T) {
	d := newFakeDaemon(t, nil, nil)
	h := startHelper(t, initiation{Socket: d.socket, Token: testToken, Width: 80, Height: 24})
	daemon := receive(t, d.claimed, "the claimed connection")
	if f := h.frame(); f.Kind != api.FrameReady {
		t.Fatalf("first frame %+v, want ready", f)
	}
	h.client.Close()
	daemon.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := daemon.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Fatalf("the daemon's connection after the client went: %v", err)
	}
	if err := h.result(); err != nil {
		t.Fatal(err)
	}
}

// Losing the daemon ends the helper at once, and it never claims again.
func TestHelperEndsWhenTheDaemonGoesWithoutClaimingAgain(t *testing.T) {
	d := newFakeDaemon(t, nil, nil)
	h := startHelper(t, initiation{Socket: d.socket, Token: testToken, Width: 80, Height: 24})
	daemon := receive(t, d.claimed, "the claimed connection")
	if f := h.frame(); f.Kind != api.FrameReady {
		t.Fatalf("first frame %+v, want ready", f)
	}
	daemon.Close()
	if err := h.result(); err != nil {
		t.Fatal(err)
	}
	if len(d.accepts) != 1 || len(d.claims) != 1 {
		t.Fatalf("the helper connected %d and claimed %d times", len(d.accepts), len(d.claims))
	}
}

// A refused claim never reports the attachment ready; the client is told
// why, in the sentence for the daemon's error.
func TestARefusedClaimTellsTheClientWhy(t *testing.T) {
	refusal := api.NewError(api.KindRetry, api.ReasonSessionHeld)
	d := newFakeDaemon(t, refusal, nil)
	h := startHelper(t, initiation{Socket: d.socket, Token: testToken, Width: 80, Height: 24})
	if f := h.frame(); f.Kind != api.FrameError || string(f.Data) != errtext.Sentence(refusal) {
		t.Fatalf("frame %+v, want the refusal's sentence", f)
	}
	if err := h.result(); err == nil {
		t.Fatal("a refused claim succeeded")
	}
}

// An abnormal end, such as a terminal whose end the runtime could not prove,
// reaches the client as an error.
func TestAnAbnormalEndTellsTheClientWhy(t *testing.T) {
	d := newFakeDaemon(t, nil, nil)
	h := startHelper(t, initiation{Socket: d.socket, Token: testToken, Width: 80, Height: 24})
	daemon := receive(t, d.claimed, "the claimed connection")
	if f := h.frame(); f.Kind != api.FrameReady {
		t.Fatalf("first frame %+v, want ready", f)
	}
	if err := daemon.WriteFrame(api.FrameEnd, []byte("the terminal ended unproven")); err != nil {
		t.Fatal(err)
	}
	if f := h.frame(); f.Kind != api.FrameError || string(f.Data) != "the terminal ended unproven" {
		t.Fatalf("frame %+v, want the end's reason", f)
	}
	if err := h.result(); err == nil {
		t.Fatal("an abnormal end succeeded")
	}
}

// An initiation the helper cannot use never reaches the daemon.
func TestHelperRefusesAnInvalidInitiation(t *testing.T) {
	d := newFakeDaemon(t, nil, nil)
	for name, init := range map[string]initiation{
		"no size":     {Socket: d.socket, Token: testToken},
		"short token": {Socket: d.socket, Token: "a", Width: 80, Height: 24},
		"no socket":   {Token: testToken, Width: 80, Height: 24},
	} {
		t.Run(name, func(t *testing.T) {
			h := startHelper(t, init)
			if f := h.frame(); f.Kind != api.FrameError || string(f.Data) != "invalid private attachment initiation" {
				t.Fatalf("frame %+v", f)
			}
			h.result()
		})
	}
	if len(d.accepts) != 0 {
		t.Fatal("an invalid initiation reached the daemon")
	}
}
