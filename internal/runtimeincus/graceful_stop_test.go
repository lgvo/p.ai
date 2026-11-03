package runtimeincus

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGuest is a container of P's NixOS image as Incus shows it. Each start
// begins its early boot: until its systemd is up, its init loses Incus's
// graceful stop, and Incus gives up with exit status 1, leaving it running.
// Its systemd comes up while P looks for it, unless its boot hangs. A hung
// guest ignores the graceful stop even with systemd up. A forced stop always
// stops it.
type fakeGuest struct {
	t         *testing.T
	instance  string
	bootHangs bool
	hung      bool
	mu        sync.Mutex
	systemdUp bool
	events    []string
}

func (g *fakeGuest) record(event string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.events = append(g.events, event)
}

func (g *fakeGuest) seen() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.events)
}

// serveFiles answers Incus file API HEAD requests on a confined user socket.
func (g *fakeGuest) serveFiles() string {
	socket := filepath.Join(g.t.TempDir(), "unix.socket.user")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		g.t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead || r.URL.Path != "/1.0/instances/"+g.instance+"/files" || r.URL.Query().Get("project") != "user-1000" {
			g.t.Errorf("unexpected file API request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
			return
		}
		header := func(typ, mode string) {
			w.Header().Set("X-Incus-uid", "0")
			w.Header().Set("X-Incus-gid", "0")
			w.Header().Set("X-Incus-mode", mode)
			w.Header().Set("X-Incus-type", typ)
		}
		switch r.URL.Query().Get("path") {
		case builderSource:
			header("directory", "0555")
		case "/run/systemd/private":
			g.mu.Lock()
			up := g.systemdUp
			g.systemdUp = !g.bootHangs
			g.mu.Unlock()
			if !up {
				g.record("systemd not up")
				http.NotFound(w, r)
				return
			}
			g.record("systemd up")
			// Incus reports a socket as a file of size 0.
			header("file", "0700")
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", "0")
		default:
			http.NotFound(w, r)
		}
	})}
	go server.Serve(listener)
	g.t.Cleanup(func() { server.Close() })
	return socket
}

func (g *fakeGuest) wrap(run runner) runner {
	return func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		switch argv[3] {
		case "start":
			g.mu.Lock()
			g.systemdUp = false
			g.mu.Unlock()
			g.record("start")
		case "stop":
			g.record("stop " + strings.Join(argv[5:], " "))
			g.mu.Lock()
			up := g.systemdUp
			g.mu.Unlock()
			if !slices.Contains(argv, "--force") && (!up || g.hung) {
				return nil, errors.New("exit status 1")
			}
		}
		return run(ctx, binary, argv, env)
	}
}

// stoppedGracefullyAfterSystemd says whether the guest got one graceful stop,
// sent after P saw its systemd up, and no forced stop.
func stoppedGracefullyAfterSystemd(events []string) bool {
	stops := 0
	for _, event := range events {
		if strings.HasPrefix(event, "stop") {
			stops++
		}
	}
	up, stop := slices.Index(events, "systemd up"), slices.Index(events, "stop --timeout 30")
	return stops == 1 && up >= 0 && up < stop
}

func TestStopRightAfterStartWaitsForGuestSystemd(t *testing.T) {
	guest := &fakeGuest{t: t, instance: "p-" + testUUID}
	f := &fakeIncus{instance: true, status: "Stopped"}
	f.socket = guest.serveFiles()
	b := fakeBackend(f)
	b.config.UserSocket = f.socket
	b.run = guest.wrap(f.run)
	ctx := context.Background()
	if got, err := b.Start(ctx, testSession()); err != nil || got.Status != "Running" {
		t.Fatalf("start: %+v %v", got, err)
	}
	got, err := b.Stop(ctx, testSession())
	if err != nil || got.Status != "Stopped" {
		t.Fatalf("stop sent during early boot: %+v %v (guest saw %v)", got, err, guest.seen())
	}
	if events := guest.seen(); !stoppedGracefullyAfterSystemd(events) {
		t.Fatalf("not one graceful stop after the guest's systemd was up: %v", events)
	}
}

