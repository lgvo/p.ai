package supervise

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/driverwire/launch"
	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/protocol"
)

// fast restarts a dead driver within a fraction of a second.
var fast = launch.Backoff{Min: 20 * time.Millisecond, Max: 200 * time.Millisecond, Stable: time.Minute}

// hello is the fixture driver's hello, with its state directory.
func hello(t *testing.T, state string) protocol.Hello {
	t.Helper()
	config, err := json.Marshal(map[string]string{"state": state})
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Hello{Config: config, Core: protocol.Core{Storage: "/var/lib/p"}}
}

// supervised starts the fixture driver under a supervisor with backoff b,
// and returns it with the driver's path and state directory.
func supervised(t *testing.T, b launch.Backoff) (*Supervisor, string, string) {
	t.Helper()
	path, digest := installed(t, fixtureDriver)
	state := t.TempDir()
	s, err := start(t.Context(), Config{Path: path, SHA256: digest, Hello: hello(t, state)}, b)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path, state
}

// startedAs asks the fixture driver how it was started.
func startedAs(t *testing.T, d sourcedriver.Driver) (started, error) {
	t.Helper()
	r, err := d.InspectRepository(t.Context(), whoami)
	if err != nil {
		return started{}, err
	}
	var s started
	if err := json.Unmarshal([]byte(r.Incarnation), &s); err != nil {
		t.Fatalf("the fixture driver answered %q: %v", r.Incarnation, err)
	}
	return s, nil
}

