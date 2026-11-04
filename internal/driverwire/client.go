package driverwire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/lgvo/p.ai/internal/strictjson"
)

// cancelGrace bounds how long a cancelled effect still waits for the
// driver's answer, which says what happened, before its outcome is unknown.
const cancelGrace = 5 * time.Second

// helloParams is what core sends in the hello: the version, the driver's own
// configuration, and the settings core owns that the driver needs.
type helloParams[C any] struct {
	Protocol string          `json:"protocol"`
	Config   json.RawMessage `json:"config"`
	Core     C               `json:"core"`
}

// helloResult is the driver's answer to the hello: the version it agreed,
// and its description.
type helloResult[D any] struct {
	Protocol     string `json:"protocol"`
	Capabilities D      `json:"capabilities"`
}

// gateParams name the call whose gate the driver calls.
type gateParams struct {
	Call uint64 `json:"call"`
}

// callbackParams name the call that lent the callback the driver calls,
// with the callback's own parameters, an object.
type callbackParams struct {
	Call   uint64          `json:"call"`
	Params json.RawMessage `json:"params"`
}

// cancelParams name the call core no longer waits for.
type cancelParams struct {
	ID uint64 `json:"id"`
}

// Client is core's end of a connection to a driver.
type Client struct {
	c *conn
}

// Connect agrees protocol p with the driver at the other end of uc, giving
// it config, the driver's own configuration, and core, the settings core
// owns that the driver needs. It returns a client for the driver and the
// description the driver answered with. The client owns uc; on an error uc
// is closed.
func Connect[C, D any](ctx context.Context, uc *net.UnixConn, p *Protocol, config json.RawMessage, core C) (*Client, D, error) {
	cl := &Client{c: newConn(uc, p, false)}
	go cl.c.readLoop(cl)
	var res helloResult[D]
	err := cl.Call(ctx, methodHello, helloParams[C]{Protocol: p.Version, Config: config, Core: core}, nil, nil, &res)
	if err == nil && res.Protocol != p.Version {
		err = fmt.Errorf("the driver speaks protocol %q, not %q", res.Protocol, p.Version)
	}
	if err != nil {
		cl.Close()
		var none D
		return nil, none, fmt.Errorf("%s hello: %w", p.Name, err)
	}
	return cl, res.Capabilities, nil
}

// Close ends the connection. Calls in flight end as if it were lost.
func (cl *Client) Close() error {
	cl.c.fail(errors.New("core closed the connection"))
	return nil
}

// Done closes once the connection is lost.
func (cl *Client) Done() <-chan struct{} { return cl.c.lost }

// Call sends a request of method and waits for its answer, decoding it into
// result, which is nil for a method that answers null. gate is the call's
// gate, run when the driver calls it, or nil; fd is passed with the request
// when it is set.
func (cl *Client) Call(ctx context.Context, method string, params any, gate func() error, fd *os.File, result any) error {
	o := CallOptions{Gate: gate}
	if fd != nil {
		o.FDs = []*os.File{fd}
	}
	return cl.CallWith(ctx, method, params, o, result)
}

// Callback is a function of core's that a call lends the driver: the driver
// calls it with parameters while the call is in flight, and core answers
// with its result, or with why it refused.
type Callback struct {
	// Run decodes the parameters, an object, and runs. Its result must
	// encode as JSON; nil answers null.
	Run func(params json.RawMessage) (any, error)
	// Gate makes it the call's gate: the driver calls it at most once, and
	// whether it ran decides how the call ends when its connection is lost
	// (see the package comment). A call has at most one gate.
	Gate bool
}

// CallOptions are what a call carries besides its method and parameters.
type CallOptions struct {
	// Gate is the call's gate, which takes no parameters and answers null,
	// or nil.
	Gate func() error
	// Callbacks are the other functions the call lends the driver, by the
	// method the driver calls each by, one of the protocol's Callbacks.
	Callbacks map[string]Callback
	// FDs are passed with the request: as many as the protocol's
	// Descriptors name for the method, in that order.
	FDs []*os.File
}

// lent are the functions o lends the driver, by method, and the method of
// its gate.
func (p *Protocol) lent(method string, o CallOptions) (map[string]Callback, string, error) {
	lent := map[string]Callback{}
	gate := ""
	if o.Gate != nil {
		g := o.Gate
		lent[methodGate], gate = Callback{Run: func(json.RawMessage) (any, error) { return nil, g() }, Gate: true}, methodGate
	}
	for name, c := range o.Callbacks {
		switch {
		case !p.Callbacks[name]:
			return nil, "", fmt.Errorf("%s: the %s protocol has no callback %q", method, p.Name, name)
		case c.Gate && gate != "":
			return nil, "", fmt.Errorf("%s: a call has one gate, not %s and %s", method, gate, name)
		case c.Gate:
			gate = name
		}
		lent[name] = c
	}
	if len(o.FDs) != len(p.Descriptors[method]) {
		return nil, "", fmt.Errorf("%s: %d descriptors, not the %d the method takes", method, len(o.FDs), len(p.Descriptors[method]))
	}
	return lent, gate, nil
}

