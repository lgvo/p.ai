// session-fixture is a test-only Unix RPC client installed inside a session.
package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"time"
)

const (
	paceMode       = "pace-75ms"
	parseErrorMode = "expect-parse-error"
)

func main() {
	if len(os.Args) != 3 && len(os.Args) != 4 {
		fail(errors.New("usage: session-fixture <socket> <base64-frame> [pace-75ms|expect-parse-error]"))
	}
	mode := ""
	if len(os.Args) == 4 {
		mode = os.Args[3]
		if mode != paceMode && mode != parseErrorMode {
			fail(errors.New("unknown session-fixture mode"))
		}
	}
	payload, err := base64.StdEncoding.DecodeString(os.Args[2])
	if err != nil {
		fail(err)
	}
	response, err := exchange(os.Args[1], payload, mode)
	if err != nil {
		fail(err)
	}
	if _, err = os.Stdout.Write(response); err != nil {
		fail(err)
	}
}

func exchange(socket string, payload []byte, mode string) ([]byte, error) {
	conn, err := net.DialTimeout("unix", socket, 3*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := 6 * time.Second
	if mode == paceMode {
		deadline = 40 * time.Second
	}
	if err = conn.SetDeadline(time.Now().Add(deadline)); err != nil {
		return nil, err
	}
	if mode == paceMode {
		for _, line := range bytes.SplitAfter(payload, []byte{'\n'}) {
			if len(line) == 0 {
				continue
			}
			if err = writeAll(conn, line); err != nil {
				return nil, err
			}
			time.Sleep(75 * time.Millisecond)
		}
	} else {
		err = writeAll(conn, payload)
		if err != nil && (mode != parseErrorMode || !isExpectedReset(err)) {
			return nil, err
		}
	}
	closeErr := conn.(*net.UnixConn).CloseWrite()
	if closeErr != nil && (mode != parseErrorMode || !isExpectedReset(closeErr)) {
		return nil, closeErr
	}
	if mode == parseErrorMode {
		// A server rejecting an oversize frame may reset the stream while
		// unread request bytes remain. Accept that outcome only after reading
		// and validating its complete bounded parse-error frame.
		return readParseError(conn)
	}
	response, err := io.ReadAll(io.LimitReader(conn, 128*1024+1))
	if err != nil {
		return nil, err
	}
	if len(response) > 128*1024 {
		return nil, errors.New("oversize response")
	}
	return response, nil
}

func readParseError(conn net.Conn) ([]byte, error) {
	reader := bufio.NewReader(io.LimitReader(conn, 4097))
	response, err := reader.ReadBytes('\n')
	if err != nil || len(response) > 4096 {
		return nil, errors.New("missing complete bounded parse-error response")
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(response, &envelope) != nil || len(envelope) != 3 ||
		string(envelope["jsonrpc"]) != `"2.0"` || string(envelope["id"]) != "null" {
		return nil, errors.New("invalid parse-error envelope")
	}
	var rpcErr map[string]json.RawMessage
	if json.Unmarshal(envelope["error"], &rpcErr) != nil || len(rpcErr) != 3 ||
		string(rpcErr["code"]) != "-32700" || string(rpcErr["kind"]) != `"parse_error"` {
		return nil, errors.New("invalid parse-error response")
	}
	var message string
	if json.Unmarshal(rpcErr["message"], &message) != nil || len(message) == 0 || len(message) > 256 {
		return nil, errors.New("invalid parse-error message")
	}
	// The framing contract closes the connection after this response. Reading
	// one more byte also rejects a second frame or trailing garbage, while the
	// limit above keeps a misbehaving server from making this read unbounded.
	if _, err = reader.ReadByte(); err == nil || (err != io.EOF && !errors.Is(err, syscall.ECONNRESET)) {
		return nil, errors.New("parse-error response was not followed by server closure")
	}
	return response, nil
}

func isExpectedReset(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}

func writeAll(conn net.Conn, payload []byte) error {
	for len(payload) > 0 {
		n, err := conn.Write(payload)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		payload = payload[n:]
	}
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
