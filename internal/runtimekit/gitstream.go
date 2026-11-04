package runtimekit

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

const gitSocketPath = "/run/p/git.sock"

// GitStream is the fixed SSH ProxyCommand transport. SSH receives only this
// operation; no caller may choose a socket, address, or command.
func GitStream() error {
	if os.Geteuid() != 1000 {
		return errors.New("Git stream requires fixed p uid 1000")
	}
	if err := validateEndpoints(); err != nil {
		return fmt.Errorf("Git endpoint: %w", err)
	}
	conn, err := net.DialTimeout("unix", gitSocketPath, 3*time.Second)
	if err != nil {
		return fmt.Errorf("connect Git endpoint: %w", err)
	}
	defer conn.Close()
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return errors.New("Git endpoint is not a Unix stream")
	}
	return bridgeGitStream(unixConn, os.Stdin, os.Stdout)
}

type halfCloseStream interface {
	io.Reader
	io.Writer
	CloseWrite() error
	Close() error
}

func bridgeGitStream(conn halfCloseStream, input io.Reader, output io.Writer) error {
	inputResult := make(chan error, 1)
	go func() {
		_, err := io.Copy(conn, input)
		if err == nil {
			err = conn.CloseWrite()
		}
		if err != nil {
			conn.Close() // Unblock the other direction on a broken input stream.
		}
		inputResult <- err
	}()
	_, outputErr := io.Copy(output, conn)
	conn.Close() // Also releases the writer when the peer ends first.
	select {
	case inputErr := <-inputResult:
		if inputErr != nil {
			return fmt.Errorf("write Git stream: %w", inputErr)
		}
	default:
	}
	if outputErr != nil {
		return fmt.Errorf("read Git stream: %w", outputErr)
	}
	return nil
}
