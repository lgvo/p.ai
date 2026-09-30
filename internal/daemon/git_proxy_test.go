package daemon

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type gitProxyFixture struct {
	m        *endpointManager
	ctx      context.Context
	cancel   context.CancelFunc
	upstream chan net.Conn
}

func newGitProxyFixture(t *testing.T) *gitProxyFixture {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := os.MkdirTemp("/tmp", "p-git-")
	if err != nil {
		t.Fatal(err)
	}
	m, err := newEndpointManager(prefix, listener.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &gitProxyFixture{m: m, ctx: ctx, cancel: cancel, upstream: make(chan net.Conn, 128)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			f.upstream <- c
		}
	}()
	t.Cleanup(func() {
		cancel()
		m.Close()
		listener.Close()
		<-done
		close(f.upstream)
		for c := range f.upstream {
			c.Close()
		}
		os.RemoveAll(prefix)
	})
	return f
}
func (f *gitProxyFixture) client(t *testing.T, session int) *net.UnixConn {
	t.Helper()
	uuid := fmt.Sprintf("%08x-1111-4111-8111-111111111111", session)
	dir, err := f.m.Ensure(f.ctx, uuid)
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: filepath.Join(dir, "git.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(5 * time.Second))
	return c
}
func (f *gitProxyFixture) remote(t *testing.T) net.Conn {
	t.Helper()
	select {
	case c := <-f.upstream:
		c.SetDeadline(time.Now().Add(5 * time.Second))
		t.Cleanup(func() { c.Close() })
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not connect upstream")
		return nil
	}
}
func gitProxySlots(t *testing.T, m *endpointManager, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(m.gitSlots) != want {
		if time.Now().After(deadline) {
			t.Fatalf("Git slots = %d, want %d", len(m.gitSlots), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestGitProxyUpstreamEOFReleasesSlots(t *testing.T) {
	f := newGitProxyFixture(t)
	// More than the complete global pool. Clients keep their input side open
	// after every upstream closes, so they cannot cooperate in releasing slots.
	for i := 0; i < 70; i++ {
		c := f.client(t, 1)
		r := f.remote(t)
		if _, err := r.Write([]byte("response")); err != nil {
			t.Fatal(err)
		}
		r.Close()
		got, err := io.ReadAll(c)
		if err != nil || string(got) != "response" {
			t.Fatalf("upstream response = %q %v", got, err)
		}
		gitProxySlots(t, f.m, 0)
	}
	c := f.client(t, 2)
	r := f.remote(t)
	r.Write([]byte("sibling"))
	r.Close()
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "sibling" {
		t.Fatalf("sibling unavailable: %q %v", got, err)
	}
	gitProxySlots(t, f.m, 0)
	f.m.mu.Lock()
	defer f.m.mu.Unlock()
	if len(f.m.conns) != 0 || len(f.m.gitBySession) != 0 {
		t.Fatalf("proxy accounting remains: conns=%d sessions=%d", len(f.m.conns), len(f.m.gitBySession))
	}
}

func TestGitProxySessionAndGlobalAdmission(t *testing.T) {
	f := newGitProxyFixture(t)
	for session := 1; session <= 8; session++ {
		for i := 0; i < gitConnectionsPerSession; i++ {
			f.client(t, session)
			f.remote(t)
		}
		rejected := f.client(t, session)
		if got, err := io.ReadAll(rejected); err != nil || len(got) != 0 {
			t.Fatalf("session excess not closed: %q %v", got, err)
		}
		if session == 1 {
			// A busy session leaves its sibling admitted and usable.
			c := f.client(t, 9)
			r := f.remote(t)
			r.Write([]byte("available"))
			r.Close()
			if got, err := io.ReadAll(c); err != nil || string(got) != "available" {
				t.Fatalf("sibling rejected: %q %v", got, err)
			}
			gitProxySlots(t, f.m, gitConnectionsPerSession)
		}
	}
	gitProxySlots(t, f.m, 64)
	rejected := f.client(t, 10)
	if got, err := io.ReadAll(rejected); err != nil || len(got) != 0 {
		t.Fatalf("global excess not closed: %q %v", got, err)
	}
	f.m.Close()
	gitProxySlots(t, f.m, 0)
}

func TestGitProxyDuplexAndGuestHalfClose(t *testing.T) {
	f := newGitProxyFixture(t)
	c := f.client(t, 1)
	r := f.remote(t)
	// Server output is available while guest input remains open.
	r.Write([]byte("ready"))
	ready := make([]byte, 5)
	if _, err := io.ReadFull(c, ready); err != nil || string(ready) != "ready" {
		t.Fatalf("duplex output blocked: %q %v", ready, err)
	}
	c.Write([]byte("request"))
	c.CloseWrite()
	request, err := io.ReadAll(r)
	if err != nil || string(request) != "request" {
		t.Fatalf("input half-close not forwarded: %q %v", request, err)
	}
	// A response generated only after EOF must not be truncated.
	r.Write([]byte("complete"))
	r.(*net.TCPConn).CloseWrite()
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "complete" {
		t.Fatalf("half-close response truncated: %q %v", got, err)
	}
	gitProxySlots(t, f.m, 0)
}

func TestGitProxyCancellationShutdownAndDeadline(t *testing.T) {
	for _, cause := range []string{"context", "shutdown", "lifetime"} {
		t.Run(cause, func(t *testing.T) {
			f := newGitProxyFixture(t)
			var c *net.UnixConn
			if cause == "lifetime" {
				// Directly drive the same connection handler with a short independent
				// deadline; production always passes the Git SSH ten-minute maximum.
				path := filepath.Join(f.m.prefix, "fixture.sock")
				l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				defer l.Close()
				c, err = net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				guest, err := l.AcceptUnix()
				if err != nil {
					t.Fatal(err)
				}
				if !f.m.admitGit(guest, "fixture") {
					t.Fatal("fixture admission failed")
				}
				go func() { defer f.m.releaseGit(guest, "fixture"); f.m.proxyGit(f.ctx, guest, 100*time.Millisecond) }()
				c.SetDeadline(time.Now().Add(5 * time.Second))
			} else {
				c = f.client(t, 1)
			}
			r := f.remote(t)
			switch cause {
			case "context":
				f.cancel()
			case "shutdown":
				f.m.Close()
			}
			if got, err := io.ReadAll(c); err != nil || len(got) != 0 {
				t.Fatalf("guest not closed on %s: %q %v", cause, got, err)
			}
			if got, err := io.ReadAll(r); err != nil || len(got) != 0 {
				t.Fatalf("upstream not closed on %s: %q %v", cause, got, err)
			}
			gitProxySlots(t, f.m, 0)
		})
	}
}
