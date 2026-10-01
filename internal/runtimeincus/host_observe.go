package runtimeincus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// HostObservation is based on current systemd state when the instance runs.
// Incus Running alone never means the persistent host is attachable.
type HostObservation struct {
	Observation
	Unit                string
	Ready               bool
	Diagnostic          string
	DiagnosticAvailable bool
}

var machineIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (b *Backend) ObserveHost(ctx context.Context, s Session) (HostObservation, error) {
	o, err := b.Inspect(ctx, s)
	if err != nil {
		return HostObservation{}, err
	}
	h := HostObservation{Observation: o, Unit: "unavailable"}
	if !o.Exists {
		return h, nil
	}
	if o.Status == "Running" {
		raw, err := b.command(ctx, "exec", b.name(s), "--mode", "non-interactive", "--", "/usr/libexec/p/systemctl", "show", "p-interactive.service", "--property=ActiveState,SubState,Result", "--no-pager")
		if err != nil {
			return h, fmt.Errorf("inspect systemd host: %w", err)
		}
		fields := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if !ok || (key != "ActiveState" && key != "SubState" && key != "Result") || fields[key] != "" {
				return h, errors.New("invalid systemd host observation")
			}
			fields[key] = value
		}
		if fields["ActiveState"] == "" || fields["SubState"] == "" || fields["Result"] == "" {
			return h, errors.New("incomplete systemd host observation")
		}
		h.Unit = fields["ActiveState"] + "/" + fields["SubState"]
		h.Ready = fields["ActiveState"] == "active" && fields["SubState"] == "running" && fields["Result"] == "success"
		if !h.Ready {
			log, e := b.command(ctx, "exec", b.name(s), "--mode", "non-interactive", "--", "/usr/libexec/p/journalctl", "-u", "p-interactive.service", "-n", "20", "--no-pager", "--output=short-iso")
			if e == nil {
				h.Diagnostic = boundedDiagnostic(log)
				h.DiagnosticAvailable = true
			}
		}
		return h, nil
	}
	if o.Status != "Stopped" {
		return h, nil
	}
	// A stopped systemd unit cannot be queried. Read only its fixed persistent
	// journal via the confined file API. Absent/oversized/corrupt journal is an
	// unavailable diagnostic, never a reason to start the guest.
	api := &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: b.name(s)}
	mid, ok, e := api.get(ctx, "/etc/machine-id")
	if e != nil || !ok || mid.typ != "file" {
		return h, nil
	}
	id := strings.TrimSpace(string(mid.data))
	if !machineIDPattern.MatchString(id) {
		return h, nil
	}
	dir, e := os.MkdirTemp("", "p-journal-")
	if e != nil {
		return h, nil
	}
	defer os.RemoveAll(dir)
	args := []string{}
	for _, name := range []string{"system", "user-1000"} {
		journal, ok, e := api.getBounded(ctx, "/var/log/journal/"+id+"/"+name+".journal", 16<<20)
		if e != nil || !ok || journal.typ != "file" || len(journal.data) == 0 {
			continue
		}
		path := filepath.Join(dir, name+".journal")
		if e = os.WriteFile(path, journal.data, 0600); e == nil {
			args = append(args, "--file", path)
		}
	}
	if len(args) == 0 {
		return h, nil
	}
	args = append(args, "-u", "p-interactive.service", "-n", "20", "--no-pager", "--output=short-iso")
	journalctl, e := b.trustedJournalctl()
	if e != nil {
		return h, nil
	}
	cmd := exec.CommandContext(ctx, journalctl, args...)
	var out limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &limitedBuffer{}
	if e = cmd.Run(); e != nil || out.exceeded {
		return h, nil
	}
	h.Diagnostic = boundedDiagnostic(out.Bytes())
	h.DiagnosticAvailable = true
	return h, nil
}

func (b *Backend) trustedJournalctl() (string, error) {
	path := b.config.JournalctlBinary
	if path == "" {
		var err error
		path, err = exec.LookPath("journalctl")
		if err != nil {
			return "", err
		}
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("journalctl path must be absolute and clean")
	}
	if err := validateAncestorsWithOptions(path, 0, true, true); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if resolved != path {
		if !pathWithinCeiling(resolved, "/nix/store") {
			return "", errors.New("journalctl link leaves Nix store")
		}
		if err := validateAncestorsWithOptions(resolved, 0, true, false); err != nil {
			return "", err
		}
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || !ownedByRootOrDaemon(info) {
		return "", errors.New("journalctl binary untrusted")
	}
	return path, nil
}

func boundedDiagnostic(data []byte) string {
	text := strings.TrimSpace(string(data))
	if len(text) > 2048 {
		text = text[len(text)-2048:]
	}
	return text
}
