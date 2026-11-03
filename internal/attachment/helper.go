// Package attachment implements `p attach` and its trusted, temporary host
// attachment helper, which carry a session's terminal that the daemon relays.
package attachment

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/errtext"
)

// claimTimeout bounds reaching the daemon and its claim, which opens the
// session's terminal.
const claimTimeout = 40 * time.Second

// Helper serves only its inherited private socket. It is a separate temporary
// host process in a fixed mode, so the daemon can tell it from any other
// client: only it may claim a pending attachment's token. It holds no runtime
// authority: the daemon opens the terminal and relays it over the helper's
// control connection.
func Helper(file *os.File) error {
	conn, err := net.FileConn(file)
	file.Close()
	if err != nil {
		return err
	}
	defer conn.Close()
	return runHelper(api.NewFrameConn(conn, nil))
}

// runHelper claims the attachment the client's init frame names, then relays
// frames between the client's carrier and the daemon until either ends. Its
// error, if any, is the client's last frame.
func runHelper(c *api.FrameConn) (resultErr error) {
	defer func() {
		if resultErr != nil {
			_ = c.WriteFrame(api.FrameError, []byte(errtext.Sentence(resultErr)))
		}
	}()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	f, err := c.ReadFrame()
	if err != nil {
		return err
	}
	var init initiation
	if f.Kind != api.FrameInit || json.Unmarshal(f.Data, &init) != nil || len(init.Token) != 64 || init.Socket == "" ||
		(api.TerminalSize{Width: init.Width, Height: init.Height}).Validate() != nil {
		return errors.New("invalid private attachment initiation")
	}
	c.SetReadDeadline(time.Time{})
	// The client's frames wait while the claim opens the terminal. Losing the
	// client, or more frames than wait, ends the attachment.
	scope, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := make(chan api.Frame, 16)
	go func() {
		defer cancel()
		for {
			next, e := c.ReadFrame()
			if e != nil || next.Kind != api.FrameInput && next.Kind != api.FrameResize {
				return
			}
			select {
			case frames <- next:
			case <-scope.Done():
				return
			default:
				return
			}
		}
	}()
	opening, stopOpening := context.WithTimeout(scope, claimTimeout)
	defer stopOpening()
	daemon, err := api.Dial(opening, init.Socket)
	if err != nil {
		return err
	}
	// Closing the connection ends the attachment: the daemon closes the
	// terminal.
	defer daemon.Close()
	// A client that already went is never counted as attached.
	if err = opening.Err(); err != nil {
		return err
	}
	claim := api.AttachmentClaimRequest{Token: init.Token, Width: init.Width, Height: init.Height}
	if _, err = api.Do(opening, daemon, api.AttachmentClaim, claim); err != nil {
		return err
	}
	relay := daemon.Frames()
	if c.WriteFrame(api.FrameReady, nil) != nil {
		return nil
	}
	ended := make(chan error, 1)
	go func() { ended <- relayOutput(relay, c) }()
	for {
		select {
		case <-scope.Done():
			return nil
		case err := <-ended:
			return err
		case frame := <-frames:
			if frame.Kind == api.FrameResize {
				var size api.TerminalSize
				if json.Unmarshal(frame.Data, &size) != nil || size.Validate() != nil {
					return errors.New("invalid terminal size")
				}
			}
			if relay.WriteFrame(frame.Kind, frame.Data) != nil {
				return nil
			}
		}
	}
}

// relayOutput copies the daemon's output to the client until the daemon's
// end frame, which says why when the attachment ended abnormally, or until
// either connection goes.
func relayOutput(relay, c *api.FrameConn) error {
	for {
		f, err := relay.ReadFrame()
		if err != nil {
			return nil
		}
		switch f.Kind {
		case api.FrameOutput:
			if c.WriteFrame(api.FrameOutput, f.Data) != nil {
				return nil
			}
		case api.FrameEnd:
			if len(f.Data) > 0 {
				return errors.New(string(f.Data))
			}
			return nil
		default:
			return errors.New("invalid attachment relay frame")
		}
	}
}
