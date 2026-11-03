package runtimeincus

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// P's states for systemd's: an unloaded unit is stopped, and a state systemd
// does not document is refused rather than guessed.
func TestServiceStatesMapSystemdActiveStates(t *testing.T) {
	for active, want := range map[string]runtimedriver.ServiceState{
		"inactive": runtimedriver.ServiceStopped, "activating": runtimedriver.ServiceStarting, "active": runtimedriver.ServiceRunning,
		"reloading": runtimedriver.ServiceRunning, "refreshing": runtimedriver.ServiceRunning, "deactivating": runtimedriver.ServiceStopping,
		"maintenance": runtimedriver.ServiceStopping, "failed": runtimedriver.ServiceFailed,
	} {
		if got, known := serviceState(active); !known || got != want {
			t.Errorf("%s: %q %v, want %q", active, got, known, want)
		}
	}
	if _, known := serviceState("unknown"); known {
		t.Error("an undocumented state was mapped")
	}
}

// runningSession is the target of a session whose runtime runs with a ready
// host and the given project services.
func runningSession(t *testing.T, w *incusWorld, units []worldUnit, unitFiles ...string) runtimedriver.Target {
	t.Helper()
	s := testSessionSpec(assemblyFixture(t))
	created := createdSession(t, w, s)
	if _, err := w.driver().Start(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	w.change(testRuntime, func(in *worldInstance) { in.units, in.unitFiles = units, unitFiles })
	return runtimedriver.Target{Session: s, At: created.Incarnation}
}

func TestServicesListTheProjectServicesSortedByName(t *testing.T) {
	w := newIncusWorld(t)
	target := runningSession(t, w, []worldUnit{
		{name: "p-project-web.service", description: "Web", active: "active", sub: "running"},
		{name: "p-project-db.service", description: "Database", active: "activating", sub: "start-pre"},
	}, "p-project-db.service", "p-project-idle.service")
	got, err := w.driver().Services(context.Background(), target)
	want := []runtimedriver.Service{
		{Name: "p-project-db.service", Description: "Database", State: runtimedriver.ServiceStarting, Detail: "activating (start-pre)"},
		{Name: "p-project-idle.service", Description: "Installed user unit; not loaded", State: runtimedriver.ServiceStopped, Detail: "installed"},
		{Name: "p-project-web.service", Description: "Web", State: runtimedriver.ServiceRunning, Detail: "active (running)"},
	}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("inventory: %+v %v", got, err)
	}
	w.change(testRuntime, func(in *worldInstance) { in.units, in.unitFiles = nil, nil })
	if got, err := w.driver().Services(context.Background(), target); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("no services: %#v %v", got, err)
	}
}

// An installed-unit inventory beyond its bound is as unusable as one the
// service manager would not list.
func TestAnInstalledUnitInventoryBeyondItsBoundIsUnavailable(t *testing.T) {
	var files []string
	for i := range runtimedriver.MaxServices + 1 {
		files = append(files, fmt.Sprintf("p-project-%02d.service", i))
	}
	w := newIncusWorld(t)
	target := runningSession(t, w, nil, files...)
	if got, err := w.driver().Services(context.Background(), target); !errors.Is(err, runtimedriver.ErrUnavailable) {
		t.Fatalf("an installed-unit inventory beyond its bound: %+v %v", got, err)
	}
}

func TestServicesAreCompleteAndBoundedOrAnError(t *testing.T) {
	var many []worldUnit
	for i := range runtimedriver.MaxServices + 1 {
		many = append(many, worldUnit{name: fmt.Sprintf("p-project-%02d.service", i), active: "inactive", sub: "dead"})
	}
	for name, units := range map[string][]worldUnit{
		"too many":              many,
		"the session host":      {{name: "p-interactive.service", active: "active", sub: "running"}},
		"unprintable":           {{name: "p-project-web.service", description: "Web\x1b[2J", active: "active", sub: "running"}},
		"an undocumented state": {{name: "p-project-web.service", active: "exploding", sub: "running"}},
	} {
		t.Run(name, func(t *testing.T) {
			w := newIncusWorld(t)
			target := runningSession(t, w, units)
			if got, err := w.driver().Services(context.Background(), target); err == nil {
				t.Fatalf("partial or invalid inventory returned: %+v", got)
			}
		})
	}
}

