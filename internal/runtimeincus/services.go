package runtimeincus

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/lgvo/p.ai/internal/control"
)

func ValidProjectService(unit string) bool { return control.ValidProjectServiceUnit(unit) }

// Services executes only fixed user-manager commands, under the workspace UID.
// A repository-controlled unit has no more authority than ordinary session work.
func (b *Backend) Services(ctx context.Context, s Session, unit, action string) (control.ServiceResult, error) {
	var result control.ServiceResult
	if action != "list" && action != "journal" && action != "start" && action != "stop" && action != "restart" {
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
	case "journal":
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
		if e != nil || len(data) > 32000 || json.Unmarshal(data, &files) != nil || len(files) > 64 {
			return result, errors.New("installed project service inventory unavailable or exceeds bounds")
		}
	}
	after, err := b.Inspect(ctx, s)
	if err != nil || !after.Exists || after.IncusUUID != before.IncusUUID || after.Generation != before.Generation || after.Status != "Running" {
		return result, errors.New("runtime identity changed during service observation/action")
	}
	result = control.ServiceResult{V: 1, UUID: s.SessionUUID, Unit: unit, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if action == "journal" {
		// Keep the RPC bounded even for long journal messages. This is a tail.
		if len(raw) > 16000 {
			result.JournalTruncated = true
			raw = raw[len(raw)-16000:]
		}
		result.Journal = strings.ToValidUTF8(string(raw), "�")
		for {
			encoded, e := json.Marshal(result)
			if e != nil {
				return control.ServiceResult{}, e
			}
			if len(encoded) <= 48000 {
				break
			}
			result.JournalTruncated = true
			result.Journal = strings.ToValidUTF8(result.Journal[len(result.Journal)/4:], "�")
		}
	} else if action == "list" {
		var rows []struct {
			Unit        string `json:"unit"`
			Description string `json:"description"`
			Active      string `json:"active"`
			Sub         string `json:"sub"`
		}
		if len(raw) > 32000 || json.Unmarshal(raw, &rows) != nil || len(rows) > 64 {
			return control.ServiceResult{}, errors.New("service inventory exceeds bounds or is invalid")
		}
		result.Services = make([]control.ProjectService, 0, len(rows))
		for _, r := range rows {
			if !ValidProjectService(r.Unit) || len(r.Description) > 256 || len(r.Active) > 32 || len(r.Sub) > 32 || strings.IndexFunc(r.Description+r.Active+r.Sub, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
				return control.ServiceResult{}, errors.New("invalid project service inventory")
			}
			result.Services = append(result.Services, control.ProjectService{Unit: r.Unit, Description: r.Description, ActiveState: r.Active, SubState: r.Sub})
		}
		for _, f := range files {
			unit := path.Base(f.UnitFile)
			if !ValidProjectService(unit) {
				return control.ServiceResult{}, errors.New("invalid installed project service")
			}
			found := false
			for _, s := range result.Services {
				if s.Unit == unit {
					found = true
					break
				}
			}
			if !found {
				result.Services = append(result.Services, control.ProjectService{Unit: unit, Description: "Installed user unit; not loaded", ActiveState: "unknown", SubState: "not-loaded"})
			}
		}
		if len(result.Services) > 64 {
			return control.ServiceResult{}, errors.New("combined service inventory exceeds bounds")
		}
		sort.Slice(result.Services, func(i, j int) bool { return result.Services[i].Unit < result.Services[j].Unit })
	}
	return result, nil
}
