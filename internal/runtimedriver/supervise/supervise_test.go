package supervise

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/driverwire/launch"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimedriver/protocol"
)

// fast restarts a dead driver within a fraction of a second.
var fast = launch.Backoff{Min: 20 * time.Millisecond, Max: 200 * time.Millisecond, Stable: time.Minute}

// supervised starts the fixture driver under a supervisor with backoff b.
func supervised(t *testing.T, b launch.Backoff) (*Supervisor, string) {
	t.Helper()
	path, digest := installed(t, fixtureDriver)
	s, err := start(t.Context(), Config{Path: path, SHA256: digest, Hello: anyHello}, b)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

// supervisedDurable starts the durable fixture driver under a supervisor
// with a new state directory, which it returns.
func supervisedDurable(t *testing.T) (*Supervisor, string) {
	t.Helper()
	state := t.TempDir()
	config, err := json.Marshal(map[string]string{"state": state})
	if err != nil {
		t.Fatal(err)
	}
	path, digest := installed(t, fixtureDurable)
	s, err := start(t.Context(), Config{Path: path, SHA256: digest, Hello: protocol.Hello{Config: config}}, fast)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, state
}

func target(id string) runtimedriver.Target {
	return runtimedriver.Target{Session: session(id)}
}

func session(id string) runtimedriver.Session {
	return runtimedriver.Session{Ref: runtimedriver.Ref{Kind: runtimedriver.KindSession, ID: id}}
}

// startedAs asks the fixture driver how it was started.
func startedAs(t *testing.T, d runtimedriver.Driver) (started, error) {
	t.Helper()
	text, err := d.HostLog(t.Context(), target("whoami"))
	if err != nil {
		return started{}, err
	}
	var s started
	if err := json.Unmarshal([]byte(text), &s); err != nil {
		t.Fatalf("the fixture driver answered %q: %v", text, err)
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

func TestTheDriverGetsOnlyItsPathAndConnection(t *testing.T) {
	s, path := supervised(t, fast)
	got, err := startedAs(t, s)
	if err != nil {
		t.Fatalf("HostLog: %v", err)
	}
	if !slices.Equal(got.Args, []string{path}) || len(got.Env) != 0 {
		t.Fatalf("the driver was started with arguments %q and environment %q, want only %q", got.Args, got.Env, path)
	}
	if c := s.Describe(); c.ImageBuilder != fixtureCapabilities.ImageBuilder || !slices.Equal(c.Networks, fixtureCapabilities.Networks) {
		t.Fatalf("Describe = %+v, want the driver's %+v", c, fixtureCapabilities)
	}
	if err := s.Check(t.Context()); err != nil {
		t.Fatalf("Check: %v", err)
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
			chmod(t, path, 0o770)
			return Config{Path: path, SHA256: digest, Hello: anyHello}
		}, "is group- or world-writable"},
		{"another SHA-256", func(t *testing.T) Config {
			path, _ := installed(t, fixtureDriver)
			return Config{Path: path, SHA256: strings.Repeat("a", 64), Hello: anyHello}
		}, "not the pinned"},
		{"another protocol version", func(t *testing.T) Config {
			path, digest := installed(t, fixtureOtherVersion)
			return Config{Path: path, SHA256: digest, Hello: anyHello}
		}, `protocol "p.runtime-driver/v0"`},
		{"a driver that exits at once", func(t *testing.T) Config {
			path := filepath.Join(t.TempDir(), "driver")
			return Config{Path: path, SHA256: script(t, path, "#!/bin/sh\nexit 0\n"), Hello: anyHello}
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

func TestADriverKilledInACallIsRestarted(t *testing.T) {
	s, _ := supervised(t, launch.Backoff{Min: 300 * time.Millisecond, Max: time.Second, Stable: time.Minute})
	before, err := startedAs(t, s)
	if err != nil {
		t.Fatalf("HostLog: %v", err)
	}
	if _, err := s.Start(t.Context(), session(crashRef)); !errors.Is(err, runtimedriver.ErrUnknownOutcome) {
		t.Fatalf("a Start the driver died in = %v, want ErrUnknownOutcome", err)
	}
	if err := s.Check(t.Context()); !errors.Is(err, runtimedriver.ErrUnavailable) {
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
	if err := s.Check(t.Context()); err != nil {
		t.Fatalf("Check after the restart: %v", err)
	}
}

func TestAReadInFlightWhenTheDriverDiesIsUnavailable(t *testing.T) {
	s, _ := supervised(t, fast)
	_, err := s.HostLog(t.Context(), target(crashRef))
	if !errors.Is(err, runtimedriver.ErrUnavailable) || errors.Is(err, runtimedriver.ErrUnknownOutcome) {
		t.Fatalf("a HostLog the driver died in = %v, want ErrUnavailable only", err)
	}
	eventually(t, "the driver to restart", func() bool { return s.Check(t.Context()) == nil })
}

// A driver killed in a create after its gate admitted it leaves the outcome
// unknown; the restarted driver settles it from the state its backend kept,
// and the create is never sent again.
func TestACreateTheDriverWasKilledInIsSettledByTheRestartedDriver(t *testing.T) {
	for _, c := range []struct {
		name    string
		session string
		want    runtimedriver.Settlement
		applied int
	}{
		{"killed after the effect", dieAfterCreating, runtimedriver.Applied, 1},
		{"killed before the effect", dieBeforeCreating, runtimedriver.NotApplied, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, state := supervisedDurable(t)
			var gates atomic.Int32
			_, err := s.Create(t.Context(), session(c.session), "", func() error { gates.Add(1); return nil })
			if !errors.Is(err, runtimedriver.ErrUnknownOutcome) || gates.Load() != 1 {
				t.Fatalf("a Create the driver was killed in = %v after %d runs of its gate, want ErrUnknownOutcome after 1", err, gates.Load())
			}
			eventually(t, "the driver to restart", func() bool { return s.Check(t.Context()) == nil })
			settled, err := s.Settle(t.Context(), runtimedriver.Effect{Kind: runtimedriver.EffectCreate, Target: target(c.session)})
			if err != nil || settled != c.want {
				t.Fatalf("Settle after the restart = %v, %v, want %v", settled, err, c.want)
			}
			asked, applied := logged(t, state, "create asked "+c.session), logged(t, state, "create applied "+c.session)
			if asked != 1 || applied != c.applied {
				t.Fatalf("the backend was asked for %d creates and applied %d, want 1 asked and %d applied", asked, applied, c.applied)
			}
		})
	}
}

// A terminal whose driver dies ends unproven; the restarted driver settles
// it once the backend's state says its attachment ended, and not before.
func TestATerminalOfADriverThatDiesIsSettledOnceItsAttachmentEnded(t *testing.T) {
	s, state := supervisedDurable(t)
	term, err := s.OpenTerminal(t.Context(), target("attached"), runtimedriver.Size{Width: 80, Height: 24})
	if err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}
	defer term.Close()
	running, err := startedAs(t, s)
	if err != nil {
		t.Fatalf("HostLog: %v", err)
	}
	if err := syscall.Kill(running.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case end := <-term.Ended():
		if end.Proven {
			t.Fatal("the terminal of a driver that died ended proven")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the terminal did not end when its driver died")
	}
	eventually(t, "the driver to restart", func() bool { return s.Check(t.Context()) == nil })
	effect := runtimedriver.Effect{Kind: runtimedriver.EffectTerminal, Target: target("attached"), Terminal: term.ID()}
	if settled, err := s.Settle(t.Context(), effect); err != nil || settled != runtimedriver.Unknown {
		t.Fatalf("Settle while the attachment's guest process runs = %v, %v, want unknown", settled, err)
	}
	endGuest(t, state, term.ID())
	if settled, err := s.Settle(t.Context(), effect); err != nil || settled != runtimedriver.Applied {
		t.Fatalf("Settle once the attachment ended = %v, %v, want applied", settled, err)
	}
}

// Every restart applies the launch checks again: a driver whose file fails
// them stays down until it passes.
func TestARestartWaitsForTheLaunchChecks(t *testing.T) {
	s, path := supervised(t, fast)
	chmod(t, path, 0o770)
	if _, err := s.Start(t.Context(), session(crashRef)); !errors.Is(err, runtimedriver.ErrUnknownOutcome) {
		t.Fatalf("a Start the driver died in = %v, want ErrUnknownOutcome", err)
	}
	time.Sleep(10 * fast.Max)
	if err := s.Check(t.Context()); !errors.Is(err, runtimedriver.ErrUnavailable) {
		t.Fatalf("Check while the driver's file fails the launch checks = %v, want ErrUnavailable", err)
	}
	chmod(t, path, 0o700)
	eventually(t, "the driver to restart", func() bool { return s.Check(t.Context()) == nil })
}

func TestCloseStopsTheDriver(t *testing.T) {
	s, _ := supervised(t, fast)
	running, err := startedAs(t, s)
	if err != nil {
		t.Fatalf("HostLog: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := syscall.Kill(running.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the driver's process %d is still there after Close: %v", running.PID, err)
	}
	if err := s.Check(t.Context()); !errors.Is(err, runtimedriver.ErrUnavailable) {
		t.Fatalf("Check after Close = %v, want ErrUnavailable", err)
	}
}