func TestServiceActionQueuesTheJobAndTheInventoryShowsItsProgress(t *testing.T) {
	w := newIncusWorld(t)
	target := runningSession(t, w, []worldUnit{
		{name: "p-project-web.service", active: "inactive", sub: "dead"},
		{name: "p-project-db.service", active: "inactive", sub: "dead", failing: true},
	})
	ctx := context.Background()
	d := w.driver()
	for _, name := range []string{"p-project-web.service", "p-project-db.service"} {
		if err := d.ServiceAction(ctx, target, name, runtimedriver.ActionStart); err != nil {
			t.Fatalf("start %s: %v", name, err)
		}
	}
	execs := w.userExecs()
	if action := execs[len(execs)-1]; !slices.Equal(action, []string{"/usr/libexec/p/systemctl", "--user", "--no-pager", "--no-block", "start", "p-project-db.service"}) {
		t.Fatalf("action command %v", action)
	}
	states := func() map[string]runtimedriver.ServiceState {
		services, err := d.Services(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]runtimedriver.ServiceState{}
		for _, s := range services {
			out[s.Name] = s.State
		}
		return out
	}
	if got := states(); got["p-project-web.service"] != runtimedriver.ServiceStarting || got["p-project-db.service"] != runtimedriver.ServiceStarting {
		t.Fatalf("queued starts: %v", got)
	}
	w.settleServices(testRuntime)
	if got := states(); got["p-project-web.service"] != runtimedriver.ServiceRunning || got["p-project-db.service"] != runtimedriver.ServiceFailed {
		t.Fatalf("finished starts: %v", got)
	}
	if err := d.ServiceAction(ctx, target, "p-project-web.service", runtimedriver.ActionStop); err != nil || states()["p-project-web.service"] != runtimedriver.ServiceStopping {
		t.Fatalf("queued stop: %v", err)
	}
	if err := d.ServiceAction(ctx, target, "p-project-web.service", "reload"); err == nil {
		t.Fatal("an action P does not offer was sent")
	}
}

func TestServiceNamesTheSessionDoesNotHaveNeverReachTheServiceManager(t *testing.T) {
	w := newIncusWorld(t)
	target := runningSession(t, w, []worldUnit{{name: "p-project-web.service", active: "active", sub: "running"}})
	ctx := context.Background()
	d := w.driver()
	for _, name := range []string{"p-interactive.service", "p-session.target", "sshd.service", "../p-project-web.service", "--root", "p-project-web.service\nsshd.service"} {
		if err := d.ServiceAction(ctx, target, name, runtimedriver.ActionRestart); !errors.Is(err, runtimedriver.ErrNoService) {
			t.Fatalf("action on %q: %v", name, err)
		}
		if _, err := d.ServiceLogs(ctx, target, name); !errors.Is(err, runtimedriver.ErrNoService) {
			t.Fatalf("logs of %q: %v", name, err)
		}
	}
	if execs := w.userExecs(); len(execs) != 0 {
		t.Fatalf("a name that is no project service reached the session: %v", execs)
	}
	if err := d.ServiceAction(ctx, target, "p-project-gone.service", runtimedriver.ActionRestart); !errors.Is(err, runtimedriver.ErrNoService) {
		t.Fatalf("action on a service the session does not have: %v", err)
	}
	if _, err := d.ServiceLogs(ctx, target, "p-project-gone.service"); !errors.Is(err, runtimedriver.ErrNoService) {
		t.Fatalf("logs of a service the session does not have: %v", err)
	}
	for _, command := range w.userExecs() {
		if slices.Contains(command, "restart") || command[0] == "/usr/libexec/p/journalctl" {
			t.Fatalf("a refused name reached the service manager: %v", command)
		}
	}
}

func TestServicesRequireAReadySessionHost(t *testing.T) {
	ctx := context.Background()
	w := newIncusWorld(t)
	target := runningSession(t, w, nil)
	for name, state := range map[string]func(*worldInstance){
		"stopped":  func(in *worldInstance) { in.status = "Stopped" },
		"starting": func(in *worldInstance) { in.host = "activating/start-post/success" },
	} {
		w.change(testRuntime, state)
		if _, err := w.driver().Services(ctx, target); !errors.Is(err, runtimedriver.ErrNotReady) {
			t.Fatalf("%s host: %v", name, err)
		}
		w.change(testRuntime, func(in *worldInstance) { in.status, in.host = "Running", worldHostReady })
	}
	w.before = func(args []string) error {
		if args[0] == "exec" {
			return errors.New("guest systemd not accepting exec")
		}
		return nil
	}
	if _, err := w.driver().Services(ctx, target); !errors.Is(err, runtimedriver.ErrUnavailable) {
		t.Fatalf("unreadable host: %v", err)
	}
	if len(w.userExecs()) != 0 {
		t.Fatalf("a session that is not ready reached the service manager: %v", w.userExecs())
	}
}

