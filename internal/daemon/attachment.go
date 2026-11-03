package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"runtime/debug"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

const attachmentTTL = 30 * time.Second
const maxSessionAttachments = 8
const maxAttachments = 128

// defaultTerminalSettleInterval is how often the daemon asks the driver to
// settle a terminal that ended unproven.
const defaultTerminalSettleInterval = 3 * time.Second

// attachment is one attachment of a session: a pending token until a claim
// redeems it, then a lease on the terminal the claim opened, until the
// terminal's end is proven. While it exists the session's removals are
// refused, and so is Stop unless its terminal ended unproven.
type attachment struct {
	session string
	expires time.Time
	// claimed is set when a claim redeems the token; from then on the
	// attachment never expires.
	claimed  bool
	terminal runtimedriver.Terminal
	target   runtimedriver.Target
	// confirmed is set once the attachment counts in the session's presence.
	confirmed bool
	// unproven is set when the terminal ended without proof that its process
	// ended. The lease then waits for the driver to settle the terminal as
	// ended, or for the session to stop.
	unproven bool
	// ended closes once the terminal's end, and whether it was proven, is
	// known.
	ended chan struct{}
}

// refusalBecause is a refusal of kind with reason and target whose cause
// keeps its text for the daemon's log and classifies as class (ErrConflict,
// ErrNotFound) inside the daemon.
func refusalBecause(kind api.ErrorKind, reason, target string, class, cause error) error {
	return &control.Refusal{Kind: kind, Reason: reason, Target: target, Cause: classifiedCause{class: class, cause: cause}}
}

// classifiedCause is cause, also matching class with errors.Is.
type classifiedCause struct{ class, cause error }

func (c classifiedCause) Error() string        { return c.cause.Error() }
func (c classifiedCause) Unwrap() error        { return c.cause }
func (c classifiedCause) Is(target error) bool { return target == c.class }

// attachmentExpired refuses an attachment step whose token or claim is no
// longer valid: it expired, was used, or never existed.
func attachmentExpired() error { return control.Stale(api.ReasonExpired) }

func (l *lifecycle) expireAttachmentsLocked() {
	for token, a := range l.attachments {
		if !a.claimed && !time.Now().Before(a.expires) {
			delete(l.attachments, token)
		}
	}
}

// hasAttachment reports whether an attachment holds or reserves session id.
func (l *lifecycle) hasAttachment(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.hasAttachmentLocked(id)
}
func (l *lifecycle) hasAttachmentLocked(id string) bool {
	l.expireAttachmentsLocked()
	for _, a := range l.attachments {
		if a.session == id {
			return true
		}
	}
	return false
}

// hasLiveAttachment reports whether an attachment of session id is pending,
// opening its terminal, or attached: any but one whose terminal ended
// unproven.
func (l *lifecycle) hasLiveAttachment(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expireAttachmentsLocked()
	for _, a := range l.attachments {
		if a.session == id && !a.unproven {
			return true
		}
	}
	return false
}

// AttachSession reserves an attachment to session id, which must be ready.
// It starts nothing: a client starts the session and waits for its start
// first. An unfinished start or other operation on the session answers as
// the journal's admission check does.
func (l *lifecycle) AttachSession(ctx context.Context, id string) (api.PendingAttachment, error) {
	var zero api.PendingAttachment
	release, err := l.lockSession(ctx, id)
	if err != nil {
		return zero, err
	}
	defer release()
	if err := l.admitSessionWork(ctx, id, ""); err != nil {
		return zero, err
	}
	view, err := l.InspectSession(ctx, id)
	if err != nil {
		return zero, err
	}
	if err := notReady(id, view.Condition); err != nil {
		return zero, err
	}
	if _, err = l.attachTarget(ctx, id); err != nil {
		return zero, err
	}
	return l.reserveAttachment(id)
}

// notReady refuses an attachment to session id in condition; nil when it is
// ready.
func notReady(id, condition string) error {
	switch condition {
	case "ready":
		return nil
	case "missing":
		return control.Conflict(api.ReasonRuntimeMissing, api.TargetSession(id))
	case "unreachable":
		return control.Failed(api.ReasonRuntime, errors.New("session runtime unreachable"))
	default:
		return control.Conflict(api.ReasonSessionNotRunning, api.TargetSession(id))
	}
}

