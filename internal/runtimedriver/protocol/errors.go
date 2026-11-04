package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lgvo/p.ai/internal/driverwire"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/strictjson"
)

// errorData keeps an error's kinds and the facts its typed errors carry.
type errorData struct {
	Kinds []string `json:"kinds"`
	// Diagnostic is a *runtimedriver.HostError's diagnostic.
	Diagnostic string `json:"diagnostic,omitempty"`
	// Terminal names the terminal of a *runtimedriver.UnendedTerminal.
	Terminal string `json:"terminal,omitempty"`
	// Supported lists the versions the driver speaks, for a refused hello.
	Supported []string `json:"supported,omitempty"`
}

// The kinds an error can have, by name: runtimedriver's, and the context
// errors, which core tells apart from other failures as it does for a
// driver in its own process.
var sentinels = driverwire.Sentinels{
	{Name: "unknown_outcome", Err: runtimedriver.ErrUnknownOutcome},
	{Name: "mismatch", Err: runtimedriver.ErrMismatch},
	{Name: "unavailable", Err: runtimedriver.ErrUnavailable},
	{Name: "unsupported", Err: runtimedriver.ErrUnsupported},
	{Name: "no_service", Err: runtimedriver.ErrNoService},
	{Name: "not_ready", Err: runtimedriver.ErrNotReady},
	{Name: "canceled", Err: context.Canceled},
	{Name: "deadline_exceeded", Err: context.DeadlineExceeded},
}

const (
	kindHost    = "host"             // a *runtimedriver.HostError
	kindUnended = "unended_terminal" // a *runtimedriver.UnendedTerminal
)

// errorKinds keeps runtimedriver's error kinds across the connection.
type errorKinds struct{}

// Data is err's data object, with every kind it has.
func (errorKinds) Data(err error) any {
	data := &errorData{Kinds: sentinels.Names(err)}
	var host *runtimedriver.HostError
	if errors.As(err, &host) {
		data.Kinds = append(data.Kinds, kindHost)
		data.Diagnostic = host.Diagnostic
	}
	var unended *runtimedriver.UnendedTerminal
	if errors.As(err, &unended) {
		data.Kinds = append(data.Kinds, kindUnended)
		data.Terminal = unended.ID
	}
	return data
}

func (errorKinds) Decode(raw json.RawMessage) (driverwire.ErrorData, error) {
	var data errorData
	if err := strictjson.Decode(raw, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

// Errors are the kinds d names. A kind P does not know makes the error one
// of its own, since its meaning cannot be kept.
func (d *errorData) Errors(message string) ([]error, error) {
	var kinds []error
	for _, name := range d.Kinds {
		switch name {
		case kindHost:
			kinds = append(kinds, &runtimedriver.HostError{Diagnostic: d.Diagnostic})
		case kindUnended:
			if d.Terminal == "" {
				return nil, fmt.Errorf("the driver named no terminal for an unended terminal: %s", message)
			}
			kinds = append(kinds, &runtimedriver.UnendedTerminal{ID: d.Terminal, Err: errors.New(message)})
		default:
			kind := sentinels.Kind(name)
			if kind == nil {
				return nil, fmt.Errorf("the driver answered an error of unknown kind %q: %s", name, message)
			}
			kinds = append(kinds, kind)
		}
	}
	return kinds, nil
}

// RemoteError is an error the other side of the connection returned, with
// the kinds it had there: errors.Is and errors.As find them.
type RemoteError = driverwire.RemoteError
