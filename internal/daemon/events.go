package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/eventhandler"
	handlers "github.com/lgvo/p.ai/internal/eventhandler/supervise"
)

// eventDelivery owns only best-effort, process-local delivery. The queue is
// deliberately not a journal: authoritative commits never wait for a handler.
type eventDelivery struct {
	dispatch       func(context.Context, eventhandler.Event) error
	stop           func() error // stops the handler, after the worker
	instance       string
	prefix         string
	queue          chan eventhandler.Event
	deadline       time.Duration
	cancel         context.CancelFunc
	done           chan struct{}
	diagMu         sync.Mutex
	closed         atomic.Bool
	sequence       atomic.Uint64
	dropped        atomic.Uint64
	lastDiagnostic map[string]time.Time // when each diagnostic was last logged
}

type observedFacts struct{ condition, policy string }
type eventContext struct{ project, branch string }

func (l *lifecycle) seedRecoveredSession(s control.Session, policy string) {
	condition, err := l.registryCondition(l.ctx, s)
	if err != nil {
		condition = "unreachable"
	}
	l.eventMu.Lock()
	defer l.eventMu.Unlock()
	if l.observed == nil {
		l.observed = make(map[string]observedFacts)
	}
	if l.eventContexts == nil {
		l.eventContexts = make(map[string]eventContext)
	}
	l.observed[s.UUID] = observedFacts{condition: condition, policy: policy}
	l.eventContexts[s.UUID] = eventContext{s.Project, s.Branch}
}

func (l *lifecycle) observeView(v api.SessionView) {
	if l.events == nil {
		return
	}
	l.eventMu.Lock()
	defer l.eventMu.Unlock()
	if l.observed == nil {
		l.observed = make(map[string]observedFacts)
	}
	if l.eventContexts == nil {
		l.eventContexts = make(map[string]eventContext)
	}
	l.eventContexts[v.UUID] = eventContext{v.Project, v.Branch}
	before, exists := l.observed[v.UUID]
	l.observed[v.UUID] = observedFacts{v.Condition, v.PolicyCondition}
	if !exists {
		return
	}
	if before.condition != "" && before.condition != v.Condition {
		l.events.emit("session.condition_changed", v.Project, v.UUID, v.Branch, "condition", v.Condition)
	}
	if before.policy != v.PolicyCondition {
		l.events.emit("session.policy_changed", v.Project, v.UUID, v.Branch, "policy_condition", v.PolicyCondition)
	}
}

// recordCreation records the context of the session a new creation op makes,
// and reports a session creation's session creating. The journal reported op
// accepted when it recorded it.
func (l *lifecycle) recordCreation(op api.Operation, branch string) {
	if l.events == nil {
		return
	}
	l.eventMu.Lock()
	defer l.eventMu.Unlock()
	if l.observed == nil {
		l.observed = make(map[string]observedFacts)
	}
	if l.eventContexts == nil {
		l.eventContexts = make(map[string]eventContext)
	}
	l.eventContexts[op.SessionUUID] = eventContext{op.Project, branch}
	if _, seen := l.observed[op.SessionUUID]; seen || op.Kind != "session.create" {
		return
	}
	l.observed[op.SessionUUID] = observedFacts{condition: "creating", policy: "current"}
	l.events.emit("session.condition_changed", op.Project, op.SessionUUID, branch, "condition", "creating")
}

func (l *lifecycle) recordCreatingSession(s control.Session) {
	if l.events == nil {
		return
	}
	l.eventMu.Lock()
	defer l.eventMu.Unlock()
	if l.observed == nil {
		l.observed = make(map[string]observedFacts)
	}
	if l.eventContexts == nil {
		l.eventContexts = make(map[string]eventContext)
	}
	l.eventContexts[s.UUID] = eventContext{s.Project, s.Branch}
	if _, exists := l.observed[s.UUID]; exists {
		return
	}
	l.observed[s.UUID] = observedFacts{condition: "creating", policy: "current"}
	l.events.emit("session.condition_changed", s.Project, s.UUID, s.Branch, "condition", "creating")
}

// recordProgress emits op's operation.progress event of phase. The event
// names the branch of op's session as last observed, or for a session not yet
// observed the branch the operation's caller named.
func (l *lifecycle) recordProgress(op api.Operation, phase string) {
	if l.events == nil {
		return
	}
	l.eventMu.Lock()
	defer l.eventMu.Unlock()
	branch := ""
	if op.SessionUUID != "" {
		context, known := l.eventContexts[op.SessionUUID]
		branch = context.branch
		if !known {
			branch = op.Intent.Branch
		}
	}
	l.events.emit("operation.progress", op.Project, op.SessionUUID, branch, "phase", phase)
}