func (l *lifecycle) reserveAttachment(id string) (api.PendingAttachment, error) {
	var zero api.PendingAttachment
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return zero, err
	}
	token := hex.EncodeToString(entropy[:])
	expires := time.Now().Add(attachmentTTL)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expireAttachmentsLocked()
	count := 0
	for _, a := range l.attachments {
		if a.session == id {
			count++
		}
	}
	if len(l.attachments) >= maxAttachments || count >= maxSessionAttachments {
		return zero, control.Full(api.ReasonAttachmentCapacity)
	}
	if l.attachments == nil {
		l.attachments = map[string]*attachment{}
	}
	l.attachments[token] = &attachment{session: id, expires: expires, ended: make(chan struct{})}
	return api.PendingAttachment{Token: token, ExpiresAt: expires}, nil
}

// attachTarget is the runtime a terminal of session id attaches to, its
// readyTarget; a refusal is logged for the host's operator.
func (l *lifecycle) attachTarget(ctx context.Context, id string) (runtimedriver.Target, error) {
	target, err := l.readyTarget(ctx, id)
	if err != nil {
		log.Printf("attachment refused: %v", err)
	}
	return target, err
}

// ClaimAttachment redeems token once: with the session locked and checked
// again, it opens the session's terminal at size, counts the attachment in
// the session's presence, and returns the terminal for the claim's
// connection to carry. From the claim on, the attachment is a lease that
// ends when the terminal's end is proven; see watchTerminal. A token that
// expired, was used or never existed is refused as expired; a host that is
// not ready, as not running; a terminal the driver could not open failed in
// the runtime. An open whose outcome is unknown keeps the lease, without
// presence: until the terminal it names settles, or, naming none, until the
// session stops.
func (l *lifecycle) ClaimAttachment(ctx context.Context, token string, size runtimedriver.Size) (control.Terminal, error) {
	var a *attachment
	l.withLock(func() { a = l.attachments[token] })
	if a == nil {
		return nil, attachmentExpired()
	}
	release, err := l.lockSession(ctx, a.session)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := l.admitSessionWork(ctx, a.session, ""); err != nil {
		return nil, err
	}
	claimed := false
	l.withLock(func() {
		l.expireAttachmentsLocked()
		if l.attachments[token] == a && !a.claimed && time.Now().Before(a.expires) {
			a.claimed, claimed = true, true
		}
	})
	if !claimed {
		return nil, attachmentExpired()
	}
	target, err := l.attachTarget(ctx, a.session)
	if err != nil {
		l.releaseAttachment(token)
		return nil, err
	}
	terminal, err := l.driver.OpenTerminal(ctx, target, size)
	var unended *runtimedriver.UnendedTerminal
	if errors.As(err, &unended) {
		// The attach began and its end is unproven: the attachment never
		// counts presence, and its lease waits for the terminal to settle.
		log.Printf("attachment refused: the runtime driver did not open the terminal and could not prove it ended: %v", err)
		l.withLock(func() { a.target, a.unproven = target, true })
		go l.settleTerminal(token, a, unended.ID)
		return nil, openFailure(ctx, err)
	}
	if errors.Is(err, runtimedriver.ErrUnknownOutcome) {
		// The attach may have begun, as when the driver's connection was
		// lost while it opened the terminal, and no terminal is named to
		// settle: the lease stays, without presence, until the session stops.
		log.Printf("attachment refused: the runtime driver may have begun the attach and named no terminal; the session stays attached until it stops: %v", err)
		l.withLock(func() { a.target, a.unproven = target, true })
		return nil, openFailure(ctx, err)
	}
	if err != nil {
		log.Printf("attachment refused: the runtime driver did not open the terminal: %v", err)
		l.releaseAttachment(token)
		if errors.Is(err, runtimedriver.ErrNotReady) {
			return nil, hostNotReady(a.session)
		}
		return nil, openFailure(ctx, err)
	}
	l.withLock(func() { a.terminal, a.target = terminal, target })
	go l.watchTerminal(token, a)
	if err = l.confirmPresence(ctx, token, a); err != nil {
		terminal.Close()
		return nil, err
	}
	return relayedTerminal{Terminal: terminal, a: a}, nil
}

// openFailure answers a terminal the driver could not open as the
// runtime's failure. Only the claim's own end answers as that end: a
// deadline inside the driver is the runtime's failure, not the request's.
func openFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return control.Failed(api.ReasonRuntime, errors.New(err.Error()))
}

