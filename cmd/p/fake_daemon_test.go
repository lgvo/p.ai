package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
)

// fakeDaemon serves the control protocol on a Unix socket with the daemon's
// own connection handling and method table; each test registers the methods
// it needs. It records every call after the hello.
type fakeDaemon struct {
	socket string
	table  *control.MethodTable
	mu     sync.Mutex
	calls  []fakeCall
	// lose counts, per method, the replies to drop by closing the
	// connection after the handler ran.
	lose map[string]int
}

type fakeCall struct {
	method string
	params json.RawMessage
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	d := &fakeDaemon{socket: filepath.Join(t.TempDir(), "control.sock"), table: control.NewMethodTable(), lose: map[string]int{}}
	listener, err := net.Listen("unix", d.socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); listener.Close() })
	control.Register(d.table, api.SystemHello, func(context.Context, api.HelloRequest) (api.HelloResult, error) {
		return api.HelloResult{Protocol: api.Protocol, BuildVersion: "test", InstanceID: "7d3e0000-0000-4000-8000-000000000000",
			Capabilities: []api.Capability{api.CapabilityGit, api.CapabilityRuntime, api.CapabilityServices}}, nil
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go control.ServeConn(ctx, conn, d.handler(conn))
		}
	}()
	return d
}

func (d *fakeDaemon) handler(conn net.Conn) control.Handler {
	dispatch := d.table.Handler()
	return func(ctx context.Context, method string, params json.RawMessage) (any, *api.Error) {
		if method != api.SystemHello.Name {
			d.mu.Lock()
			d.calls = append(d.calls, fakeCall{method, append(json.RawMessage(nil), params...)})
			d.mu.Unlock()
		}
		result, answer := dispatch(ctx, method, params)
		d.mu.Lock()
		lost := d.lose[method] > 0
		if lost {
			d.lose[method]--
		}
		d.mu.Unlock()
		if lost {
			conn.Close()
		}
		return result, answer
	}
}

// handle serves m with h.
func handle[Req api.Request, Res any](d *fakeDaemon, m api.Method[Req, Res], h func(Req) (Res, error)) {
	control.Register(d.table, m, func(_ context.Context, r Req) (Res, error) { return h(r) })
}

// loseReply drops the next reply to method after its handler ran.
func (d *fakeDaemon) loseReply(method string) {
	d.mu.Lock()
	d.lose[method]++
	d.mu.Unlock()
}

// methods lists the methods called, in order.
func (d *fakeDaemon) methods() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var names []string
	for _, c := range d.calls {
		names = append(names, c.method)
	}
	return names
}

// params lists the params of each call of method, in order.
func (d *fakeDaemon) params(method string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var all []string
	for _, c := range d.calls {
		if c.method == method {
			all = append(all, string(c.params))
		}
	}
	return all
}

// count is how many times method was called.
func (d *fakeDaemon) count(method string) int { return len(d.params(method)) }

// cliRun is one run of a client command.
type cliRun struct {
	stdout, stderr string
	code           int
	err            error
	// pauses are the pauses between automatic repeats.
	pauses int
}

// runCLI runs a client command against d with ctx and returns what it
// printed and its exit status.
func runCLI(ctx context.Context, d *fakeDaemon, args ...string) cliRun {
	var stdout, stderr bytes.Buffer
	var r cliRun
	c := &cli{stdout: &stdout, stderr: &stderr, pause: func(ctx context.Context, _ time.Duration) error {
		r.pauses++
		return ctx.Err()
	}}
	// The socket flag goes before any "--", after which every argument is
	// positional.
	end := len(args)
	for i, arg := range args {
		if arg == "--" {
			end = i
			break
		}
	}
	args = append(append(append([]string{}, args[:end]...), "--socket", d.socket), args[end:]...)
	r.err = c.runCommand(ctx, args)
	r.code = exitStatus(r.err)
	if r.err != nil {
		stderr.WriteString(r.err.Error())
	}
	r.stdout, r.stderr = stdout.String(), stderr.String()
	return r
}

// operation is an operation as the daemon answers it.
func operation(id, kind, status string) api.Operation {
	return api.Operation{ID: id, Key: "k", Kind: kind, Project: "app", Status: status, Phase: "accepted", Request: json.RawMessage(`{}`),
		CreatedAt: "2026-11-03T00:00:00Z", UpdatedAt: "2026-11-03T00:00:00Z"}
}

// inspectRunning serves operation.inspect with a running operation, as
// p operation wait reads it first.
func inspectRunning(d *fakeDaemon) {
	handle(d, api.OperationInspect, func(r api.OperationInspectRequest) (api.OperationResult, error) {
		return api.OperationResult{Operation: operation(r.ID, "session.create", "running")}, nil
	})
}

func contains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("output does not contain %q:\n%s", want, got)
	}
}

// countLines is how many lines of text contain s.
func countLines(text, s string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, s) {
			n++
		}
	}
	return n
}
