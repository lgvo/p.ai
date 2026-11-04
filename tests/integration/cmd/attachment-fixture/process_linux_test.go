package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	// A real executable with the same argv shape as p attach-helper. Keep it
	// alive on an inherited pipe so the tests need no daemon, Incus, or VM.
	if os.Getenv("P_ATTACHMENT_DISCOVERY_CHILD") == "1" {
		fmt.Println("ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestTerminalPipeCloseInterruptsReaderAfterResize(t *testing.T) {
	for _, resize := range []bool{false, true} {
		t.Run(fmt.Sprintf("resize=%v", resize), func(t *testing.T) {
			cmd := exec.Command("/bin/sh", "-c", "printf 'ready\\n'; read value")
			terminal, err := startTerminal(cmd)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				terminal.kill()
				_ = terminal.waitExit(time.Second)
				terminal.closePipe()
			})
			if err := (&fixture{}).wait(2*time.Second, "PTY child ready", func() (bool, error) {
				terminal.mu.Lock()
				defer terminal.mu.Unlock()
				return strings.Contains(terminal.text, "ready"), nil
			}); err != nil {
				t.Fatal(err)
			}
			if resize {
				if err := terminal.resize(113, 37); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := terminal.pty.SyscallConn()
			if err != nil {
				t.Fatal(err)
			}
			var flags int
			var size *unix.Winsize
			var controlErr error
			if err := raw.Control(func(fd uintptr) {
				flags, controlErr = unix.FcntlInt(fd, unix.F_GETFL, 0)
				if controlErr == nil {
					size, controlErr = unix.IoctlGetWinsize(int(fd), unix.TIOCGWINSZ)
				}
			}); err != nil || controlErr != nil {
				t.Fatalf("PTY state: %v, %v", err, controlErr)
			}
			if flags&unix.O_NONBLOCK == 0 {
				t.Error("PTY reader is blocking, so Close cannot interrupt it")
			}
			if resize && (size.Row != 37 || size.Col != 113) {
				t.Errorf("PTY resize = %+v", size)
			}
			terminal.closePipe()
			if err := terminal.waitExit(2 * time.Second); err != nil {
				t.Fatalf("closing master failed to hang up real PTY child: %v", err)
			}
		})
	}
}

func TestKillAttachmentHelperFromNonleaderThread(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	// Same bytes under a different inode are not the trusted executable.
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	otherExe := filepath.Join(t.TempDir(), "other-p")
	if err := os.WriteFile(otherExe, data, 0700); err != nil {
		t.Fatal(err)
	}
	wrongExecutable, _ := startNonleaderChild(t, otherExe, "attach-helper")
	wrongMode, _ := startNonleaderChild(t, exe, "fixture-decoy")
	extraArgument, _ := startNonleaderChild(t, exe, "attach-helper", "extra")
	child, thread := startNonleaderChild(t, exe, "attach-helper")
	leaderChildren, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", os.Getpid(), os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	for _, pid := range strings.Fields(string(leaderChildren)) {
		if pid == strconv.Itoa(child.Process.Pid) {
			t.Fatalf("regression setup placed helper under leader instead of thread %d", thread)
		}
	}
	parent, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Release()
	found, err := killAttachmentHelper(parent, identity)
	if err != nil || !found {
		t.Fatalf("kill helper forked by nonleader %d: found=%v, %v", thread, found, err)
	}
	err = child.Wait()
	status, ok := child.ProcessState.Sys().(syscall.WaitStatus)
	if err == nil || !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("helper was not SIGKILLed: %v, %v", child.ProcessState, err)
	}
	for _, decoy := range []*exec.Cmd{wrongExecutable, wrongMode, extraArgument} {
		if err := decoy.Process.Signal(syscall.Signal(0)); err != nil {
			t.Errorf("discovery killed decoy PID %d: %v", decoy.Process.Pid, err)
		}
	}
	if found, err := killAttachmentHelper(parent, identity); err != nil || found {
		t.Fatalf("selected a decoy after helper exit: found=%v, %v", found, err)
	}
}

func TestKillAttachmentHelperRefusesAmbiguousChildren(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := startNonleaderChild(t, exe, "attach-helper")
	second, _ := startNonleaderChild(t, exe, "attach-helper")
	parent, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Release()
	if found, err := killAttachmentHelper(parent, identity); err == nil || found {
		t.Fatalf("accepted ambiguous helpers: found=%v, %v", found, err)
	}
	for _, child := range []*exec.Cmd{first, second} {
		if err := child.Process.Signal(syscall.Signal(0)); err != nil {
			t.Fatalf("ambiguous child was killed: %v", err)
		}
	}
}

func startNonleaderChild(t *testing.T, executable string, args ...string) (*exec.Cmd, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = append(os.Environ(), "P_ATTACHMENT_DISCOVERY_CHILD=1")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { input.Close(); output.Close() })
	cmd.Stderr = os.Stderr
	type started struct {
		thread int
		err    error
	}
	startedCh := make(chan started, 1)
	release := make(chan struct{})
	var start func()
	start = func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if syscall.Gettid() == os.Getpid() {
			// Occupy the leader until the child is reaped, forcing the next
			// goroutine to start it on another thread.
			go start()
		} else {
			startedCh <- started{syscall.Gettid(), cmd.Start()}
		}
		<-release // Do not let thread exit reparent its child to the leader.
	}
	go start()
	result := <-startedCh
	if result.err != nil {
		close(release)
		t.Fatal(result.err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		if cmd.ProcessState == nil {
			_ = cmd.Wait()
		}
		close(release)
	})
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("child readiness: %q, %v", line, err)
	}
	return cmd, result.thread
}
