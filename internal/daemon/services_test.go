package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/control"
)

// servicesLifecycle returns a lifecycle that knows one established session
// whose project policy is current.
func servicesLifecycle(t *testing.T) (*lifecycle, control.Session) {
	t.Helper()
	ctx := context.Background()
	store, err := control.OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	policy := control.ProjectPolicy{Network: "none", FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/sh"}}
	snapshot, _, err := control.ProjectPolicySnapshot(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateProject(ctx, "app", snapshot); err != nil {
		t.Fatal(err)
	}
	selection := control.CreationSelection{RuntimeID: "runtime", RuntimeSHA256: strings.Repeat("a", 64), HostID: "host", HostSHA256: strings.Repeat("b", 64), SourceID: "source", SourceSHA256: strings.Repeat("c", 64)}
	op, session, err := store.BeginSessionCreate(ctx, control.ReserveSessionRequest{Key: "services", Project: "app", Branch: "work", Choice: "existing"}, strings.Repeat("d", 64), selection, func(context.Context, control.ReserveSessionRequest) (string, bool, error) {
		return strings.Repeat("e", 40), true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CompleteCreation(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	cfg := control.RuntimeConfig{ProjectPolicies: map[string]control.ProjectPolicy{"app": policy}}
	return &lifecycle{ctx: ctx, store: store, cfg: cfg}, session
}

// servicesRequestInFlight starts a services request on session that pauses at
// its confinement check, the first runtime observation it makes, until end is
// called or the context the check runs on ends, as an incus client is killed
// when its context ends. Later confinement checks pass at once.
func servicesRequestInFlight(t *testing.T, l *lifecycle, request context.Context, session, unit, action string) (end func()) {
	t.Helper()
	paused, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	first := true
	l.confinementCheck = func(call context.Context) error {
		mu.Lock()
		pause := first
		first = false
		mu.Unlock()
		if pause {
			close(paused)
			select {
			case <-resume:
			case <-call.Done():
				return call.Err()
			}
		}
		return nil
	}
	go func() {
		defer close(done)
		_, _ = l.observeServices(request, session, unit, action)
	}()
	<-paused
	ended := false
	end = func() {
		if !ended {
			ended = true
			close(resume)
			<-done
		}
	}
	t.Cleanup(end)
	return end
}

func TestStopWaitsForAServicesReadInsteadOfBeingRefused(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct{ action, unit string }{
		{"list", ""},
		{"logs", "p-project-notes-db.service"},
	} {
		t.Run(c.action, func(t *testing.T) {
			l, session := servicesLifecycle(t)
			end := servicesRequestInFlight(t, l, ctx, session.UUID, c.unit, c.action)
			if _, err := l.observeServices(ctx, session.UUID, "", "list"); errors.Is(err, control.ErrConflict) {
				t.Fatalf("a services %s in flight refused another read", c.action)
			}
			answer := make(chan error, 1)
			go func() {
				_, err := l.StopSession(ctx, session.UUID)
				answer <- err
			}()
			// While Stop waits for the read it holds the session, so a new
			// read is refused.
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := l.observeServices(ctx, session.UUID, "", "list"); errors.Is(err, control.ErrConflict) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("Stop did not take the session while a services %s was in flight", c.action)
				}
				time.Sleep(time.Millisecond)
			}
			select {
			case err := <-answer:
				t.Fatalf("Stop answered while a services %s was in flight: %v", c.action, err)
			case <-time.After(100 * time.Millisecond):
			}
			end()
			select {
			case err := <-answer:
				// Without a runtime in the test, an admitted Stop fails
				// later, when it reaches the runtime.
				if errors.Is(err, control.ErrConflict) {
					t.Fatalf("Stop was refused after a services %s: %v", c.action, err)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("Stop did not go on after a services %s ended", c.action)
			}
		})
	}
}

// Cancelling a read kills its incus client, and Incus keeps an exec it has
// already created on the instance for up to 10 s, so the read's runtime work
// must end before a Stop, and a removal after it, can go on.
func TestStopWaitsForTheRuntimeWorkOfAServicesReadItsClientCancelled(t *testing.T) {
	for _, c := range []struct{ action, unit string }{
		{"list", ""},
		{"logs", "p-project-notes-db.service"},
	} {
		t.Run(c.action, func(t *testing.T) {
			l, session := servicesLifecycle(t)
			request, cancel := context.WithCancel(context.Background())
			end := servicesRequestInFlight(t, l, request, session.UUID, c.unit, c.action)
			cancel()
			answer := make(chan error, 1)
			go func() {
				_, err := l.StopSession(context.Background(), session.UUID)
				answer <- err
			}()
			select {
			case err := <-answer:
				t.Fatalf("Stop answered while the runtime work of a cancelled services %s ran: %v", c.action, err)
			case <-time.After(200 * time.Millisecond):
			}
			end()
			select {
			case err := <-answer:
				if errors.Is(err, control.ErrConflict) {
					t.Fatalf("Stop was refused after a cancelled services %s: %v", c.action, err)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("Stop did not go on after the runtime work of a cancelled services %s ended", c.action)
			}
		})
	}
}

func TestStopIsRefusedDuringAServiceAction(t *testing.T) {
	l, session := servicesLifecycle(t)
	servicesRequestInFlight(t, l, context.Background(), session.UUID, "p-project-notes-db.service", "restart")
	answer := make(chan error, 1)
	go func() {
		_, err := l.StopSession(context.Background(), session.UUID)
		answer <- err
	}()
	select {
	case err := <-answer:
		if !errors.Is(err, control.ErrConflict) {
			t.Fatalf("Stop admitted during a service action: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stop waited for a service action instead of being refused")
	}
}

func TestServicesReadIsRefusedWhileAServiceActionHoldsTheSession(t *testing.T) {
	l, session := servicesLifecycle(t)
	servicesRequestInFlight(t, l, context.Background(), session.UUID, "p-project-notes-db.service", "restart")
	if _, err := l.observeServices(context.Background(), session.UUID, "", "list"); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("services read admitted while a service action held the session: %v", err)
	}
}
