package protocol

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

// Inherited is the connection the daemon gave the driver it started, on
// ConnFD. Call it first thing: the connection becomes close-on-exec and
// ConnFD is closed, and ExecutableFD is marked close-on-exec, so no process
// the driver starts inherits either.
func Inherited() (*net.UnixConn, error) {
	syscall.CloseOnExec(ExecutableFD)
	f := os.NewFile(ConnFD, "runtime driver connection")
	c, err := net.FileConn(f)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("descriptor %d is not a runtime driver connection: %w", ConnFD, err)
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		c.Close()
		return nil, fmt.Errorf("descriptor %d is not a Unix socket", ConnFD)
	}
	return uc, nil
}
