package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/strictjson"
)

// writeWait bounds the write of one frame. A peer that reads nothing for
// that long has hung, and its connection is ended.
const writeWait = 30 * time.Second

// message is one JSON-RPC 2.0 message: a request when it has a method and an
// ID, a notification when it has a method and no ID, and otherwise a
// response.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *uint64         `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *wireError      `json:"error,omitempty"`
}

// decodeMessage reads one frame strictly: one JSON object with no duplicate,
// unknown or inexact keys, that is a request or notification with an object
// of parameters, or a response.
func decodeMessage(frame []byte) (*message, error) {
	var m message
	if err := strictjson.Decode(frame, &m); err != nil {
		return nil, fmt.Errorf("malformed frame: %w", err)
	}
	valid := m.JSONRPC == "2.0"
	if m.Method != "" {
		valid = valid && bytes.HasPrefix(m.Params, []byte("{")) && m.Result == nil && m.Error == nil
	} else {
		valid = valid && m.ID != nil && m.Params == nil && (m.Result == nil) != (m.Error == nil)
	}
	if !valid {
		return nil, errors.New("the frame is not a JSON-RPC 2.0 request, notification or response")
	}
	return &m, nil
}

var errFrameTooLarge = fmt.Errorf("the message exceeds the %d-byte frame bound", MaxFrameBytes)

// frameReader reads newline-terminated frames of at most MaxFrameBytes.
type frameReader struct {
	r *bufio.Reader
	// fds keeps the descriptors that arrived with the frames, or is nil
	// for a side that accepts none: the kernel then discards them.
	fds *descriptorReader
}

func newFrameReader(uc *net.UnixConn, acceptFDs bool) *frameReader {
	if !acceptFDs {
		return &frameReader{r: bufio.NewReaderSize(uc, 64<<10)}
	}
	fds := &descriptorReader{conn: uc, oob: make([]byte, syscall.CmsgSpace(4*maxQueuedFDs))}
	return &frameReader{r: bufio.NewReaderSize(fds, 64<<10), fds: fds}
}

// next reads the next frame, without its newline.
func (f *frameReader) next() ([]byte, error) {
	var frame []byte
	for {
		chunk, err := f.r.ReadSlice('\n')
		if len(frame)+len(chunk) > MaxFrameBytes {
			return nil, errFrameTooLarge
		}
		frame = append(frame, chunk...)
		switch {
		case err == nil:
			return frame[:len(frame)-1], nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(frame) > 0:
			return nil, errors.New("the connection ended inside a frame")
		}
		return nil, err
	}
}

// takeFD is the oldest descriptor that arrived and was not taken yet.
func (f *frameReader) takeFD() *os.File {
	if f.fds == nil || len(f.fds.queue) == 0 {
		return nil
	}
	fd := f.fds.queue[0]
	f.fds.queue = f.fds.queue[1:]
	return fd
}

// close closes the descriptors nothing took.
func (f *frameReader) close() {
	if f.fds == nil {
		return
	}
	for _, fd := range f.fds.queue {
		fd.Close()
	}
	f.fds.queue = nil
}

// maxQueuedFDs bounds the descriptors that arrived ahead of the requests
// that take them.
const maxQueuedFDs = 8

// descriptorReader reads a Unix connection and keeps, in order, the
// descriptors that arrive with its bytes. A descriptor arrives with the
// first byte of the frame it was sent with, so by the time a frame is read
// whole, its descriptor is queued behind those of earlier frames. Only the
// read loop uses it.
type descriptorReader struct {
	conn  *net.UnixConn
	oob   []byte
	queue []*os.File
}

func (r *descriptorReader) Read(p []byte) (int, error) {
	n, oobn, flags, _, err := r.conn.ReadMsgUnix(p, r.oob)
	n, oobn = max(n, 0), max(oobn, 0) // a failed recvmsg reports -1
	if oobn > 0 {
		if e := r.keep(r.oob[:oobn]); e != nil {
			return n, e
		}
	}
	if flags&syscall.MSG_CTRUNC != 0 {
		return n, errors.New("descriptors arrived that did not fit their buffer")
	}
	return n, err
}

func (r *descriptorReader) keep(oob []byte) error {
	messages, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return err
	}
	var failed error
	for i := range messages {
		fds, err := syscall.ParseUnixRights(&messages[i])
		if err != nil {
			failed = errors.New("a control message other than descriptors arrived")
			continue
		}
		for _, fd := range fds {
			r.queue = append(r.queue, os.NewFile(uintptr(fd), "terminal connection"))
		}
	}
	if len(r.queue) > maxQueuedFDs {
		failed = errors.New("more descriptors arrived than requests take")
	}
	return failed
}

// gateState is what became of a call's gate.
type gateState int

const (
	gateIdle    gateState = iota // not called, or refused before it ran
	gateClaimed                  // the driver called it; it runs unless the call ended first
	gateRunning
	gateDone
)

