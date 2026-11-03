package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimedriver/memory"
)

// servicesFixture is an established session whose project policy is current
// and whose runtime runs with its session host ready, on a driver that offers
// project services.
func servicesFixture(t *testing.T) *driverFixture {
	t.Helper()
	f := newDriverFixture(t, runtimedriver.Capabilities{Services: true})
	f.createRuntime()
	if _, err := memory.NewDriver(f.world).Start(f.ctx, f.spec()); err != nil {
		t.Fatal(err)
	}
	f.establish()
	return f
}

func servicesLifecycle(t *testing.T) (*lifecycle, control.Session) {
	f := servicesFixture(t)
	return f.l, f.session
}

// servicesCall makes one services call: list, logs, or an action.
func servicesCall(ctx context.Context, l *lifecycle, session, name, action string) error {
	var err error
	switch action {
	case "list":
		_, err = l.SessionServices(ctx, session)
	case "logs":
		_, err = l.SessionServiceLogs(ctx, session, name)
	default:
		_, err = l.SessionServiceAction(ctx, session, name, action)
	}
	return err
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
		_ = servicesCall(request, l, session, unit, action)
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
			if err := servicesCall(ctx, l, session.UUID, "", "list"); errors.Is(err, control.ErrConflict) {
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
				if err := servicesCall(ctx, l, session.UUID, "", "list"); errors.Is(err, control.ErrConflict) {
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
				if err != nil {
					t.Fatalf("Stop failed after a services %s: %v", c.action, err)
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
				if err != nil {
					t.Fatalf("Stop failed after a cancelled services %s: %v", c.action, err)
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
	if err := servicesCall(context.Background(), l, session.UUID, "", "list"); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("services read admitted while a service action held the session: %v", err)
	}
}

// serviceCalls counts the services calls that reach the driver and keeps the
// target of the last one.
type serviceCalls struct {
	runtimedriver.Driver
	calls  int
	target runtimedriver.Target
}

func (d *serviceCalls) Services(ctx context.Context, t runtimedriver.Target) ([]runtimedriver.Service, error) {
	d.calls, d.target = d.calls+1, t
	return d.Driver.Services(ctx, t)
}

func (d *serviceCalls) ServiceAction(ctx context.Context, t runtimedriver.Target, name string, a runtimedriver.Action) error {
	d.calls, d.target = d.calls+1, t
	return d.Driver.ServiceAction(ctx, t, name, a)
}

func (d *serviceCalls) ServiceLogs(ctx context.Context, t runtimedriver.Target, name string) (runtimedriver.Logs, error) {
	d.calls, d.target = d.calls+1, t
	return d.Driver.ServiceLogs(ctx, t, name)
}

func TestServicesAreTheRuntimesAtItsCurrentIncarnation(t *testing.T) {
	f := servicesFixture(t)
	f.world.AddService(f.ref(), "p-project-db.service", "Database", runtimedriver.ServiceRunning, false)
	f.world.AddService(f.ref(), "p-project-api.service", "API", runtimedriver.ServiceFailed, false)
	calls := &serviceCalls{Driver: memory.NewDriver(f.world)}
	f.l.driver = calls
	got, err := f.l.SessionServices(f.ctx, f.session.UUID)
	if err != nil {
		t.Fatal(err)
	}
	want := []api.ProjectService{
		{Name: "p-project-api.service", Description: "API", State: api.ServiceFailed, Detail: "failed"},
		{Name: "p-project-db.service", Description: "Database", State: api.ServiceRunning, Detail: "running"},
	}
	if got.UUID != f.session.UUID || got.ObservedAt == "" || len(got.Services) != len(want) || got.Services[0] != want[0] || got.Services[1] != want[1] {
		t.Fatalf("inventory %+v, want %+v", got, want)
	}
	if at := f.observe().Incarnation; calls.calls != 1 || calls.target.At != at || calls.target.Session.Ref != f.ref() {
		t.Fatalf("%d calls at %+v, want one at incarnation %s", calls.calls, calls.target, at)
	}
	empty := servicesFixture(t)
	if got, err := empty.l.SessionServices(empty.ctx, empty.session.UUID); err != nil || got.Services == nil || len(got.Services) != 0 {
		t.Fatalf("a session without services: %+v %v, want an empty list", got, err)
	}
}

func TestServiceActionReturnsOnceTheRuntimeQueuedIt(t *testing.T) {
	f := servicesFixture(t)
	name := f.world.AddService(f.ref(), "p-project-db.service", "Database", runtimedriver.ServiceStopped, false)
	state := func() api.ServiceState {
		t.Helper()
		got, err := f.l.SessionServices(f.ctx, f.session.UUID)
		if err != nil || len(got.Services) != 1 {
			t.Fatalf("inventory %+v %v", got, err)
		}
		return got.Services[0].State
	}
	result, err := f.l.SessionServiceAction(f.ctx, f.session.UUID, name, "start")
	if err != nil || result.Name != name || result.ObservedAt == "" {
		t.Fatalf("start: %+v %v", result, err)
	}
	if s := state(); s != api.ServiceStarting {
		t.Fatalf("after start: %s, want starting", s)
	}
	f.world.Progress(f.ref(), name)
	if s := state(); s != api.ServiceRunning {
		t.Fatalf("after the job: %s, want running", s)
	}
}

func TestServiceNamesTheSessionDoesNotHaveNeverReachItsServices(t *testing.T) {
	f := servicesFixture(t)
	f.world.AddService(f.ref(), "p-project-db.service", "Database", runtimedriver.ServiceRunning, false)
	for _, name := range []string{"p-interactive.service", "p-session.target", "sshd.service", "../p-project-db.service"} {
		_, err := f.l.SessionServiceAction(f.ctx, f.session.UUID, name, "stop")
		requireRefusal(t, err, api.KindNotFound, api.ReasonService, api.TargetSession(f.session.UUID))
		_, err = f.l.SessionServiceLogs(f.ctx, f.session.UUID, name)
		requireRefusal(t, err, api.KindNotFound, api.ReasonService, api.TargetSession(f.session.UUID))
	}
	calls := &serviceCalls{Driver: memory.NewDriver(f.world)}
	f.l.driver = calls
	for _, name := range []string{"", "p-project-db.service\nsshd.service", strings.Repeat("a", runtimedriver.MaxServiceName+1)} {
		_, err := f.l.SessionServiceAction(f.ctx, f.session.UUID, name, "stop")
		var refusal *control.Refusal
		if !errors.As(err, &refusal) || refusal.Kind != api.KindInvalid || refusal.Field != "name" {
			t.Fatalf("action on %q: %v, want invalid name", name, err)
		}
	}
	if reached := f.world.Reached(f.ref()); len(reached) != 0 || calls.calls != 0 {
		t.Fatalf("the service manager was reached for %v, the driver %d times", reached, calls.calls)
	}
}

// TestServicesFailuresAnswerTheirKindOnce goes through the control API: a
// service the session does not have is not found, a service manager that
// cannot be reached failed services, and a driver without project services
// has them not configured, each after one call that is never retried.
func TestServicesFailuresAnswerTheirKindOnce(t *testing.T) {
	for _, c := range []struct {
		name     string
		services bool
		arrange  func(*driverFixture)
		service  string
		want     func(*driverFixture) *api.Error
	}{
		{"no such service", true, func(*driverFixture) {}, "p-project-gone.service", func(f *driverFixture) *api.Error {
			e := api.NewError(api.KindNotFound, api.ReasonService)
			e.Data.Target = api.TargetSession(f.session.UUID)
			return e
		}},
		{"unreachable service manager", true, func(f *driverFixture) { f.world.BreakServiceManager(f.ref()) }, "p-project-db.service", func(*driverFixture) *api.Error {
			return api.NewError(api.KindFailed, api.ReasonServices)
		}},
		{"driver without services", false, func(*driverFixture) {}, "p-project-db.service", func(*driverFixture) *api.Error {
			return api.NewError(api.KindNotConfigured, api.ReasonServices)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newDriverFixture(t, runtimedriver.Capabilities{Services: c.services})
			f.createRuntime()
			if _, err := memory.NewDriver(f.world).Start(f.ctx, f.spec()); err != nil {
				t.Fatal(err)
			}
			f.establish()
			f.world.AddService(f.ref(), "p-project-db.service", "Database", runtimedriver.ServiceRunning, false)
			c.arrange(f)
			calls := &serviceCalls{Driver: memory.NewDriver(f.world)}
			f.l.driver = calls
			h := control.NewHandler(control.HandlerConfig{Store: f.l.store, Lifecycle: f.l})
			params, err := json.Marshal(api.SessionServiceActionRequest{UUID: f.session.UUID, Name: c.service, Action: "restart"})
			if err != nil {
				t.Fatal(err)
			}
			if _, answer := h(f.ctx, "session.service.action", params); answer == nil || answer.Error() != c.want(f).Error() {
				t.Fatalf("answer %v, want %v", answer, c.want(f))
			}
			if calls.calls != 1 {
				t.Fatalf("the driver was called %d times, want once", calls.calls)
			}
		})
	}
}

// stoppedIncomplete is a driver that sees the session's runtime stopped and
// missing part of its spec.
type stoppedIncomplete struct{ *memory.Driver }

func (d stoppedIncomplete) Inspect(ctx context.Context, s runtimedriver.Session) (runtimedriver.Observation, error) {
	o, err := d.Driver.Inspect(ctx, s)
	o.State, o.Complete, o.Host = runtimedriver.StateStopped, false, runtimedriver.HostState{}
	return o, err
}

// TestServicesOfASessionThatIsNotReadyNeverReachTheDriver: core refuses a
// session whose runtime is absent, stopped, or running without a ready host
// before any services operation, answering that the session is not running;
// a host it cannot read, or a runtime missing part of its spec, is a runtime
// failure.
func TestServicesOfASessionThatIsNotReadyNeverReachTheDriver(t *testing.T) {
	for _, c := range []struct {
		name    string
		arrange func(*driverFixture) runtimedriver.Driver
		failed  bool
	}{
		{"absent runtime", func(f *driverFixture) runtimedriver.Driver { return memory.NewDriver(memory.NewWorld(memory.Config{})) }, false},
		{"stopped runtime", func(f *driverFixture) runtimedriver.Driver {
			d := memory.NewDriver(f.world)
			if _, err := d.Stop(f.ctx, f.spec()); err != nil {
				f.t.Fatal(err)
			}
			return d
		}, false},
		{"host not ready", func(f *driverFixture) runtimedriver.Driver {
			f.world.UnreadyHost(f.ref(), "the host restarts")
			return memory.NewDriver(f.world)
		}, false},
		{"host that cannot be read", func(f *driverFixture) runtimedriver.Driver { return unknownHost{memory.NewDriver(f.world)} }, true},
		{"stopped runtime missing part of its spec", func(f *driverFixture) runtimedriver.Driver { return stoppedIncomplete{memory.NewDriver(f.world)} }, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := servicesFixture(t)
			f.world.AddService(f.ref(), "p-project-db.service", "Database", runtimedriver.ServiceRunning, false)
			calls := &serviceCalls{Driver: c.arrange(f)}
			f.l.driver = calls
			want := api.NewError(api.KindConflict, api.ReasonSessionNotRunning)
			want.Data.Target = api.TargetSession(f.session.UUID)
			if c.failed {
				want = api.NewError(api.KindFailed, api.ReasonRuntime)
			}
			h := control.NewHandler(control.HandlerConfig{Store: f.l.store, Lifecycle: f.l})
			for method, params := range map[string]any{
				"session.services":       api.SessionServicesRequest{UUID: f.session.UUID},
				"session.service.action": api.SessionServiceActionRequest{UUID: f.session.UUID, Name: "p-project-db.service", Action: "restart"},
				"session.service.logs":   api.SessionServiceLogsRequest{UUID: f.session.UUID, Name: "p-project-db.service"},
			} {
				raw, err := json.Marshal(params)
				if err != nil {
					t.Fatal(err)
				}
				if _, answer := h(f.ctx, method, raw); answer == nil || answer.Error() != want.Error() {
					t.Fatalf("%s: answer %v, want %v", method, answer, want)
				}
			}
			if calls.calls != 0 || len(f.world.Reached(f.ref())) != 0 {
				t.Fatalf("services reached the driver %d times, the service manager for %v", calls.calls, f.world.Reached(f.ref()))
			}
		})
	}
}

func TestServiceLogsResultStaysWithinItsBound(t *testing.T) {
	f := servicesFixture(t)
	name := f.world.AddService(f.ref(), "p-project-db.service", "Database", runtimedriver.ServiceRunning, false)
	// Control characters take six bytes each once encoded.
	f.world.SetLogs(f.ref(), name, []string{strings.Repeat("\x01", 15000)})
	got, err := f.l.SessionServiceLogs(f.ctx, f.session.UUID, name)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxServicesResult || !got.LogsTruncated || got.Logs == "" || !strings.HasSuffix(got.Logs, "\x01\n") {
		t.Fatalf("logs of %d encoded bytes, truncated %t", len(encoded), got.LogsTruncated)
	}
	f.world.SetLogs(f.ref(), name, []string{"ready"})
	if got, err = f.l.SessionServiceLogs(f.ctx, f.session.UUID, name); err != nil || got.Logs != "ready\n" || got.LogsTruncated {
		t.Fatalf("short logs: %+v %v", got, err)
	}
}