// eventually waits until ok holds, failing the test after ten seconds.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// checks are the processes that ran the fixture driver's Check, in order.
func checks(t *testing.T, state string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(state, "checks"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return strings.Fields(string(raw))
}

// The driver gets its path as its only argument and, of the daemon's
// environment, only HOME and SSH_AUTH_SOCK: with them it reaches origins
// as the daemon's account; its config names the programs it runs. Its
// hello names the file the supervisor checked as its executable, beside
// the settings the daemon gave.
func TestTheDriverGetsOnlyItsCheckedPathAndTheDaemonsHomeAndSSHAgent(t *testing.T) {
	t.Setenv("HOME", "/home/p")
	t.Setenv("SSH_AUTH_SOCK", "/run/user/1000/ssh-agent.sock")
	t.Setenv("PATH", "/run/current-system/sw/bin")
	t.Setenv("P_SECRET", "not the driver's")
	s, path, _ := supervised(t, fast)
	got, err := startedAs(t, s)
	if err != nil {
		t.Fatalf("InspectRepository: %v", err)
	}
	want := []string{"HOME=/home/p", "SSH_AUTH_SOCK=/run/user/1000/ssh-agent.sock"}
	if !slices.Equal(got.Args, []string{path}) || !slices.Equal(got.Env, want) {
		t.Fatalf("the driver was started with arguments %q and environment %q, want only %q and %q", got.Args, got.Env, path, want)
	}
	if got.Core != (protocol.Core{Storage: "/var/lib/p", Executable: path}) {
		t.Fatalf("the driver's hello carried %+v, want the daemon's storage and the executable %s", got.Core, path)
	}
	if d := s.Describe(); d.Backend != "memory" || !slices.Equal(d.Commands, []string{"git-upload-pack", "git-receive-pack"}) {
		t.Fatalf("Describe = %+v, want the driver's", d)
	}

	os.Unsetenv("SSH_AUTH_SOCK") // t.Setenv restores it when the test ends
	s, _, _ = supervised(t, fast)
	if got, err := startedAs(t, s); err != nil || !slices.Equal(got.Env, []string{"HOME=/home/p"}) {
		t.Fatalf("without an SSH agent the driver was started with environment %q (%v), want only HOME", got.Env, err)
	}
}

// The first start's failure is the supervisor's, so the daemon refuses to
// start.
func TestStartReturnsTheFirstStartsFailure(t *testing.T) {
	for _, c := range []struct {
		name    string
		arrange func(t *testing.T) Config
		want    string
	}{
		{"a launch check", func(t *testing.T) Config {
			path, digest := installed(t, fixtureDriver)
			if err := os.Chmod(path, 0o770); err != nil {
				t.Fatal(err)
			}
			return Config{Path: path, SHA256: digest, Hello: hello(t, t.TempDir())}
		}, "is group- or world-writable"},
		{"another SHA-256", func(t *testing.T) Config {
			path, _ := installed(t, fixtureDriver)
			return Config{Path: path, SHA256: strings.Repeat("a", 64), Hello: hello(t, t.TempDir())}
		}, "not the pinned"},
		{"another protocol version", func(t *testing.T) Config {
			path, digest := installed(t, fixtureOtherVersion)
			return Config{Path: path, SHA256: digest, Hello: hello(t, t.TempDir())}
		}, `protocol "p.source-driver/v0"`},
		{"a config the driver refuses", func(t *testing.T) Config {
			path, digest := installed(t, fixtureDriver)
			return Config{Path: path, SHA256: digest, Hello: protocol.Hello{Config: json.RawMessage(`{}`)}}
		}, "names no state directory"},
		{"a failed check", func(t *testing.T) Config {
			path, digest := installed(t, fixtureDriver)
			state := t.TempDir()
			if err := os.WriteFile(filepath.Join(state, "unchecked"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return Config{Path: path, SHA256: digest, Hello: hello(t, state)}
		}, "the fixture's storage is unchecked"},
		{"a driver that exits at once", func(t *testing.T) Config {
			path := filepath.Join(t.TempDir(), "driver")
			return Config{Path: path, SHA256: script(t, path, "#!/bin/sh\nexit 0\n"), Hello: hello(t, t.TempDir())}
		}, "hello"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, err := start(t.Context(), c.arrange(t), fast)
			if err == nil {
				s.Close()
				t.Fatal("the supervisor started")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Start = %v, want an error naming %q", err, c.want)
			}
		})
	}
}

// A driver whose process dies in a call is restarted. The call ends as the
// protocol says: an effect its gate admitted is unknown, a read
// unavailable; calls while the driver is down are unavailable.
func TestADriverKilledInACallIsRestarted(t *testing.T) {
	s, _, _ := supervised(t, launch.Backoff{Min: 300 * time.Millisecond, Max: time.Second, Stable: time.Minute})
	before, err := startedAs(t, s)
	if err != nil {
		t.Fatalf("InspectRepository: %v", err)
	}
	gates := 0
	if err := s.CreateBranch(t.Context(), crash, "work", "a1", func() error { gates++; return nil }); !errors.Is(err, sourcedriver.ErrUnknownOutcome) || gates != 1 {
		t.Fatalf("a CreateBranch the driver died in after its gate = %v after %d gates, want ErrUnknownOutcome after 1", err, gates)
	}
	if err := s.Check(t.Context()); !errors.Is(err, sourcedriver.ErrUnavailable) {
		t.Fatalf("Check while the driver is down = %v, want ErrUnavailable", err)
	}
	var after started
	eventually(t, "the driver to restart", func() bool {
		after, err = startedAs(t, s)
		return err == nil
	})
	if after.PID == before.PID {
		t.Fatalf("the driver answers from process %d, as before it died", after.PID)
	}
	if _, err := s.Branches(t.Context(), crash); !errors.Is(err, sourcedriver.ErrUnavailable) || errors.Is(err, sourcedriver.ErrUnknownOutcome) {
		t.Fatalf("a Branches the driver died in = %v, want ErrUnavailable only", err)
	}
	eventually(t, "the driver to restart", func() bool { return s.Check(t.Context()) == nil })
}

// Every driver started checks itself before it serves, as the first did: a
// restarted bundled driver ends there the services its predecessor left.
// One whose check fails stays down until a start passes it.
func TestEveryDriverStartedChecksItselfBeforeItServes(t *testing.T) {
	s, _, state := supervised(t, fast)
	first, err := startedAs(t, s)
	if err != nil {
		t.Fatalf("InspectRepository: %v", err)
	}
	if got := checks(t, state); !slices.Equal(got, []string{strconv.Itoa(first.PID)}) {
		t.Fatalf("checks by processes %q, want one by the driver %d", got, first.PID)
	}
	unchecked := filepath.Join(state, "unchecked")
	if err := os.WriteFile(unchecked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(first.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a restarted driver to fail its check", func() bool { return len(checks(t, state)) >= 3 })
	if _, err := startedAs(t, s); !errors.Is(err, sourcedriver.ErrUnavailable) {
		t.Fatalf("a call while every restarted driver fails its check = %v, want ErrUnavailable", err)
	}
	if err := os.Remove(unchecked); err != nil {
		t.Fatal(err)
	}
	var serving started
	eventually(t, "a restarted driver to serve", func() bool {
		serving, err = startedAs(t, s)
		return err == nil
	})
	if got := checks(t, state); got[len(got)-1] != strconv.Itoa(serving.PID) {
		t.Fatalf("checks by processes %q, want the last by the serving driver %d", got, serving.PID)
	}
}

// A restarted driver that describes another backend would find none of the
// instance's repositories: it is refused, and the driver stays down until
// one describes the first one's.
func TestARestartedDriverDescribingAnotherBackendIsRefused(t *testing.T) {
	s, _, state := supervised(t, fast)
	first, err := startedAs(t, s)
	if err != nil {
		t.Fatalf("InspectRepository: %v", err)
	}
	backend := filepath.Join(state, "backend")
	if err := os.WriteFile(backend, []byte("forge"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(first.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * fast.Max)
	if _, err := startedAs(t, s); !errors.Is(err, sourcedriver.ErrUnavailable) {
		t.Fatalf("a call while the restarted driver describes another backend = %v, want ErrUnavailable", err)
	}
	if err := os.Remove(backend); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a restarted driver to serve", func() bool {
		_, err := startedAs(t, s)
		return err == nil
	})
	if d := s.Describe(); d.Backend != "memory" {
		t.Fatalf("Describe = %+v, want the first start's backend", d)
	}
}

func TestCloseStopsTheDriver(t *testing.T) {
	s, _, _ := supervised(t, fast)
	running, err := startedAs(t, s)
	if err != nil {
		t.Fatalf("InspectRepository: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := syscall.Kill(running.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the driver's process %d is still there after Close: %v", running.PID, err)
	}
	if err := s.Check(t.Context()); !errors.Is(err, sourcedriver.ErrUnavailable) {
		t.Fatalf("Check after Close = %v, want ErrUnavailable", err)
	}
}
