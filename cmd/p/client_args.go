package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

func apiArguments(args []string) (string, string, json.RawMessage, error) {
	if len(args) == 1 && filepath.IsAbs(args[0]) {
		return "", "", nil, usageError("usage: p api [CONTROL_SOCKET] METHOD [JSON_PARAMS]")
	}
	explicit := ""
	// Preserve the original socket/method form alongside method/JSON params.
	if len(args) == 3 || len(args) == 2 && (filepath.IsAbs(args[0]) || !json.Valid([]byte(args[1]))) {
		explicit, args = args[0], args[1:]
		if explicit == "" {
			return "", "", nil, errors.New("control socket must be an absolute path")
		}
	}
	if len(args) < 1 || len(args) > 2 || args[0] == "" {
		return "", "", nil, usageError("usage: p api [CONTROL_SOCKET] METHOD [JSON_PARAMS]")
	}
	socket, err := api.ClientSocket(explicit)
	if err != nil {
		return "", "", nil, err
	}
	params := json.RawMessage(`{}`)
	if args[0] == api.SystemHello.Name {
		// p api agrees the protocol itself; a repeated hello names it again.
		params = json.RawMessage(`{"protocol":"` + api.Protocol + `"}`)
	}
	if len(args) == 2 {
		params = json.RawMessage(args[1])
	}
	return socket, args[0], params, nil
}

// apiDeadline is the time p api gives one call. operation.wait may take its
// whole timeout, so its call gets the timeout plus 5 seconds to connect,
// agree the protocol and read the reply; any other call 10 seconds.
func apiDeadline(method string, params json.RawMessage) time.Duration {
	if method == api.OperationWait.Name {
		var wait struct {
			Timeout int `json:"timeout"`
		}
		if json.Unmarshal(params, &wait) == nil && wait.Timeout >= 1 && wait.Timeout <= api.MaxWaitSeconds {
			return time.Duration(wait.Timeout)*time.Second + 5*time.Second
		}
	}
	return 10 * time.Second
}
