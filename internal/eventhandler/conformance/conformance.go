// Package conformance is the event-handler conformance suite: what an
// executable event handler, started as P's daemon starts it, does with the
// hello, the launch checks, events in order, a missed deadline and a
// crash. It runs against the bundled p-event-file and the example filter.
package conformance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/driverwire"
	"github.com/lgvo/p.ai/internal/driverwire/launch"
	"github.com/lgvo/p.ai/internal/eventhandler"
	"github.com/lgvo/p.ai/internal/eventhandler/supervise"
	"github.com/lgvo/p.ai/internal/plugin"
)

// Fixture is the handler under test.
type Fixture struct {
	// Install writes the handler's package into dir, a new private
	// directory: its plugin.json and its entry.
	Install func(t *testing.T, dir string)
	// Config returns a config the handler takes, which keeps what it does
	// in private, a new private directory.
	Config func(t *testing.T, private string) json.RawMessage
	// RefusedConfig is a config the handler refuses.
	RefusedConfig json.RawMessage
	// Delivered returns the IDs of the events the handler handled with a
	// config of Config(private), in the order it handled them.
	Delivered func(t *testing.T, private string) []string
	// Handles says whether the handler handles e rather than skipping it.
	Handles func(e eventhandler.Event) bool
}

// Run runs the suite against f.
func Run(t *testing.T, f Fixture) {
	t.Run("the hello", func(t *testing.T) { testHello(t, f) })
	t.Run("the hello's refusals", func(t *testing.T) { testHelloRefusals(t, f) })
	t.Run("the launch checks", func(t *testing.T) { testLaunchChecks(t, f) })
	t.Run("events in order", func(t *testing.T) { testInOrder(t, f) })
	t.Run("a missed deadline", func(t *testing.T) { testDeadline(t, f) })
	t.Run("a crash", func(t *testing.T) { testCrash(t, f) })
}

// handler is an installed package and the private directory its config
// names.
type handler struct {
	pkg     plugin.Package
	private string
	config  json.RawMessage
}

func install(t *testing.T, f Fixture) handler {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "package")
	private := filepath.Join(t.TempDir(), "private")
	for _, d := range []string{dir, private} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.Install(t, dir)
	pkg, err := plugin.Conformance(dir)
	if err != nil {
		t.Fatalf("the handler's package does not conform: %v", err)
	}
	if pkg.Manifest.Capability != plugin.CapabilityEventHandler {
		t.Fatalf("the package provides %s", pkg.Manifest.Capability)
	}
	return handler{pkg: pkg, private: private, config: f.Config(t, private)}
}

func (h handler) start(t *testing.T) *supervise.Supervisor {
	t.Helper()
	s, err := supervise.Start(t.Context(), supervise.Config{Path: h.pkg.Path, SHA256: h.pkg.SHA256, Config: h.config})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func (h handler) entry() string { return filepath.Join(h.pkg.Path, h.pkg.Manifest.Entry) }

// event is a valid event with id, of a kind and value that cycle through
// the reduced kinds, so that a filter handles some and skips others.
func event(id string, n int) eventhandler.Event {
	kinds := []struct{ kind, field, value string }{
		{"session.condition_changed", "condition", "ready"},
		{"session.condition_changed", "condition", "stopped"},
		{"operation.progress", "phase", "running"},
		{"session.attachment_changed", "count", "1"},
		{"session.unattended_changed", "unattended_condition", "idle"},
		{"session.policy_changed", "policy_condition", "outdated"},
	}
	k := kinds[n%len(kinds)]
	return eventhandler.Event{Schema: eventhandler.EventSchema, ID: id, Kind: k.kind,
		OccurredAt: time.Date(2026, 11, 3, 0, 0, n, 0, time.UTC).Format(time.RFC3339Nano),
		Instance:   "p-conformance", Project: "app", Session: "session", Branch: "main", Fields: map[string]string{k.field: k.value}}
}

// handled sends e and checks that the handler did what f says it does.
func handled(t *testing.T, f Fixture, s *supervise.Supervisor, e eventhandler.Event) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	status, err := s.Handle(ctx, e)
	want := eventhandler.Skipped
	if f.Handles(e) {
		want = eventhandler.Handled
	}
	if err != nil || status != want {
		t.Fatalf("Handle(%s %v) = %q, %v; want %q", e.Kind, e.Fields, status, err, want)
	}
}

// The handler answers the hello as its package names it, and takes its
// config: Start requires both.
func testHello(t *testing.T, f Fixture) {
	h := install(t, f)
	h.start(t)
}

