package attachment

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

const testOperation = "/1.0/operations/11111111-1111-1111-1111-111111111111"
const testInstance = "p-22222222-2222-2222-2222-222222222222"

type nativeFixture struct {
	socket           string
	started          chan struct{}
	controlConnected chan struct{}
	controlClosed    chan struct{}
	ended            atomic.Bool
	removed          atomic.Bool
	failWait         chan struct{}
	resize           chan map[string]any
}

func newNativeFixture(t *testing.T) *nativeFixture {
	t.Helper()
	f := &nativeFixture{socket: filepath.Join(t.TempDir(), "incus.sock"), started: make(chan struct{}), controlConnected: make(chan struct{}), controlClosed: make(chan struct{}), resize: make(chan map[string]any, 1), failWait: make(chan struct{})}
	listener, err := net.Listen("unix", f.socket)
	if err != nil {
		t.Fatal(err)
	}
	upgrader := websocket.Upgrader{}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("project") != "p-test" {
			t.Error("native project confinement lost")
			http.Error(w, "project", 400)
			return
		}
		switch r.URL.Path {
		case "/1.0/instances/" + testInstance + "/exec":
			var body struct {
				Command     []string          `json:"command"`
				Interactive bool              `json:"interactive"`
				Wait        bool              `json:"wait-for-websocket"`
				Environment map[string]string `json:"environment"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Command) != 1 || body.Command[0] != "/usr/libexec/p/attach" || !body.Interactive || !body.Wait || body.Environment["TERM"] != "xterm-256color" {
				t.Error("untrusted exec request")
				http.Error(w, "exec", 400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"type": "async", "operation": testOperation, "metadata": map[string]any{"status_code": 103, "metadata": map[string]any{"fds": map[string]string{"0": "data-secret", "control": "control-secret"}}}})
		case testOperation + "/websocket":
			ws, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer ws.Close()
			if r.URL.Query().Get("secret") == "control-secret" {
				close(f.controlConnected)
				// Mirrors pinned Incus 7.4: no NextReader (and therefore no nonce
				// pong) until instance.Exec has succeeded and the PTY exists.
				<-f.started
				defer close(f.controlClosed)
				for {
					_, raw, e := ws.ReadMessage()
					if e != nil {
						return
					}
					var value map[string]any
					if json.Unmarshal(raw, &value) == nil {
						select {
						case f.resize <- value:
						default:
						}
					}
				}
			} else {
				for {
					kind, raw, e := ws.ReadMessage()
					if e != nil {
						return
					}
					if e = ws.WriteMessage(kind, raw); e != nil {
						return
					}
				}
			}
		case testOperation + "/wait":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for !f.ended.Load() {
				select {
				case <-r.Context().Done():
					return
				case <-f.failWait:
					w.Write([]byte("{"))
					return
				case <-ticker.C:
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"type": "sync", "metadata": map[string]any{"status_code": 200, "metadata": map[string]any{"return": 0}}})
		case testOperation:
			if f.removed.Load() {
				http.NotFound(w, r)
				return
			}
			code := 103
			if f.ended.Load() {
				code = 200
			}
			json.NewEncoder(w).Encode(map[string]any{"type": "sync", "metadata": map[string]any{"status_code": code, "metadata": map[string]any{"return": 0}}})
		default:
			t.Errorf("unexpected Incus path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})}
	go server.Serve(listener)
	t.Cleanup(func() {
		select {
		case <-f.started:
		default:
			close(f.started)
		}
		server.Close()
	})
	return f
}
func (f *nativeFixture) launch() control.AttachmentLaunch {
	return control.AttachmentLaunch{V: 1, Socket: f.socket, Spec: plugin.AttachSpec{Project: "p-test", Instance: testInstance, Argv: []string{"/usr/libexec/p/attach"}}}
}
func await(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out: " + name)
	}
}
func TestNativeConfirmationWaitsForExecPongAndRunningOperation(t *testing.T) {
	for _, alreadyEnded := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "exited"}[alreadyEnded], func(t *testing.T) {
			f := newNativeFixture(t)
			f.ended.Store(alreadyEnded)
			n, err := newNative(f.launch())
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				n.closeChannel()
				if n.cancelOperation != nil {
					n.cancelOperation()
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- n.establish(ctx, f.launch(), 80, 24) }()
			await(t, f.controlConnected, "control upgrade")
			select {
			case err := <-result:
				if alreadyEnded && err != nil {
					close(f.started)
					return
				}
				t.Fatalf("upgrade became confirmation: %v", err)
			case <-time.After(30 * time.Millisecond):
			}
			close(f.started)
			if err = <-result; (err != nil) != alreadyEnded {
				t.Fatalf("ended=%v result=%v", alreadyEnded, err)
			}
		})
	}
}

type leaseFixture struct {
	socket     string
	confirmed  chan struct{}
	closed     chan struct{}
	connection chan net.Conn
	confirms   atomic.Int32
}

func newLeaseFixture(t *testing.T, launch control.AttachmentLaunch, gates ...<-chan struct{}) *leaseFixture {
	t.Helper()
	f := &leaseFixture{socket: filepath.Join(t.TempDir(), "rpc.sock"), confirmed: make(chan struct{}), closed: make(chan struct{}), connection: make(chan net.Conn, 1)}
	listener, err := net.Listen("unix", f.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		defer close(f.closed)
		f.connection <- conn
		reader := bufio.NewScanner(conn)
		for reader.Scan() {
			var req struct {
				ID     int            `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if json.Unmarshal(reader.Bytes(), &req) != nil {
				return
			}
			var result any = map[string]int{"v": 1}
			switch req.Method {
			case "attachment.claim":
				if req.Params["token"] != strings.Repeat("a", 64) {
					return
				}
				result = launch
			case "attachment.confirm":
				if f.confirms.Add(1) == 1 {
					close(f.confirmed)
				}
				if len(gates) > 0 {
					<-gates[0]
				}
			case "attachment.ping":
			default:
				return
			}
			if json.NewEncoder(conn).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}) != nil {
				return
			}
		}
	}()
	return f
}
func TestHelperCarrierLossRetainsLeaseUntilNativeTeardown(t *testing.T) {
	native := newNativeFixture(t)
	close(native.started)
	lease := newLeaseFixture(t, native.launch())
	helperConn, clientConn := net.Pipe()
	defer clientConn.Close()
	helperDone := make(chan error, 1)
	go func() { defer helperConn.Close(); helperDone <- runHelper(&carrier{Conn: helperConn}) }()
	client := &carrier{Conn: clientConn}
	if err := client.writeJSON(carrierInit, initiation{Socket: lease.socket, Token: strings.Repeat("a", 64), Width: 80, Height: 24}); err != nil {
		t.Fatal(err)
	}
	ready, err := client.read()
	if err != nil || ready.kind != carrierReady {
		t.Fatalf("not ready: %+v %v", ready, err)
	}
	await(t, lease.confirmed, "confirmation")
	if err = client.write(carrierInput, []byte("echo")); err != nil {
		t.Fatal(err)
	}
	output, err := client.read()
	if err != nil || output.kind != carrierOutput || string(output.data) != "echo" {
		t.Fatalf("PTY bytes: %+v %v", output, err)
	}
	if err = client.writeJSON(carrierResize, size{100, 40}); err != nil {
		t.Fatal(err)
	}
	select {
	case resize := <-native.resize:
		if resize["command"] != "window-resize" {
			t.Fatal(resize)
		}
	case <-time.After(time.Second):
		t.Fatal("resize missing")
	}
	client.Close()
	await(t, native.controlClosed, "temporary exec kill")
	select {
	case <-lease.closed:
		t.Fatal("presence released before temporary exec completed")
	case <-time.After(100 * time.Millisecond):
	}
	native.ended.Store(true)
	select {
	case <-helperDone:
	case <-time.After(3 * time.Second):
		t.Fatal("helper did not complete teardown")
	}
	await(t, lease.closed, "lease release")
	if lease.confirms.Load() != 1 {
		t.Fatal("unexpected reconfirmation")
	}
}
func TestDaemonLossImmediatelyClosesChannelWithoutReregistering(t *testing.T) {
	native := newNativeFixture(t)
	close(native.started)
	lease := newLeaseFixture(t, native.launch())
	helperConn, clientConn := net.Pipe()
	defer clientConn.Close()
	helperDone := make(chan error, 1)
	go func() { defer helperConn.Close(); helperDone <- runHelper(&carrier{Conn: helperConn}) }()
	client := &carrier{Conn: clientConn}
	if err := client.writeJSON(carrierInit, initiation{lease.socket, strings.Repeat("a", 64), 80, 24}); err != nil {
		t.Fatal(err)
	}
	if f, e := client.read(); e != nil || f.kind != carrierReady {
		t.Fatal(f, e)
	}
	conn := <-lease.connection
	conn.Close()
	await(t, native.controlClosed, "daemon-loss teardown")
	// Drain optional diagnostic so the helper can finish its private carrier.
	go io.Copy(io.Discard, clientConn)
	select {
	case <-helperDone:
	case <-time.After(3 * time.Second):
		t.Fatal("helper survived lost lease")
	}
	if lease.confirms.Load() != 1 {
		t.Fatal("lease was re-registered")
	}
}
func TestLeaseReaderDetectsIdleEOF(t *testing.T) {
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "rpc.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c, e := listener.Accept()
		if e == nil {
			c.Close()
		}
	}()
	rpc, err := dialLease(context.Background(), listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.fail()
	await(t, rpc.lost, "idle lease EOF")
	wg.Wait()
}