// CallWith sends a request of method with what o carries and waits for its
// answer, decoding it into result, which is nil for a method that answers
// null. The driver may call back what o lends until the call returns.
func (cl *Client) CallWith(ctx context.Context, method string, params any, o CallOptions, result any) error {
	p := cl.c.p
	lent, gate, err := p.lent(method, o)
	if err != nil {
		return err
	}
	out := &outgoing{done: make(chan struct{}), lent: lent, gate: gate}
	id, err := cl.c.register(out)
	if err != nil {
		return p.unavailable(method, err)
	}
	m, err := newRequest(id, method, params)
	if err == nil {
		err = cl.c.send(m, o.FDs...)
	}
	if err != nil {
		cl.c.forget(id)
		if errors.Is(err, errFrameTooLarge) {
			return fmt.Errorf("%s: %w", method, err)
		}
		// A frame cut short is never read, so the request was not made.
		return p.unavailable(method+" was not sent", err)
	}
	return cl.await(ctx, id, method, out, result)
}

// await waits for the outcome of call id of method, sent with context ctx.
// When ctx ended by the time the call returns, its error carries ctx's
// error, however the call ended: the driver's answer, the lost connection
// or the end of waiting, and after any wait for what it lent. So the caller
// tells its own cancellation from the driver's failure; the error keeps its
// other kinds, and a success the driver answered is returned.
func (cl *Client) await(ctx context.Context, id uint64, method string, o *outgoing, result any) error {
	var err error
	select {
	case <-o.done:
		err = cl.answer(method, o, result)
	case <-cl.c.lost:
		err = cl.stopWaiting(id, method, o, cl.c.lostCause(), result)
	case <-ctx.Done():
		err = cl.waitCancelled(ctx, id, method, o, result)
	}
	if err == nil || ctx.Err() == nil || errors.Is(err, ctx.Err()) {
		return err
	}
	return fmt.Errorf("%w: %w", err, ctx.Err())
}

// waitCancelled asks the driver to cancel call id of method, waits a grace
// for an effect's answer, which says what happened, and then stops waiting.
func (cl *Client) waitCancelled(ctx context.Context, id uint64, method string, o *outgoing, result any) error {
	if cancel, err := newNotification(methodCancel, cancelParams{ID: id}); err == nil {
		cl.c.send(cancel)
	}
	if cl.c.p.Effects[method] {
		grace := time.NewTimer(cancelGrace)
		defer grace.Stop()
		select {
		case <-o.done:
			return cl.answer(method, o, result)
		case <-cl.c.lost:
			return cl.stopWaiting(id, method, o, cl.c.lostCause(), result)
		case <-grace.C:
		}
	}
	return cl.stopWaiting(id, method, o, ctx.Err(), result)
}

// answer is the outcome of a call the driver answered, once what it lent is
// over.
func (cl *Client) answer(method string, o *outgoing, result any) error {
	state, gateErr := cl.endLent(o)
	if state == gateDone && gateErr != nil {
		// The gate refused, so nothing happened, whatever the driver says.
		if o.err == nil {
			return fmt.Errorf("%s: the driver answered success after its gate refused: %w", method, gateErr)
		}
		return &RemoteError{Code: o.err.Code, Message: o.err.Message, kinds: []error{gateErr}}
	}
	if o.err != nil {
		return fromWireError(o.err)
	}
	if err := decodeResult(o.result, result); err != nil {
		if cl.c.p.Effects[method] {
			return cl.c.p.unknownOutcome(method+": malformed answer", err)
		}
		return fmt.Errorf("%s: malformed answer: %w", method, err)
	}
	return nil
}

func decodeResult(raw json.RawMessage, result any) error {
	if result == nil {
		if string(raw) != "null" {
			return errors.New("a result where none was due")
		}
		return nil
	}
	return strictjson.Decode(raw, result)
}

// stopWaiting ends a call whose answer will not come, because the
// connection was lost or the caller stopped waiting for cause, once what it
// lent is over. Its outcome is unknown only if it is an effect that may have
// happened: one without a gate, or whose gate admitted it.
func (cl *Client) stopWaiting(id uint64, method string, o *outgoing, cause error, result any) error {
	state, gateErr := cl.endLent(o)
	cl.c.mu.Lock()
	_, waiting := cl.c.pending[id]
	delete(cl.c.pending, id)
	cl.c.mu.Unlock()
	if !waiting {
		// The answer came while the gate ran, or was being delivered.
		<-o.done
		return cl.answer(method, o, result)
	}
	switch {
	case state == gateDone && gateErr != nil:
		return fmt.Errorf("%s: %w", method, gateErr)
	case o.gate != "" && (state == gateIdle || state == gateClaimed):
		return cl.c.p.unavailable(method+" never reached its gate, so nothing happened", cause)
	case cl.c.p.Effects[method]:
		return cl.c.p.unknownOutcome(method, cause)
	}
	return cl.c.p.unavailable(method, cause)
}

