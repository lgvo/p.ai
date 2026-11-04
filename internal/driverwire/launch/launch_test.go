package launch

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fixture is how to start the fixture driver at path.
func fixture(path, digest string, env []string) Config[*fixtureConn] {
	return Config[*fixtureConn]{Name: "fixture driver", Path: path, SHA256: digest, Env: env, Hello: fixtureHello}
}

// The driver gets its path as its only argument and exactly the
// environment it is given: none of the daemon's.
func TestTheDriverGetsItsPathAndOnlyTheEnvironmentItIsGiven(t *testing.T) {
	for _, env := range [][]string{nil, {"HOME=/home/p", "SSH_AUTH_SOCK=/run/user/1000/agent.sock"}} {
		path, digest := installed(t, fixtureDriver)
		s, err := Start(t.Context(), fixture(path, digest, env))
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		c, ok := s.Current()
		if !ok {
			t.Fatal("no driver runs after Start")
		}
		got := c.started
		s.Close()
		if !slices.Equal(got.Args, []string{path}) || !slices.Equal(got.Env, env) {
			t.Fatalf("the driver given environment %q was started with arguments %q and environment %q, want only %q and that environment", env, got.Args, got.Env, path)
		}
	}
}

// A file put at the path after the launch checks is never started.
func TestTheFileCheckedIsTheFileStarted(t *testing.T) {
	path, digest := installed(t, fixtureDriver)
	exe, err := checkExecutable(path, digest, uint32(os.Geteuid()))
	if err != nil {
		t.Fatalf("the launch checks refused the fixture: %v", err)
	}
	defer exe.Close()
	replacement := filepath.Join(filepath.Dir(path), "replacement")
	script(t, replacement, "#!/bin/sh\nexit 7\n")
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	p, err := startChecked(t.Context(), fixture(path, digest, nil), exe)
	if err != nil {
		t.Fatalf("the checked driver did not start: %v", err)
	}
	defer p.stop()
	if p.client.started.PID != p.proc.Pid {
		t.Fatalf("the started process %d is not the checked driver, which reported %+v", p.proc.Pid, p.client.started)
	}
}