// The handler refuses a hello of another version or capability, an event
// before the hello, and a config it does not take.
func testHelloRefusals(t *testing.T, f Fixture) {
	h := install(t, f)
	hello := func(version, capability string, config json.RawMessage) string {
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "hello",
			"params": map[string]any{"protocol": version, "config": config, "core": map[string]string{"capability": capability}}})
		return string(raw)
	}
	e, _ := json.Marshal(event("e-1", 0))
	for _, c := range []struct {
		name, request string
		code          float64
	}{
		{"another version", hello("p.event-handler/v0", eventhandler.Capability, h.config), -32001},
		{"another capability", hello(eventhandler.Version, "agent-adapter", h.config), -32000},
		{"an event before the hello", `{"jsonrpc":"2.0","id":1,"method":"event.handle","params":` + string(e) + `}`, -32002},
	} {
		t.Run(c.name, func(t *testing.T) {
			answer := exchange(t, h.entry(), c.request)
			if code := errorCode(answer); code != c.code {
				t.Fatalf("the handler answered %s, want error code %v", answer, c.code)
			}
		})
	}
	t.Run("a config it does not take", func(t *testing.T) {
		h := install(t, f)
		h.config = f.RefusedConfig
		if s, err := supervise.Start(t.Context(), supervise.Config{Path: h.pkg.Path, SHA256: h.pkg.SHA256, Config: h.config}); err == nil {
			s.Close()
			t.Fatal("a handler started with a config it does not take")
		}
	})
}

// exchange starts the handler's entry as the daemon would, with its
// connection on descriptor 3 and no environment, sends request and returns
// the handler's answer.
func exchange(t *testing.T, entry, request string) []byte {
	t.Helper()
	local, remote, err := driverwire.SocketPair("handler connection")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	cmd := exec.Command(entry)
	cmd.Env, cmd.ExtraFiles, cmd.Stderr = []string{}, []*os.File{remote}, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	remote.Close()
	defer cmd.Wait()
	defer local.Close()
	local.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := local.Write([]byte(request + "\n")); err != nil {
		t.Fatal(err)
	}
	answer, err := bufio.NewReader(local).ReadBytes('\n')
	if err != nil {
		t.Fatalf("the handler did not answer: %v", err)
	}
	return answer
}

