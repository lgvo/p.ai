package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// maxEndDiagnostic bounds why an attachment ended abnormally, as its end
// frame tells the helper.
const maxEndDiagnostic = 512

// relayTerminal carries t over conn once a claim is answered, reading frames
// first from buffered: input and resize frames from the helper go to t, and
// t's output goes back in output frames. When the helper's side ends, sends a
// frame the relay does not take, or t refuses one, the relay closes t. Once
// t's output ends, an end frame says why when t's Read failed, and the relay
// closes t and conn and returns. A panic in t is logged and ends the relay.
func relayTerminal(conn net.Conn, buffered io.Reader, t Terminal) {
	frames := api.NewFrameConn(conn, buffered)
	frames.SetReadDeadline(time.Time{})
	var once sync.Once
	end := func() { once.Do(func() { closeTerminal(t) }) }
	output := make(chan struct{})
	go func() {
		defer close(output)
		defer conn.Close()
		defer end()
		defer recoverRelay("terminal output")
		buffer := make([]byte, 16384)
		for {
			n, err := t.Read(buffer)
			if n > 0 {
				if frames.WriteFrame(api.FrameOutput, buffer[:n]) != nil {
					return
				}
			}
			if err != nil {
				diagnostic := ""
				if !errors.Is(err, io.EOF) {
					diagnostic = err.Error()
					if len(diagnostic) > maxEndDiagnostic {
						diagnostic = diagnostic[:maxEndDiagnostic]
					}
				}
				_ = frames.WriteFrame(api.FrameEnd, []byte(diagnostic))
				return
			}
		}
	}()
	// Ending the terminal ends its output, so the output side always sends
	// the end frame; input that t refused is not sent again.
	func() {
		defer end()
		defer recoverRelay("terminal input")
		for {
			f, err := frames.ReadFrame()
			if err != nil || !relayInput(t, f) {
				return
			}
		}
	}()
	<-output
}

// watchHangup cancels a claim, with cancel, when its connection ends while
// the claim runs, without consuming what the connection sends: a byte that
// arrives ends the watch and stays in reader for the relay. stop ends the
// watch and returns once it ended.
func watchHangup(conn net.Conn, reader *bufio.Reader, cancel context.CancelFunc) (stop func()) {
	var stopping atomic.Bool
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		if _, err := reader.Peek(1); err != nil && !stopping.Load() {
			cancel()
		}
	}()
	return func() {
		stopping.Store(true)
		conn.SetReadDeadline(time.Now())
		<-watched
		conn.SetReadDeadline(time.Time{})
	}
}

// relayInput hands one frame from the helper to t, and reports whether the
// relay goes on.
func relayInput(t Terminal, f api.Frame) bool {
	switch f.Kind {
	case api.FrameInput:
		_, err := t.Write(f.Data)
		return err == nil
	case api.FrameResize:
		var size api.TerminalSize
		if json.Unmarshal(f.Data, &size) != nil || size.Validate() != nil {
			return false
		}
		return t.Resize(runtimedriver.Size{Width: size.Width, Height: size.Height}) == nil
	}
	return false
}

// closeTerminal ends t's attachment. Nothing is left to answer, so a panic in
// Close is logged and recovered.
func closeTerminal(t Terminal) {
	defer recoverRelay("closing a terminal")
	t.Close()
}

func recoverRelay(what string) {
	if value := recover(); value != nil {
		logPanic("attachment relay: "+what, value)
	}
}
