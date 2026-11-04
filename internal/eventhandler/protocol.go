// Package eventhandler is the event-handler capability: the reduced events
// P sends, and the protocol an executable event handler speaks with P's
// daemon, built on driverwire: JSON-RPC 2.0 over a Unix socket pair, one
// newline-terminated JSON frame per message, decoded strictly.
//
// The connection starts with a hello: P sends Version, the capability and
// the handler's config from trusted host configuration, and the handler
// answers with the version it agreed and its Identity, its plugin ID and
// version, or refuses the config. Then P sends one event at a time, in
// order, with event.handle, and the handler answers Handled or Skipped. The
// handler calls nothing back: event-handler defines no method of P's. P
// never waits on a handler for a decision; an event it could not deliver is
// lost, and nothing is settled.
package eventhandler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"

	"github.com/lgvo/p.ai/internal/driverwire"
	"github.com/lgvo/p.ai/internal/strictjson"
)

// Version is the protocol this build speaks. P and the handler agree it in
// the hello; a handler that speaks another one is refused.
const Version = "p.event-handler/v1"

// Capability is the capability P names in the hello.
const Capability = "event-handler"

// Status is what a handler did with an event.
type Status string

const (
	Handled Status = "handled" // it acted on the event
	Skipped Status = "skipped" // it chose to do nothing with it
)

// Identity is what a handler answers the hello with: the plugin ID and
// version of its package.
type Identity struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// ErrUnavailable is the kind of a call that did not reach a handler or got
// no answer from it: the handler is not running, or its connection was
// lost.
var ErrUnavailable = errors.New("the event handler is unavailable")

// core is the part of the hello P owns: the capability it starts the
// handler for.
type core struct {
	Capability string `json:"capability"`
}

const methodHandle = "event.handle"

type handleResult struct {
	Status Status `json:"status"`
}

// wire is the event handler's protocol over driverwire. event.handle
// changes nothing P depends on, so no call is an effect and none ends with
// an unknown outcome.
var wire = &driverwire.Protocol{
	Name:           "event handler",
	Version:        Version,
	Unavailable:    ErrUnavailable,
	UnknownOutcome: ErrUnavailable,
	Kinds:          errorKinds{},
}

// Client is P's end of a connection to an event handler.
type Client struct {
	c        *driverwire.Client
	identity Identity
}

// Connect agrees the protocol with the handler at the other end of uc,
// giving it config, and returns a client for it. The client owns uc; on an
// error uc is closed.
func Connect(ctx context.Context, uc *net.UnixConn, config json.RawMessage) (*Client, error) {
	c, identity, err := driverwire.Connect[core, Identity](ctx, uc, wire, config, core{Capability: Capability})
	if err != nil {
		return nil, err
	}
	return &Client{c: c, identity: identity}, nil
}

// Identity is what the handler answered the hello with.
func (cl *Client) Identity() Identity { return cl.identity }

// Close ends the connection, which tells the handler to exit.
func (cl *Client) Close() error { return cl.c.Close() }

// Done closes once the connection is lost.
func (cl *Client) Done() <-chan struct{} { return cl.c.Done() }

// Handle sends e, which must be valid, and returns what the handler did with
// it. A call whose context ends returns at once with the context's error;
// the handler may still be at work on the event.
func (cl *Client) Handle(ctx context.Context, e Event) (Status, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	if data, err := json.Marshal(e); err != nil || len(data) > MaxEventBytes {
		return "", errors.New("event exceeds 4 KiB")
	}
	var res handleResult
	if err := cl.c.Call(ctx, methodHandle, e, nil, nil, &res); err != nil {
		return "", err
	}
	if res.Status != Handled && res.Status != Skipped {
		return "", fmt.Errorf("the event handler answered status %q, not %q or %q", res.Status, Handled, Skipped)
	}
	return res.Status, nil
}

// Handler handles one event, which P reduced and validated, and says what
// it did with it. An error is diagnostic: P logs that delivery failed.
type Handler func(ctx context.Context, e Event) (Status, error)

// Open makes the handler from its config in trusted host configuration,
// which the hello carries, or refuses the config.
type Open func(config json.RawMessage) (Handler, error)

// Inherited is the connection the daemon gave the handler it started, on
// driverwire.ConnFD. Call it first thing: the connection becomes
// close-on-exec and its descriptor is closed, so no process the handler
// starts inherits it.
func Inherited() (*net.UnixConn, error) { return wire.Inherited() }

// Serve answers the hello on uc as the handler identity names, with the
// handler open makes from its config, then serves events until the
// connection ends or ctx does. Serve owns uc. It returns nil once P closed
// the connection or ctx ended, and otherwise why the connection ended, such
// as a refused hello.
func Serve(ctx context.Context, uc *net.UnixConn, identity Identity, open Open) error {
	return driverwire.Serve(ctx, uc, wire, func(_ context.Context, config json.RawMessage, c core) (driverwire.Handler, Identity, error) {
		if c.Capability != Capability {
			return nil, Identity{}, fmt.Errorf("the event handler provides %s, not %q", Capability, c.Capability)
		}
		handle, err := open(config)
		if err != nil {
			return nil, Identity{}, err
		}
		return func(ctx context.Context, call driverwire.Call) (any, error) {
			if call.Method != methodHandle {
				return nil, driverwire.NoMethod(call.Method)
			}
			e, err := driverwire.Params[Event](call.Params)
			if err != nil {
				return nil, err
			}
			if err := e.Validate(); err != nil {
				return nil, err
			}
			status, err := handle(ctx, e)
			if err != nil {
				return nil, err
			}
			if status != Handled && status != Skipped {
				return nil, fmt.Errorf("the handler did %q with the event", status)
			}
			return handleResult{Status: status}, nil
		}, identity, nil
	})
}

// errorData keeps an error's kinds across the connection.
type errorData struct {
	Kinds []string `json:"kinds"`
	// Supported lists the versions the handler speaks, for a refused hello.
	Supported []string `json:"supported,omitempty"`
}

// sentinels are the kinds an error can have, by name.
var sentinels = driverwire.Sentinels{
	{Name: "unavailable", Err: ErrUnavailable},
	{Name: "canceled", Err: context.Canceled},
	{Name: "deadline_exceeded", Err: context.DeadlineExceeded},
}

type errorKinds struct{}

func (errorKinds) Data(err error) any { return &errorData{Kinds: sentinels.Names(err)} }

func (errorKinds) Decode(raw json.RawMessage) (driverwire.ErrorData, error) {
	var data errorData
	if err := strictjson.Decode(raw, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

func (d *errorData) Errors(message string) ([]error, error) {
	var kinds []error
	for _, name := range d.Kinds {
		kind := sentinels.Kind(name)
		if kind == nil {
			return nil, fmt.Errorf("the event handler answered an error of unknown kind %q: %s", name, message)
		}
		kinds = append(kinds, kind)
	}
	return kinds, nil
}
