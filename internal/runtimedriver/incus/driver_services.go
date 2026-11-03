package incus

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// A project service of an Incus session is a systemd user unit named
// p-project-*.service. Every services call runs a fixed systemctl or
// journalctl command as the session user, with no more authority than the
// session's own work.

var projectUnitPattern = regexp.MustCompile(`^p-project-[A-Za-z0-9][A-Za-z0-9_.-]{0,100}\.service$`)

// projectUnit reports whether unit names a project service: never the
// session host's or P's own units, and nothing systemctl reads as an option
// or a path.
func projectUnit(unit string) bool { return projectUnitPattern.MatchString(unit) }

// Services lists the project services of the target's running session whose
// host is ready, sorted by name.
func (d *Driver) Services(ctx context.Context, t runtimedriver.Target) ([]runtimedriver.Service, error) {
	b := d.backend
	s, before, err := b.serviceTarget(ctx, t)
	if err != nil {
		return nil, err
	}
	services, err := b.serviceInventory(ctx, s)
	if err != nil {
		return nil, err
	}
	if err := b.servicesRecheck(ctx, s, before); err != nil {
		return nil, err
	}
	return services, nil
}

// ServiceAction queues a start, stop or restart of one project service and
// returns once the user manager accepted the job. Like every mutation, it
// proves the driver's boundary first.
func (d *Driver) ServiceAction(ctx context.Context, t runtimedriver.Target, name string, a runtimedriver.Action) error {
	if a != runtimedriver.ActionStart && a != runtimedriver.ActionStop && a != runtimedriver.ActionRestart {
		return errors.New("unsupported service action")
	}
	b := d.backend
	s, before, err := b.projectService(ctx, t, name)
	if err != nil {
		return err
	}
	if err := d.Check(ctx); err != nil {
		return err
	}
	if _, err := b.userCommand(ctx, s, "/usr/libexec/p/systemctl", "--user", "--no-pager", "--no-block", string(a), name); err != nil {
		return serviceManagerUnavailable()
	}
	return b.servicesRecheck(ctx, s, before)
}

// ServiceLogs returns the newest journal entries of one project service.
func (d *Driver) ServiceLogs(ctx context.Context, t runtimedriver.Target, name string) (runtimedriver.Logs, error) {
	b := d.backend
	s, before, err := b.projectService(ctx, t, name)
	if err != nil {
		return runtimedriver.Logs{}, err
	}
	raw, err := b.serviceJournal(ctx, s, name)
	if err != nil {
		return runtimedriver.Logs{}, err
	}
	if err := b.servicesRecheck(ctx, s, before); err != nil {
		return runtimedriver.Logs{}, err
	}
	return logTail(raw), nil
}

// serviceTarget checks the target of a services call: a running runtime of
// the session, at the target's incarnation, whose session host is ready.
func (b *Backend) serviceTarget(ctx context.Context, t runtimedriver.Target) (Session, Observation, error) {
	s, err := b.NativeSession(t.Session)
	if err != nil {
		return Session{}, Observation{}, err
	}
	o, err := b.Inspect(ctx, s)
	if err != nil {
		return Session{}, Observation{}, err
	}
	if !o.Exists || o.Status != "Running" {
		return Session{}, Observation{}, withKind(runtimedriver.ErrNotReady, "project services require a ready session")
	}
	if t.At != "" && incarnationOf(o) != t.At {
		return Session{}, Observation{}, mismatch("the session's runtime is another incarnation")
	}
	host, err := b.observeHostOf(ctx, s, o)
	if err != nil {
		return Session{}, Observation{}, kindError{runtimedriver.ErrUnavailable, err}
	}
	if !host.Ready {
		return Session{}, Observation{}, withKind(runtimedriver.ErrNotReady, "project services require a ready session")
	}
	return s, o, nil
}

// projectService checks the target and that its session has the project
// service name. A name that is not a project service, or one the session
// does not have, never reaches the service manager.
func (b *Backend) projectService(ctx context.Context, t runtimedriver.Target, name string) (Session, Observation, error) {
	if !projectUnit(name) {
		return Session{}, Observation{}, runtimedriver.ErrNoService
	}
	s, before, err := b.serviceTarget(ctx, t)
	if err != nil {
		return Session{}, Observation{}, err
	}
	services, err := b.serviceInventory(ctx, s)
	if err != nil {
		return Session{}, Observation{}, err
	}
	if !slices.ContainsFunc(services, func(service runtimedriver.Service) bool { return service.Name == name }) {
		return Session{}, Observation{}, runtimedriver.ErrNoService
	}
	return s, before, nil
}

