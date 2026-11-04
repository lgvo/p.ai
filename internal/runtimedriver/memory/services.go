package memory

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// serviceManager runs a runtime's project services.
type serviceManager struct {
	services map[string]*service // by name
	reached  []string            // every name it was asked to act on or read the log of
	broken   bool                // it cannot be reached
}

type service struct {
	description  string
	state        runtimedriver.ServiceState
	failsToStart bool
	logs         []string // oldest first
}

// Services lists the project services of a running session whose host is
// ready, sorted by name.
func (d *Driver) Services(_ context.Context, t runtimedriver.Target) ([]runtimedriver.Service, error) {
	var list []runtimedriver.Service
	err := d.onServices(t, func(m *serviceManager) error {
		if len(m.services) > runtimedriver.MaxServices {
			return fmt.Errorf("the session has %d services, more than %d", len(m.services), runtimedriver.MaxServices)
		}
		list = []runtimedriver.Service{}
		for name, svc := range m.services {
			if !printable(name, runtimedriver.MaxServiceName) || name == "" || !printable(svc.description, maxServiceDescription) {
				list = nil
				return fmt.Errorf("the service inventory holds a name or description that is not printable within its bound: %q", name)
			}
			list = append(list, runtimedriver.Service{Name: name, Description: svc.description, State: svc.state, Detail: string(svc.state)})
		}
		slices.SortFunc(list, func(a, b runtimedriver.Service) int { return strings.Compare(a.Name, b.Name) })
		return nil
	})
	return list, err
}

// maxServiceDescription bounds a service's description in the inventory,
// as the Incus driver bounds a unit's.
const maxServiceDescription = 256

// printable reports whether text is at most limit bytes of printable
// characters.
func printable(text string, limit int) bool {
	return len(text) <= limit && utf8.ValidString(text) && strings.IndexFunc(text, func(r rune) bool { return !unicode.IsPrint(r) }) < 0
}

// ServiceAction queues an action and returns: the service shows starting or
// stopping until Progress finishes the job. Starting a running service or
// stopping a stopped one does nothing; a failed service stays failed until
// its next start.
func (d *Driver) ServiceAction(_ context.Context, t runtimedriver.Target, name string, a runtimedriver.Action) error {
	if a != runtimedriver.ActionStart && a != runtimedriver.ActionStop && a != runtimedriver.ActionRestart {
		return fmt.Errorf("unknown service action %q", a)
	}
	return d.onServices(t, func(m *serviceManager) error {
		svc, err := m.service(name)
		if err != nil {
			return err
		}
		switch {
		case a == runtimedriver.ActionRestart:
			svc.state = runtimedriver.ServiceStarting
		case a == runtimedriver.ActionStart && svc.state != runtimedriver.ServiceRunning && svc.state != runtimedriver.ServiceStarting:
			svc.state = runtimedriver.ServiceStarting
		case a == runtimedriver.ActionStop && (svc.state == runtimedriver.ServiceRunning || svc.state == runtimedriver.ServiceStarting):
			svc.state = runtimedriver.ServiceStopping
		}
		return nil
	})
}

// ServiceLogs returns the newest entries of a service's log, at most
// MaxLogEntries and MaxLogBytes, as valid UTF-8, one entry per line.
func (d *Driver) ServiceLogs(_ context.Context, t runtimedriver.Target, name string) (runtimedriver.Logs, error) {
	var logs runtimedriver.Logs
	err := d.onServices(t, func(m *serviceManager) error {
		svc, err := m.service(name)
		if err == nil {
			logs = tail(svc.logs)
		}
		return err
	})
	return logs, err
}

func tail(entries []string) runtimedriver.Logs {
	var logs runtimedriver.Logs
	if len(entries) > runtimedriver.MaxLogEntries {
		entries = entries[len(entries)-runtimedriver.MaxLogEntries:]
	}
	text := ""
	for _, e := range entries {
		text += strings.ToValidUTF8(e, "\uFFFD") + "\n"
	}
	if len(text) > runtimedriver.MaxLogBytes {
		cut := len(text) - runtimedriver.MaxLogBytes
		for !utf8.RuneStart(text[cut]) {
			cut++
		}
		text, logs.Truncated = text[cut:], true
	}
	logs.Text = text
	return logs
}

// onServices runs use on the service manager of the ready target, under
// the World's lock.
func (d *Driver) onServices(t runtimedriver.Target, use func(*serviceManager) error) error {
	if err := d.alive(); err != nil {
		return err
	}
	if !d.w.config.Capabilities.Services {
		return runtimedriver.ErrUnsupported
	}
	o, err := d.w.readyTarget(t)
	if err != nil {
		return err
	}
	d.w.mu.Lock()
	defer d.w.mu.Unlock()
	if o.services.broken {
		return fmt.Errorf("the session's service manager cannot be reached: %w", runtimedriver.ErrUnavailable)
	}
	return use(&o.services)
}

// service is the named project service; a name the session does not have
// never reaches the manager.
func (m *serviceManager) service(name string) (*service, error) {
	svc := m.services[name]
	if svc == nil {
		return nil, fmt.Errorf("%q: %w", name, runtimedriver.ErrNoService)
	}
	m.reached = append(m.reached, name)
	return svc, nil
}

// AddService adds a project service to the session of r and returns its
// name, which is the name it is defined with.
func (w *World) AddService(r runtimedriver.Ref, name, description string, state runtimedriver.ServiceState, failsToStart bool) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	m := &w.mustRuntime(r).services
	if m.services == nil {
		m.services = map[string]*service{}
	}
	m.services[name] = &service{description: description, state: state, failsToStart: failsToStart}
	return name
}

// Progress finishes the queued job of the named service of r: starting
// becomes running, or failed with a log entry saying so; stopping becomes
// stopped.
func (w *World) Progress(r runtimedriver.Ref, name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	svc := w.mustService(r, name)
	switch {
	case svc.state == runtimedriver.ServiceStarting && svc.failsToStart:
		svc.state = runtimedriver.ServiceFailed
		svc.logs = append(svc.logs, name+" failed to start")
	case svc.state == runtimedriver.ServiceStarting:
		svc.state = runtimedriver.ServiceRunning
	case svc.state == runtimedriver.ServiceStopping:
		svc.state = runtimedriver.ServiceStopped
	}
}

// SetLogs replaces the log of the named service of r with entries, oldest
// first.
func (w *World) SetLogs(r runtimedriver.Ref, name string, entries []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.mustService(r, name).logs = slices.Clone(entries)
}

// Reached lists every name the service manager of r was asked to act on or
// to read the log of.
func (w *World) Reached(r runtimedriver.Ref) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.mustRuntime(r).services.reached)
}

// BreakServiceManager makes the service manager of r unreachable.
func (w *World) BreakServiceManager(r runtimedriver.Ref) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.mustRuntime(r).services.broken = true
}

// mustService is the named service of r. Called with w.mu held.
func (w *World) mustService(r runtimedriver.Ref, name string) *service {
	svc := w.mustRuntime(r).services.services[name]
	if svc == nil {
		panic(fmt.Sprintf("memory: no service %s in %s", name, r.ID))
	}
	return svc
}