func errorCode(answer []byte) float64 {
	var m struct {
		Error *struct {
			Code float64 `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(answer, &m) != nil || m.Error == nil {
		return 0
	}
	return m.Error.Code
}

// A package that someone else may change, or that differs from the pinned
// digest, never starts; and a handler whose package changes while it runs
// is not started again once it ends.
func testLaunchChecks(t *testing.T, f Fixture) {
	for _, c := range []struct {
		name    string
		arrange func(t *testing.T, h *handler)
		want    string
	}{
		{"a loosened mode", func(t *testing.T, h *handler) {
			chmod(t, h.entry(), 0o770)
		}, "is group- or world-writable"},
		{"a changed digest", func(t *testing.T, h *handler) {
			h.pkg.SHA256 = strings.Repeat("0", 64)
		}, "not the pinned"},
		{"an entry without its execute bit", func(t *testing.T, h *handler) {
			chmod(t, h.entry(), 0o600)
		}, "lacks its owner's execute bit"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := install(t, f)
			c.arrange(t, &h)
			s, err := supervise.Start(t.Context(), supervise.Config{Path: h.pkg.Path, SHA256: h.pkg.SHA256, Config: h.config})
			if err == nil {
				s.Close()
				t.Fatal("the handler started")
			}
			if !errors.Is(err, launch.ErrLaunchCheck) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Start = %v, want a refusal %q", err, c.want)
			}
		})
	}
	t.Run("a package changed while the handler runs", func(t *testing.T) {
		h := install(t, f)
		s := h.start(t)
		chmod(t, h.entry(), 0o770)
		kill(t, pid(t, h.entry()), syscall.SIGKILL)
		time.Sleep(time.Second)
		chmod(t, h.entry(), 0o700)
		time.Sleep(500 * time.Millisecond)
		if _, err := s.Handle(t.Context(), event("e-1", 0)); !errors.Is(err, eventhandler.ErrUnavailable) {
			t.Fatalf("Handle after a restart that failed its launch checks = %v, want the handler stopped", err)
		}
	})
}

// Events reach the handler one at a time, in the order sent, each handled
// or skipped as the handler says.
func testInOrder(t *testing.T, f Fixture) {
	h := install(t, f)
	s := h.start(t)
	var want []string
	for n := range 24 {
		e := event("e-"+strconv.Itoa(n), n)
		handled(t, f, s, e)
		if f.Handles(e) {
			want = append(want, e.ID)
		}
	}
	if got := f.Delivered(t, h.private); !slices.Equal(got, want) {
		t.Fatalf("the handler handled %v, want %v in that order", got, want)
	}
}

// A handler that misses an event's deadline is killed and started again:
// later events reach the new one.
func testDeadline(t *testing.T, f Fixture) {
	h := install(t, f)
	s := h.start(t)
	stuck := pid(t, h.entry())
	kill(t, stuck, syscall.SIGSTOP)
	if !stopped(stuck) {
		// A traced process, as under proot in the Nix build's checks, goes
		// on when it is stopped, so it cannot be made to miss a deadline.
		kill(t, stuck, syscall.SIGCONT)
		t.Skip("the handler cannot be stopped here: it runs traced")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	_, err := s.Handle(ctx, event("e-stuck", 0))
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		kill(t, stuck, syscall.SIGKILL)
		t.Fatalf("Handle to a stopped handler = %v, want its deadline", err)
	}
	after := restarted(t, f, s, h, stuck)
	if alive(stuck) {
		t.Fatalf("the handler %d that missed its deadline still runs beside %d", stuck, after)
	}
}

// A handler that dies is started again: an event sent while it is down is
// refused as unavailable, which the daemon counts and logs as dropped, and later
// events reach the new one.
func testCrash(t *testing.T, f Fixture) {
	h := install(t, f)
	s := h.start(t)
	dead := pid(t, h.entry())
	kill(t, dead, syscall.SIGKILL)
	if _, err := s.Handle(t.Context(), event("e-dropped", 0)); !errors.Is(err, eventhandler.ErrUnavailable) {
		t.Fatalf("Handle while the handler is down = %v, want it unavailable", err)
	}
	restarted(t, f, s, h, dead)
	for _, id := range f.Delivered(t, h.private) {
		if id == "e-dropped" {
			t.Fatal("the event sent while the handler was down was handled")
		}
	}
}

// restarted waits until a handler other than old handles events again and
// returns its process ID.
func restarted(t *testing.T, f Fixture, s *supervise.Supervisor, h handler, old int) int {
	t.Helper()
	e := event("e-after", 0)
	for limit := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		_, err := s.Handle(ctx, e)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(limit) {
			t.Fatalf("the handler was not started again: %v", err)
		}
	}
	now := pid(t, h.entry())
	if now == old {
		t.Fatalf("the handler %d still runs", old)
	}
	if f.Handles(e) && !slices.Contains(f.Delivered(t, h.private), "e-after") {
		t.Fatal("the restarted handler did not handle the later event")
	}
	return now
}

// pid is the process ID of the handler this process started from entry.
func pid(t *testing.T, entry string) int {
	t.Helper()
	for limit := time.Now().Add(5 * time.Second); time.Now().Before(limit); time.Sleep(20 * time.Millisecond) {
		dirs, err := os.ReadDir("/proc")
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range dirs {
			n, err := strconv.Atoi(d.Name())
			if err != nil {
				continue
			}
			cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", n))
			if err != nil {
				continue
			}
			argv0, _, _ := bytes.Cut(cmdline, []byte{0})
			if string(argv0) == entry && parent(n) == os.Getpid() && alive(n) {
				return n
			}
		}
	}
	t.Fatalf("no handler runs from %s", entry)
	return 0
}

// stat is the fields of /proc/PID/stat after the command: the state first.
func stat(pid int) []string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return nil
	}
	return strings.Fields(string(data[bytes.LastIndexByte(data, ')')+1:]))
}

func parent(pid int) int {
	fields := stat(pid)
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}

// alive reports whether pid runs or is stopped: neither gone nor a zombie.
func alive(pid int) bool {
	fields := stat(pid)
	return len(fields) > 0 && fields[0] != "Z" && fields[0] != "X"
}

// stopped waits up to a second for pid to be stopped by a signal.
func stopped(pid int) bool {
	for limit := time.Now().Add(time.Second); time.Now().Before(limit); time.Sleep(10 * time.Millisecond) {
		if fields := stat(pid); len(fields) > 0 && fields[0] == "T" {
			return true
		}
	}
	return false
}

func kill(t *testing.T, pid int, sig syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(pid, sig); err != nil {
		t.Fatal(err)
	}
}

func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// CopyExecutable copies the running test binary to path with mode 0700: a
// handler's entry that runs the handler's main when its test binary
// dispatches on the name it is started under.
func CopyExecutable(t *testing.T, path string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o700); err != nil {
		t.Fatal(err)
	}
}