// userCommand runs a command as the session user, UID and GID 1000, with
// its user manager's runtime directory and bus.
func (b *Backend) userCommand(ctx context.Context, s Session, argv ...string) ([]byte, error) {
	args := []string{"exec", b.name(s), "--mode", "non-interactive", "--user", "1000", "--group", "1000", "--env", "XDG_RUNTIME_DIR=/run/user/1000", "--env", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus", "--"}
	return b.command(ctx, append(args, argv...)...)
}

func serviceManagerUnavailable() error {
	return withKind(runtimedriver.ErrUnavailable, "session user service manager unavailable or command refused")
}

// serviceInventory lists the session's project services: the units the user
// manager has loaded, and the installed units it has not, which it unloads
// only while they are inactive. The list is complete or an error.
func (b *Backend) serviceInventory(ctx context.Context, s Session) ([]runtimedriver.Service, error) {
	raw, err := b.userCommand(ctx, s, "/usr/libexec/p/systemctl", "--user", "list-units", "--all", "--type=service", "--no-pager", "--plain", "--output=json", "p-project-*.service")
	if err != nil {
		return nil, serviceManagerUnavailable()
	}
	data, err := b.userCommand(ctx, s, "/usr/libexec/p/systemctl", "--user", "list-unit-files", "--type=service", "--no-pager", "--output=json", "p-project-*.service")
	if err != nil && !emptyUnitFileInventory(ctx, data, err) {
		return nil, withKind(runtimedriver.ErrUnavailable, "installed project service inventory unavailable or exceeds bounds")
	}
	var files []struct {
		UnitFile string `json:"unit_file"`
	}
	if len(data) > 32000 || json.Unmarshal(data, &files) != nil || len(files) > runtimedriver.MaxServices {
		return nil, withKind(runtimedriver.ErrUnavailable, "installed project service inventory unavailable or exceeds bounds")
	}
	var rows []struct {
		Unit        string `json:"unit"`
		Description string `json:"description"`
		Active      string `json:"active"`
		Sub         string `json:"sub"`
	}
	if len(raw) > 32000 || json.Unmarshal(raw, &rows) != nil || len(rows) > runtimedriver.MaxServices {
		return nil, errors.New("service inventory exceeds bounds or is invalid")
	}
	services := make([]runtimedriver.Service, 0, len(rows))
	for _, r := range rows {
		state, known := serviceState(r.Active)
		if !known || !projectUnit(r.Unit) || len(r.Description) > 256 || len(r.Sub) > 32 || strings.IndexFunc(r.Description+r.Sub, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
			return nil, errors.New("invalid project service inventory")
		}
		services = append(services, runtimedriver.Service{Name: r.Unit, Description: r.Description, State: state, Detail: r.Active + " (" + r.Sub + ")"})
	}
	for _, f := range files {
		unit := path.Base(f.UnitFile)
		if !projectUnit(unit) {
			return nil, errors.New("invalid installed project service")
		}
		if !slices.ContainsFunc(services, func(service runtimedriver.Service) bool { return service.Name == unit }) {
			services = append(services, runtimedriver.Service{Name: unit, Description: "Installed user unit; not loaded", State: runtimedriver.ServiceStopped, Detail: "installed"})
		}
	}
	if len(services) > runtimedriver.MaxServices {
		return nil, errors.New("combined service inventory exceeds bounds")
	}
	slices.SortFunc(services, func(a, b runtimedriver.Service) int { return strings.Compare(a.Name, b.Name) })
	return services, nil
}

// serviceJournal reads the newest journal entries of one project service.
func (b *Backend) serviceJournal(ctx context.Context, s Session, unit string) ([]byte, error) {
	raw, err := b.userCommand(ctx, s, "/usr/libexec/p/journalctl", "--user-unit="+unit, "--no-pager", "--output=short-iso", "-n", "100")
	if err != nil {
		return nil, serviceManagerUnavailable()
	}
	return raw, nil
}

// logTail is the newest part of a log: at most MaxLogBytes of valid UTF-8,
// cut at a character boundary.
func logTail(raw []byte) runtimedriver.Logs {
	text := strings.ToValidUTF8(string(raw), "�")
	if len(text) <= runtimedriver.MaxLogBytes {
		return runtimedriver.Logs{Text: text}
	}
	cut := len(text) - runtimedriver.MaxLogBytes
	for !utf8.RuneStart(text[cut]) {
		cut++
	}
	return runtimedriver.Logs{Text: text[cut:], Truncated: true}
}

// servicesRecheck proves that a services call ran in the runtime its target
// check saw: the same incarnation, still running.
func (b *Backend) servicesRecheck(ctx context.Context, s Session, before Observation) error {
	const changed = "runtime identity changed during service observation/action"
	after, err := b.Inspect(ctx, s)
	if err != nil {
		return errors.Join(errors.New(changed), err)
	}
	if !after.Exists || after.IncusUUID != before.IncusUUID || after.Generation != before.Generation || after.Status != "Running" {
		return mismatch(changed)
	}
	return nil
}

// serviceState is P's state for systemd's active state; known is false for a
// state systemd does not document.
func serviceState(active string) (state runtimedriver.ServiceState, known bool) {
	switch active {
	case "inactive":
		return runtimedriver.ServiceStopped, true
	case "activating":
		return runtimedriver.ServiceStarting, true
	case "active", "reloading", "refreshing":
		return runtimedriver.ServiceRunning, true
	case "deactivating", "maintenance":
		return runtimedriver.ServiceStopping, true
	case "failed":
		return runtimedriver.ServiceFailed, true
	}
	return "", false
}

// systemctl list-unit-files exits 1 when the glob matches no installed units,
// even with a successful manager query and valid empty JSON output. Accept
// only that specific bounded result, preserving other execution failures.
func emptyUnitFileInventory(ctx context.Context, data []byte, err error) bool {
	var exited *exec.ExitError
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		len(data) > 32000 || !errors.As(err, &exited) || exited.ExitCode() != 1 {
		return false
	}
	var rows []json.RawMessage
	return json.Unmarshal(data, &rows) == nil && rows != nil && len(rows) == 0
}
