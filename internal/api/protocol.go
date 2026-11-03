// Package api is the control-API contract that the daemon and its clients
// share: one request and one result type per method, bound to the method's
// name (Method), the framing limits, error objects, and the client.
// docs/control-api.md describes the methods.
package api

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

const (
	// Protocol is the control protocol this build speaks. A connection agrees
	// it once, with system.hello, before any other request.
	Protocol      = "p.control/v2"
	MaxFrameBytes = 65536
)

// ReadFrame reads one newline-terminated frame of at most MaxFrameBytes.
func ReadFrame(reader *bufio.Reader) ([]byte, error) {
	line, err := reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) || len(line) > MaxFrameBytes {
		return nil, errors.New("frame exceeds 64 KiB")
	}
	if err != nil {
		if errors.Is(err, io.EOF) && len(line) > 0 {
			return nil, errors.New("unterminated frame")
		}
		return nil, err
	}
	return bytes.TrimSuffix(line, []byte{'\n'}), nil
}
