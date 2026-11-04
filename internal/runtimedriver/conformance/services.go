package conformance

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

var serviceCases = []testCase{
	{"a driver without services refuses all three operations as unsupported", func(t *testing.T, f Fixture) {
		if f.Driver().Describe().Services {
			t.Skip("the driver offers services")
		}
		s := f.Session(SessionOptions{})
		at := running(t, f, s).Incarnation
		wantEveryServiceCall(t, f, target(s, at), "p-project-web.service", runtimedriver.ErrUnsupported)
	}},
	{"the inventory lists every project service, sorted by name, with its state", func(t *testing.T, f Fixture) {
		r := servicesRunning(t, f)
		want := []runtimedriver.Service{}
		for _, svc := range []NativeService{
			{Base: "web", Description: "Web server", State: runtimedriver.ServiceRunning},
			{Base: "db", Description: "Database", State: runtimedriver.ServiceStopped},
			{Base: "api", Description: "API", State: runtimedriver.ServiceFailed},
			{Base: "worker", Description: "Worker", State: runtimedriver.ServiceStarting},
			{Base: "cache", Description: "Cache", State: runtimedriver.ServiceStopping},
		} {
			name := f.Services().AddService(r.ref(), svc)
			want = append(want, runtimedriver.Service{Name: name, Description: svc.Description, State: svc.State})
		}
		slices.SortFunc(want, func(a, b runtimedriver.Service) int { return strings.Compare(a.Name, b.Name) })
		got := r.services(t)
		if len(got) != len(want) {
			t.Fatalf("Services = %+v, want %+v", got, want)
		}
		for i := range want {
			if got[i].Name != want[i].Name || got[i].Description != want[i].Description || got[i].State != want[i].State {
				t.Fatalf("Services = %+v, want %+v", got, want)
			}
		}
	}},
	{"the inventory holds at most MaxServices services and is an error beyond that, never a subset", func(t *testing.T, f Fixture) {
		r := servicesRunning(t, f)
		for i := range runtimedriver.MaxServices {
			f.Services().AddService(r.ref(), NativeService{Base: fmt.Sprintf("svc-%02d", i), Description: "bounded", State: runtimedriver.ServiceStopped})
		}
		if got := r.services(t); len(got) != runtimedriver.MaxServices {
			t.Fatalf("Services listed %d services, want %d", len(got), runtimedriver.MaxServices)
		}
		f.Services().AddService(r.ref(), NativeService{Base: "one-too-many", Description: "bounded", State: runtimedriver.ServiceStopped})
		if got, err := f.Driver().Services(t.Context(), r.target); err == nil {
			t.Fatalf("Services over its bound = %d services, want an error", len(got))
		}
	}},
	{"services need a running session whose host is ready", func(t *testing.T, f Fixture) {
		if !f.Driver().Describe().Services {
			t.Skip("the driver offers no services")
		}
		s := f.Session(SessionOptions{})
		at := created(t, f, s).Incarnation
		wantEveryServiceCall(t, f, target(s, at), "p-project-web.service", runtimedriver.ErrNotReady)
		r := servicesOn(t, f, s)
		name := f.Services().AddService(r.ref(), NativeService{Base: "web", State: runtimedriver.ServiceStopped})
		f.Sessions().UnreadyHost(r.ref(), "the session host is restarting")
		wantEveryServiceCall(t, f, r.target, name, runtimedriver.ErrNotReady)
	}},
	{"a services call at another incarnation is refused", func(t *testing.T, f Fixture) {
		r := servicesRunning(t, f)
		name := f.Services().AddService(r.ref(), NativeService{Base: "web", State: runtimedriver.ServiceStopped})
		other := created(t, f, f.Session(SessionOptions{})).Incarnation
		at := target(r.target.Session, other)
		if _, err := f.Driver().Services(t.Context(), at); err == nil {
			t.Error("Services at another incarnation succeeded")
		}
		if err := f.Driver().ServiceAction(t.Context(), at, name, runtimedriver.ActionStart); err == nil {
			t.Error("ServiceAction at another incarnation succeeded")
		}
		if _, err := f.Driver().ServiceLogs(t.Context(), at, name); err == nil {
			t.Error("ServiceLogs at another incarnation succeeded")
		}
		if got := r.state(t, name); got != runtimedriver.ServiceStopped {
			t.Fatalf("the refused action left the service %s", got)
		}
	}},
	{"an action returns once queued and the inventory shows its progress", func(t *testing.T, f Fixture) {
		r := servicesRunning(t, f)
		web := f.Services().AddService(r.ref(), NativeService{Base: "web", State: runtimedriver.ServiceStopped})
		broken := f.Services().AddService(r.ref(), NativeService{Base: "broken", State: runtimedriver.ServiceStopped, FailsToStart: true})
		for _, step := range []struct {
			name    string
			action  runtimedriver.Action
			queued  runtimedriver.ServiceState
			settled runtimedriver.ServiceState
		}{
			{web, runtimedriver.ActionStart, runtimedriver.ServiceStarting, runtimedriver.ServiceRunning},
			{web, runtimedriver.ActionRestart, runtimedriver.ServiceStarting, runtimedriver.ServiceRunning},
			{web, runtimedriver.ActionStop, runtimedriver.ServiceStopping, runtimedriver.ServiceStopped},
			{broken, runtimedriver.ActionStart, runtimedriver.ServiceStarting, runtimedriver.ServiceFailed},
		} {
			if err := f.Driver().ServiceAction(t.Context(), r.target, step.name, step.action); err != nil {
				t.Fatalf("ServiceAction(%s, %s): %v", step.name, step.action, err)
			}
			if got := r.state(t, step.name); got != step.queued {
				t.Fatalf("after %s of %s the service is %s, want %s", step.action, step.name, got, step.queued)
			}
			f.Services().Progress(r.ref(), step.name)
			if got := r.state(t, step.name); got != step.settled {
				t.Fatalf("once %s of %s finished the service is %s, want %s", step.action, step.name, got, step.settled)
			}
		}
	}},
	{"starting a running service or stopping a stopped one does nothing", func(t *testing.T, f Fixture) {
		r := servicesRunning(t, f)
		up := f.Services().AddService(r.ref(), NativeService{Base: "up", State: runtimedriver.ServiceRunning})
		down := f.Services().AddService(r.ref(), NativeService{Base: "down", State: runtimedriver.ServiceStopped})
		if err := f.Driver().ServiceAction(t.Context(), r.target, up, runtimedriver.ActionStart); err != nil {
			t.Fatalf("ServiceAction(start): %v", err)
		}
		if err := f.Driver().ServiceAction(t.Context(), r.target, down, runtimedriver.ActionStop); err != nil {
			t.Fatalf("ServiceAction(stop): %v", err)
		}
		if got := r.state(t, up); got != runtimedriver.ServiceRunning {
			t.Fatalf("starting a running service left it %s", got)
		}
		if got := r.state(t, down); got != runtimedriver.ServiceStopped {
			t.Fatalf("stopping a stopped service left it %s", got)
		}
	}},
	{"names that are not the session's project services are ErrNoService and never reach the service manager", func(t *testing.T, f Fixture) {
		r := servicesRunning(t, f)
		f.Services().AddService(r.ref(), NativeService{Base: "demo", State: runtimedriver.ServiceStopped})
		// Scenario 56's names, then one the session does not have.
		refused := []string{"p-interactive.service", "p-session.target", "sshd.service", "../p-project-demo.service", "p-project-missing.service"}
		for _, name := range refused {
			if err := f.Driver().ServiceAction(t.Context(), r.target, name, runtimedriver.ActionStart); !errors.Is(err, runtimedriver.ErrNoService) {
				t.Errorf("ServiceAction(%s) = %v, want ErrNoService", name, err)
			}
			if _, err := f.Driver().ServiceLogs(t.Context(), r.target, name); !errors.Is(err, runtimedriver.ErrNoService) {
				t.Errorf("ServiceLogs(%s) = %v, want ErrNoService", name, err)
			}
		}
		for _, reached := range f.Services().Reached(r.ref()) {
			if slices.Contains(refused, reached) {
				t.Errorf("%s reached the service manager", reached)
			}
		}
	}},
	{"logs are the newest entries, at most MaxLogEntries", func(t *testing.T, f Fixture) {
		r := servicesRunning(t, f)
		name := f.Services().AddService(r.ref(), NativeService{Base: "chatty", State: runtimedriver.ServiceRunning})
		var entries []string
		for i := range 150 {
			entries = append(entries, fmt.Sprintf("entry-%03d", i))
		}
		f.Services().SetLogs(r.ref(), name, entries)
		logs := r.logs(t, name)
		shown := 0
		for _, e := range entries {
			if strings.Contains(logs.Text, e) {
				shown++
			}
		}
		lines := strings.Split(strings.TrimRight(logs.Text, "\n"), "\n")
		if shown > runtimedriver.MaxLogEntries || strings.Contains(logs.Text, "entry-000") || !strings.Contains(lines[len(lines)-1], "entry-149") {
			t.Fatalf("ServiceLogs showed %d entries ending %q, want at most %d ending with the newest", shown, lines[len(lines)-1], runtimedriver.MaxLogEntries)
		}
		if logs.Truncated {
			t.Fatal("ServiceLogs reported truncation for entries within the byte bound")
		}
	}},
	{"logs are a valid UTF-8 tail of at most MaxLogBytes, truncation reported", func(t *testing.T, f Fixture) {
		r := servicesRunning(t, f)
		name := f.Services().AddService(r.ref(), NativeService{Base: "verbose", State: runtimedriver.ServiceRunning})
		var entries []string
		for i := range 50 {
			entries = append(entries, fmt.Sprintf("entry-%03d %s\xff\xfe", i, strings.Repeat("é", 200)))
		}
		f.Services().SetLogs(r.ref(), name, entries)
		logs := r.logs(t, name)
		if len(logs.Text) > runtimedriver.MaxLogBytes || !utf8.ValidString(logs.Text) || !logs.Truncated {
			t.Fatalf("ServiceLogs = %d bytes, valid UTF-8 %t, truncated %t; want at most %d valid bytes, truncated",
				len(logs.Text), utf8.ValidString(logs.Text), logs.Truncated, runtimedriver.MaxLogBytes)
		}
		if !strings.Contains(logs.Text, "entry-049") || strings.Contains(logs.Text, "entry-000") {
			t.Fatal("ServiceLogs is not the tail of the log")
		}
	}},
	{"an inventory holding a description that is not printable is an error, never a subset", func(t *testing.T, f Fixture) {
		r := servicesRunning(t, f)
		f.Services().AddService(r.ref(), NativeService{Base: "web", Description: "Web server", State: runtimedriver.ServiceRunning})
		f.Services().AddService(r.ref(), NativeService{Base: "bell", Description: "rings \a the terminal", State: runtimedriver.ServiceStopped})
		if got, err := f.Driver().Services(t.Context(), r.target); err == nil {
			t.Fatalf("Services with a control character in a description = %+v, want an error", got)
		}
	}},
	{"an unreachable service manager is ErrUnavailable", func(t *testing.T, f Fixture) {
		r := servicesRunning(t, f)
		name := f.Services().AddService(r.ref(), NativeService{Base: "web", State: runtimedriver.ServiceStopped})
		f.Services().BreakManager(r.ref())
		wantEveryServiceCall(t, f, r.target, name, runtimedriver.ErrUnavailable)
	}},
}