// attachmentChanged is called only after the attachment reducer succeeds.
func (l *lifecycle) attachmentChanged(_ context.Context, id string, count int, cleared bool) {
	if l.events == nil {
		return
	}
	l.eventMu.Lock()
	defer l.eventMu.Unlock()
	context, ok := l.eventContexts[id]
	if !ok {
		return
	}
	l.events.emit("session.attachment_changed", context.project, id, context.branch, "count", strconv.Itoa(count))
	if cleared {
		l.events.emit("session.unattended_changed", context.project, id, context.branch, "unattended_condition", "none")
	}
}

// newEventDelivery starts the event handler cfg selects, after its launch
// checks, and delivers events to it until Close. A handler that does not
// start refuses the daemon's start.
func newEventDelivery(parent context.Context, cfg *control.EventsConfig, instance string) (*eventDelivery, error) {
	if cfg == nil {
		return nil, nil
	}
	prefix, err := newEventID()
	if err != nil {
		return nil, errors.New("event identity unavailable")
	}
	handler, err := handlers.Start(parent, handlers.Config{Path: cfg.Path, SHA256: cfg.SHA256, Config: cfg.Config})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	d := &eventDelivery{instance: instance, prefix: prefix, queue: make(chan eventhandler.Event, 64), deadline: 2 * time.Second, cancel: cancel, done: make(chan struct{}), stop: handler.Close}
	d.dispatch = func(ctx context.Context, event eventhandler.Event) error {
		_, err := handler.Handle(ctx, event)
		return err
	}
	go d.run(ctx)
	return d, nil
}

// run delivers queued events one at a time, in order, each within the
// deadline. An event the handler is down for is dropped and counted, and
// the drop is logged at once; a handler that misses the deadline is
// restarted by its supervisor, and delivery goes on with the next event.
func (d *eventDelivery) run(ctx context.Context) {
	defer close(d.done)
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-d.queue:
			d.reportDrops()
			if ctx.Err() != nil {
				return
			}
			deadline := d.deadline
			if deadline <= 0 {
				deadline = 2 * time.Second
			}
			call, cancel := context.WithTimeout(ctx, deadline)
			err := d.dispatch(call, event)
			missed := errors.Is(call.Err(), context.DeadlineExceeded)
			cancel()
			switch {
			case err == nil || ctx.Err() != nil:
			case missed:
				d.diagnostic("event handler deadline exceeded; restarting it")
			case errors.Is(err, eventhandler.ErrUnavailable):
				d.dropped.Add(1)
				d.reportDrops()
			default:
				d.diagnostic("event handler delivery failed")
			}
		}
	}
}

// reportDrops logs that events were dropped, if any were counted since it
// last did.
func (d *eventDelivery) reportDrops() {
	if d.dropped.Swap(0) != 0 {
		d.diagnostic("event delivery dropped")
	}
}

// diagnostic logs message, at most once a minute for each message, with no
// event or handler detail.
func (d *eventDelivery) diagnostic(message string) {
	d.diagMu.Lock()
	defer d.diagMu.Unlock()
	if d.lastDiagnostic == nil {
		d.lastDiagnostic = map[string]time.Time{}
	}
	if time.Since(d.lastDiagnostic[message]) >= time.Minute {
		log.Print(message)
		d.lastDiagnostic[message] = time.Now()
	}
}

// Close stops delivery, then the handler.
func (d *eventDelivery) Close() {
	if d == nil {
		return
	}
	d.closed.Store(true)
	d.cancel()
	d.reportDrops()
	select {
	case <-d.done:
	case <-time.After(250 * time.Millisecond):
	}
	if d.stop != nil {
		d.stop()
	}
}

func (d *eventDelivery) emit(kind, project, session, branch, field, value string) {
	if d == nil {
		return
	}
	prefix := d.prefix
	if prefix == "" {
		prefix = "e-test"
	}
	id := prefix + "-" + strconv.FormatUint(d.sequence.Add(1), 10)
	event := eventhandler.Event{Schema: "p.event/v1", ID: id, Kind: kind,
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano), Instance: d.instance,
		Project: project, Session: session, Branch: branch, Fields: map[string]string{field: value}}
	d.enqueue(event)
}

func newEventID() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return "e-" + hex.EncodeToString(nonce[:]), nil
}

func (d *eventDelivery) enqueue(event eventhandler.Event) {
	if d == nil {
		return
	}
	if err := event.Validate(); err != nil {
		d.dropped.Add(1)
		return
	}
	if d.closed.Load() {
		return
	}
	select {
	case d.queue <- event:
	default:
		d.dropped.Add(1)
	}
}
