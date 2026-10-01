package runtimekit

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

type testHalfCloseStream struct {
	toServer   *io.PipeWriter
	fromServer *io.PipeReader
}

func (c *testHalfCloseStream) Read(p []byte) (int, error)  { return c.fromServer.Read(p) }
func (c *testHalfCloseStream) Write(p []byte) (int, error) { return c.toServer.Write(p) }
func (c *testHalfCloseStream) CloseWrite() error           { return c.toServer.Close() }
func (c *testHalfCloseStream) Close() error {
	c.toServer.Close()
	return c.fromServer.Close()
}

func TestGitStreamPropagatesInputEOFAndReturnsFinalResponse(t *testing.T) {
	toServerReader, toServerWriter := io.Pipe()
	fromServerReader, fromServerWriter := io.Pipe()
	conn := &testHalfCloseStream{toServer: toServerWriter, fromServer: fromServerReader}
	serverResult := make(chan string, 1)
	go func() {
		request, err := io.ReadAll(toServerReader)
		if err != nil {
			serverResult <- "read failed: " + err.Error()
			fromServerWriter.Close()
			return
		}
		serverResult <- string(request)
		_, _ = fromServerWriter.Write([]byte("final response"))
		fromServerWriter.Close()
	}()
	var output bytes.Buffer
	if err := bridgeGitStream(conn, strings.NewReader("ssh request"), &output); err != nil {
		t.Fatal(err)
	}
	if got := <-serverResult; got != "ssh request" {
		t.Fatalf("server received %q", got)
	}
	if got := output.String(); got != "final response" {
		t.Fatalf("client received %q", got)
	}
}