func TestBuilderDeleteRightAfterStartWaitsForGuestSystemd(t *testing.T) {
	r := testBuilder()
	guest := &fakeGuest{t: t, instance: builderName(r)}
	f := &fakeBuilderIncus{request: r, exists: true, status: "Stopped", rootLimit: true}
	f.socket = guest.serveFiles()
	f.base.socket = f.socket
	b := builderBackend(f)
	b.config.UserSocket = f.socket
	b.run = guest.wrap(f.run)
	ctx := context.Background()
	if got, err := b.StartBuilder(ctx, r); err != nil || got.Status != "Running" {
		t.Fatalf("start: %+v %v", got, err)
	}
	got, err := b.DeleteBuilder(ctx, r)
	if err != nil || got.Exists {
		t.Fatalf("delete sent its stop during early boot: %+v %v (guest saw %v)", got, err, guest.seen())
	}
	if events := guest.seen(); !stoppedGracefullyAfterSystemd(events) {
		t.Fatalf("not one graceful stop after the guest's systemd was up: %v", events)
	}
}

func TestFailedStopNamesIncusError(t *testing.T) {
	incus := filepath.Join(t.TempDir(), "incus")
	refusal := `Error: Failed shutting down instance, status is "Running": context deadline exceeded`
	if err := os.WriteFile(incus, []byte("#!/bin/sh\necho '"+refusal+"' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	f := &fakeIncus{instance: true, status: "Running"}
	f.socket = (&fakeGuest{t: t, instance: "p-" + testUUID, systemdUp: true}).serveFiles()
	b := fakeBackend(f)
	b.config.UserSocket = f.socket
	b.config.Binary = incus
	b.runLifecycle = runLifecycleCommand
	_, err := b.Stop(context.Background(), testSession())
	if err == nil || !strings.Contains(err.Error(), refusal) {
		t.Fatalf("failed stop does not name Incus's error: %v", err)
	}
}

func TestStopIsSentWhenGuestSystemdNeverComesUp(t *testing.T) {
	guest := &fakeGuest{t: t, instance: "p-" + testUUID, bootHangs: true}
	f := &fakeIncus{instance: true, status: "Stopped"}
	f.socket = guest.serveFiles()
	b := fakeBackend(f)
	b.config.UserSocket = f.socket
	b.run = guest.wrap(f.run)
	ctx := context.Background()
	if got, err := b.Start(ctx, testSession()); err != nil || got.Status != "Running" {
		t.Fatalf("start: %+v %v", got, err)
	}
	began := time.Now()
	got, err := b.Stop(ctx, testSession())
	if err != nil || got.Status != "Stopped" {
		t.Fatalf("stop after the wait: %+v %v", got, err)
	}
	if elapsed := time.Since(began); elapsed < 30*time.Second || elapsed > 35*time.Second {
		t.Fatalf("stop not sent after a 30 s wait: %s", elapsed)
	}
	events := guest.seen()
	graceful := slices.Index(events, "stop --timeout 30")
	if graceful < 1 || events[graceful-1] != "systemd not up" || !slices.Equal(events[graceful:], []string{"stop --timeout 30", "stop --force"}) {
		t.Fatalf("graceful stop not sent after the wait, then forced: %d events, last %v", len(events), events[max(0, len(events)-3):])
	}
}

func TestStopForcesAGuestThatIgnoresTheGracefulStop(t *testing.T) {
	guest := &fakeGuest{t: t, instance: "p-" + testUUID, systemdUp: true, hung: true}
	f := &fakeIncus{instance: true, status: "Running"}
	f.socket = guest.serveFiles()
	b := fakeBackend(f)
	b.config.UserSocket = f.socket
	b.run = guest.wrap(f.run)
	got, err := b.Stop(context.Background(), testSession())
	if err != nil || got.Status != "Stopped" {
		t.Fatalf("hung guest not stopped: %+v %v", got, err)
	}
	if events := guest.seen(); !slices.Equal(events, []string{"systemd up", "stop --timeout 30", "stop --force"}) {
		t.Fatalf("not graceful, then forced after the graceful timeout: %v", events)
	}
}

func TestStopBuilderForcesAGuestThatIgnoresTheGracefulStop(t *testing.T) {
	r := testBuilder()
	guest := &fakeGuest{t: t, instance: builderName(r), systemdUp: true, hung: true}
	f := &fakeBuilderIncus{request: r, exists: true, status: "Running", rootLimit: true}
	f.socket = guest.serveFiles()
	f.base.socket = f.socket
	b := builderBackend(f)
	b.config.UserSocket = f.socket
	b.run = guest.wrap(f.run)
	got, err := b.StopBuilder(context.Background(), r)
	if err != nil || got.Status != "Stopped" {
		t.Fatalf("hung builder not stopped: %+v %v", got, err)
	}
	if events := guest.seen(); !slices.Equal(events, []string{"systemd up", "stop --timeout 30", "stop --force"}) {
		t.Fatalf("not graceful, then forced after the graceful timeout: %v", events)
	}
}
