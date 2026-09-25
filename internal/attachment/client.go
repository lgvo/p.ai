package attachment

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/lgvo/p.ai/internal/control"
	"golang.org/x/sys/unix"
)

// Client requests the decision, transfers the secret on an inherited private
// socket, and carries terminal bytes. It has no native Incus authority.
func Client(ctx context.Context, socket, id string) error {
	params, _ := json.Marshal(map[string]any{"v": 1, "uuid": id})
	response, err := control.Call(ctx, socket, "session.attach", params)
	if err != nil {
		return err
	}
	var reply struct {
		Result control.PendingAttachment `json:"result"`
		Error  *control.RPCError         `json:"error"`
	}
	if json.Unmarshal(response, &reply) != nil {
		return errors.New("invalid attach response")
	}
	if reply.Error != nil {
		return errors.New(reply.Error.Message)
	}
	if reply.Result.V != 1 || len(reply.Result.Token) != 64 || !time.Now().Before(reply.Result.ExpiresAt) {
		return errors.New("invalid pending attachment")
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	parentFile := os.NewFile(uintptr(pair[0]), "attachment-client")
	helperFile := os.NewFile(uintptr(pair[1]), "attachment-helper")
	defer helperFile.Close()
	conn, err := net.FileConn(parentFile)
	parentFile.Close()
	if err != nil {
		return err
	}
	defer conn.Close()
	c := &carrier{Conn: conn}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(executable, "attach-helper")
	cmd.ExtraFiles = []*os.File{helperFile}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return err
	}
	helperFile.Close()
	childDone := make(chan error, 1)
	go func() { childDone <- cmd.Wait() }()
	width, height := 80, 24
	if term.IsTerminal(os.Stdin.Fd()) {
		if w, h, e := term.GetSize(os.Stdin.Fd()); e == nil {
			width, height = attachmentSize(w, h)
		}
		old, e := term.MakeRaw(os.Stdin.Fd())
		if e != nil {
			return e
		}
		defer term.Restore(os.Stdin.Fd(), old)
	}
	if err = c.writeJSON(carrierInit, initiation{Socket: socket, Token: reply.Result.Token, Width: width, Height: height}); err != nil {
		return err
	}
	inputDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 16384)
		for {
			n, e := os.Stdin.Read(buffer)
			if n > 0 {
				if w := c.write(carrierInput, buffer[:n]); w != nil {
					inputDone <- w
					return
				}
			}
			if e != nil {
				inputDone <- e
				return
			}
		}
	}()
	outputDone := make(chan error, 1)
	go func() {
		for {
			frame, e := c.read()
			if e != nil {
				outputDone <- e
				return
			}
			switch frame.kind {
			case carrierReady:
			case carrierOutput:
				if _, e = os.Stdout.Write(frame.data); e != nil {
					outputDone <- e
					return
				}
			case carrierError:
				outputDone <- errors.New(string(frame.data))
				return
			default:
				outputDone <- errors.New("invalid helper carrier frame")
				return
			}
		}
	}()
	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	defer signal.Stop(resize)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e := <-childDone:
			if e != nil {
				return e
			}
			childDone = nil
		case e := <-inputDone:
			if e == io.EOF {
				return nil
			}
			return e
		case e := <-outputDone:
			if e == io.EOF {
				return nil
			}
			return e
		case <-resize:
			if w, h, e := term.GetSize(os.Stdin.Fd()); e == nil {
				w, h = attachmentSize(w, h)
				if e = c.writeJSON(carrierResize, size{w, h}); e != nil {
					return e
				}
			}
		}
	}
}

// A PTY can report zero dimensions when its parent has no terminal, or while
// its window is minimized. The helper and Incus require usable dimensions.
func attachmentSize(width, height int) (int, int) {
	if width < 1 || width > 65535 {
		width = 80
	}
	if height < 1 || height > 65535 {
		height = 24
	}
	return width, height
}
