package runtimeincus

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/lgvo/p.ai/internal/api"
)

func ValidProjectService(unit string) bool { return api.ValidProjectServiceUnit(unit) }

// ServiceObservation is what one services call observed: the inventory for
// list, the log tail for logs, and only the time for an action.
type ServiceObservation struct {
	Services      []api.ProjectService
	Logs          string
	LogsTruncated bool
	ObservedAt    string
}

// Services executes only fixed user-manager commands, under the workspace UID.
// A repository-controlled unit has no more authority than ordinary session work.
func (b *Backend) Services(ctx context.Context, s Session, unit, action string) (ServiceObservation, error) {
	var result ServiceObservation
	if action != "list" && action != "logs" && action != "start" && action != "stop" && action != "restart" {
		return result, errors.New("unsupported service action")
	}
	if action == "list" && unit != "" || action != "list" && !ValidProjectService(unit) {
		return result, errors.New("only session-user p-project-*.service units are supported")
	}
	before, err := b.ObserveHost(ctx, s)
	if err != nil {
		return result, err
	}
	if !before.Exists || before.Status != "Running" || !before.Ready {
		return result, errors.New("project services require a ready session")
	}
	args := []string{"exec", b.name(s), "--mode", "non-interactive", "--user", "1000", "--group", "1000", "--env", "XDG_RUNTIME_DIR=/run/user/1000", "--env", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus", "--"}
	userArgs := append([]string(nil), args...)
	switch action {
	case "list":
		args = append(args, "/usr/libexec/p/systemctl", "--user", "list-units", "--all", "--type=service", "--no-pager", "--plain", "--output=json", "p-project-*.service")
	case "logs":
		args = append(args, "/usr/libexec/p/journalctl", "--user-unit="+unit, "--no-pager", "--output=short-iso", "-n", "100")
	default:
		args = append(args, "/usr/libexec/p/systemctl", "--user", "--no-pager", action, unit)
	}
	raw, err := b.command(ctx, args...)
	if err != nil {
		return result, errors.New("session user service manager unavailable or command refused")
	}
	var files []struct {
		UnitFile string `json:"unit_file"`
	}
	if action == "list" {
		fileArgs := append([]string(nil), userArgs...)
		fileArgs = append(fileArgs, "/usr/libexec/p/systemctl", "--user", "list-unit-files", "--type=service", "--no-pager", "--output=json", "p-project-*.service")
		data, e := b.command(ctx, fileArgs...)
		if e != nil && !emptyUnitFileInventory(ctx, data, e) || len(data) > 32000 || json.Unmarshal(data, &files) != nil || len(files) > 64 {
			return result, errors.New("installed project service inventory unavailable or exceeds bounds")
		}
	}
	after, err := b.Inspect(ctx, s)
	if err != nil || !after.Exists || after.IncusUUID != before.IncusUUID || after.Generation != before.Generation || after.Status != "Running" {
		return result, errors.New("runtime identity changed during service observation/action")
	}
	result = ServiceObservation{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if action == "logs" {
		// Keep the RPC bounded even for long log messages. This is a tail.
		if len(raw) > 16000 {
			result.LogsTruncated = true
			raw = raw[len(raw)-16000:]
		}
		result.Logs = strings.ToValidUTF8(string(raw), "�")
		for {
			encoded, e := json.Marshal(api.SessionServiceLogsResult{UUID: s.SessionUUID, Name: unit, Logs: result.Logs, LogsTruncated: result.LogsTruncated, ObservedAt: result.ObservedAt})
			if e != nil {
				return ServiceObservation{}, e
			}
			if len(encoded) <= 48000 {
				break
			}
			result.LogsTruncated = true
			result.Logs = strings.ToValidUTF8(result.Logs[len(result.Logs)/4:], "�")
		}
	} else if action == "list" {
		var rows []struct {
			Unit        string `json:"unit"`
			Description string `json:"description"`
			Active      string `json:"active"`
			Sub         string `json:"sub"`
		}
		if len(raw) > 32000 || json.Unmarshal(raw, &rows) != nil || len(rows) > 64 {
			return ServiceObservation{}, errors.New("service inventory exceeds bounds or is invalid")
		}
		result.Services = make([]api.ProjectService, 0, len(rows))
		for _, r := range rows {
			state, known := serviceState(r.Active)
			if !known || !ValidProjectService(r.Unit) || len(r.Description) > 256 || len(r.Sub) > 32 || strings.IndexFunc(r.Description+r.Sub, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
				return ServiceObservation{}, errors.New("invalid project service inventory")
			}
			result.Services = append(result.Services, api.ProjectService{Name: r.Unit, Description: r.Description, State: state, Detail: r.Active + " (" + r.Sub + ")"})
		}
		for _, f := range files {
			unit := path.Base(f.UnitFile)
			if !ValidProjectService(unit) {
				return ServiceObservation{}, errors.New("invalid installed project service")
			}
			found := false
			for _, s := range result.Services {
				if s.Name == unit {
					found = true
					break
				}
			}
			if !found {
				// The manager unloads only inactive units.
				result.Services = append(result.Services, api.ProjectService{Name: unit, Description: "Installed user unit; not loaded", State: api.ServiceStopped, Detail: "installed"})
			}
		}
		if len(result.Services) > 64 {
			return ServiceObservation{}, errors.New("combined service inventory exceeds bounds")
		}
		sort.Slice(result.Services, func(i, j int) bool { return result.Services[i].Name < result.Services[j].Name })
	}
	return result, nil
}

// serviceState is P's state for systemd's active state; known is false for a
// state systemd does not document.
func serviceState(active string) (state api.ServiceState, known bool) {
	switch active {
	case "inactive":
		return api.ServiceStopped, true
	case "activating":
		return api.ServiceStarting, true
	case "active", "reloading", "refreshing":
		return api.ServiceRunning, true
	case "deactivating", "maintenance":
		return api.ServiceStopping, true
	case "failed":
		return api.ServiceFailed, true
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
