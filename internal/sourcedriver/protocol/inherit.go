package protocol

import "net"

// Inherited is the connection the daemon gave the driver it started, on
// driverwire.ConnFD. Call it first thing: the connection becomes
// close-on-exec and its descriptor is closed, and the executable's
// descriptor is marked close-on-exec, so no process the driver starts
// inherits either.
func Inherited() (*net.UnixConn, error) { return wire.Inherited() }
