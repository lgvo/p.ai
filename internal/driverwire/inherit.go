package driverwire

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

// Inherited is the connection the launcher gave the driver of protocol p it
// started, on ConnFD. Call it first thing: the connection becomes
// close-on-exec and ConnFD is closed, and ExecutableFD is marked
// close-on-exec, so no process the driver starts inherits either.
func (p *Protocol) Inherited() (*net.UnixConn, error) {
	syscall.CloseOnExec(ExecutableFD)
	f := os.NewFile(ConnFD, p.Name+" connection")
	c, err := net.FileConn(f)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("descriptor %d is not a %s connection: %w", ConnFD, p.Name, err)
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		c.Close()
		return nil, fmt.Errorf("descriptor %d is not a Unix socket", ConnFD)
	}
	return uc, nil
}

// SocketPair is a connected pair of Unix stream sockets: one end as a
// connection, the other as the file to pass, named name.
func SocketPair(name string) (*net.UnixConn, *os.File, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	local := os.NewFile(uintptr(fds[0]), name)
	remote := os.NewFile(uintptr(fds[1]), name)
	c, err := net.FileConn(local)
	local.Close()
	if err != nil {
		remote.Close()
		return nil, nil, err
	}
	return c.(*net.UnixConn), remote, nil
}
