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
	"syscall"
	"time"

	"github.com/lgvo/p.ai/internal/driverwire"
	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// outputDrain bounds how long core waits, once the driver answered a
// ServeGit, for the service's last output to reach the client: the service
// is over, and only bytes in flight remain.
const outputDrain = 10 * time.Second

// ServeGit serves command on a connection of its own for the channel's
// input and output and a pipe for its standard error, whose far ends go to
// the driver with the request. Core feeds the client's input to the service
// from the driver's first read of it, once open admitted the service, and
// returns once the driver answered and the service's output reached the
// client, or outputDrain passed.
func (cl *Client) ServeGit(ctx context.Context, command string, stream sourcedriver.Stream, open sourcedriver.Open) error {
	local, remote, err := driverwire.SocketPair("Git stream")
	if err != nil {
		return fmt.Errorf("a connection for the Git service: %w", err)
	}
	errRead, errWrite, err := os.Pipe()
	if err != nil {
		local.Close()
		remote.Close()
		return fmt.Errorf("a pipe for the Git service's errors: %w", err)
	}
	output, diagnostics := relay(stream.Out, local), relay(stream.Err, errRead)
	s := &serving{open: open, in: stream.In, conn: local}
	err = cl.c.CallWith(ctx, methodServeGit, serveGitParams{Command: command}, driverwire.CallOptions{
		Callbacks: map[string]driverwire.Callback{
			methodOpen: {Run: s.openService, Gate: true}, methodApprove: {Run: s.approveUpdate}, methodInput: {Run: s.feedInput},
		},
		FDs: []*os.File{remote, errWrite},
	}, nil)
	// Core's copies of the driver's ends go, so the driver's closing them,
	// or its death, ends the output.
	remote.Close()
	errWrite.Close()
	drained := drain(ctx, output, diagnostics)
	local.Close()
	errRead.Close()
	if err == nil {
		err = drained
	}
	return err
}

// serving is core's side of one serve_git call: the functions it lends the
// driver.
type serving struct {
	open sourcedriver.Open
	in   io.Reader
	conn *net.UnixConn

	mu      sync.Mutex
	opened  bool
	approve sourcedriver.Approve
	feeding bool
}

// openService is the call's gate: core's open, which authorizes the service
// before any Git runs.
func (s *serving) openService(raw json.RawMessage) (any, error) {
	p, err := driverwire.Params[openParams](raw)
	if err != nil {
		return nil, err
	}
	service, approve, err := s.open(p.Kind, p.Project)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.opened, s.approve = true, approve
	s.mu.Unlock()
	return wireService(service), nil
}

// feedInput starts copying the client's input to the service, which reads
// it for the first time: once only, and only after open admitted it.
func (s *serving) feedInput(raw json.RawMessage) (any, error) {
	if _, err := driverwire.Params[noParams](raw); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.opened {
		return nil, errors.New("the driver read the client's input before open admitted its service")
	}
	if !s.feeding {
		s.feeding = true
		go s.feed()
	}
	return nil, nil
}

// feed copies the client's input to the service until it ends, then ends
// the service's input. A client that never ends its input keeps it blocked
// until core closes the channel.
func (s *serving) feed() {
	io.Copy(s.conn, s.in)
	s.conn.CloseWrite()
}

// approveUpdate is the Approve that open returned.
func (s *serving) approveUpdate(raw json.RawMessage) (any, error) {
	p, err := driverwire.Params[approveParams](raw)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	opened, approve := s.opened, s.approve
	s.mu.Unlock()
	switch {
	case !opened:
		return nil, errors.New("the driver asked for approval before open admitted its service")
	case approve == nil:
		return nil, errors.New("core's open gave the service no Approve")
	}
	return nil, approve(p.Branch, p.Old, p.New)
}

// relay copies one of the service's outputs to w, discarding it when w is
// nil, and delivers how the copy ended. The driver's closing its end with
// input it did not read resets the connection once the output was read
// whole, which ends the output as its end of file would.
func relay(w io.Writer, from io.Reader) <-chan error {
	done := make(chan error, 1)
	if w == nil {
		w = io.Discard
	}
	go func() {
		_, err := io.Copy(w, from)
		if errors.Is(err, syscall.ECONNRESET) {
			err = nil
		}
		done <- err
	}()
	return done
}

// drain waits for the relays to end, for at most outputDrain or until ctx
// ends, and returns the first failure.
func drain(ctx context.Context, relays ...<-chan error) error {
	timer := time.NewTimer(outputDrain)
	defer timer.Stop()
	var first error
	for _, r := range relays {
		select {
		case err := <-r:
			if first == nil {
				first = err
			}
		case <-timer.C:
			return errors.New("the client did not take the Git service's output")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return first
}

// serveGit serves a serve_git call on the driver's side: the channel's
// input and output on the first descriptor, its standard error on the
// second, and core's open and approve called back over the connection.
// Both ends close once the driver's ServeGit returned, before its answer
// goes.
func (s *server) serveGit(ctx context.Context, c driverwire.Call) error {
	errOut := c.FDs[1]
	defer errOut.Close()
	conn, err := streamConn(c.FDs[0])
	if err != nil {
		return err
	}
	defer func() {
		conn.CloseWrite()
		conn.Close()
	}()
	p, err := driverwire.Params[serveGitParams](c.Params)
	if err != nil {
		return err
	}
	open := func(kind sourcedriver.ServiceKind, project string) (sourcedriver.Service, sourcedriver.Approve, error) {
		var res wireService
		if err := c.Callback(methodOpen, openParams{Kind: kind, Project: project}, &res); err != nil {
			return sourcedriver.Service{}, nil, err
		}
		approve := func(branch string, old, new sourcedriver.OID) error {
			return c.Callback(methodApprove, approveParams{Branch: branch, Old: old, New: new}, nil)
		}
		return sourcedriver.Service(res), approve, nil
	}
	in := &input{conn: conn, start: func() error { return c.Callback(methodInput, noParams{}, nil) }}
	return s.driver.ServeGit(ctx, p.Command, sourcedriver.Stream{In: in, Out: conn, Err: errOut}, open)
}

// input is the client's input as the driver reads it: its first read asks
// core to start sending it.
type input struct {
	conn  *net.UnixConn
	start func() error

	once    sync.Once
	refused error
}

func (in *input) Read(p []byte) (int, error) {
	in.once.Do(func() { in.refused = in.start() })
	if in.refused != nil {
		return 0, in.refused
	}
	return in.conn.Read(p)
}

// streamConn is the Git stream core passed: a Unix socket, which it owns.
func streamConn(fd *os.File) (*net.UnixConn, error) {
	c, err := net.FileConn(fd)
	fd.Close()
	if err != nil {
		return nil, fmt.Errorf("the Git stream is not a connection: %w", err)
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		c.Close()
		return nil, errors.New("the Git stream is not a Unix socket")
	}
	return uc, nil
}
