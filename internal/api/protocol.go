// Package api is the control-API contract that clients use: the wire types
// they read, framing limits, error objects, and the one-request client.
// docs/control-api.md owns the method schemas.
package api

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

const (
	ProtocolVersion = "p.control/v1"
	MaxFrameBytes   = 65536
)

type RPCError struct {
	Code    int    `json:"code"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

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
