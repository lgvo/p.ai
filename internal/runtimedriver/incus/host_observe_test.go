package incus

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

func TestHostReadinessRequiresActualSystemdState(t *testing.T) {
	f := &fakeIncus{instance: true, status: "Running"}
	b := fakeBackend(f)
	unit := "ActiveState=activating\nSubState=start-post\nResult=success\n"
	commands := 0
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		args := argv[3:]
		if len(args) > 0 && args[0] == "exec" {
			commands++
			want := []string{"exec", "p-" + testUUID, "--mode", "non-interactive", "--", "/usr/libexec/p/systemctl", "show", "p-interactive.service", "--property=ActiveState,SubState,Result", "--no-pager"}
			if slices.Equal(args, want) {
				return []byte(unit), nil
			}
			return nil, errors.New("unexpected native exec")
		}
		return f.run(ctx, binary, argv, env)
	}
	h, err := b.ObserveHost(context.Background(), testSession())
	if err != nil || h.Ready || h.Unit != "activating/start-post" {
		t.Fatalf("Incus Running became ready: %+v %v", h, err)
	}
	unit = "ActiveState=active\nSubState=running\nResult=success\n"
	h, err = b.ObserveHost(context.Background(), testSession())
	if err != nil || !h.Ready || h.Unit != "active/running" || commands < 2 {
		t.Fatalf("systemd active not ready: %+v %v", h, err)
	}
}

// A long journal of multibyte characters is cut on a character boundary, so
// the host's diagnostic stays valid UTF-8.
func TestHostDiagnosticTailIsValidUTF8(t *testing.T) {
	journal := []byte(strings.Repeat("€", 1000) + "\xff end")
	got := boundedDiagnostic(journal)
	if len(got) > 2048 || !utf8.ValidString(got) || !strings.HasSuffix(got, "\uFFFD end") {
		t.Fatalf("diagnostic tail: %d bytes, valid=%t, %q", len(got), utf8.ValidString(got), got[len(got)-8:])
	}
}

// A stopped host's journal read that Incus's file API never answers leaves
// the last words unknown instead of holding the caller.
func TestAStalledJournalReadLeavesTheLastWordsUnknown(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "incus.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			defer conn.Close() // accepted, never answered
		}
	}()
	f := &fakeIncus{instance: true, status: "Stopped", socket: socket}
	b := fakeBackend(f)
	b.config.UserSocket = socket
	defer func(wait time.Duration) { lastWordsWait = wait }(lastWordsWait)
	lastWordsWait = 100 * time.Millisecond
	started := time.Now()
	h, err := b.ObserveHost(context.Background(), testSession())
	if err != nil || h.DiagnosticAvailable || time.Since(started) > 5*time.Second {
		t.Fatalf("stalled journal read: %+v %v after %s", h, err, time.Since(started))
	}
}

// Inspect of a stopped runtime reads no file of it: only HostLog reads a
// stopped host's journal.
func TestInspectOfAStoppedRuntimeReadsNoFile(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "incus.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	reads := make(chan struct{}, 16)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			reads <- struct{}{}
			conn.Close()
		}
	}()
	f := &fakeIncus{instance: true, status: "Stopped", socket: socket}
	b := fakeBackend(f)
	b.config.UserSocket = socket
	o, err := b.InspectCreated(context.Background(), testSession())
	if err != nil {
		t.Fatal(err)
	}
	observed := b.observe(context.Background(), testSession(), o)
	if observed.State != runtimedriver.StateStopped || observed.Host != (runtimedriver.HostState{}) || len(reads) != 0 {
		t.Fatalf("stopped observation: %+v, %d file reads", observed, len(reads))
	}
	if _, err := b.observeHostOf(context.Background(), testSession(), o); err != nil || len(reads) == 0 {
		t.Fatalf("the host's journal was not read for its last words: %v", err)
	}
}