// outgoing is a request this side sent and waits on. Its fields other than
// done are set before done closes; the gate's fields are guarded by the
// connection's mutex.
type outgoing struct {
	done   chan struct{}
	result json.RawMessage
	err    *wireError

	gate      runtimedriver.Gate // a client call's gate, or nil
	gateState gateState
	gateErr   error
	gateOver  chan struct{} // made as the gate starts running; closes once it returned
	ending    bool          // the call is returning; a gate that has not started is refused
}

// handler takes the requests and notifications the peer sends. It runs in
// the read loop, in order; an error ends the connection.
type handler interface {
	request(m *message) error
}

// conn is one side of a protocol connection: it sends requests and
// responses, matches responses to its requests, and hands the peer's
// requests to a handler.
type conn struct {
	uc      *net.UnixConn
	frames  *frameReader
	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  uint64
	pending map[uint64]*outgoing
	closed  bool
	cause   error
	lost    chan struct{} // closes once the connection is lost
}

func newConn(uc *net.UnixConn, acceptFDs bool) *conn {
	return &conn{uc: uc, frames: newFrameReader(uc, acceptFDs), pending: map[uint64]*outgoing{}, lost: make(chan struct{})}
}

// readLoop reads frames until the connection is lost.
func (c *conn) readLoop(h handler) {
	defer c.frames.close()
	for {
		frame, err := c.frames.next()
		if err != nil {
			c.fail(err)
			return
		}
		m, err := decodeMessage(frame)
		if err == nil {
			if m.Method == "" {
				c.deliver(m)
				continue
			}
			err = h.request(m)
		}
		if err != nil {
			c.fail(err)
			return
		}
	}
}

// deliver hands a response to the request it answers. A response to a
// request nobody waits on any more is dropped.
func (c *conn) deliver(m *message) {
	c.mu.Lock()
	o := c.pending[*m.ID]
	delete(c.pending, *m.ID)
	c.mu.Unlock()
	if o != nil {
		o.result, o.err = m.Result, m.Error
		close(o.done)
	}
}

// register reserves an ID for a request of o.
func (c *conn) register(o *outgoing) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, c.cause
	}
	c.nextID++
	c.pending[c.nextID] = o
	return c.nextID, nil
}

func (c *conn) forget(id uint64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// send writes m as one frame, with fd passed alongside when it is set. A
// message over the frame bound is refused before anything is written; a
// failed write ends the connection, since the peer may have read part of
// the frame.
func (c *conn) send(m *message, fd *os.File) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(raw)+1 > MaxFrameBytes {
		return errFrameTooLarge
	}
	raw = append(raw, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.isLost() {
		return errLost
	}
	c.uc.SetWriteDeadline(time.Now().Add(writeWait))
	if fd != nil {
		n, _, err := c.uc.WriteMsgUnix(raw, syscall.UnixRights(int(fd.Fd())), nil)
		if err != nil {
			c.fail(err)
			return err
		}
		raw = raw[n:]
	}
	if len(raw) > 0 {
		if _, err := c.uc.Write(raw); err != nil {
			c.fail(err)
			return err
		}
	}
	return nil
}

// newRequest is the request id of method with params.
func newRequest(id uint64, method string, params any) (*message, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return &message{JSONRPC: "2.0", ID: &id, Method: method, Params: raw}, nil
}

// newNotification is a notification of method with params.
func newNotification(method string, params any) (*message, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return &message{JSONRPC: "2.0", Method: method, Params: raw}, nil
}

// respond answers the request id with result, or with err when it is set.
// A result over the frame bound is answered with an error instead, of kind
// ErrUnknownOutcome after an effect and ErrUnavailable after a read.
func (c *conn) respond(id uint64, method string, result any, err error) {
	if err == nil {
		raw, e := json.Marshal(result)
		if e == nil {
			e = c.send(&message{JSONRPC: "2.0", ID: &id, Result: raw}, nil)
		}
		if e == nil || !errors.Is(e, errFrameTooLarge) {
			return
		}
		if effects[method] {
			err = unknownOutcome("the driver's answer could not be sent", e)
		} else {
			err = unavailable("the driver's answer could not be sent", e)
		}
	}
	c.respondError(id, toWireError(err))
}

func (c *conn) respondError(id uint64, e *wireError) {
	if err := c.send(&message{JSONRPC: "2.0", ID: &id, Error: e}, nil); errors.Is(err, errFrameTooLarge) {
		// An error message too long for a frame: its kinds go with the
		// start of it.
		short := *e
		short.Message = short.Message[:4096]
		c.send(&message{JSONRPC: "2.0", ID: &id, Error: &short}, nil)
	}
}

// fail ends the connection, once, for cause.
func (c *conn) fail(cause error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed, c.cause = true, cause
	close(c.lost)
	c.mu.Unlock()
	c.uc.Close()
}

func (c *conn) isLost() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// lostCause is why the connection was lost.
func (c *conn) lostCause() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cause
}
