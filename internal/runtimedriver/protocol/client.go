package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"time"

	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/strictjson"
)

// cancelGrace bounds how long a cancelled effect still waits for the
// driver's answer, which says what happened, before its outcome is unknown.
const cancelGrace = 5 * time.Second

// Client is a runtime driver at the other end of a protocol connection.
type Client struct {
	c            *conn
	capabilities runtimedriver.Capabilities
}

var _ runtimedriver.Driver = (*Client)(nil)

// Connect agrees the protocol with the driver at the other end of uc,
// giving it h, and returns a client for it. The client owns uc; on an error
// uc is closed.
func Connect(ctx context.Context, uc *net.UnixConn, h Hello) (*Client, error) {
	cl := &Client{c: newConn(uc, false)}
	go cl.c.readLoop(cl)
	var res helloResult
	err := cl.call(ctx, methodHello, helloParams{Protocol: Version, Config: h.Config, Core: h.Core}, nil, nil, &res)
	if err == nil && res.Protocol != Version {
		err = fmt.Errorf("the driver speaks protocol %q, not %q", res.Protocol, Version)
	}
	if err != nil {
		cl.Close()
		return nil, fmt.Errorf("runtime driver hello: %w", err)
	}
	cl.capabilities = runtimedriver.Capabilities(res.Capabilities)
	return cl, nil
}

// Close ends the connection. Calls in flight end as if it were lost.
func (cl *Client) Close() error {
	cl.c.fail(errors.New("core closed the connection"))
	return nil
}

// Done closes once the connection is lost.
func (cl *Client) Done() <-chan struct{} { return cl.c.lost }

// call sends a request of method and waits for its answer, decoding it
// into result, which is nil for a method that answers null. g is the call's
// gate, run when the driver calls it; fd is passed with the request.
func (cl *Client) call(ctx context.Context, method string, params any, g runtimedriver.Gate, fd *os.File, result any) error {
	o := &outgoing{done: make(chan struct{}), gate: g}
	id, err := cl.c.register(o)
	if err != nil {
		return unavailable(method, err)
	}
	m, err := newRequest(id, method, params)
	if err == nil {
		err = cl.c.send(m, fd)
	}
	if err != nil {
		cl.c.forget(id)
		if errors.Is(err, errFrameTooLarge) {
			return fmt.Errorf("%s: %w", method, err)
		}
		// A frame cut short is never read, so the request was not made.
		return unavailable(method+" was not sent", err)
	}
	return cl.await(ctx, id, method, o, result)
}