// endLent ends what call o lent as the call returns, however it ends, so
// that nothing it lent runs once it returned: the call's caller and what it
// lent share state. A gate or callback the driver called that has not
// started is refused, and one that runs is waited for. It returns what
// became of the gate.
func (cl *Client) endLent(o *outgoing) (gateState, error) {
	cl.c.mu.Lock()
	o.ending = true
	state, over := o.gateState, o.gateOver
	cl.c.mu.Unlock()
	if state == gateRunning {
		<-over
	}
	o.callbacks.Wait()
	cl.c.mu.Lock()
	defer cl.c.mu.Unlock()
	return o.gateState, o.gateErr
}

// request takes the driver's requests: calls of what a call in flight lent
// it, its gate and its callbacks.
func (cl *Client) request(m *message) error {
	if m.ID == nil {
		return fmt.Errorf("the driver sent notification %q", m.Method)
	}
	id := *m.ID
	if m.Method != methodGate && !cl.c.p.Callbacks[m.Method] {
		go cl.c.respondError(id, &wireError{Code: codeNoMethod, Message: fmt.Sprintf("core answers no method %q", m.Method)})
		return nil
	}
	call, params, err := lentParams(m)
	if err != nil {
		go cl.c.respondError(id, &wireError{Code: codeInvalidParams, Message: err.Error()})
		return nil
	}
	cl.c.mu.Lock()
	o := cl.c.pending[call]
	var c Callback
	if o != nil {
		c = o.lent[m.Method]
	}
	refusal := ""
	switch {
	case c.Run == nil && m.Method == methodGate:
		refusal = "no call waits on a gate"
	case c.Run == nil:
		refusal = fmt.Sprintf("no call lent %s", m.Method)
	case c.Gate && o.gateState != gateIdle:
		refusal = "the call's gate was already called"
	case c.Gate:
		o.gateState = gateClaimed
	case o.ending:
		refusal = "core no longer waits on the call"
	default:
		o.callbacks.Add(1)
	}
	cl.c.mu.Unlock()
	if refusal != "" {
		go cl.c.respondError(id, &wireError{Code: codeDriver, Message: refusal})
		return nil
	}
	if c.Gate {
		go cl.runGate(id, m.Method, o, c, params)
	} else {
		go cl.runCallback(id, m.Method, o, c, params)
	}
	return nil
}

// lentParams are the call a request of the driver's names, and the
// parameters of the callback it calls, none for the gate.
func lentParams(m *message) (uint64, json.RawMessage, error) {
	if m.Method == methodGate {
		var p gateParams
		err := strictjson.Decode(m.Params, &p)
		return p.Call, nil, err
	}
	var p callbackParams
	if err := strictjson.Decode(m.Params, &p); err != nil {
		return 0, nil, err
	}
	if !bytes.HasPrefix(p.Params, []byte("{")) {
		return 0, nil, errors.New("a callback's parameters are an object")
	}
	return p.Call, p.Params, nil
}

// runGate runs the gate c of call o for the driver's request id of method
// and answers it, unless the call ended first.
func (cl *Client) runGate(id uint64, method string, o *outgoing, c Callback, params json.RawMessage) {
	cl.c.mu.Lock()
	if cl.c.closed || o.ending {
		o.gateState = gateIdle
		cl.c.mu.Unlock()
		cl.refuseLate(id, "the gate")
		return
	}
	o.gateState, o.gateOver = gateRunning, make(chan struct{})
	cl.c.mu.Unlock()
	result, err := run(c, params)
	cl.c.mu.Lock()
	o.gateState, o.gateErr = gateDone, err
	cl.c.mu.Unlock()
	close(o.gateOver)
	cl.c.respond(id, method, result, err)
}

// runCallback runs the callback c of call o for the driver's request id of
// method and answers it, unless the call ended first.
func (cl *Client) runCallback(id uint64, method string, o *outgoing, c Callback, params json.RawMessage) {
	cl.c.mu.Lock()
	ending := cl.c.closed || o.ending
	cl.c.mu.Unlock()
	if ending {
		o.callbacks.Done()
		cl.refuseLate(id, method)
		return
	}
	result, err := run(c, params)
	o.callbacks.Done()
	cl.c.respond(id, method, result, err)
}

// refuseLate answers the driver's request id of what a call lent, called as
// the call ended: it was not run, so nothing happened.
func (cl *Client) refuseLate(id uint64, what string) {
	p := cl.c.p
	cl.c.respondError(id, p.toWireError(p.unavailable(what+" was not run", errors.New("core no longer waits on the call"))))
}

// run runs c with params, turning a panic into its refusal.
func run(c Callback, params json.RawMessage) (result any, err error) {
	defer func() {
		if v := recover(); v != nil {
			result, err = nil, fmt.Errorf("the gate or callback panicked: %v", v)
		}
	}()
	return c.Run(params)
}
