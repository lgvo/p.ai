package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"

	"github.com/lgvo/p.ai/internal/strictjson"
)

// errInvalidCall reports a request that Call refuses to send.
var errInvalidCall = errors.New("invalid input")

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

// Call sends one request and returns the complete response envelope.
func Call(ctx context.Context, socket, method string, params json.RawMessage) (json.RawMessage, error) {
	if len(method) == 0 || len(method) > 128 || len(params) > MaxFrameBytes/2 || !json.Valid(params) {
		return nil, errInvalidCall
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	request := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}{"2.0", 1, method, params}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return nil, err
	}
	line, err := ReadFrame(bufio.NewReaderSize(conn, MaxFrameBytes+1))
	if err != nil {
		return nil, err
	}
	var response struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *RPCError       `json:"error"`
	}
	if err := strictjson.Decode(line, &response); err != nil || response.JSONRPC != "2.0" || response.ID != 1 {
		return nil, errors.New("invalid daemon response")
	}
	if (len(response.Result) == 0) == (response.Error == nil) {
		return nil, errors.New("daemon response must contain exactly one result or error")
	}
	return json.RawMessage(line), nil
}
