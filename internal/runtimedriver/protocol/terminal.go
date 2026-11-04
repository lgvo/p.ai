package protocol

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/lgvo/p.ai/internal/driverwire"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// resizeWait bounds a resize, which has no context of its own.
const resizeWait = 10 * time.Second

// closeWait bounds how long a closed terminal's connection stays open for
// the driver to report the terminal's end.
const closeWait = 30 * time.Second

// OpenTerminal opens a terminal on a connection of its own: a socket pair
// whose far end goes to the driver with the request.
func (cl *Client) OpenTerminal(ctx context.Context, t runtimedriver.Target, size runtimedriver.Size) (runtimedriver.Terminal, error) {
	local, remote, err := driverwire.SocketPair("terminal connection")
	if err != nil {
		return nil, fmt.Errorf("a connection for the terminal: %w", err)
	}
	var res terminalParams
	err = cl.c.Call(ctx, methodOpenTerminal, openTerminalParams{Target: toTarget(t), Size: wireSize(size)}, nil, remote, &res)
	remote.Close()
	if err == nil && res.Terminal == "" {
		err = fmt.Errorf("%s: the driver named no terminal: %w", methodOpenTerminal, runtimedriver.ErrUnknownOutcome)
	}
	if err != nil {
		local.Close()
		return nil, err
	}
	term := &terminal{cl: cl, id: res.Terminal, conn: local, ended: make(chan runtimedriver.Completion, 1), over: make(chan struct{})}
	go term.wait()
	return term, nil
}

// terminal is a driver's terminal seen from core: its bytes on its own
// connection, its resizing and end as calls on the driver's.
type terminal struct {
	cl    *Client
	id    string
	conn  *net.UnixConn
	ended chan runtimedriver.Completion
	over  chan struct{} // closes once the end was delivered

	mu     sync.Mutex
	closed bool
}

var errTerminalClosed = errors.New("the terminal is closed")

func (t *terminal) ID() string { return t.id }

func (t *terminal) Read(p []byte) (int, error) { return t.conn.Read(p) }

func (t *terminal) Write(p []byte) (int, error) {
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return 0, errTerminalClosed
	}
	return t.conn.Write(p)
}

func (t *terminal) Resize(size runtimedriver.Size) error {
	ctx, cancel := context.WithTimeout(context.Background(), resizeWait)
	defer cancel()
	return t.cl.c.Call(ctx, methodResizeTerminal, resizeTerminalParams{Terminal: t.id, Size: wireSize(size)}, nil, nil, nil)
}

func (t *terminal) Ended() <-chan runtimedriver.Completion { return t.ended }

// Close hangs the terminal up by closing the connection's write side. The
// connection closes once the driver reported the end, or after closeWait.
func (t *terminal) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()
	t.conn.CloseWrite()
	go func() {
		wait := time.NewTimer(closeWait)
		defer wait.Stop()
		select {
		case <-t.over:
		case <-wait.C:
		}
		t.conn.Close()
	}()
	return nil
}

// wait delivers the terminal's end: proven only when the driver says so.
// A lost connection ends it unproven.
func (t *terminal) wait() {
	var res waitTerminalResult
	err := t.cl.c.Call(context.Background(), methodWaitTerminal, terminalParams{Terminal: t.id}, nil, nil, &res)
	t.ended <- runtimedriver.Completion{Proven: err == nil && res.Proven}
	close(t.ended)
	close(t.over)
}
