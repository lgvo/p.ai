package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

const attachmentTTL = 30 * time.Second
const maxSessionAttachments = 8
const maxAttachments = 128

type attachment struct {
	session    string
	expires    time.Time
	spec       plugin.AttachSpec
	connection *control.Connection
	confirmed  bool
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
		if a.connection == nil && !time.Now().Before(a.expires) {
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
	spec, err := l.freshAttachSpec(ctx, id)
	if err != nil {
		return zero, err
	}
	return l.reserveAttachment(id, spec)
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

func (l *lifecycle) reserveAttachment(id string, spec plugin.AttachSpec) (api.PendingAttachment, error) {
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
	l.attachments[token] = &attachment{session: id, expires: expires, spec: spec}
	return api.PendingAttachment{Token: token, ExpiresAt: expires, Spec: spec}, nil
}
func (l *lifecycle) freshAttachSpec(ctx context.Context, id string) (plugin.AttachSpec, error) {
	s, err := startEligible(ctx, id, l.store.GetSession, l.policyCondition)
	if err != nil {
		return plugin.AttachSpec{}, err
	}
	native, err := l.nativeSession(ctx, s)
	if err != nil {
		return plugin.AttachSpec{}, err
	}
	spec, err := l.runtime.AttachSpec(ctx, native, l.hostPlan)
	if err != nil {
		var failure *runtimeincus.AttachmentFailure
		if errors.As(err, &failure) {
			log.Printf("attachment verification refused: %s", failure.Code)
			if failure.Code == "host-not-ready" {
				return plugin.AttachSpec{}, refusalBecause(api.KindConflict, api.ReasonSessionNotRunning, api.TargetSession(id), control.ErrConflict, err)
			}
		} else {
			log.Printf("attachment runtime refused: policy-or-native-observation")
		}
		return plugin.AttachSpec{}, control.Failed(api.ReasonRuntime, err)
	}
	return spec, nil
}
func (l *lifecycle) ClaimAttachment(ctx context.Context, token string, conn *control.Connection) (api.AttachmentLaunch, error) {
	var candidate *attachment
	session := ""
	l.withLock(func() {
		candidate = l.attachments[token]
		if candidate != nil {
			session = candidate.session
		}
	})
	if session == "" {
		return api.AttachmentLaunch{}, attachmentExpired()
	}
	release, err := l.lockSession(ctx, session)
	if err != nil {
		return api.AttachmentLaunch{}, err
	}
	defer release()
	if err := l.admitSessionWork(ctx, session, ""); err != nil {
		return api.AttachmentLaunch{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expireAttachmentsLocked()
	a := l.attachments[token]
	if a == nil || a != candidate || a.connection != nil || !time.Now().Before(a.expires) {
		return api.AttachmentLaunch{}, attachmentExpired()
	}
	// Own installs cleanup atomically against connection closure. The callback
	// runs after Connection releases its own lock, avoiding lock inversion.
	if err := conn.Own(func() { l.releaseAttachment(token) }); err != nil {
		return api.AttachmentLaunch{}, err
	}
	a.connection = conn
	return api.AttachmentLaunch{Socket: l.incus.UserSocket, Spec: a.spec}, nil
}
func (l *lifecycle) releaseAttachment(token string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.attachments[token]
	if a == nil {
		return
	}
	if a.confirmed {
		l.store.ReleaseAttachmentWithCommit(a.session, func(count int) {
			if l.onAttachment != nil {
				l.onAttachment(l.ctx, a.session, count, false)
			}
		})
	}
	delete(l.attachments, token)
}
func (l *lifecycle) ConfirmAttachment(ctx context.Context, conn *control.Connection, operation string) error {
	var a *attachment
	claimed := false
	l.withLock(func() {
		for _, candidate := range l.attachments {
			if candidate.connection == conn {
				a = candidate
				break
			}
		}
		claimed = a != nil && !a.confirmed && time.Now().Before(a.expires)
	})
	if !claimed {
		return attachmentExpired()
	}
	release, err := l.lockSession(ctx, a.session)
	if err != nil {
		return err
	}
	defer release()
	if err := l.admitSessionWork(ctx, a.session, ""); err != nil {
		return err
	}
	// Fresh checks at confirmation also refuse host/ownership drift during launch.
	if _, err = l.freshAttachSpec(ctx, a.session); err != nil {
		return err
	}
	session, err := l.store.GetSession(ctx, a.session)
	if err != nil {
		return err
	}
	native, err := l.nativeSession(ctx, session)
	if err != nil {
		return err
	}
	if err = l.runtime.ValidateAttachmentOperation(ctx, native, operation); err != nil {
		return control.Failed(api.ReasonRuntime, err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	live := false
	for _, candidate := range l.attachments {
		if candidate == a {
			live = true
			break
		}
	}
	if !live || a.confirmed || !time.Now().Before(a.expires) {
		return attachmentExpired()
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
