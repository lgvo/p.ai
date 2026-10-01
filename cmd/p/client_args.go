package main

import (
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/lgvo/p.ai/internal/control"
)

func apiArguments(args []string) (string, string, json.RawMessage, error) {
	if len(args) == 1 && filepath.IsAbs(args[0]) {
		return "", "", nil, errors.New("usage: p api [CONTROL_SOCKET] METHOD [JSON_PARAMS]")
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
		return "", "", nil, errors.New("usage: p api [CONTROL_SOCKET] METHOD [JSON_PARAMS]")
	}
	socket, err := control.ClientSocket(explicit)
	if err != nil {
		return "", "", nil, err
	}
	params := json.RawMessage(`{"v":1}`)
	if len(args) == 2 {
		params = json.RawMessage(args[1])
	}
	return socket, args[0], params, nil
}
