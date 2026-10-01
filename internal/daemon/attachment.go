package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"time"

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

func (l *lifecycle) expireAttachmentsLocked() {
	for token, a := range l.attachments {
		if a.connection == nil && !time.Now().Before(a.expires) {
			delete(l.attachments, token)
		}
	}
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
func (l *lifecycle) AttachSession(ctx context.Context, id string) (control.PendingAttachment, error) {
	var zero control.PendingAttachment
	// Start owns the session lock while its asynchronous readiness check runs.
	// Retry only that in-progress start; conflicting lifecycle operations fail.
	view, err := l.StartSession(ctx, id)
	if err != nil {
		l.mu.Lock()
		starting := l.startActive[id]
		l.mu.Unlock()
		if !errors.Is(err, control.ErrConflict) || !starting {
			return zero, err
		}
		view.Condition = "starting"
	}
	for view.Condition == "starting" {
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		view, err = l.InspectSession(ctx, id)
		if err != nil {
			return zero, err
		}
	}
	if view.Condition != "ready" {
		return zero, control.ErrConflict
	}
	// A host can become observably ready just before Start's goroutine releases
	// its lock. Wait for that owned start to finish instead of rejecting attach.
	for {
		l.mu.Lock()
		starting := l.startActive[id]
		l.mu.Unlock()
		if !starting {
			break
		}
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	release, err := l.lockSession(ctx, id)
	if err != nil {
		return zero, err
	}
	defer release()
	if err := l.checkNoWorkspaceInspect(ctx, id); err != nil {
		return zero, err
	}
	spec, err := l.freshAttachSpec(ctx, id)
	if err != nil {
		return zero, err
	}
	return l.reserveAttachment(id, spec)
}
func (l *lifecycle) reserveAttachment(id string, spec plugin.AttachSpec) (control.PendingAttachment, error) {
	var zero control.PendingAttachment
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
		return zero, control.ErrConflict
	}
	if l.attachments == nil {
		l.attachments = map[string]*attachment{}
	}
	l.attachments[token] = &attachment{session: id, expires: expires, spec: spec}
	return control.PendingAttachment{V: 1, Token: token, ExpiresAt: expires, Spec: spec}, nil
}
func (l *lifecycle) freshAttachSpec(ctx context.Context, id string) (plugin.AttachSpec, error) {
	s, err := startEligible(ctx, id, l.store.GetSession, l.policyCondition)
	if err != nil {
		return plugin.AttachSpec{}, err
	}
	native, err := l.runtimeSession(ctx, s)
	if err != nil {
		return plugin.AttachSpec{}, err
	}
	state, err := plugin.RunRuntime(ctx, l.runtimePlugin, "runtime.attach", runtimeincus.Scoped{Backend: l.runtime, Session: native, HostAssets: &l.hostPlan})
	if err != nil {
		var failure *runtimeincus.AttachmentFailure
		if errors.As(err, &failure) {
			log.Printf("attachment verification refused: %s", failure.Code)
		} else {
			log.Printf("attachment runtime refused: policy-or-native-observation")
		}
		return plugin.AttachSpec{}, err
	}
	if state.AttachSpec == nil {
		return plugin.AttachSpec{}, control.ErrConflict
	}
	return *state.AttachSpec, nil
}
func (l *lifecycle) ClaimAttachment(ctx context.Context, token string, conn *control.Connection) (control.AttachmentLaunch, error) {
	l.mu.Lock()
	candidate := l.attachments[token]
	session := ""
	if candidate != nil {
		session = candidate.session
	}
	l.mu.Unlock()
	if session == "" {
		return control.AttachmentLaunch{}, control.ErrConflict
	}
	release, err := l.lockSession(ctx, session)
	if err != nil {
		return control.AttachmentLaunch{}, err
	}
	defer release()
	if err := l.checkNoWorkspaceInspect(ctx, session); err != nil {
		return control.AttachmentLaunch{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expireAttachmentsLocked()
	a := l.attachments[token]
	if a == nil || a != candidate || a.connection != nil || !time.Now().Before(a.expires) {
		return control.AttachmentLaunch{}, control.ErrConflict
	}
	// Own installs cleanup atomically against connection closure. The callback
	// runs after Connection releases its own lock, avoiding lock inversion.
	if err := conn.Own(func() { l.releaseAttachment(token) }); err != nil {
		return control.AttachmentLaunch{}, err
	}
	a.connection = conn
	return control.AttachmentLaunch{V: 1, Socket: l.cfg.IncusUserSocket, Spec: a.spec}, nil
}
func (l *lifecycle) releaseAttachment(token string) {
	l.mu.Lock()
	a := l.attachments[token]
	if a == nil {
		l.mu.Unlock()
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
	l.mu.Unlock()
}
func (l *lifecycle) ConfirmAttachment(ctx context.Context, conn *control.Connection, operation string) error {
	l.mu.Lock()
	var a *attachment
	for _, candidate := range l.attachments {
		if candidate.connection == conn {
			a = candidate
			break
		}
	}
	if a == nil || a.confirmed || !time.Now().Before(a.expires) {
		l.mu.Unlock()
		return control.ErrConflict
	}
	l.mu.Unlock()
	release, err := l.lockSession(ctx, a.session)
	if err != nil {
		return err
	}
	defer release()
	if err := l.checkNoWorkspaceInspect(ctx, a.session); err != nil {
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
	native, err := l.runtimeSession(ctx, session)
	if err != nil {
		return err
	}
	if err = l.runtime.ValidateAttachmentOperation(ctx, native, operation); err != nil {
		return err
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
		return control.ErrConflict
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
