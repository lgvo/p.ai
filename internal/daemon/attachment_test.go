package daemon

import (
	"context"
	"errors"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
	"strings"
	"testing"
	"time"
)

func TestAttachmentPendingClaimExpiryAndStopExclusion(t *testing.T) {
	ctx := context.Background()
	token := strings.Repeat("a", 64)
	pending := &attachment{session: "session", expires: time.Now().Add(time.Minute), spec: plugin.AttachSpec{Project: "p", Instance: "fixed", Argv: []string{"/usr/libexec/p/attach"}}}
	l := &lifecycle{attachments: map[string]*attachment{token: pending}}
	// Stop must reject before touching native/runtime authority or the store.
	if _, err := l.StopSession(ctx, "session"); !errors.Is(err, control.ErrConflict) {
		t.Fatal("pending attachment did not block stop", err)
	}
	conn := &control.Connection{}
	launch, err := l.ClaimAttachment(ctx, token, conn)
	if err != nil || launch.Spec.Instance != "fixed" {
		t.Fatal(launch, err)
	}
	if _, err = l.ClaimAttachment(ctx, token, &control.Connection{}); !errors.Is(err, control.ErrConflict) {
		t.Fatal("one-use token replayed", err)
	}
	pending.expires = time.Now().Add(-time.Second)
	l.mu.Lock()
	blocked := l.hasAttachmentLocked("session")
	l.mu.Unlock()
	if !blocked {
		t.Fatal("claimed channel lost stop exclusion during launch teardown")
	}
	if err = l.ConfirmAttachment(ctx, conn, "/1.0/operations/11111111-1111-1111-1111-111111111111"); !errors.Is(err, control.ErrConflict) {
		t.Fatal("expired token confirmed", err)
	}
	l.releaseAttachment(token)
	if len(l.attachments) != 0 {
		t.Fatal("failed launch retained pending state")
	}
	l.attachments[token] = &attachment{session: "session", expires: time.Now().Add(-time.Second)}
	l.mu.Lock()
	blocked = l.hasAttachmentLocked("session")
	l.mu.Unlock()
	if blocked {
		t.Fatal("unclaimed expired token blocked stop")
	}
}
func TestAttachmentUnknownConnectionCannotConfirm(t *testing.T) {
	l := &lifecycle{}
	if err := l.ConfirmAttachment(context.Background(), &control.Connection{}, "/1.0/operations/11111111-1111-1111-1111-111111111111"); !errors.Is(err, control.ErrConflict) {
		t.Fatal("unclaimed connection confirmed", err)
	}
}

func TestAttachmentReservationLimitsAndExpiredCapacity(t *testing.T) {
	l := &lifecycle{}
	for i := 0; i < maxSessionAttachments; i++ {
		if _, err := l.reserveAttachment("one", plugin.AttachSpec{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.reserveAttachment("one", plugin.AttachSpec{}); !errors.Is(err, control.ErrConflict) {
		t.Fatal("per-session bound missing")
	}
	for _, a := range l.attachments {
		a.expires = time.Now().Add(-time.Second)
	}
	if _, err := l.reserveAttachment("one", plugin.AttachSpec{}); err != nil {
		t.Fatal("expiry did not free capacity", err)
	}
	for i := len(l.attachments); i < maxAttachments; i++ {
		if _, err := l.reserveAttachment(string(rune(i+100)), plugin.AttachSpec{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.reserveAttachment("extra", plugin.AttachSpec{}); !errors.Is(err, control.ErrConflict) {
		t.Fatal("global bound missing")
	}
}