func TestFailedNativeLaunchNeverConfirms(t *testing.T) {
	native := newNativeFixture(t)
	close(native.started)
	native.ended.Store(true)
	lease := newLeaseFixture(t, native.launch())
	helperConn, clientConn := net.Pipe()
	defer clientConn.Close()
	done := make(chan error, 1)
	go func() { defer helperConn.Close(); done <- runHelper(&carrier{Conn: helperConn}) }()
	client := &carrier{Conn: clientConn}
	if err := client.writeJSON(carrierInit, initiation{lease.socket, strings.Repeat("a", 64), 80, 24}); err != nil {
		t.Fatal(err)
	}
	frame, err := client.read()
	if err != nil || frame.kind != carrierError {
		t.Fatal(frame, err)
	}
	if err = <-done; err == nil {
		t.Fatal("failed exec succeeded")
	}
	if lease.confirms.Load() != 0 {
		t.Fatal("failed launch asserted presence")
	}
	await(t, lease.closed, "failed-launch release")
}
func TestCarrierLossDuringConfirmationKeepsReachableLease(t *testing.T) {
	native := newNativeFixture(t)
	close(native.started)
	gate := make(chan struct{})
	lease := newLeaseFixture(t, native.launch(), gate)
	helperConn, clientConn := net.Pipe()
	defer clientConn.Close()
	done := make(chan error, 1)
	go func() { defer helperConn.Close(); done <- runHelper(&carrier{Conn: helperConn}) }()
	client := &carrier{Conn: clientConn}
	if err := client.writeJSON(carrierInit, initiation{lease.socket, strings.Repeat("a", 64), 80, 24}); err != nil {
		t.Fatal(err)
	}
	await(t, lease.confirmed, "in-flight confirmation")
	client.Close()
	await(t, native.controlClosed, "in-flight-confirm carrier teardown")
	select {
	case <-lease.closed:
		t.Fatal("carrier cancellation closed in-flight lease")
	case <-time.After(50 * time.Millisecond):
	}
	native.ended.Store(true)
	close(gate)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("teardown did not finish")
	}
	await(t, lease.closed, "post-completion release")
}

