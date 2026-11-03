package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lgvo/p.ai/internal/strictjson"
)

// DefaultControlSocket is the standard NixOS instance's private control socket.
const DefaultControlSocket = "/var/lib/p/control.sock"

// ClientSocket selects the explicit endpoint, P_SOCKET, or the standard instance.
// An invalid override fails rather than connecting to a different instance.
func ClientSocket(explicit string) (string, error) {
	socket := explicit
	if socket == "" {
		socket = os.Getenv("P_SOCKET")
	}
	if socket == "" {
		socket = DefaultControlSocket
	}
	if !filepath.IsAbs(socket) {
		return "", errors.New("control socket must be an absolute path (check P_SOCKET or the explicit socket argument)")
	}
	return socket, nil
}

// Caller makes one call: it sends method with params and decodes the result
// into result. An error the daemon answers is an *Error. Clients call
// through Do, which binds params and result to the method's types.
type Caller interface {
	Call(ctx context.Context, method string, params, result any) error
}

// Do validates req and makes one typed call of m.
func Do[Req Request, Res any](ctx context.Context, c Caller, m Method[Req, Res], req Req) (Res, error) {
	var res Res
	if err := req.Validate(); err != nil {
		return res, err
	}
	err := c.Call(ctx, m.Name, req, &res)
	return res, err
}

// Conn is one connection to the daemon that has agreed the protocol. It
// makes one call at a time.
type Conn struct {
	mu     sync.Mutex
	conn   net.Conn
	reader *bufio.Reader
	next   int
	hello  HelloResult
}

// Dial connects to the control socket and sends system.hello.
func Dial(ctx context.Context, socket string) (*Conn, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	c := &Conn{conn: conn, reader: bufio.NewReaderSize(conn, MaxFrameBytes+1)}
	hello, err := Do(ctx, c, SystemHello, HelloRequest{Protocol: Protocol})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if hello.Protocol != Protocol {
		conn.Close()
		return nil, fmt.Errorf("daemon agreed protocol %q, not %q", hello.Protocol, Protocol)
	}
	c.hello = hello
	return c, nil
}

// Hello is the daemon's answer to this connection's system.hello.
func (c *Conn) Hello() HelloResult { return c.hello }

func (c *Conn) Close() error { return c.conn.Close() }

// Call implements Caller.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	line, err := c.exchange(ctx, method, raw)
	if err != nil {
		return err
	}
	var response struct {
		Result json.RawMessage `json:"result"`
		Error  *Error          `json:"error"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		return err
	}
	if response.Error != nil {
		return response.Error
	}
	if err := strictjson.Decode(response.Result, result); err != nil {
		return fmt.Errorf("invalid %s result: %w", method, err)
	}
	return nil
}

// exchange sends one request and returns its response envelope after checking
// that it answers this request with exactly one result or error.
func (c *Conn) exchange(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if len(method) == 0 || len(method) > 128 || len(params) > MaxFrameBytes/2 || !json.Valid(params) {
		return nil, errors.New("invalid input")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if deadline, ok := ctx.Deadline(); ok {
		c.conn.SetDeadline(deadline)
	} else {
		c.conn.SetDeadline(time.Time{})
	}
	// A cancelled context interrupts a blocked write or read.
	stop := context.AfterFunc(ctx, func() { c.conn.SetDeadline(time.Now()) })
	defer stop()
	c.next++
	request := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}{"2.0", c.next, method, params}
	if err := json.NewEncoder(c.conn).Encode(request); err != nil {
		return nil, contextError(ctx, err)
	}
	line, err := ReadFrame(c.reader)
	if err != nil {
		return nil, contextError(ctx, err)
	}
	var response struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *Error          `json:"error"`
	}
	if err := strictjson.Decode(line, &response); err != nil || response.JSONRPC != "2.0" || response.ID != c.next {
		return nil, errors.New("invalid daemon response")
	}
	if (len(response.Result) == 0) == (response.Error == nil) {
		return nil, errors.New("daemon response must contain exactly one result or error")
	}
	return json.RawMessage(line), nil
}

func contextError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// CallRaw connects, agrees the protocol, sends one request with raw params
// and returns the complete response envelope. Only `p api` sends untyped
// params; every other client calls through Do.
func CallRaw(ctx context.Context, socket, method string, params json.RawMessage) (json.RawMessage, error) {
	c, err := Dial(ctx, socket)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.exchange(ctx, method, params)
}
