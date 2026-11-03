package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/strictjson"
)

// terminalDrain bounds how long an ended terminal's remaining output may
// take to reach core before its connection closes.
const terminalDrain = 10 * time.Second

// Open makes the driver a hello configures.
type Open func(ctx context.Context, h Hello) (runtimedriver.Driver, error)

// Serve answers the hello on uc with the driver open makes, then serves
// that driver until the connection is lost or ctx ends. Calls in flight
// then have their contexts cancelled and the driver's terminals are hung
// up. Serve owns uc. It returns nil once core closed the connection or ctx
// ended, and otherwise why the connection ended, such as a refused hello.
func Serve(ctx context.Context, uc *net.UnixConn, open Open) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &server{c: newConn(uc, true), open: open, ctx: ctx, inflight: map[uint64]context.CancelFunc{}, terminals: map[string]*serverTerminal{}}
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
	s.closeTerminals()
	if cause := s.c.lostCause(); !errors.Is(cause, io.EOF) && !errors.Is(cause, context.Canceled) {
		return cause
	}
	return nil
}

// server is the driver's side of one connection.
type server struct {
	c    *conn
	open Open
	ctx  context.Context
	// driver is set by the hello, in the read loop, before any call is
	// dispatched.
	driver runtimedriver.Driver

	mu        sync.Mutex
	inflight  map[uint64]context.CancelFunc
	terminals map[string]*serverTerminal
	closing   bool
}

