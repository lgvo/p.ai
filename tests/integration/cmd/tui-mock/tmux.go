package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// tmuxHost stands for the sessions' hosts: one local tmux server with a
// tmux session per session. An attachment is a real tmux client in a PTY,
// which control's relay carries to the production helper. No VM, privileged
// daemon or host service is used.
type tmuxHost struct {
	mu      sync.Mutex
	dir     string
	socket  string
	clients map[*tmuxClient]bool
	closed  bool
}

// tmuxClient is one attachment: a tmux client attached to its session.
type tmuxClient struct {
	pty  *os.File
	cmd  *exec.Cmd
	once sync.Once
	// done closes once the client exited.
	done chan struct{}
}

func newTmuxHost(dir string) (*tmuxHost, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, err
	}
	return &tmuxHost{dir: dir, socket: filepath.Join(dir, "tmux.sock"), clients: map[*tmuxClient]bool{}}, nil
}

func (h *tmuxHost) command(args ...string) *exec.Cmd {
	cmd := exec.Command("tmux", append([]string{"-S", h.socket, "-f", "/dev/null"}, args...)...)
	cmd.Env = []string{}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "TMUX=") && !strings.HasPrefix(value, "TMUX_PANE=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	return cmd
}

// attach starts session id's tmux session unless it runs, and attaches a new
// client to it at width × height.
func (h *tmuxHost) attach(id string, width, height int) (*tmuxClient, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, errors.New("fixture is closed")
	}
	workspace := filepath.Join(h.dir, "workspaces", id)
	home := filepath.Join(workspace, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		return nil, err
	}
	if err := h.command("has-session", "-t", id).Run(); err != nil {
		// Start a fixed shell with no host startup files or inherited P socket.
		rc := filepath.Join(home, ".mock-bashrc")
		if err := os.WriteFile(rc, []byte("PS1='[mock-session]$ '\n"), 0600); err != nil {
			return nil, err
		}
		cmd := h.command("new-session", "-d", "-s", id, "-c", workspace, "-e", "HOME="+home, "-e", "P_SOCKET="+filepath.Join(h.dir, "control.sock"), "-x", strconv.Itoa(width), "-y", strconv.Itoa(height), "bash", "--noprofile", "--rcfile", rc)
		cmd.Env = append(cmd.Env, "TERM=xterm-256color")
		if output, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("tmux new: %w: %s", err, output)
		}
		if output, err := h.command("set-option", "-t", id, "status-right", "MOCK · Ctrl-B d detach").CombinedOutput(); err != nil {
			return nil, fmt.Errorf("tmux status: %w: %s", err, output)
		}
	}
	cmd := h.command("attach-session", "-t", id)
	cmd.Env = append(cmd.Env, "TERM=xterm-256color")
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(width), Rows: uint16(height)})
	if err != nil {
		return nil, err
	}
	// pty's ioctl uses File.Fd(), which switches the reader to blocking mode.
	// Restore pollable reads so Close interrupts the relay's read.
	if err := syscall.SetNonblock(int(master.Fd()), true); err != nil {
		master.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	c := &tmuxClient{pty: master, cmd: cmd, done: make(chan struct{})}
	h.clients[c] = true
	go func() {
		_ = cmd.Wait()
		c.Close()
		h.mu.Lock()
		delete(h.clients, c)
		h.mu.Unlock()
		close(c.done)
	}()
	return c, nil
}

func (h *tmuxHost) stopSession(id string) { _ = h.command("kill-session", "-t", id).Run() }

func (h *tmuxHost) close() {
	h.mu.Lock()
	h.closed = true
	for c := range h.clients {
		c.Close()
	}
	h.mu.Unlock()
	_ = h.command("kill-server").Run()
	_ = os.Remove(h.socket)
}

// Read returns the client's output. A PTY's output ends in an error once its
// client exited; that is the terminal's end.
func (c *tmuxClient) Read(p []byte) (int, error) {
	n, err := c.pty.Read(p)
	if err != nil {
		return n, io.EOF
	}
	return n, nil
}

func (c *tmuxClient) Write(p []byte) (int, error) { return c.pty.Write(p) }

func (c *tmuxClient) Resize(s runtimedriver.Size) error {
	return pty.Setsize(c.pty, &pty.Winsize{Cols: uint16(s.Width), Rows: uint16(s.Height)})
}

// Close ends the attachment: the client exits, and its session stays.
func (c *tmuxClient) Close() error {
	c.once.Do(func() {
		_ = c.cmd.Process.Kill()
		c.pty.Close()
	})
	return nil
}