func TestServicesActOnlyOnTheTargetsIncarnation(t *testing.T) {
	ctx := context.Background()
	w := newIncusWorld(t)
	target := runningSession(t, w, []worldUnit{{name: "p-project-web.service", active: "active", sub: "running"}})
	other := target
	other.At = "00000000-0000-4000-8000-000000000000/00000000-0000-4000-8000-000000000000"
	if err := w.driver().ServiceAction(ctx, other, "p-project-web.service", runtimedriver.ActionStop); !errors.Is(err, runtimedriver.ErrMismatch) || len(w.userExecs()) != 0 {
		t.Fatalf("another incarnation: %v %v", err, w.userExecs())
	}
	if _, err := w.driver().Services(ctx, runtimedriver.Target{Session: target.Session}); err != nil {
		t.Fatalf("any incarnation: %v", err)
	}
	w.after = func(args []string) error {
		if slices.Contains(args, "list-units") {
			w.mu.Lock()
			w.instance(testRuntime).config["volatile.uuid.generation"] = w.newUUID()
			w.mu.Unlock()
		}
		return nil
	}
	if _, err := w.driver().Services(ctx, runtimedriver.Target{Session: target.Session}); !errors.Is(err, runtimedriver.ErrMismatch) {
		t.Fatalf("runtime restored during the call: %v", err)
	}
}

func TestAServiceManagerThatCannotBeReachedIsUnavailable(t *testing.T) {
	ctx := context.Background()
	w := newIncusWorld(t)
	target := runningSession(t, w, []worldUnit{{name: "p-project-web.service", active: "active", sub: "running"}})
	w.before = func(args []string) error {
		if args[0] == "exec" && slices.Contains(args, "--user") {
			return errors.New("Failed to connect to bus")
		}
		return nil
	}
	if _, err := w.driver().Services(ctx, target); !errors.Is(err, runtimedriver.ErrUnavailable) {
		t.Fatalf("inventory: %v", err)
	}
	if err := w.driver().ServiceAction(ctx, target, "p-project-web.service", runtimedriver.ActionStop); !errors.Is(err, runtimedriver.ErrUnavailable) {
		t.Fatalf("action: %v", err)
	}
}

func TestServiceLogsAreABoundedValidUTF8Tail(t *testing.T) {
	ctx := context.Background()
	w := newIncusWorld(t)
	short := "2026-11-03T10:00:00+0000 p web[42]: listening on :8080\n"
	long := "\xff" + strings.Repeat("é", runtimedriver.MaxLogBytes) + "last line\n"
	target := runningSession(t, w, []worldUnit{
		{name: "p-project-web.service", active: "active", sub: "running", logs: short},
		{name: "p-project-db.service", active: "active", sub: "running", logs: long},
	})
	if got, err := w.driver().ServiceLogs(ctx, target, "p-project-web.service"); err != nil || got.Text != short || got.Truncated {
		t.Fatalf("short log: %+v %v", got, err)
	}
	got, err := w.driver().ServiceLogs(ctx, target, "p-project-db.service")
	if err != nil || !got.Truncated || len(got.Text) > runtimedriver.MaxLogBytes || len(got.Text) < runtimedriver.MaxLogBytes-1 || !utf8.ValidString(got.Text) || !strings.HasSuffix(got.Text, "éélast line\n") {
		t.Fatalf("long log: %d bytes, truncated %t, valid %t, %v", len(got.Text), got.Truncated, utf8.ValidString(got.Text), err)
	}
	execs := w.userExecs()
	if logs := execs[len(execs)-1]; !slices.Equal(logs, []string{"/usr/libexec/p/journalctl", "--user-unit=p-project-db.service", "--no-pager", "--output=short-iso", "-n", "100"}) {
		t.Fatalf("journal command %v", logs)
	}
}

func TestServiceActionProvesTheBoundaryBeforeTheAction(t *testing.T) {
	w := newIncusWorld(t)
	target := runningSession(t, w, []worldUnit{{name: "p-project-web.service", active: "inactive", sub: "dead"}})
	w.mu.Lock()
	w.unsafeProfile = true
	w.mu.Unlock()
	if err := w.driver().ServiceAction(context.Background(), target, "p-project-web.service", runtimedriver.ActionStart); err == nil {
		t.Fatal("an action was queued while the project's confinement was broken")
	}
	for _, command := range w.userExecs() {
		if slices.Contains(command, "--no-block") {
			t.Fatalf("the action reached the service manager: %v", command)
		}
	}
}
