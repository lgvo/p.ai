package driverwire

import (
	"encoding/json"
	"errors"
	"fmt"
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
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
	// decoded is Data as the protocol's kinds decoded it, as the frame was
	// read; nil when the error object had no data.
	decoded ErrorData
}

// ErrorKinds keeps a protocol's error kinds across the connection: the kinds
// its contract defines, and the facts its typed errors carry, in the data
// object of an error object. Each protocol supplies its own.
type ErrorKinds interface {
	// Data is the data object of err's error object, which names every kind
	// err has in its field kinds. It must encode as JSON.
	Data(err error) any
	// Decode decodes the data object of an error object strictly. A data
	// object it refuses is a malformed frame, which ends the connection. It
	// must take the data object of a refused hello: kinds, empty, and
	// supported, the versions the driver speaks.
	Decode(data json.RawMessage) (ErrorData, error)
}

// ErrorData is the data object of an error object, decoded.
type ErrorData interface {
	// Errors are the errors of the kinds it names, which errors.Is and
	// errors.As find, for an error object whose message is message. A kind
	// the protocol does not know is an error of its own, since its meaning
	// cannot be kept.
	Errors(message string) ([]error, error)
}

// Sentinel is an error kind that errors.Is finds, and its name on the wire.
type Sentinel struct {
	Name string
	Err  error
}

// Sentinels is a protocol's table of the sentinel kinds its errors can have.
type Sentinels []Sentinel

// Names are the names of the kinds err is, in the table's order; empty, not
// nil, when it is none.
func (t Sentinels) Names(err error) []string {
	names := []string{}
	for _, s := range t {
		if errors.Is(err, s.Err) {
			names = append(names, s.Name)
		}
	}
	return names
}

// Kind is the kind named name, or nil when the table has none of that name.
func (t Sentinels) Kind(name string) error {
	for _, s := range t {
		if s.Name == name {
			return s.Err
		}
	}
	return nil
}

// versionData is the data object of a refused hello.
type versionData struct {
	Kinds     []string `json:"kinds"`
	Supported []string `json:"supported"`
}

// toWireError is err with every kind it has.
func (p *Protocol) toWireError(err error) *wireError {
	code := codeDriver
	var invalid errInvalidParams
	var noMethod errNoMethod
	switch {
	case errors.As(err, &invalid):
		code = codeInvalidParams
	case errors.As(err, &noMethod):
		code = codeNoMethod
	}
	data, e := json.Marshal(p.Kinds.Data(err))
	if e != nil {
		// The protocol's data object is a fixed shape of its own: one that
		// does not encode is a fault of the protocol, not of a call.
		panic(fmt.Sprintf("the %s protocol's error data does not encode: %v", p.Name, e))
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

// fromWireError is the error e describes. A kind the protocol does not know
// makes it an error of its own, since its meaning cannot be kept.
func fromWireError(e *wireError) error {
	remote := &RemoteError{Code: e.Code, Message: e.Message}
	if e.decoded == nil {
		return remote
	}
	kinds, err := e.decoded.Errors(e.Message)
	if err != nil {
		return err
	}
	remote.kinds = kinds
	return remote
}

// errInvalidParams marks parameters that could not be decoded: the call was
// refused before the driver saw it.
type errInvalidParams struct{ err error }

func (e errInvalidParams) Error() string { return "invalid parameters: " + e.err.Error() }

// errNoMethod is a call of a method the protocol does not have.
type errNoMethod struct{ method string }

func (e errNoMethod) Error() string { return fmt.Sprintf("the protocol has no method %q", e.method) }

// NoMethod is the error of a call of a method the protocol does not have.
func NoMethod(method string) error { return errNoMethod{method} }

// unavailable is an error of the protocol's Unavailable kind: nothing
// happened.
func (p *Protocol) unavailable(what string, cause error) error {
	return fmt.Errorf("%s: %w: %w", what, cause, p.Unavailable)
}

// unknownOutcome is an error of the protocol's UnknownOutcome kind.
func (p *Protocol) unknownOutcome(what string, cause error) error {
	return fmt.Errorf("%s: %w: %w", what, cause, p.UnknownOutcome)
}