// confirmPresence counts attachment a in its session's presence, which
// clears the session's unattended report, unless a's lease already ended or
// the claim was cancelled, as when its helper hung up while the terminal
// opened.
func (l *lifecycle) confirmPresence(ctx context.Context, token string, a *attachment) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.attachments[token] != a {
		return attachmentExpired()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, ok, _, err := l.store.ConfirmAttachmentWithCommit(ctx, a.session, func(count int, cleared bool) {
		// Only enqueue immutable data under the reducer lock; never re-enter lifecycle.
		if l.onAttachment != nil {
			l.onAttachment(ctx, a.session, count, cleared)
		}
	})
	if err != nil {
		return err
	}
	if !ok {
		return control.ErrConflict
	}
	a.confirmed = true
	return nil
}

// watchTerminal ends attachment a's lease, and its presence, when its
// terminal's end is proven. A terminal that ended unproven keeps the lease
// until it settles; see settleTerminal.
func (l *lifecycle) watchTerminal(token string, a *attachment) {
	defer recoverWatcher()
	var completion runtimedriver.Completion
	select {
	case completion = <-a.terminal.Ended():
	case <-l.ctx.Done():
		return
	}
	l.withLock(func() { a.unproven = !completion.Proven })
	close(a.ended)
	if completion.Proven {
		l.releaseAttachment(token)
		return
	}
	log.Printf("an attachment of session %s ended unproven; it stays attached until the runtime driver settles its terminal or the session stops", a.session)
	l.settleTerminal(token, a, a.terminal.ID())
}

// settleTerminal keeps the lease of attachment a, whose terminal id ended
// unproven, so the session's removals stay refused, until the driver settles
// the terminal as ended, asked every few seconds while the daemon runs, or
// the session stops (a stop ends the lease then).
func (l *lifecycle) settleTerminal(token string, a *attachment, id string) {
	defer recoverWatcher()
	effect := runtimedriver.Effect{Kind: runtimedriver.EffectTerminal, Target: a.target, Terminal: id}
	interval := l.terminalSettleInterval
	if interval <= 0 {
		interval = defaultTerminalSettleInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-ticker.C:
		}
		leased := false
		l.withLock(func() { leased = l.attachments[token] == a })
		if !leased {
			return
		}
		if settled, err := l.driver.Settle(l.ctx, effect); err == nil && settled == runtimedriver.Applied {
			l.releaseAttachment(token)
			return
		}
	}
}

// recoverWatcher logs a panic of an attachment's watcher: nothing waits on
// it.
func recoverWatcher() {
	if value := recover(); value != nil {
		log.Printf("attachment watcher panicked: %v\n%s", value, debug.Stack())
	}
}

// endUnprovenLeases ends the leases of session id's terminals that ended
// unproven, once its runtime stopped: no process of theirs outlives it.
func (l *lifecycle) endUnprovenLeases(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for token, a := range l.attachments {
		if a.session == id && a.unproven {
			l.releaseAttachmentLocked(token)
		}
	}
}

func (l *lifecycle) releaseAttachment(token string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releaseAttachmentLocked(token)
}

// releaseAttachmentLocked ends the attachment token names, and its presence
// when it counted.
func (l *lifecycle) releaseAttachmentLocked(token string) {
	a := l.attachments[token]
	if a == nil {
		return
	}
	delete(l.attachments, token)
	if a.confirmed {
		l.store.ReleaseAttachmentWithCommit(a.session, func(count int) {
			if l.onAttachment != nil {
				l.onAttachment(l.ctx, a.session, count, false)
			}
		})
	}
}

// errUnprovenEnd tells the client of an attachment whose terminal ended
// without proof that its process ended.
var errUnprovenEnd = errors.New("the terminal ended without proof from the runtime that its process ended; the session counts as attached until the runtime confirms the end or the session is stopped")

// proofWait bounds how long a relayed terminal whose output ended waits for
// the driver's word on whether its end is proven.
const proofWait = 5 * time.Second

// relayedTerminal is an attachment's terminal as its connection carries it.
// When its output ends, it waits up to proofWait for the end's proof, and
// ends with errUnprovenEnd when there is none.
type relayedTerminal struct {
	runtimedriver.Terminal
	a *attachment
}

func (t relayedTerminal) Read(p []byte) (int, error) {
	n, err := t.Terminal.Read(p)
	if err == nil {
		return n, nil
	}
	select {
	case <-t.a.ended:
		if !t.a.unproven {
			return n, io.EOF
		}
	case <-time.After(proofWait):
	}
	return n, errUnprovenEnd
}
