package main

import (
	"bytes"
	"errors"
	"io"
	"net"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const parseErrorFrame = "{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32700,\"kind\":\"parse_error\",\"message\":\"invalid session frame\"}}\n"

// The server reads only the framing ceiling, writes one error, then closes
// with request bytes still unread. Linux resets that Unix stream on close.
func resetServer(t *testing.T, response string) (string, <-chan error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		if _, err = io.ReadFull(conn, make([]byte, 65537)); err != nil {
			done <- err
			return
		}
		if response != "" {
			_, err = conn.Write([]byte(response))
		}
		done <- err
	}()
	return path, done
}

func TestOversizeUnixResetRetainsOnlyValidatedParseError(t *testing.T) {
	payload := bytes.Repeat([]byte{'x'}, 66000)
	path, done := resetServer(t, parseErrorFrame)
	response, err := exchange(path, payload, "")
	if err == nil || response != nil || !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("strict client accepted reset: response=%q err=%v", response, err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}

	path, done = resetServer(t, parseErrorFrame)
	response, err = exchange(path, payload, parseErrorMode)
	if err != nil || string(response) != parseErrorFrame {
		t.Fatalf("validated parse-error frame lost: response=%q err=%v", response, err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func TestOversizeUnixResetRequiresRealParseError(t *testing.T) {
	payload := bytes.Repeat([]byte{'x'}, 66000)
	for _, response := range []string{
		"",
		"{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32600,\"kind\":\"invalid_request\",\"message\":\"wrong\"}}\n",
		parseErrorFrame[:len(parseErrorFrame)-1],
	} {
		path, done := resetServer(t, response)
		if got, err := exchange(path, payload, parseErrorMode); err == nil || got != nil {
			t.Fatalf("accepted missing or wrong frame: response=%q got=%q", response, got)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestParseErrorRequiresServerClosure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response string
		keepOpen bool
	}{
		{name: "trailing byte", response: parseErrorFrame + "x"},
		{name: "second frame", response: parseErrorFrame + parseErrorFrame},
		{name: "still open", response: parseErrorFrame, keepOpen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, client := net.Pipe()
			defer server.Close()
			defer client.Close()
			if err := client.SetDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			go func() {
				_, _ = server.Write([]byte(tc.response))
				if !tc.keepOpen {
					server.Close()
				}
			}()
			if got, err := readParseError(client); err == nil || got != nil {
				t.Fatalf("accepted parse-error frame without server closure: response=%q got=%q", tc.response, got)
			}
		})
	}
}
