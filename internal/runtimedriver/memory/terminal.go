package memory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// attachment is one terminal's native state: the guest process attached to
// the session host, and the channel a driver holds to it. Its fields other
// than the streams are guarded by the World's lock.
type attachment struct {
	id      string
	runtime *object
	driver  *Driver // the driver whose channel carries it
	input   *stream // what the client writes and the guest reads
	output  *stream // what the guest writes and the client reads
	size    runtimedriver.Size

	channelOpen  bool
	processEnded bool
	ended        chan runtimedriver.Completion
}

// endProcess ends the guest process. While the channel is open, the driver's
// witness sees it end and the terminal ends proven.
func (a *attachment) endProcess() {
	if a.processEnded {
		return
	}
	a.processEnded = true
	a.closeChannel(true)
}

// dropChannel closes only the channel; the terminal ends unproven and the
// guest process keeps running.
func (a *attachment) dropChannel() { a.closeChannel(false) }

func (a *attachment) closeChannel(proven bool) {
	if !a.channelOpen {
		return
	}
	a.channelOpen = false
	a.input.close()
	a.output.close()
	a.ended <- runtimedriver.Completion{Proven: proven}
	close(a.ended)
}

// OpenTerminal attaches to the session host of a running runtime whose host
// is ready. The attachment's record is the completion witness: it exists
// before any input flows.
func (d *Driver) OpenTerminal(_ context.Context, t runtimedriver.Target, size runtimedriver.Size) (runtimedriver.Terminal, error) {
	if err := d.alive(); err != nil {
		return nil, err
	}
	if size.Width <= 0 || size.Height <= 0 {
		return nil, errors.New("invalid terminal size")
	}
	w := d.w
	o, err := w.readyTarget(t)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	a := &attachment{
		id: fmt.Sprintf("terminal-%d", w.next()), runtime: o, driver: d, input: newStream(), output: newStream(), size: size,
		channelOpen: true, ended: make(chan runtimedriver.Completion, 1),
	}
	w.attachments[a.id] = a
	return &terminal{w: w, a: a}, nil
}

// readyTarget is the running runtime of t, at t.At when it is set, whose
// session host is ready; ErrNotReady otherwise.
func (w *World) readyTarget(t runtimedriver.Target) (*object, error) {
	if err := w.checkSession(t.Session); err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	o, err := w.observe(t.Session)
	switch {
	case err != nil:
		return nil, err
	case o == nil:
		return nil, fmt.Errorf("the session has no runtime: %w", runtimedriver.ErrNotReady)
	case t.At != "" && o.incarnation != t.At:
		return nil, fmt.Errorf("the runtime is at incarnation %s, not %s: %w", o.incarnation, t.At, runtimedriver.ErrNotReady)
	case o.terminating || o.state != runtimedriver.StateRunning || !o.host.ready:
		return nil, runtimedriver.ErrNotReady
	}
	return o, nil
}

// terminal is the client side of an attachment.
type terminal struct {
	w *World
	a *attachment
}

func (t *terminal) ID() string { return t.a.id }

func (t *terminal) Read(p []byte) (int, error) { return t.a.output.Read(p) }

func (t *terminal) Write(p []byte) (int, error) { return t.a.input.Write(p) }

// Close hangs up an open channel: the attached guest process ends, as the
// driver's witness sees. A channel already closed reaches nothing.
func (t *terminal) Close() error {
	t.w.mu.Lock()
	defer t.w.mu.Unlock()
	if t.a.channelOpen {
		t.a.endProcess()
	}
	return nil
}

func (t *terminal) Resize(size runtimedriver.Size) error {
	if size.Width <= 0 || size.Height <= 0 {
		return errors.New("invalid terminal size")
	}
	t.w.mu.Lock()
	defer t.w.mu.Unlock()
	if !t.a.channelOpen {
		return errors.New("the terminal has ended")
	}
	t.a.size = size
	return nil
}

func (t *terminal) Ended() <-chan runtimedriver.Completion { return t.a.ended }

// GuestTerminal is the session host's side of an attachment: Read returns
// what the client wrote, and the client reads what Write writes.
type GuestTerminal struct {
	w *World
	a *attachment
}

// Terminal is the guest side of the attachment the terminal id names.
func (w *World) Terminal(id string) *GuestTerminal {
	w.mu.Lock()
	defer w.mu.Unlock()
	a := w.attachments[id]
	if a == nil {
		panic(fmt.Sprintf("memory: no terminal %s", id))
	}
	return &GuestTerminal{w: w, a: a}
}

func (g *GuestTerminal) Read(p []byte) (int, error) { return g.a.input.Read(p) }

func (g *GuestTerminal) Write(p []byte) (int, error) { return g.a.output.Write(p) }

// Size is the PTY's size as the guest sees it.
func (g *GuestTerminal) Size() runtimedriver.Size {
	g.w.mu.Lock()
	defer g.w.mu.Unlock()
	return g.a.size
}

// EndProcess ends the attached guest process.
func (g *GuestTerminal) EndProcess() {
	g.w.mu.Lock()
	defer g.w.mu.Unlock()
	g.a.endProcess()
}

// DropChannel closes only the attachment's channel.
func (g *GuestTerminal) DropChannel() {
	g.w.mu.Lock()
	defer g.w.mu.Unlock()
	g.a.dropChannel()
}

// stream is one direction of a terminal. Writes never wait; a read waits
// for data or for the stream to close, then returns io.EOF.
type stream struct {
	mu     sync.Mutex
	more   *sync.Cond
	buf    bytes.Buffer
	closed bool
}

func newStream() *stream {
	s := &stream{}
	s.more = sync.NewCond(&s.mu)
	return s
}

func (s *stream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, io.ErrClosedPipe
	}
	s.more.Broadcast()
	return s.buf.Write(p)
}

func (s *stream) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.buf.Len() == 0 && !s.closed {
		s.more.Wait()
	}
	if s.buf.Len() == 0 {
		return 0, io.EOF
	}
	return s.buf.Read(p)
}

func (s *stream) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.more.Broadcast()
}
