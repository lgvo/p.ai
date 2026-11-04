package protocol

import "net"

// Inherited is the connection the daemon gave the driver it started, on
// ConnFD. Call it first thing: the connection becomes close-on-exec and
// ConnFD is closed, and ExecutableFD is marked close-on-exec, so no process
// the driver starts inherits either.
func Inherited() (*net.UnixConn, error) { return wire.Inherited() }
