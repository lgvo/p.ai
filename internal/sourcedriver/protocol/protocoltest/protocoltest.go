// Package protocoltest serves a source driver over the protocol to a client
// in the same process, for tests: the conformance suite runs over the
// protocol with each driver's own fixture.
package protocoltest

import (
	"context"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/protocol"
)

// Remote is a driver served on one end of a socket pair, with a client
// connected to it on the other.
type Remote struct {
	t     *testing.T
	start func() (sourcedriver.Driver, func())
	hello protocol.Hello

	client *protocol.Client
	end    *net.UnixConn // the driver's end, to lose the connection with
	kill   func()
	done   chan error // Serve's return
}

// Serve serves a driver start makes, with hello h, and connects a client.
// start returns the driver and how to kill it, as its process's death
// would: once killed, nothing it started may change its backend.
func Serve(t *testing.T, start func() (sourcedriver.Driver, func()), h protocol.Hello) *Remote {
	t.Helper()
	r := &Remote{t: t, start: start, hello: h}
	r.connect()
	t.Cleanup(r.stop)
	return r
}

// Driver is the client in front of the served driver.
func (r *Remote) Driver() sourcedriver.Driver { return r.client }

// Restart loses the connection and kills the driver, as the death of its
// process would: calls in flight end as the protocol says for a lost
// connection, and no answer comes. It then serves a new driver over the
// same backend.
func (r *Remote) Restart() {
	r.stop()
	r.connect()
}

func (r *Remote) connect() {
	r.t.Helper()
	core, end := pair(r.t)
	d, kill := r.start()
	r.end, r.kill, r.done = end, kill, make(chan error, 1)
	go func() {
		r.done <- protocol.Serve(context.Background(), end, func(context.Context, protocol.Hello) (sourcedriver.Driver, error) { return d, nil })
	}()
	client, err := protocol.Connect(r.t.Context(), core, r.hello)
	if err != nil {
		r.t.Fatalf("Connect: %v", err)
	}
	r.client = client
}

func (r *Remote) stop() {
	r.end.Close()
	r.kill()
	<-r.done
	r.client.Close()
}

// pair is a connected pair of Unix stream sockets: core's end and the
// driver's.
func pair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	conns := make([]*net.UnixConn, 2)
	for i, fd := range fds {
		f := os.NewFile(uintptr(fd), "pair")
		c, err := net.FileConn(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		conns[i] = c.(*net.UnixConn)
	}
	return conns[0], conns[1]
}
