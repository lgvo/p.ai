package api

import (
	"bufio"
	"io"
	"net"
	"strings"
	"testing"
)

// The frames after a claim's answer may already sit in the reader that read
// the answer; the frame connection reads them first, then the connection.
func TestFrameConnReadsBufferedBytesBeforeTheConnection(t *testing.T) {
	near, far := net.Pipe()
	defer near.Close()
	defer far.Close()
	sender := NewFrameConn(far, nil)
	go func() {
		far.Write([]byte("{\"answer\":1}\n"))
		sender.WriteFrame(FrameOutput, []byte("first"))
		sender.WriteJSON(FrameResize, TerminalSize{Width: 100, Height: 40})
	}()
	reader := bufio.NewReader(near)
	if line, err := reader.ReadString('\n'); err != nil || line != "{\"answer\":1}\n" {
		t.Fatalf("answer: %q %v", line, err)
	}
	frames := NewFrameConn(near, reader)
	if f, err := frames.ReadFrame(); err != nil || f.Kind != FrameOutput || string(f.Data) != "first" {
		t.Fatalf("first frame: %+v %v", f, err)
	}
	if f, err := frames.ReadFrame(); err != nil || f.Kind != FrameResize || string(f.Data) != `{"width":100,"height":40}` {
		t.Fatalf("second frame: %+v %v", f, err)
	}
}

func TestFrameConnRefusesFramesOverTheBound(t *testing.T) {
	near, far := net.Pipe()
	defer near.Close()
	defer far.Close()
	if err := NewFrameConn(far, nil).WriteFrame(FrameInput, make([]byte, MaxFrameData+1)); err == nil {
		t.Fatal("an oversized frame was written")
	}
	go far.Write([]byte{byte(FrameOutput), 0, 0, 0x80, 0x01})
	if _, err := NewFrameConn(near, nil).ReadFrame(); err == nil || err == io.EOF || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("an oversized frame header was accepted: %v", err)
	}
}
