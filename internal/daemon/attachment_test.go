package daemon

import (
	"context"
	"errors"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAttachmentPendingClaimExpiryAndStopExclusion(t *testing.T) {
	ctx := context.Background()
	token := strings.Repeat("a", 64)
	pending := &attachment{session: "session", expires: time.Now().Add(time.Minute), spec: plugin.AttachSpec{Project: "p", Instance: "fixed", Argv: []string{"/usr/libexec/p/attach"}}}
	l := &lifecycle{attachments: map[string]*attachment{token: pending}}
	// Stop must reject before touching native/runtime authority; it reads
	// only the journal, which holds nothing for the session.
	stopping := &lifecycle{store: newJournalFixture(t).store, attachments: map[string]*attachment{token: {session: sessionUUID(1), expires: time.Now().Add(time.Minute)}}}
	if _, err := stopping.StopSession(ctx, sessionUUID(1)); !errors.Is(err, control.ErrConflict) {
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
func TestAttachmentReleasePanicLeavesLifecycleLockFree(t *testing.T) {
	ctx := context.Background()
	store, err := control.OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	policy, _, err := control.ProjectPolicySnapshot(control.ProjectPolicy{Network: "none", FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/sh"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateProject(ctx, "app", policy); err != nil {
		t.Fatal(err)
	}
	selection := control.CreationSelection{HostID: "host", HostSHA256: strings.Repeat("b", 64), SourceID: "source", SourceSHA256: strings.Repeat("c", 64)}
	op, session, err := store.BeginSessionCreate(ctx, control.ReserveSessionRequest{Key: "attached", Project: "app", Branch: "work", Choice: "existing"}, strings.Repeat("d", 64), selection, func(context.Context, control.ReserveSessionRequest) (string, bool, error) {
		return strings.Repeat("e", 40), true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CompleteCreation(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = store.ConfirmAttachment(ctx, session.UUID); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 64)
	l := &lifecycle{ctx: ctx, store: store,
		attachments:  map[string]*attachment{token: {session: session.UUID, expires: time.Now().Add(time.Minute), confirmed: true}},
		onAttachment: func(context.Context, string, int, bool) { panic("attachment event bug") }}
	// The control server recovers a panic in the release it runs at connection close.
	panicked := func() (value any) {
		defer func() { value = recover() }()
		l.releaseAttachment(token)
		return nil
	}()
	if panicked == nil {
		t.Fatal("release did not reach the panicking attachment callback")
	}
	answered := make(chan error, 1)
	go func() {
		answered <- l.ConfirmAttachment(ctx, &control.Connection{}, "/1.0/operations/11111111-1111-1111-1111-111111111111")
	}()
	select {
	case err := <-answered:
		if !errors.Is(err, control.ErrConflict) {
			t.Fatal("unclaimed connection confirmed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the lifecycle lock stayed held after a panic in attachment release")
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