// A Config with Open starts the file Open returns, which applied its own
// launch checks, instead of checking an executable at Path.
func TestOpenGivesTheFileStarted(t *testing.T) {
	path, digest := installed(t, fixtureDriver)
	cfg := fixture(filepath.Join(t.TempDir(), "absent", fixtureDriver), "", nil)
	var gotAccount uint32
	cfg.Open = func(account uint32) (*os.File, error) {
		gotAccount = account
		return checkExecutable(path, digest, account)
	}
	s, err := Start(t.Context(), cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	c, ok := s.Current()
	if !ok || c.started.PID == 0 {
		t.Fatal("the file Open returned did not run")
	}
	if gotAccount != uint32(os.Geteuid()) {
		t.Fatalf("Open got account %d, want P's account %d", gotAccount, os.Geteuid())
	}
	cfg.Open = func(uint32) (*os.File, error) { return nil, Refusal(path, "is refused") }
	if s, err := Start(t.Context(), cfg); err == nil || !errors.Is(err, ErrLaunchCheck) || !strings.Contains(err.Error(), "the fixture driver failed its launch checks") {
		if s != nil {
			s.Close()
		}
		t.Fatalf("Start with an Open that refuses = %v, want its refusal", err)
	}
}

// A driver whose restart fails its launch checks stays stopped when its
// Config says so, even once the checks would pass again; otherwise it is
// tried again.
func TestARestartThatFailsItsLaunchChecks(t *testing.T) {
	fast := Backoff{Min: 20 * time.Millisecond, Max: 40 * time.Millisecond, Stable: time.Minute}
	for _, stop := range []bool{true, false} {
		t.Run(fmt.Sprintf("StopAfterFailedCheck=%v", stop), func(t *testing.T) {
			path, digest := installed(t, fixtureDriver)
			cfg := fixture(path, digest, nil)
			cfg.Backoff, cfg.StopAfterFailedCheck = fast, stop
			s, err := Start(t.Context(), cfg)
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer s.Close()
			first, _ := s.Current()
			chmod(t, path, 0o770)
			if err := syscall.Kill(first.started.PID, syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			time.Sleep(300 * time.Millisecond)
			if _, ok := s.Current(); ok {
				t.Fatal("a driver that fails its launch checks runs")
			}
			chmod(t, path, 0o700)
			restarted := false
			for limit := time.Now().Add(time.Second); time.Now().Before(limit) && !restarted; time.Sleep(20 * time.Millisecond) {
				_, restarted = s.Current()
			}
			if restarted == stop {
				t.Fatalf("restarted once its checks pass again: %v, want %v", restarted, !stop)
			}
		})
	}
}

func TestTheBackoffDoublesUpToItsBound(t *testing.T) {
	b := Backoff{Min: 100 * time.Millisecond, Max: time.Second}
	var got []time.Duration
	for wait := time.Duration(0); len(got) < 6; {
		wait = b.next(wait)
		got = append(got, wait)
	}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, time.Second, time.Second}
	if !slices.Equal(got, want) {
		t.Fatalf("waits %v, want %v", got, want)
	}
}

// A driver takes the children it started in its process group along,
// however it ends: dead on its own, stopped, or killed. Its whole group is
// killed before it is started again, so nothing it started still acts for
// it.
func TestADriverLeavesNoChildOfItsGroupBehind(t *testing.T) {
	fast := Backoff{Min: 20 * time.Millisecond, Max: 200 * time.Millisecond, Stable: time.Minute}
	t.Run("a driver that dies", func(t *testing.T) {
		path, digest := installed(t, fixtureParent)
		cfg := fixture(path, digest, nil)
		cfg.Backoff = fast
		s, err := Start(t.Context(), cfg)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer s.Close()
		first, _ := s.Current()
		child := first.started.Child
		wantRunning(t, child)
		if err := syscall.Kill(first.started.PID, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		for limit := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			if c, ok := s.Current(); ok && c != first {
				break
			}
			if time.Now().After(limit) {
				t.Fatal("the driver was not started again")
			}
		}
		wantGone(t, child, "the driver that died")
	})
	t.Run("a driver stopped", func(t *testing.T) {
		path, digest := installed(t, fixtureParent)
		s, err := Start(t.Context(), fixture(path, digest, nil))
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		c, _ := s.Current()
		child := c.started.Child
		wantRunning(t, child)
		s.Close()
		wantGone(t, child, "the stopped driver")
	})
	t.Run("a driver killed", func(t *testing.T) {
		path, digest := installed(t, fixtureParent)
		cfg := fixture(path, digest, nil)
		child, ran := 0, false
		cfg.Accept = func(c *fixtureConn) error {
			child = c.started.Child
			ran = child != 0 && running(t, child)
			return errors.New("refused")
		}
		if s, err := Start(t.Context(), cfg); err == nil {
			s.Close()
			t.Fatal("a driver Accept refused started")
		}
		if !ran {
			t.Fatalf("the driver's child %d did not run before the driver was killed; the case needs it to", child)
		}
		wantGone(t, child, "the killed driver")
	})
}

// running reports whether process pid runs: neither gone nor a zombie,
// which is dead and waits for whoever adopted it to reap it.
func running(t *testing.T, pid int) bool {
	t.Helper()
	// A process that ends while its stat is read answers ESRCH.
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
	return len(fields) > 0 && fields[0] != "Z" && fields[0] != "X"
}

func wantRunning(t *testing.T, pid int) {
	t.Helper()
	if pid == 0 || !running(t, pid) {
		t.Fatalf("the driver's child %d does not run; the case needs it to", pid)
	}
}

// wantGone checks that the child pid of driver stops running within five
// seconds, and kills it when it does not.
func wantGone(t *testing.T, pid int, driver string) {
	t.Helper()
	for limit := time.Now().Add(5 * time.Second); time.Now().Before(limit); time.Sleep(10 * time.Millisecond) {
		if !running(t, pid) {
			return
		}
	}
	syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("the child %d of %s outlived it", pid, driver)
}
