package control

import (
	"errors"
	"os"
	"path/filepath"
)

// DefaultControlSocket is the standard NixOS instance's private control socket.
const DefaultControlSocket = "/var/lib/p/control.sock"

// ClientSocket selects the explicit endpoint, P_SOCKET, or the standard instance.
// An invalid override fails rather than connecting to a different instance.
func ClientSocket(explicit string) (string, error) {
	socket := explicit
	if socket == "" {
		socket = os.Getenv("P_SOCKET")
	}
	if socket == "" {
		socket = DefaultControlSocket
	}
	if !filepath.IsAbs(socket) {
		return "", errors.New("control socket must be an absolute path (check P_SOCKET or the explicit socket argument)")
	}
	return socket, nil
}
