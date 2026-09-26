package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type FileLogConfig struct {
	Path     string `json:"path"`
	MaxBytes int64  `json:"max_bytes"`
}

// Event is a reduced core event. Callers cannot pass arbitrary payloads or
// credentials through this handler.
type Event struct {
	Schema     string            `json:"schema"`
	ID         string            `json:"id"`
	Kind       string            `json:"kind"`
	OccurredAt string            `json:"occurred_at"`
	Instance   string            `json:"instance"`
	Project    string            `json:"project,omitempty"`
	Session    string            `json:"session,omitempty"`
	Branch     string            `json:"branch,omitempty"`
	Fields     map[string]string `json:"fields,omitempty"`
}

var atomRE = regexp.MustCompile(`^[a-zA-Z0-9._:/-]{1,256}$`)

var eventField = map[string]string{
	"session.condition_changed":  "condition",
	"session.attachment_changed": "count",
	"session.unattended_changed": "unattended_condition",
	"session.policy_changed":     "policy_condition",
	"operation.progress":         "phase",
}

var allowedValues = map[string]map[string]bool{
	"condition":            {"creating": true, "starting": true, "ready": true, "stopped": true, "missing": true, "unreachable": true, "discarding": true, "deleting": true},
	"unattended_condition": {"none": true, "running": true, "attention": true, "idle": true, "failed": true, "unknown": true},
	"policy_condition":     {"current": true, "outdated": true, "invalid": true},
	"phase":                {"accepted": true, "running": true, "blocked": true, "completed": true, "failed": true, "cancelled": true},
}

func (e Event) Validate() error {
	if e.Schema != "p.event/v1" {
		return errors.New("unsupported event schema")
	}
	field, known := eventField[e.Kind]
	if !atomRE.MatchString(e.ID) || !atomRE.MatchString(e.Instance) || !known {
		return errors.New("invalid event identity or kind")
	}
	if len(e.OccurredAt) > 64 {
		return errors.New("invalid event time")
	}
	if _, err := time.Parse(time.RFC3339Nano, e.OccurredAt); err != nil {
		return errors.New("invalid event time")
	}
	for _, value := range []string{e.Project, e.Session, e.Branch} {
		if len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("invalid event context")
		}
	}
	if len(e.Fields) != 1 {
		return errors.New("event requires exactly one reduced field")
	}
	value, ok := e.Fields[field]
	if !ok {
		return fmt.Errorf("event %s requires %s", e.Kind, field)
	}
	if field == "count" {
		n, err := strconv.ParseUint(value, 10, 32)
		if err != nil || n > 1000000 || strconv.FormatUint(n, 10) != value {
			return errors.New("invalid attachment count")
		}
	} else if !allowedValues[field][value] {
		return fmt.Errorf("invalid %s value", field)
	}
	return nil
}

var logMu sync.Mutex

// Emit dispatches a reduced event through a selected declarative package and
// the grant-checked core file broker. It is intentionally synchronous.
func Emit(active []Active, event Event) error {
	return EmitContext(context.Background(), active, event)
}

func EmitContext(ctx context.Context, active []Active, event Event) error {
	_, err := DispatchEvent(ctx, active, event)
	return err
}

func DispatchEvent(ctx context.Context, active []Active, event Event) (CommandResult, error) {
	var result CommandResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := event.Validate(); err != nil {
		return result, err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return result, err
	}
	if len(data) > 4096 {
		return result, errors.New("event exceeds 4 KiB")
	}
	data = append(data, '\n')
	for _, selected := range active {
		m := selected.Package.Manifest
		if m.Capability != "event-handler" {
			continue
		}
		if m.Runtime.Kind == "wasi-command" {
			return RunEvent(ctx, selected, event)
		}
		current, err := Conformance(selected.Package.Path)
		if err != nil {
			return result, fmt.Errorf("selected package digest changed or invalid: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if current.SHA256 != selected.Package.SHA256 {
			return result, errors.New("selected package digest changed")
		}
		if m.Runtime.Kind != "declarative" || m.Runtime.Entry != "event.file.append" || !slicesContains(selected.Grants, "event.file.append") {
			return result, errors.New("event handler lacks supported broker grant")
		}
		var config FileLogConfig
		if err := strictJSON(selected.Config, &config); err != nil {
			return result, err
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := appendFile(config, data); err != nil {
			return result, err
		}
		return CommandResult{Schema: WASIResultSchema, Status: "appended"}, nil
	}
	return result, errors.New("no event handler active")
}

func slicesContains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func appendFile(config FileLogConfig, line []byte) error {
	logMu.Lock()
	defer logMu.Unlock()
	if int64(len(line)) > config.MaxBytes {
		return errors.New("event exceeds file limit")
	}
	dirFD, name, err := openPrivateParent(config.Path)
	if err != nil {
		return err
	}
	defer syscall.Close(dirFD)
	f, err := noFollowOpenAt(dirFD, name, syscall.O_WRONLY|syscall.O_APPEND|syscall.O_CREAT, 0600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		f.Close()
		return errors.New("log file must be private, owned, regular, and unlinked elsewhere")
	}
	if info.Size()+int64(len(line)) > config.MaxBytes {
		if err := f.Close(); err != nil {
			return err
		}
		// One bounded prior segment is retained; events are diagnostic, not truth.
		if err := syscall.Renameat(dirFD, name, dirFD, name+".1"); err != nil {
			return err
		}
		f, err = noFollowOpenAt(dirFD, name, syscall.O_WRONLY|syscall.O_APPEND|syscall.O_CREAT|syscall.O_EXCL, 0600)
		if err != nil {
			return err
		}
	}
	_, err = f.Write(line)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func noFollowOpenAt(dirFD int, name string, flags int, mode uint32) (*os.File, error) {
	fd, err := syscall.Openat(dirFD, name, flags|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

// openPrivateParent walks every ancestor by directory descriptor so a
// symlinked component cannot redirect the broker to another host location.
func openPrivateParent(path string) (int, string, error) {
	name := filepath.Base(path)
	if name == "." || name == ".." || name == "/" {
		return -1, "", errors.New("invalid log basename")
	}
	dir := filepath.Dir(path)
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", err
	}
	components := strings.Split(strings.TrimPrefix(dir, "/"), "/")
	for _, component := range components {
		if component == "" {
			continue
		}
		next, openErr := syscall.Openat(fd, component, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		syscall.Close(fd)
		if openErr != nil {
			return -1, "", openErr
		}
		fd = next
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err != nil {
			syscall.Close(fd)
			return -1, "", err
		}
		if st.Uid != 0 && st.Uid != uint32(os.Geteuid()) {
			syscall.Close(fd)
			return -1, "", errors.New("untrusted log directory owner")
		}
		if st.Mode&0022 != 0 && !(st.Uid == 0 && st.Mode&syscall.S_ISVTX != 0) {
			syscall.Close(fd)
			return -1, "", errors.New("writable log directory ancestry")
		}
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		syscall.Close(fd)
		return -1, "", err
	}
	if st.Uid != uint32(os.Geteuid()) || st.Mode&0077 != 0 {
		syscall.Close(fd)
		return -1, "", fmt.Errorf("log directory must be private and owned by P (path=%s uid=%d mode=%#o euid=%d)", dir, st.Uid, st.Mode, os.Geteuid())
	}
	return fd, name, nil
}

func ParseEvent(data []byte) (Event, error) {
	var event Event
	if len(data) > 4096 {
		return event, errors.New("event exceeds 4 KiB")
	}
	if err := strictJSON(bytes.TrimSpace(data), &event); err != nil {
		return event, err
	}
	return event, event.Validate()
}