// await waits for the outcome of call id of method, sent with context ctx.
// When ctx ended by the time the call returns, its error carries ctx's
// error, however the call ended: the driver's answer, the lost connection
// or the end of waiting, and after any wait for its gate. So the caller
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
		cl.c.send(cancel, nil)
	}
	if effects[method] {
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

// answer is the outcome of a call the driver answered, once its gate is
// over.
func (cl *Client) answer(method string, o *outgoing, result any) error {
	state, gateErr := cl.endGate(o)
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
		if effects[method] {
			return unknownOutcome(method+": malformed answer", err)
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
// connection was lost or the caller stopped waiting for cause, once its gate
// is over. Its outcome is unknown only if it is an effect that may have
// happened: one without a gate, or whose gate admitted it.
func (cl *Client) stopWaiting(id uint64, method string, o *outgoing, cause error, result any) error {
	state, gateErr := cl.endGate(o)
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
	case o.gate != nil && (state == gateIdle || state == gateClaimed):
		return unavailable(method+" never reached its gate, so nothing happened", cause)
	case effects[method]:
		return unknownOutcome(method, cause)
	}
	return unavailable(method, cause)
}

// endGate ends the gate of call o as the call returns, however it ends, so
// that no gate runs once its call returned: the call's caller and its gate
// share state. A gate the driver called that has not started is refused,
// and one that runs is waited for. It returns what became of the gate.
func (cl *Client) endGate(o *outgoing) (gateState, error) {
	cl.c.mu.Lock()
	o.ending = true
	state, over := o.gateState, o.gateOver
	cl.c.mu.Unlock()
	if state == gateRunning {
		<-over
	}
	cl.c.mu.Lock()
	defer cl.c.mu.Unlock()
	return o.gateState, o.gateErr
}

// request takes the driver's requests: only gates.
func (cl *Client) request(m *message) error {
	if m.ID == nil {
		return fmt.Errorf("the driver sent notification %q", m.Method)
	}
	id := *m.ID
	if m.Method != methodGate {
		go cl.c.respondError(id, &wireError{Code: codeNoMethod, Message: "core answers only gates"})
		return nil
	}
	var p gateParams
	if err := strictjson.Decode(m.Params, &p); err != nil {
		go cl.c.respondError(id, &wireError{Code: codeInvalidParams, Message: err.Error()})
		return nil
	}
	cl.c.mu.Lock()
	o := cl.c.pending[p.Call]
	refusal := ""
	switch {
	case o == nil || o.gate == nil:
		refusal = "no call waits on a gate"
	case o.gateState != gateIdle:
		refusal = "the call's gate was already called"
	default:
		o.gateState = gateClaimed
	}
	cl.c.mu.Unlock()
	if refusal != "" {
		go cl.c.respondError(id, &wireError{Code: codeDriver, Message: refusal})
		return nil
	}
	go cl.runGate(id, o)
	return nil
}

// runGate runs the gate of call o for the driver's request id and answers
// it, unless the call ended first.
func (cl *Client) runGate(id uint64, o *outgoing) {
	cl.c.mu.Lock()
	if cl.c.closed || o.ending {
		o.gateState = gateIdle
		cl.c.mu.Unlock()
		cl.c.respondError(id, toWireError(unavailable("the gate was not run", errors.New("core no longer waits on the call"))))
		return
	}
	o.gateState, o.gateOver = gateRunning, make(chan struct{})
	cl.c.mu.Unlock()
	err := callGate(o.gate)
	cl.c.mu.Lock()
	o.gateState, o.gateErr = gateDone, err
	cl.c.mu.Unlock()
	close(o.gateOver)
	cl.c.respond(id, methodGate, nil, err)
}

// callGate runs g, turning a panic into its refusal.
func callGate(g runtimedriver.Gate) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("the gate panicked: %v", v)
		}
	}()
	return g()
}

func (cl *Client) Describe() runtimedriver.Capabilities {
	c := cl.capabilities
	c.Networks = slices.Clone(c.Networks)
	return c
}

func (cl *Client) Check(ctx context.Context) error {
	return cl.call(ctx, methodCheck, noParams{}, nil, nil, nil)
}

func (cl *Client) Inventory(ctx context.Context) (runtimedriver.Inventory, error) {
	var res wireInventory
	if err := cl.call(ctx, methodInventory, noParams{}, nil, nil, &res); err != nil {
		return runtimedriver.Inventory{}, err
	}
	return res.inventory(), nil
}

func (cl *Client) observe(ctx context.Context, method string, params any, g runtimedriver.Gate) (runtimedriver.Observation, error) {
	var res wireObservation
	if err := cl.call(ctx, method, params, g, nil, &res); err != nil {
		return runtimedriver.Observation{}, err
	}
	return res.observation(), nil
}

func (cl *Client) Inspect(ctx context.Context, s runtimedriver.Session) (runtimedriver.Observation, error) {
	return cl.observe(ctx, methodInspect, sessionParams{Session: toSession(s)}, nil)
}

func (cl *Client) Create(ctx context.Context, s runtimedriver.Session, at runtimedriver.Incarnation, g runtimedriver.Gate) (runtimedriver.Observation, error) {
	return cl.observe(ctx, methodCreate, createParams{Session: toSession(s), At: at, Gate: g != nil}, g)
}

func (cl *Client) Start(ctx context.Context, s runtimedriver.Session) (runtimedriver.Observation, error) {
	return cl.observe(ctx, methodStart, sessionParams{Session: toSession(s)}, nil)
}

func (cl *Client) Stop(ctx context.Context, s runtimedriver.Session) (runtimedriver.Observation, error) {
	return cl.observe(ctx, methodStop, sessionParams{Session: toSession(s)}, nil)
}

func (cl *Client) HostLog(ctx context.Context, t runtimedriver.Target) (string, error) {
	var res hostLogResult
	err := cl.call(ctx, methodHostLog, targetParams{Target: toTarget(t)}, nil, nil, &res)
	return res.Text, err
}

