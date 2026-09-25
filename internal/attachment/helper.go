package attachment

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lgvo/p.ai/internal/control"
)

// Helper serves only its inherited private socket. It is a separate temporary
// host process so client SIGKILL closes the carrier without killing the owner
// that must await Incus teardown before releasing a reachable lease.
func Helper(file *os.File) error {
	conn, err := net.FileConn(file)
	file.Close()
	if err != nil {
		return err
	}
	defer conn.Close()
	return runHelper(&carrier{Conn: conn})
}
func runHelper(c *carrier) (resultErr error) {
	defer func() {
		if resultErr != nil {
			_ = c.write(carrierError, []byte(resultErr.Error()))
		}
	}()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	f, err := c.read()
	if err != nil {
		return err
	}
	var init initiation
	if f.kind != carrierInit || json.Unmarshal(f.data, &init) != nil || len(init.Token) != 64 || init.Socket == "" || init.Width < 1 || init.Height < 1 || init.Width > 65535 || init.Height > 65535 {
		return errors.New("invalid private attachment initiation")
	}
	c.SetReadDeadline(time.Time{})
	scope, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := make(chan carrierFrame, 16)
	carrierDone := make(chan struct{})
	go func() {
		defer close(carrierDone)
		for {
			next, e := c.read()
			if e != nil {
				cancel()
				return
			}
			if next.kind != carrierInput && next.kind != carrierResize {
				cancel()
				return
			}
			select {
			case frames <- next:
			case <-scope.Done():
				return
			default:
				cancel()
				return
			}
		}
	}()
	rpc, err := dialLease(scope, init.Socket)
	if err != nil {
		return err
	}
	defer rpc.fail()
	var launch control.AttachmentLaunch
	if err = rpc.call(scope, "attachment.claim", map[string]any{"v": 1, "token": init.Token}, &launch); err != nil {
		return err
	}
	leaseCtx, stopMonitor := context.WithCancel(context.Background())
	defer stopMonitor()
	go rpc.monitor(leaseCtx)
	go func() {
		select {
		case <-rpc.lost:
			cancel()
		case <-scope.Done():
		}
	}()
	n, err := newNative(launch)
	if err != nil {
		return err
	}
	// Cleanup uses a context independent of the carrier. While RPC is reachable,
	// confirmed presence is retained until Incus reports the temporary exec done.
	defer func() {
		cleanup, cancelCleanup := context.WithCancel(context.Background())
		defer cancelCleanup()
		go func() {
			select {
			case <-rpc.lost:
				cancelCleanup()
			case <-cleanup.Done():
			}
		}()
		if e := n.teardown(cleanup); resultErr == nil && e != nil {
			resultErr = e
		}
	}()
	opening, stopOpening := context.WithTimeout(scope, 15*time.Second)
	err = n.establish(opening, launch, init.Width, init.Height)
	stopOpening()
	if err != nil {
		return err
	}
	select {
	case <-n.controlDone:
		return errors.New("attachment exited before confirmation")
	case <-n.observerDone:
		return errors.New("attachment completion observer ended before confirmation")
	default:
	}
	// Carrier loss must tear down the native channel even while confirmation is
	// in flight. Keep the RPC exchange independent so a reachable, possibly
	// already-confirmed lease is not closed before native completion.
	go func() {
		select {
		case <-scope.Done():
		case <-n.observerDone:
			cancel()
		case <-n.controlDone:
			cancel()
		}
		n.closeChannel()
	}()
	select {
	case <-scope.Done():
		return scope.Err()
	case <-n.controlDone:
		return errors.New("attachment exited before confirmation")
	case <-n.observerDone:
		return errors.New("attachment completion observer ended before confirmation")
	default:
	}
	if err = rpc.call(leaseCtx, "attachment.confirm", map[string]any{"v": 1, "operation": n.operation}, nil); err != nil {
		return err
	}
	select {
	case <-scope.Done():
		return scope.Err()
	case <-n.controlDone:
		return errors.New("attachment ended during confirmation")
	case <-n.observerDone:
		return errors.New("attachment completion observer ended during confirmation")
	default:
	}
	if err = c.write(carrierReady, nil); err != nil {
		return err
	}
	outputDone := make(chan error, 1)
	go func() {
		for {
			kind, reader, e := n.data.NextReader()
			if e != nil {
				outputDone <- e
				return
			}
			// Incus sends a text frame as its write barrier/end-of-stream marker.
			if kind == websocket.TextMessage {
				outputDone <- nil
				return
			}
			if kind != websocket.BinaryMessage {
				outputDone <- errors.New("invalid Incus terminal frame")
				return
			}
			buffer := make([]byte, 16384)
			for {
				count, e := reader.Read(buffer)
				if count > 0 {
					if w := c.write(carrierOutput, buffer[:count]); w != nil {
						outputDone <- w
						return
					}
				}
				if e == io.EOF {
					break
				}
				if e != nil {
					outputDone <- e
					return
				}
			}
		}
	}()
	for {
		select {
		case <-scope.Done():
			return nil
		case <-carrierDone:
			return nil
		case <-n.controlDone:
			return nil
		case <-n.observerDone:
			return nil
		case e := <-outputDone:
			return e
		case frame := <-frames:
			if frame.kind == carrierInput {
				n.data.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err = n.data.WriteMessage(websocket.BinaryMessage, frame.data); err != nil {
					return err
				}
			} else {
				var dimensions size
				if json.Unmarshal(frame.data, &dimensions) != nil {
					return errors.New("invalid terminal resize")
				}
				if err = n.resize(dimensions.Width, dimensions.Height); err != nil {
					return err
				}
			}
		}
	}
}