func TestNativeCompletionWitnessSurvivesOperationExpiry(t *testing.T) {
	f := newNativeFixture(t)
	close(f.started)
	n, err := newNative(f.launch())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		n.closeChannel()
		if n.cancelOperation != nil {
			n.cancelOperation()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = n.establish(ctx, f.launch(), 80, 24); err != nil {
		t.Fatal(err)
	}
	f.removed.Store(true)
	f.ended.Store(true)
	await(t, n.observerDone, "retained operation completion")
	if err = n.teardown(ctx); err != nil {
		t.Fatal("operation expiry lost completion proof", err)
	}
}

func TestObserverFailureStartsTeardownDuringConfirmation(t *testing.T) {
	native := newNativeFixture(t)
	close(native.started)
	gate := make(chan struct{})
	lease := newLeaseFixture(t, native.launch(), gate)
	helperConn, clientConn := net.Pipe()
	defer clientConn.Close()
	done := make(chan error, 1)
	go func() { defer helperConn.Close(); done <- runHelper(&carrier{Conn: helperConn}) }()
	client := &carrier{Conn: clientConn}
	if err := client.writeJSON(carrierInit, initiation{lease.socket, strings.Repeat("a", 64), 80, 24}); err != nil {
		t.Fatal(err)
	}
	await(t, lease.confirmed, "in-flight confirmation")
	close(native.failWait)
	await(t, native.controlClosed, "failed observer immediate teardown")
	select {
	case <-lease.closed:
		t.Fatal("observer failure released reachable lease")
	case <-time.After(50 * time.Millisecond):
	}
	native.ended.Store(true)
	close(gate)
	go io.Copy(io.Discard, clientConn)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("failed observer teardown did not finish")
	}
	await(t, lease.closed, "native-proved release after observer failure")
}
