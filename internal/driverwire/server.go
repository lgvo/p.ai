package driverwire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"github.com/lgvo/p.ai/internal/strictjson"
)

// Open makes the driver a hello configures, from config, the driver's own
// configuration, and core, the settings core owns that the driver needs. It
// returns the handler of the driver's calls and the description the driver
// answers the hello with.
type Open[C, D any] func(ctx context.Context, config json.RawMessage, core C) (Handler, D, error)

// Handler runs one call core made and returns its result, or why it failed.
// Each call runs in a goroutine of its own, with a context that ends when
// core cancels the call or the connection is lost.
type Handler func(ctx context.Context, c Call) (any, error)

// Call is one call core made.
type Call struct {
	Method string
	Params json.RawMessage
	// FDs are the descriptors the request carried, in the order the
	// protocol's Descriptors name them for the method, and none for another
	// method. The handler owns them.
	FDs []*os.File

	s  *server
	id uint64
}

// Gate is the call's gate when core gave it one, as given says, and
// otherwise a gate that refuses. The gate asks core to run the call's gate
// and returns its answer; the connection's loss refuses it.
func (c Call) Gate(given bool) func() error {
	if given {
		return func() error { return c.s.callCore(methodGate, gateParams{Call: c.id}, nil) }
	}
	return noGate
}

// Callback asks core to run its callback method that this call lent, with
// params, an object, and decodes core's result into result, which is nil
// for a callback that answers null. It returns core's refusal, and the
// connection's loss refuses it.
func (c Call) Callback(method string, params, result any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	return c.s.callCore(method, callbackParams{Call: c.id, Params: raw}, result)
}

// Params decodes a call's parameters strictly. Parameters of another shape
// refuse the call before the driver sees it.
func Params[P any](raw json.RawMessage) (P, error) {
	var p P
	if err := strictjson.Decode(raw, &p); err != nil {
		return p, errInvalidParams{err}
	}
	return p, nil
}

// Serve answers the hello of protocol p on uc with the driver open makes,
// then serves that driver's calls until the connection is lost or ctx ends.
// Calls in flight then have their contexts cancelled. Serve owns uc. It
// returns nil once core closed the connection or ctx ended, and otherwise
// why the connection ended, such as a refused hello.
func Serve[C, D any](ctx context.Context, uc *net.UnixConn, p *Protocol, open Open[C, D]) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &server{c: newConn(uc, p, len(p.Descriptors) > 0), ctx: ctx, inflight: map[uint64]context.CancelFunc{}}
	s.hello = func(id uint64, raw json.RawMessage) error { return hello(s, open, id, raw) }
	read := make(chan struct{})
	go func() {
		s.c.readLoop(s)
		close(read)
	}()
	select {
	case <-read:
	case <-ctx.Done():
		s.c.fail(ctx.Err())
		<-read
	}
	cancel()
	if cause := s.c.lostCause(); !errors.Is(cause, io.EOF) && !errors.Is(cause, context.Canceled) {
		return cause
	}
	return nil
}

// server is the driver's side of one connection.
type server struct {
	c     *conn
	ctx   context.Context
	hello func(id uint64, raw json.RawMessage) error
	// handler is set by the hello, in the read loop, before any call is
	// dispatched.
	handler Handler

	mu       sync.Mutex
	inflight map[uint64]context.CancelFunc
}

// request takes core's requests and notifications, in the read loop. It
// dispatches each call to its own goroutine.
func (s *server) request(m *message) error {
	if m.ID == nil {
		return s.cancel(m)
	}
	id := *m.ID
	switch {
	case s.handler == nil && m.Method != methodHello:
		s.c.respondError(id, &wireError{Code: codeHelloRequired, Message: "the hello comes first"})
		return errors.New("a call came before the hello")
	case s.handler == nil:
		return s.hello(id, m.Params)
	case m.Method == methodHello:
		s.c.respondError(id, &wireError{Code: codeHelloRequired, Message: "the hello came already"})
		return errors.New("a second hello")
	}
	var fds []*os.File
	for _, name := range s.c.p.Descriptors[m.Method] {
		fd := s.c.frames.takeFD(name)
		if fd == nil {
			for _, f := range fds {
				f.Close()
			}
			s.c.respondError(id, &wireError{Code: codeInvalidRequest, Message: m.Method + " came without its " + name})
			return errors.New(m.Method + " came without its " + name)
		}
		fds = append(fds, fd)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.mu.Lock()
	s.inflight[id] = cancel
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.inflight, id)
			s.mu.Unlock()
			cancel()
		}()
		result, err := s.handler(ctx, Call{Method: m.Method, Params: m.Params, FDs: fds, s: s, id: id})
		s.c.respond(id, m.Method, result, err)
	}()
	return nil
}

// cancel takes a notification: only cancel, which cancels the context of
// the call it names, if it still runs.
func (s *server) cancel(m *message) error {
	if m.Method != methodCancel {
		return fmt.Errorf("core sent notification %q", m.Method)
	}
	var p cancelParams
	if err := strictjson.Decode(m.Params, &p); err != nil {
		return fmt.Errorf("malformed cancel: %w", err)
	}
	s.mu.Lock()
	cancel := s.inflight[p.ID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// hello agrees the version and makes the driver. A refusal ends the
// connection once it is answered.
func hello[C, D any](s *server, open Open[C, D], id uint64, raw json.RawMessage) error {
	version := s.c.p.Version
	var p helloParams[C]
	if err := strictjson.Decode(raw, &p); err != nil {
		s.c.respondError(id, &wireError{Code: codeInvalidParams, Message: err.Error()})
		return fmt.Errorf("malformed hello: %w", err)
	}
	if p.Protocol != version {
		data, err := json.Marshal(versionData{Kinds: []string{}, Supported: []string{version}})
		if err != nil {
			return err
		}
		s.c.respondError(id, &wireError{Code: codeVersion, Message: fmt.Sprintf("the driver speaks protocol %q, not %q", version, p.Protocol), Data: data})
		return fmt.Errorf("core speaks protocol %q, not %q", p.Protocol, version)
	}
	if p.Config == nil || string(p.Config) == "null" {
		s.c.respondError(id, &wireError{Code: codeInvalidParams, Message: "the hello carries no driver config"})
		return errors.New("the hello carries no driver config")
	}
	h, description, err := open(s.ctx, p.Config, p.Core)
	if err != nil {
		s.c.respond(id, methodHello, nil, err)
		return fmt.Errorf("the driver could not be configured: %w", err)
	}
	s.handler = h
	s.c.respond(id, methodHello, helloResult[D]{Protocol: version, Capabilities: description}, nil)
	return nil
}

// noGate is the gate of a call core gave none: calling it refuses.
func noGate() error { return errors.New("core gave this call no gate") }

// callCore sends core a request of method with params, a call of what one
// of core's calls lent, and decodes core's answer into result. The
// connection's loss refuses it.
func (s *server) callCore(method string, params, result any) error {
	o := &outgoing{done: make(chan struct{})}
	id, err := s.c.register(o)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	m, err := newRequest(id, method, params)
	if err == nil {
		err = s.c.send(m)
	}
	if err != nil {
		s.c.forget(id)
		return fmt.Errorf("%s: %w", method, err)
	}
	select {
	case <-o.done:
	case <-s.c.lost:
		select {
		case <-o.done:
		default:
			return fmt.Errorf("%s: %w", method, s.c.errLost)
		}
	}
	if o.err != nil {
		return fromWireError(o.err)
	}
	return decodeResult(o.result, result)
}
