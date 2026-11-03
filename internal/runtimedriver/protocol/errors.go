package protocol

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// The JSON-RPC error codes the protocol uses.
const (
	codeInvalidRequest = -32600
	codeNoMethod       = -32601
	codeInvalidParams  = -32602
	codeDriver         = -32000 // the operation failed; data.kinds says how
	codeVersion        = -32001 // the hello named a version the driver does not speak
	codeHelloRequired  = -32002 // a request came before the hello, or a second hello
)

// wireError is a JSON-RPC error object.
type wireError struct {
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Data    *errorData `json:"data,omitempty"`
}

// errorData keeps an error's kinds and the facts its typed errors carry.
type errorData struct {
	Kinds []string `json:"kinds"`
	// Diagnostic is a *runtimedriver.HostError's diagnostic.
	Diagnostic string `json:"diagnostic,omitempty"`
	// Terminal names the terminal of a *runtimedriver.UnendedTerminal.
	Terminal string `json:"terminal,omitempty"`
	// Supported lists the versions the driver speaks, for codeVersion.
	Supported []string `json:"supported,omitempty"`
}

// sentinel is an error kind and its name on the wire.
type sentinel struct {
	name string
	err  error
}

// The kinds an error can have, by name: runtimedriver's, and the context
// errors, which core tells apart from other failures as it does for a
// driver in its own process.
var sentinels = []sentinel{
	{"unknown_outcome", runtimedriver.ErrUnknownOutcome},
	{"mismatch", runtimedriver.ErrMismatch},
	{"unavailable", runtimedriver.ErrUnavailable},
	{"unsupported", runtimedriver.ErrUnsupported},
	{"no_service", runtimedriver.ErrNoService},
	{"not_ready", runtimedriver.ErrNotReady},
	{"canceled", context.Canceled},
	{"deadline_exceeded", context.DeadlineExceeded},
}

const (
	kindHost    = "host"             // a *runtimedriver.HostError
	kindUnended = "unended_terminal" // a *runtimedriver.UnendedTerminal
)

// toWireError is err with every kind it has.
func toWireError(err error) *wireError {
	code := codeDriver
	var invalid errInvalidParams
	var noMethod errNoMethod
	switch {
	case errors.As(err, &invalid):
		code = codeInvalidParams
	case errors.As(err, &noMethod):
		code = codeNoMethod
	}
	data := &errorData{Kinds: []string{}}
	for _, s := range sentinels {
		if errors.Is(err, s.err) {
			data.Kinds = append(data.Kinds, s.name)
		}
	}
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
	return &wireError{Code: code, Message: err.Error(), Data: data}
}

// RemoteError is an error the other side of the connection returned, with
// the kinds it had there: errors.Is and errors.As find them.
type RemoteError struct {
	Code    int
	Message string
	kinds   []error
}

func (e *RemoteError) Error() string   { return e.Message }
func (e *RemoteError) Unwrap() []error { return e.kinds }

// fromWireError is the error e describes. A kind P does not know makes it
// an error of its own, since its meaning cannot be kept.
func fromWireError(e *wireError) error {
	remote := &RemoteError{Code: e.Code, Message: e.Message}
	if e.Data == nil {
		return remote
	}
	for _, name := range e.Data.Kinds {
		switch name {
		case kindHost:
			remote.kinds = append(remote.kinds, &runtimedriver.HostError{Diagnostic: e.Data.Diagnostic})
		case kindUnended:
			if e.Data.Terminal == "" {
				return fmt.Errorf("the driver named no terminal for an unended terminal: %s", e.Message)
			}
			remote.kinds = append(remote.kinds, &runtimedriver.UnendedTerminal{ID: e.Data.Terminal, Err: errors.New(e.Message)})
		default:
			i := slices.IndexFunc(sentinels, func(s sentinel) bool { return s.name == name })
			if i < 0 {
				return fmt.Errorf("the driver answered an error of unknown kind %q: %s", name, e.Message)
			}
			remote.kinds = append(remote.kinds, sentinels[i].err)
		}
	}
	return remote
}

// errInvalidParams marks parameters that could not be decoded: the call was
// refused before the driver saw it.
type errInvalidParams struct{ err error }

func (e errInvalidParams) Error() string { return "invalid parameters: " + e.err.Error() }

// errLost is the cause of a call whose connection was lost.
var errLost = errors.New("the connection to the runtime driver was lost")

// unavailable is an error of kind ErrUnavailable: nothing happened.
func unavailable(what string, cause error) error {
	return fmt.Errorf("%s: %w: %w", what, cause, runtimedriver.ErrUnavailable)
}

// unknownOutcome is an error of kind ErrUnknownOutcome.
func unknownOutcome(what string, cause error) error {
	return fmt.Errorf("%s: %w: %w", what, cause, runtimedriver.ErrUnknownOutcome)
}