func (cl *Client) ReadWorkspace(ctx context.Context, t runtimedriver.Target, path string, limit int64) (runtimedriver.File, error) {
	var res wireFile
	if err := cl.call(ctx, methodReadWorkspace, readWorkspaceParams{Target: toTarget(t), Path: path, Limit: limit}, nil, nil, &res); err != nil {
		return runtimedriver.File{}, err
	}
	return runtimedriver.File(res), nil
}

func (cl *Client) WriteWorkspace(ctx context.Context, t runtimedriver.Target, path string, f runtimedriver.File, g runtimedriver.Gate) error {
	return cl.call(ctx, methodWriteWorkspace, writeWorkspaceParams{Target: toTarget(t), Path: path, File: wireFile(f), Gate: g != nil}, g, nil, nil)
}

func (cl *Client) RemoveWorkspace(ctx context.Context, t runtimedriver.Target, path string, g runtimedriver.Gate) error {
	return cl.call(ctx, methodRemoveWorkspace, removeWorkspaceParams{Target: toTarget(t), Path: path, Gate: g != nil}, g, nil, nil)
}

func (cl *Client) Services(ctx context.Context, t runtimedriver.Target) ([]runtimedriver.Service, error) {
	var res []wireService
	if err := cl.call(ctx, methodServices, targetParams{Target: toTarget(t)}, nil, nil, &res); err != nil {
		return nil, err
	}
	return each(res, func(s wireService) runtimedriver.Service { return runtimedriver.Service(s) }), nil
}

func (cl *Client) ServiceAction(ctx context.Context, t runtimedriver.Target, name string, a runtimedriver.Action) error {
	return cl.call(ctx, methodServiceAction, serviceActionParams{Target: toTarget(t), Name: name, Action: a}, nil, nil, nil)
}

func (cl *Client) ServiceLogs(ctx context.Context, t runtimedriver.Target, name string) (runtimedriver.Logs, error) {
	var res wireLogs
	if err := cl.call(ctx, methodServiceLogs, serviceParams{Target: toTarget(t), Name: name}, nil, nil, &res); err != nil {
		return runtimedriver.Logs{}, err
	}
	return runtimedriver.Logs(res), nil
}

func (cl *Client) BuildImage(ctx context.Context, b runtimedriver.Build, g runtimedriver.Gate) (runtimedriver.Image, error) {
	var res wireImage
	if err := cl.call(ctx, methodBuildImage, buildImageParams{Build: toBuild(b), Gate: g != nil}, g, nil, &res); err != nil {
		return runtimedriver.Image{}, err
	}
	return res.image(), nil
}

func (cl *Client) Images(ctx context.Context) ([]runtimedriver.Image, error) {
	var res []wireImage
	if err := cl.call(ctx, methodImages, noParams{}, nil, nil, &res); err != nil {
		return nil, err
	}
	return each(res, wireImage.image), nil
}

func (cl *Client) InspectImage(ctx context.Context, ref runtimedriver.ImageRef) (runtimedriver.Image, bool, error) {
	var res inspectImageResult
	if err := cl.call(ctx, methodInspectImage, inspectImageParams{Ref: ref}, nil, nil, &res); err != nil {
		return runtimedriver.Image{}, false, err
	}
	return res.Image.image(), res.Found, nil
}

func (cl *Client) DeleteImage(ctx context.Context, img runtimedriver.Image, g runtimedriver.Gate) error {
	return cl.call(ctx, methodDeleteImage, deleteImageParams{Image: toImage(img), Gate: g != nil}, g, nil, nil)
}

func (cl *Client) Settle(ctx context.Context, e runtimedriver.Effect) (runtimedriver.Settlement, error) {
	var res wireSettlement
	if err := cl.call(ctx, methodSettle, settleParams{Effect: toEffect(e)}, nil, nil, &res); err != nil {
		return runtimedriver.Unknown, err
	}
	return res.settlement()
}

func (cl *Client) Delete(ctx context.Context, r runtimedriver.Ref, at runtimedriver.Incarnation, g runtimedriver.Gate) error {
	return cl.call(ctx, methodDelete, deleteParams{Ref: wireRef(r), At: at, Gate: g != nil}, g, nil, nil)
}
