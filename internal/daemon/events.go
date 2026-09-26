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

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

// eventDelivery owns only best-effort, process-local delivery. The queue is
// deliberately not a journal: authoritative commits never wait for a handler.
type eventDelivery struct {
	active         []plugin.Active
	dispatch       func(context.Context, plugin.Event) error
	instance       string
	prefix         string
	queue          chan plugin.Event
	deadline       time.Duration
	cancel         context.CancelFunc
	done           chan struct{}
	diagMu         sync.Mutex
	closed         atomic.Bool
	sequence       atomic.Uint64
	dropped        atomic.Uint64
	lastDiagnostic time.Time
}

type observedFacts struct{ condition, policy string }
type eventContext struct{ project, branch string }

func (l *lifecycle) seedRecoveredSession(s control.Session, policy string) {
	l.eventMu.Lock()
	defer l.eventMu.Unlock()
	if l.observed == nil {
		l.observed = make(map[string]observedFacts)
	}
	if l.eventContexts == nil {
		l.eventContexts = make(map[string]eventContext)
	}
	condition := ""
	if s.Registry == "creating" {
		condition = "creating"
	}
	if s.Registry == "removing" {
		condition = "deleting"
	}
	l.observed[s.UUID] = observedFacts{condition: condition, policy: policy}
	l.eventContexts[s.UUID] = eventContext{s.Project, s.Branch}
}

func (l *lifecycle) observeView(v control.SessionView) {
	if l.events == nil {
		return
	}
	l.eventMu.Lock()
	if l.observed == nil {
		l.observed = make(map[string]observedFacts)
	}
	if l.eventContexts == nil {
		l.eventContexts = make(map[string]eventContext)
	}
	l.eventContexts[v.UUID] = eventContext{v.Project, v.Branch}
	before, exists := l.observed[v.UUID]
	l.observed[v.UUID] = observedFacts{v.Condition, v.PolicyCondition}
	defer l.eventMu.Unlock()
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

func (l *lifecycle) recordCreation(op control.Operation, branch string) {
	if l.events == nil {
		return
	}
	l.eventMu.Lock()
	if l.progress == nil {
		l.progress = make(map[string]string)
	}
	if l.observed == nil {
		l.observed = make(map[string]observedFacts)
	}
	if l.eventContexts == nil {
		l.eventContexts = make(map[string]eventContext)
	}
	l.eventContexts[op.SessionUUID] = eventContext{op.Project, branch}
	if l.progress[op.ID] != "" {
		l.eventMu.Unlock()
		return
	}
	l.progress[op.ID] = "accepted"
	defer l.eventMu.Unlock()
	l.events.emit("operation.progress", op.Project, op.SessionUUID, branch, "phase", "accepted")
	if op.Kind == "session.create" {
		l.observed[op.SessionUUID] = observedFacts{condition: "creating", policy: "current"}
		l.events.emit("session.condition_changed", op.Project, op.SessionUUID, branch, "condition", "creating")
	}
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

func (l *lifecycle) recordProgress(op control.Operation, phase string) {
	if l.events == nil {
		return
	}
	l.eventMu.Lock()
	if l.progress == nil {
		l.progress = make(map[string]string)
	}
	if l.progress[op.ID] == phase {
		l.eventMu.Unlock()
		return
	}
	l.progress[op.ID] = phase
	if phase == "blocked" || phase == "completed" || phase == "failed" || phase == "cancelled" {
		delete(l.progress, op.ID)
	}
	defer l.eventMu.Unlock()
	context := l.eventContexts[op.SessionUUID]
	l.events.emit("operation.progress", op.Project, op.SessionUUID, context.branch, "phase", phase)
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

func newEventDelivery(parent context.Context, cfg *control.EventsConfig, instance string) (*eventDelivery, error) {
	if cfg == nil {
		return nil, nil
	}
	if err := privateTrustedFile(cfg.ActivationPath); err != nil {
		return nil, err
	}
	selected, err := loadEventSelection(*cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	prefix, err := newEventID()
	if err != nil {
		cancel()
		return nil, errors.New("event identity unavailable")
	}
	d := &eventDelivery{active: []plugin.Active{selected}, instance: instance, prefix: prefix, queue: make(chan plugin.Event, 64), deadline: 2 * time.Second, cancel: cancel, done: make(chan struct{})}
	d.dispatch = func(ctx context.Context, event plugin.Event) error {
		_, err := plugin.DispatchEvent(ctx, d.active, event)
		return err
	}
	go d.run(ctx)
	return d, nil
}

// Caller must first check the trusted host file and its ancestry. This seam
// validates the package, digest, capability, and exact grant independently.
func loadEventSelection(cfg control.EventsConfig) (plugin.Active, error) {
	active, err := plugin.LoadActivation(cfg.ActivationPath)
	if err != nil {
		return plugin.Active{}, err
	}
	var selected *plugin.Active
	for i := range active {
		if active[i].Package.Manifest.ID == cfg.PluginID {
			selected = &active[i]
			break
		}
	}
	if selected == nil || selected.Package.Manifest.Capability != "event-handler" ||
		len(selected.Grants) != 1 || selected.Grants[0] != "event.file.append" {
		return plugin.Active{}, errors.New("selected event handler unavailable")
	}
	return *selected, nil
}

func (d *eventDelivery) run(ctx context.Context) {
	defer close(d.done)
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-d.queue:
			if d.dropped.Swap(0) != 0 {
				d.diagnostic("event delivery dropped")
			}
			if ctx.Err() != nil {
				return
			}
			deadline := d.deadline
			if deadline <= 0 {
				deadline = 2 * time.Second
			}
			call, cancel := context.WithTimeout(ctx, deadline)
			// A host filesystem call cannot be interrupted by Go context. Retire
			// this worker on a timeout, so at most one call can remain in flight.
			finished := make(chan error, 1)
			go func() { finished <- d.dispatch(call, event) }()
			select {
			case err := <-finished:
				cancel()
				if err != nil {
					d.diagnostic("event handler delivery failed")
				}
			case <-call.Done():
				cancel()
				if ctx.Err() == nil {
					d.diagnostic("event handler deadline exceeded; delivery disabled")
				}
				return
			}
		}
	}
}

func (d *eventDelivery) diagnostic(message string) {
	d.diagMu.Lock()
	defer d.diagMu.Unlock()
	if time.Since(d.lastDiagnostic) >= time.Minute {
		log.Print(message)
		d.lastDiagnostic = time.Now()
	}
}

func (d *eventDelivery) Close() {
	if d == nil {
		return
	}
	d.closed.Store(true)
	d.cancel()
	if d.dropped.Swap(0) != 0 {
		d.diagnostic("event delivery dropped")
	}
	select {
	case <-d.done:
	case <-time.After(250 * time.Millisecond):
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
	event := plugin.Event{Schema: "p.event/v1", ID: id, Kind: kind,
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

func (d *eventDelivery) enqueue(event plugin.Event) {
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