// request takes core's requests and notifications, in the read loop. It
// dispatches each call to its own goroutine.
func (s *server) request(m *message) error {
	if m.ID == nil {
		return s.cancel(m)
	}
	id := *m.ID
	switch {
	case s.driver == nil && m.Method != methodHello:
		s.c.respondError(id, &wireError{Code: codeHelloRequired, Message: "the hello comes first"})
		return errors.New("a call came before the hello")
	case s.driver == nil:
		return s.hello(id, m.Params)
	case m.Method == methodHello:
		s.c.respondError(id, &wireError{Code: codeHelloRequired, Message: "the hello came already"})
		return errors.New("a second hello")
	}
	var fd *os.File
	if m.Method == methodOpenTerminal {
		if fd = s.c.frames.takeFD(); fd == nil {
			s.c.respondError(id, &wireError{Code: codeInvalidRequest, Message: "open_terminal came without its connection"})
			return errors.New("open_terminal came without its connection")
		}
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
		result, err := s.dispatch(ctx, id, m.Method, m.Params, fd)
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
func (s *server) hello(id uint64, raw json.RawMessage) error {
	var p helloParams
	if err := strictjson.Decode(raw, &p); err != nil {
		s.c.respondError(id, &wireError{Code: codeInvalidParams, Message: err.Error()})
		return fmt.Errorf("malformed hello: %w", err)
	}
	if p.Protocol != Version {
		s.c.respondError(id, &wireError{Code: codeVersion, Message: fmt.Sprintf("the driver speaks protocol %q, not %q", Version, p.Protocol),
			Data: &errorData{Kinds: []string{}, Supported: []string{Version}}})
		return fmt.Errorf("core speaks protocol %q, not %q", p.Protocol, Version)
	}
	if p.Config == nil || string(p.Config) == "null" {
		s.c.respondError(id, &wireError{Code: codeInvalidParams, Message: "the hello carries no driver config"})
		return errors.New("the hello carries no driver config")
	}
	d, err := s.open(s.ctx, Hello{Config: p.Config, Core: p.Core})
	if err != nil {
		s.c.respond(id, methodHello, nil, err)
		return fmt.Errorf("the driver could not be configured: %w", err)
	}
	s.driver = d
	s.c.respond(id, methodHello, helloResult{Protocol: Version, Capabilities: wireCapabilities(d.Describe())}, nil)
	return nil
}

// params decodes a call's parameters strictly.
func params[P any](raw json.RawMessage) (P, error) {
	var p P
	if err := strictjson.Decode(raw, &p); err != nil {
		return p, errInvalidParams{err}
	}
	return p, nil
}

// noGate is the gate of a call core gave none: calling it refuses.
func noGate() error { return errors.New("core gave this call no gate") }

// dispatch runs call id of method on the driver and returns its result.
func (s *server) dispatch(ctx context.Context, id uint64, method string, raw json.RawMessage, fd *os.File) (any, error) {
	d := s.driver
	gate := func(given bool) runtimedriver.Gate {
		if given {
			return s.gate(id)
		}
		return noGate
	}
	switch method {
	case methodCheck:
		if _, err := params[noParams](raw); err != nil {
			return nil, err
		}
		return nil, d.Check(ctx)
	case methodInventory:
		if _, err := params[noParams](raw); err != nil {
			return nil, err
		}
		inv, err := d.Inventory(ctx)
		return toInventory(inv), err
	case methodInspect, methodStart, methodStop:
		p, err := params[sessionParams](raw)
		if err != nil {
			return nil, err
		}
		call := map[string]func(context.Context, runtimedriver.Session) (runtimedriver.Observation, error){
			methodInspect: d.Inspect, methodStart: d.Start, methodStop: d.Stop,
		}[method]
		o, err := call(ctx, p.Session.session())
		return toObservation(o), err
	case methodCreate:
		p, err := params[createParams](raw)
		if err != nil {
			return nil, err
		}
		o, err := d.Create(ctx, p.Session.session(), p.At, gate(p.Gate))
		return toObservation(o), err
	case methodHostLog:
		p, err := params[targetParams](raw)
		if err != nil {
			return nil, err
		}
		text, err := d.HostLog(ctx, p.Target.target())
		return hostLogResult{Text: text}, err
	case methodOpenTerminal:
		p, err := params[openTerminalParams](raw)
		if err != nil {
			fd.Close()
			return nil, err
		}
		return s.openTerminal(ctx, p, fd)
	case methodResizeTerminal:
		p, err := params[resizeTerminalParams](raw)
		if err != nil {
			return nil, err
		}
		st, err := s.terminal(p.Terminal)
		if err != nil {
			return nil, err
		}
		return nil, st.term.Resize(runtimedriver.Size(p.Size))
	case methodWaitTerminal:
		p, err := params[terminalParams](raw)
		if err != nil {
			return nil, err
		}
		return s.waitTerminal(ctx, p.Terminal)
	case methodReadWorkspace:
		p, err := params[readWorkspaceParams](raw)
		if err != nil {
			return nil, err
		}
		f, err := d.ReadWorkspace(ctx, p.Target.target(), p.Path, p.Limit)
		return wireFile(f), err
	case methodWriteWorkspace:
		p, err := params[writeWorkspaceParams](raw)
		if err != nil {
			return nil, err
		}
		return nil, d.WriteWorkspace(ctx, p.Target.target(), p.Path, runtimedriver.File(p.File), gate(p.Gate))
	case methodRemoveWorkspace:
		p, err := params[removeWorkspaceParams](raw)
		if err != nil {
			return nil, err
		}
		return nil, d.RemoveWorkspace(ctx, p.Target.target(), p.Path, gate(p.Gate))
	case methodServices:
		p, err := params[targetParams](raw)
		if err != nil {
			return nil, err
		}
		services, err := d.Services(ctx, p.Target.target())
		return each(services, func(s runtimedriver.Service) wireService { return wireService(s) }), err
	case methodServiceAction:
		p, err := params[serviceActionParams](raw)
		if err != nil {
			return nil, err
		}
		return nil, d.ServiceAction(ctx, p.Target.target(), p.Name, p.Action)
	case methodServiceLogs:
		p, err := params[serviceParams](raw)
		if err != nil {
			return nil, err
		}
		logs, err := d.ServiceLogs(ctx, p.Target.target(), p.Name)
		return wireLogs(logs), err
	case methodBuildImage:
		p, err := params[buildImageParams](raw)
		if err != nil {
			return nil, err
		}
		img, err := d.BuildImage(ctx, p.Build.build(), gate(p.Gate))
		return toImage(img), err
	case methodImages:
		if _, err := params[noParams](raw); err != nil {
			return nil, err
		}
		images, err := d.Images(ctx)
		return each(images, toImage), err
	case methodInspectImage:
		p, err := params[inspectImageParams](raw)
		if err != nil {
			return nil, err
		}
		img, found, err := d.InspectImage(ctx, p.Ref)
		return inspectImageResult{Image: toImage(img), Found: found}, err
	case methodDeleteImage:
		p, err := params[deleteImageParams](raw)
		if err != nil {
			return nil, err
		}
		return nil, d.DeleteImage(ctx, p.Image.image(), gate(p.Gate))
	case methodSettle:
		p, err := params[settleParams](raw)
		if err != nil {
			return nil, err
		}
		settled, err := d.Settle(ctx, p.Effect.effect())
		if err != nil {
			return nil, err
		}
		return toSettlement(settled)
	case methodDelete:
		p, err := params[deleteParams](raw)
		if err != nil {
			return nil, err
		}
		return nil, d.Delete(ctx, runtimedriver.Ref(p.Ref), p.At, gate(p.Gate))
	}
	return nil, errNoMethod{method}
}

// errNoMethod is a call of a method the protocol does not have.
type errNoMethod struct{ method string }

func (e errNoMethod) Error() string { return fmt.Sprintf("the protocol has no method %q", e.method) }

// gate is the gate of call: it asks core to run the call's gate and
// returns its answer. The connection's loss refuses it.
func (s *server) gate(call uint64) runtimedriver.Gate {
	return func() error {
		o := &outgoing{done: make(chan struct{})}
		id, err := s.c.register(o)
		if err != nil {
			return fmt.Errorf("gate: %w", err)
		}
		m, err := newRequest(id, methodGate, gateParams{Call: call})
		if err == nil {
			err = s.c.send(m, nil)
		}
		if err != nil {
			s.c.forget(id)
			return fmt.Errorf("gate: %w", err)
		}
		select {
		case <-o.done:
		case <-s.c.lost:
			select {
			case <-o.done:
			default:
				return fmt.Errorf("gate: %w", errLost)
			}
		}
		if o.err != nil {
			return fromWireError(o.err)
		}
		return decodeResult(o.result, nil)
	}
}

// serverTerminal is a terminal the driver opened and the connection that
// carries its bytes to core.
type serverTerminal struct {
	term   runtimedriver.Terminal
	conn   *net.UnixConn
	ended  chan struct{} // closes once the driver delivered the end
	proven bool          // set before ended closes
	hangUp func()
}

// openTerminal opens the driver's terminal and relays it over fd, the
// connection core passed for it.
func (s *server) openTerminal(ctx context.Context, p openTerminalParams, fd *os.File) (any, error) {
	c, err := net.FileConn(fd)
	fd.Close()
	uc, ok := c.(*net.UnixConn)
	if err != nil || !ok {
		if c != nil {
			c.Close()
		}
		return nil, errors.Join(errors.New("the terminal's connection is not a Unix socket"), err)
	}
	term, err := s.driver.OpenTerminal(ctx, p.Target.target(), runtimedriver.Size(p.Size))
	if err != nil {
		uc.Close()
		return nil, err
	}
	st := &serverTerminal{term: term, conn: uc, ended: make(chan struct{}), hangUp: sync.OnceFunc(func() { term.Close() })}
	s.mu.Lock()
	refused := s.closing || s.terminals[term.ID()] != nil
	if !refused {
		s.terminals[term.ID()] = st
	}
	s.mu.Unlock()
	if refused {
		st.hangUp()
		uc.Close()
		return nil, &runtimedriver.UnendedTerminal{ID: term.ID(), Err: errors.New("the driver named two terminals alike, or the connection is ending")}
	}
	go st.relay()
	return terminalParams{Terminal: term.ID()}, nil
}

// relay carries the terminal's bytes until it ends: its output to core,
// whose end closes the connection's write side, and core's input to it.
// Core's closing its write side hangs the terminal up.
func (st *serverTerminal) relay() {
	output := make(chan struct{})
	go func() {
		io.Copy(st.conn, st.term)
		st.conn.CloseWrite()
		close(output)
	}()
	go func() {
		io.Copy(st.term, st.conn)
		st.hangUp()
	}()
	completion, ok := <-st.term.Ended()
	st.proven = ok && completion.Proven
	close(st.ended)
	select {
	case <-output:
	case <-time.After(terminalDrain):
	}
	st.conn.Close()
}

func (s *server) terminal(id string) (*serverTerminal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.terminals[id]
	if st == nil {
		return nil, fmt.Errorf("no open terminal %q", id)
	}
	return st, nil
}

// waitTerminal answers once the terminal ended, with whether its end was
// proven. Core waits once, so the terminal is then forgotten.
func (s *server) waitTerminal(ctx context.Context, id string) (any, error) {
	st, err := s.terminal(id)
	if err != nil {
		return nil, err
	}
	select {
	case <-st.ended:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	s.mu.Lock()
	delete(s.terminals, id)
	s.mu.Unlock()
	return waitTerminalResult{Proven: st.proven}, nil
}

// closeTerminals hangs up every terminal once the connection is lost.
func (s *server) closeTerminals() {
	s.mu.Lock()
	s.closing = true
	terminals := s.terminals
	s.terminals = map[string]*serverTerminal{}
	s.mu.Unlock()
	for _, st := range terminals {
		st.hangUp()
		st.conn.Close()
	}
}
