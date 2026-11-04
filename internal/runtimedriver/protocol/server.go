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

	"github.com/lgvo/p.ai/internal/driverwire"
	"github.com/lgvo/p.ai/internal/runtimedriver"
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
	s := &server{terminals: map[string]*serverTerminal{}}
	err := driverwire.Serve(ctx, uc, wire, func(ctx context.Context, config json.RawMessage, core Core) (driverwire.Handler, wireCapabilities, error) {
		d, err := open(ctx, Hello{Config: config, Core: core})
		if err != nil {
			return nil, wireCapabilities{}, err
		}
		s.driver = d
		return s.dispatch, wireCapabilities(d.Describe()), nil
	})
	s.closeTerminals()
	return err
}

// server is the driver's side of one connection.
type server struct {
	// driver is set by the hello, in the read loop, before any call is
	// dispatched.
	driver runtimedriver.Driver

	mu        sync.Mutex
	terminals map[string]*serverTerminal
	closing   bool
}

// dispatch runs call c on the driver and returns its result.
func (s *server) dispatch(ctx context.Context, c driverwire.Call) (any, error) {
	d, raw, gate := s.driver, c.Params, c.Gate
	switch c.Method {
	case methodCheck:
		if _, err := driverwire.Params[noParams](raw); err != nil {
			return nil, err
		}
		return nil, d.Check(ctx)
	case methodInventory:
		if _, err := driverwire.Params[noParams](raw); err != nil {
			return nil, err
		}
		inv, err := d.Inventory(ctx)
		return toInventory(inv), err
	case methodInspect, methodStart, methodStop:
		p, err := driverwire.Params[sessionParams](raw)
		if err != nil {
			return nil, err
		}
		call := map[string]func(context.Context, runtimedriver.Session) (runtimedriver.Observation, error){
			methodInspect: d.Inspect, methodStart: d.Start, methodStop: d.Stop,
		}[c.Method]
		o, err := call(ctx, p.Session.session())
		return toObservation(o), err
	case methodCreate:
		p, err := driverwire.Params[createParams](raw)
		if err != nil {
			return nil, err
		}
		o, err := d.Create(ctx, p.Session.session(), p.At, gate(p.Gate))
		return toObservation(o), err
	case methodHostLog:
		p, err := driverwire.Params[targetParams](raw)
		if err != nil {
			return nil, err
		}
		text, err := d.HostLog(ctx, p.Target.target())
		return hostLogResult{Text: text}, err
	case methodOpenTerminal:
		fd := c.FDs[0]
		p, err := driverwire.Params[openTerminalParams](raw)
		if err != nil {
			fd.Close()
			return nil, err
		}
		return s.openTerminal(ctx, p, fd)
	case methodResizeTerminal:
		p, err := driverwire.Params[resizeTerminalParams](raw)
		if err != nil {
			return nil, err
		}
		st, err := s.terminal(p.Terminal)
		if err != nil {
			return nil, err
		}
		return nil, st.term.Resize(runtimedriver.Size(p.Size))
	case methodWaitTerminal:
		p, err := driverwire.Params[terminalParams](raw)
		if err != nil {
			return nil, err
		}
		return s.waitTerminal(ctx, p.Terminal)
	case methodReadWorkspace:
		p, err := driverwire.Params[readWorkspaceParams](raw)
		if err != nil {
			return nil, err
		}
		f, err := d.ReadWorkspace(ctx, p.Target.target(), p.Path, p.Limit)
		return wireFile(f), err
	case methodWriteWorkspace:
		p, err := driverwire.Params[writeWorkspaceParams](raw)
		if err != nil {
			return nil, err
		}
		return nil, d.WriteWorkspace(ctx, p.Target.target(), p.Path, runtimedriver.File(p.File), gate(p.Gate))
	case methodRemoveWorkspace:
		p, err := driverwire.Params[removeWorkspaceParams](raw)
		if err != nil {
			return nil, err
		}
		return nil, d.RemoveWorkspace(ctx, p.Target.target(), p.Path, gate(p.Gate))
	case methodServices:
		p, err := driverwire.Params[targetParams](raw)
		if err != nil {
			return nil, err
		}
		services, err := d.Services(ctx, p.Target.target())
		return each(services, func(s runtimedriver.Service) wireService { return wireService(s) }), err
	case methodServiceAction:
		p, err := driverwire.Params[serviceActionParams](raw)
		if err != nil {
			return nil, err
		}
		return nil, d.ServiceAction(ctx, p.Target.target(), p.Name, p.Action)
	case methodServiceLogs:
		p, err := driverwire.Params[serviceParams](raw)
		if err != nil {
			return nil, err
		}
		logs, err := d.ServiceLogs(ctx, p.Target.target(), p.Name)
		return wireLogs(logs), err
	case methodBuildImage:
		p, err := driverwire.Params[buildImageParams](raw)
		if err != nil {
			return nil, err
		}
		img, err := d.BuildImage(ctx, p.Build.build(), gate(p.Gate))
		return toImage(img), err
	case methodImages:
		if _, err := driverwire.Params[noParams](raw); err != nil {
			return nil, err
		}
		images, err := d.Images(ctx)
		return each(images, toImage), err
	case methodInspectImage:
		p, err := driverwire.Params[inspectImageParams](raw)
		if err != nil {
			return nil, err
		}
		img, found, err := d.InspectImage(ctx, p.Ref)
		return inspectImageResult{Image: toImage(img), Found: found}, err
	case methodDeleteImage:
		p, err := driverwire.Params[deleteImageParams](raw)
		if err != nil {
			return nil, err
		}
		return nil, d.DeleteImage(ctx, p.Image.image(), gate(p.Gate))
	case methodSettle:
		p, err := driverwire.Params[settleParams](raw)
		if err != nil {
			return nil, err
		}
		settled, err := d.Settle(ctx, p.Effect.effect())
		if err != nil {
			return nil, err
		}
		return toSettlement(settled)
	case methodDelete:
		p, err := driverwire.Params[deleteParams](raw)
		if err != nil {
			return nil, err
		}
		return nil, d.Delete(ctx, runtimedriver.Ref(p.Ref), p.At, gate(p.Gate))
	}
	return nil, driverwire.NoMethod(c.Method)
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
