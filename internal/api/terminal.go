package api

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// Terminal frames carry an attached terminal over a stream: between `p
// attach` and its helper on their private carrier, and between the helper
// and the daemon on the helper's control connection once attachment.claim is
// answered. A frame is its kind, the length of its data as a big-endian
// uint32, and the data.
type FrameKind byte

const (
	// FrameInit is the client's first frame to its helper: the control
	// socket, the pending attachment's token and the terminal's size.
	FrameInit FrameKind = 1
	// FrameInput is keyboard input toward the terminal.
	FrameInput FrameKind = 2
	// FrameOutput is the terminal's output toward the client.
	FrameOutput FrameKind = 3
	// FrameResize is a TerminalSize: the client's terminal changed size.
	FrameResize FrameKind = 4
	// FrameReady tells the client that its terminal is open.
	FrameReady FrameKind = 5
	// FrameError tells the client why its attachment failed; its data is
	// the message.
	FrameError FrameKind = 6
	// FrameEnd is the daemon's last frame: the terminal ended. Its data is
	// empty, or why the attachment ended abnormally.
	FrameEnd FrameKind = 7
)

// MaxFrameData bounds the data of one terminal frame.
const MaxFrameData = 32768

// Frame is one terminal frame.
type Frame struct {
	Kind FrameKind
	Data []byte
}

// TerminalSize is a terminal's size in character cells, each from 1 to
// 65535.
type TerminalSize struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

func (s TerminalSize) Validate() error {
	if s.Width < 1 || s.Width > 65535 {
		return invalid("width")
	}
	if s.Height < 1 || s.Height > 65535 {
		return invalid("height")
	}
	return nil
}

// FrameConn reads and writes terminal frames on a connection. Writes are
// serialized, and each must finish within five seconds.
type FrameConn struct {
	net.Conn
	reader  io.Reader
	writeMu sync.Mutex
}

// NewFrameConn reads frames from buffered, which holds what was already read
// from conn and then reads conn; nil reads conn alone.
func NewFrameConn(conn net.Conn, buffered io.Reader) *FrameConn {
	if buffered == nil {
		buffered = conn
	}
	return &FrameConn{Conn: conn, reader: buffered}
}

// ReadFrame reads the next frame.
func (c *FrameConn) ReadFrame() (Frame, error) {
	var header [5]byte
	if _, err := io.ReadFull(c.reader, header[:]); err != nil {
		return Frame{}, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > MaxFrameData {
		return Frame{}, errors.New("terminal frame too large")
	}
	f := Frame{Kind: FrameKind(header[0]), Data: make([]byte, length)}
	_, err := io.ReadFull(c.reader, f.Data)
	return f, err
}

// WriteFrame writes one frame of kind with data.
func (c *FrameConn) WriteFrame(kind FrameKind, data []byte) error {
	if len(data) > MaxFrameData {
		return errors.New("terminal frame too large")
	}
	frame := make([]byte, 5+len(data))
	frame[0] = byte(kind)
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(data)))
	copy(frame[5:], data)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	for len(frame) > 0 {
		n, err := c.Conn.Write(frame)
		if err != nil {
			return err
		}
		frame = frame[n:]
	}
	return nil
}

// WriteJSON writes one frame of kind whose data is v as JSON.
func (c *FrameConn) WriteJSON(kind FrameKind, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.WriteFrame(kind, raw)
}

// Frames is the rest of this connection as terminal frames, once
// attachment.claim is answered: it reads first what the connection already
// buffered, and has no deadline.
func (c *Conn) Frames() *FrameConn {
	c.conn.SetDeadline(time.Time{})
	return NewFrameConn(c.conn, c.reader)
}