// servicesUnderTest is a running session with a ready host.
type servicesUnderTest struct {
	f      Fixture
	target runtimedriver.Target
}

// servicesRunning starts a new session, skipping the case when the driver
// offers no services.
func servicesRunning(t *testing.T, f Fixture) servicesUnderTest {
	t.Helper()
	if !f.Driver().Describe().Services {
		t.Skip("the driver offers no services")
	}
	s := f.Session(SessionOptions{})
	created(t, f, s)
	return servicesOn(t, f, s)
}

// servicesOn starts the created session s.
func servicesOn(t *testing.T, f Fixture, s runtimedriver.Session) servicesUnderTest {
	t.Helper()
	o, err := f.Driver().Start(t.Context(), s)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return servicesUnderTest{f: f, target: target(s, o.Incarnation)}
}

func (r servicesUnderTest) ref() runtimedriver.Ref { return r.target.Session.Ref }

func (r servicesUnderTest) services(t *testing.T) []runtimedriver.Service {
	t.Helper()
	got, err := r.f.Driver().Services(t.Context(), r.target)
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	return got
}

func (r servicesUnderTest) state(t *testing.T, name string) runtimedriver.ServiceState {
	t.Helper()
	for _, svc := range r.services(t) {
		if svc.Name == name {
			return svc.State
		}
	}
	t.Fatalf("Services has no %s", name)
	return ""
}

func (r servicesUnderTest) logs(t *testing.T, name string) runtimedriver.Logs {
	t.Helper()
	logs, err := r.f.Driver().ServiceLogs(t.Context(), r.target, name)
	if err != nil {
		t.Fatalf("ServiceLogs: %v", err)
	}
	return logs
}

// wantEveryServiceCall checks that the three services operations at target
// fail with want.
func wantEveryServiceCall(t *testing.T, f Fixture, at runtimedriver.Target, name string, want error) {
	t.Helper()
	if _, err := f.Driver().Services(t.Context(), at); !errors.Is(err, want) {
		t.Errorf("Services = %v, want %v", err, want)
	}
	if err := f.Driver().ServiceAction(t.Context(), at, name, runtimedriver.ActionStart); !errors.Is(err, want) {
		t.Errorf("ServiceAction = %v, want %v", err, want)
	}
	if _, err := f.Driver().ServiceLogs(t.Context(), at, name); !errors.Is(err, want) {
		t.Errorf("ServiceLogs = %v, want %v", err, want)
	}
}
