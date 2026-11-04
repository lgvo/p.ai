// Package driverwire is how P's daemon reaches a driver that runs as its own
// executable, whatever the driver's contract: JSON-RPC 2.0 over a Unix stream
// connection, one newline-terminated JSON frame per message, decoded
// strictly. Requests go both ways: core calls the driver's operations, and
// the driver calls back the functions core lent a call in flight: its gate,
// and the protocol's other callbacks, such as an authorization with
// parameters and a result. A protocol built on it supplies a Protocol and
// its own methods; Connect gives core a Client on a connection, and Serve
// serves a driver on one.
//
// The connection starts with a hello: core sends the version, the driver's
// configuration and the settings core owns that the driver needs, and the
// driver answers with the version it agreed and its description, or
// refuses. Nothing else is sent before it.
//
// A request of a method that takes descriptors, such as a connection of its
// own for a terminal's bytes, carries them alongside (SCM_RIGHTS).
//
// The driver calls back what a call lent only while the call is in flight:
// a call never returns while its gate or a callback runs, so one that runs
// when the connection is lost or the call is cancelled is waited for, and
// one called after is refused. A call whose connection is lost after its
// request was sent then ends as follows: an effect returns an error of the
// protocol's UnknownOutcome kind, unless it took a gate that never ran,
// which proves that nothing happened, or that refused, whose refusal it
// returns; a read returns an error of its Unavailable kind. Errors keep their kinds across the connection, the
// context errors included. A call whose context ended returns an error that
// carries the context's error, whether the driver's answer or the end of
// waiting for it came first; a success the driver answered is returned.
package driverwire

// MaxFrameBytes bounds one frame, its newline included.
const MaxFrameBytes = 16 << 20

// The descriptors a launcher gives the driver it starts: the connection,
// and the driver's own executable, which it was started from and may close.
const (
	ConnFD       = 3
	ExecutableFD = 4
)

// Protocol is what a protocol built on driverwire supplies: what sets its
// driver's calls apart.
type Protocol struct {
	// Name says which driver the protocol reaches, in messages, such as
	// "runtime driver".
	Name string
	// Version is the protocol this build speaks. Core and the driver agree
	// it in the hello; a driver that speaks another one is refused.
	Version string
	// Effects are the methods that change state. A lost call to one may
	// have happened; a lost call to any other method changed nothing.
	Effects map[string]bool
	// Descriptors are the methods whose requests carry descriptors, each
	// with what its descriptors are, in the order they are passed, which
	// names them in errors.
	Descriptors map[string][]string
	// Callbacks are the methods the driver calls core by, besides gate: the
	// functions a call of core's may lend the driver while it is in flight.
	Callbacks map[string]bool
	// Unavailable is the contract's kind of a call that did not happen, and
	// UnknownOutcome its kind of an effect that may have happened.
	Unavailable, UnknownOutcome error
	// Kinds keeps the errors' kinds across the connection.
	Kinds ErrorKinds
}

// The methods every protocol has. methodGate goes from the driver to core:
// it calls the call's gate, which takes no parameters.
const (
	methodHello = "hello"
	methodGate  = "gate"
	// methodCancel is a notification: core no longer waits for the call it
	// names, whose context the driver cancels.
	methodCancel = "cancel"
)
