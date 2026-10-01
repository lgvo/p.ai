package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// The fixture imitates only the native attachment wire contract. The terminal
// is a real local tmux client; no VM, privileged daemon or host service is used.
type nativeServer struct {
	mu        sync.Mutex
	dir, tmux string
	server    *http.Server
	ops       map[string]*nativeExec
	closed    bool
}
type nativeExec struct {
	mu                                    sync.Mutex
	uuid, path, dataSecret, controlSecret string
	width, height                         uint16
	pty                                   *os.File
	cmd                                   *exec.Cmd
	started, done                         chan struct{}
	ended                                 bool
	startOnce, endOnce                    sync.Once
}

func newNative(dir string) (*nativeServer, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, err
	}
	n := &nativeServer{dir: dir, tmux: filepath.Join(dir, "tmux.sock"), ops: map[string]*nativeExec{}}
	listener, err := net.Listen("unix", filepath.Join(dir, "native.sock"))
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(filepath.Join(dir, "native.sock"), 0600); err != nil {
		listener.Close()
		return nil, err
	}
	n.server = &http.Server{Handler: http.HandlerFunc(n.handle)}
	go n.server.Serve(listener)
	return n, nil
}
func (n *nativeServer) tmuxCommand(args ...string) *exec.Cmd {
	cmd := exec.Command("tmux", append([]string{"-S", n.tmux, "-f", "/dev/null"}, args...)...)
	cmd.Env = []string{}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "TMUX=") && !strings.HasPrefix(value, "TMUX_PANE=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	return cmd
}
func (n *nativeServer) start(op *nativeExec) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return fmt.Errorf("fixture is closed")
	}
	workspace := filepath.Join(n.dir, "workspaces", op.uuid)
	home := filepath.Join(workspace, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		return err
	}
	if err := n.tmuxCommand("has-session", "-t", op.uuid).Run(); err != nil {
		// Start a fixed shell with no host startup files or inherited P socket.
		rc := filepath.Join(home, ".mock-bashrc")
		if err := os.WriteFile(rc, []byte("PS1='[mock-session]$ '\n"), 0600); err != nil {
			return err
		}
		cmd := n.tmuxCommand("new-session", "-d", "-s", op.uuid, "-c", workspace, "-e", "HOME="+home, "-e", "P_SOCKET="+filepath.Join(n.dir, "control.sock"), "-x", strconv.Itoa(int(op.width)), "-y", strconv.Itoa(int(op.height)), "bash", "--noprofile", "--rcfile", rc)
		cmd.Env = append(cmd.Env, "TERM=xterm-256color")
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("tmux new: %w: %s", err, output)
		}
		if output, err := n.tmuxCommand("set-option", "-t", op.uuid, "status-right", "MOCK · Ctrl-B d detach").CombinedOutput(); err != nil {
			return fmt.Errorf("tmux status: %w: %s", err, output)
		}
	}
	cmd := n.tmuxCommand("attach-session", "-t", op.uuid)
	cmd.Env = append(cmd.Env, "TERM=xterm-256color")
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: op.width, Rows: op.height})
	if err != nil {
		return err
	}
	// pty's ioctl uses File.Fd(), which switches the reader to blocking mode.
	// Restore pollable reads so teardown interrupts the output goroutine.
	if err := syscall.SetNonblock(int(master.Fd()), true); err != nil {
		master.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	op.mu.Lock()
	op.cmd, op.pty = cmd, master
	op.mu.Unlock()
	go func() { _ = cmd.Wait(); op.finish() }()
	return nil
}
func (op *nativeExec) finish() {
	op.endOnce.Do(func() {
		op.mu.Lock()
		op.ended = true
		master, cmd := op.pty, op.cmd
		op.mu.Unlock()
		if master != nil {
			master.Close()
		}
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		close(op.done)
	})
}
func (n *nativeServer) running(path, id string) bool {
	n.mu.Lock()
	op := n.ops[path]
	n.mu.Unlock()
	if op == nil || op.uuid != id {
		return false
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	return op.pty != nil && !op.ended
}
func (n *nativeServer) stopSession(id string) { _ = n.tmuxCommand("kill-session", "-t", id).Run() }
func (n *nativeServer) close() {
	n.mu.Lock()
	n.closed = true
	for _, op := range n.ops {
		op.finish()
	}
	n.mu.Unlock()
	n.server.Close()
	_ = n.tmuxCommand("kill-server").Run()
	_ = os.Remove(n.tmux)
}
func syncReply(w http.ResponseWriter, code int) {
	_ = json.NewEncoder(w).Encode(object{"type": "sync", "metadata": object{"status_code": code, "metadata": object{"return": 0}}})
}
func (n *nativeServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("project") != "p-mock" {
		http.Error(w, "fixture project required", 400)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/1.0/instances/") && strings.HasSuffix(r.URL.Path, "/exec") {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/1.0/instances/p-"), "/exec")
		if _, err := uuid.Parse(id); err != nil || r.Method != "POST" {
			http.Error(w, "invalid instance", 400)
			return
		}
		var body struct {
			Command     []string `json:"command"`
			Width       uint16   `json:"width"`
			Height      uint16   `json:"height"`
			Interactive bool     `json:"interactive"`
			Wait        bool     `json:"wait-for-websocket"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body) != nil || len(body.Command) != 1 || body.Command[0] != "/usr/libexec/p/attach" || !body.Interactive || !body.Wait || body.Width == 0 || body.Height == 0 {
			http.Error(w, "invalid fixed attachment", 400)
			return
		}
		op := &nativeExec{uuid: id, path: "/1.0/operations/" + uuid.NewString(), dataSecret: uuid.NewString(), controlSecret: uuid.NewString(), width: body.Width, height: body.Height, started: make(chan struct{}), done: make(chan struct{})}
		n.mu.Lock()
		n.ops[op.path] = op
		n.mu.Unlock()
		_ = json.NewEncoder(w).Encode(object{"type": "async", "operation": op.path, "metadata": object{"metadata": object{"fds": object{"0": op.dataSecret, "control": op.controlSecret}}}})
		return
	}
	path := strings.TrimSuffix(strings.TrimSuffix(r.URL.Path, "/websocket"), "/wait")
	n.mu.Lock()
	op := n.ops[path]
	n.mu.Unlock()
	if op == nil {
		http.NotFound(w, r)
		return
	}
	switch r.URL.Path {
	case op.path:
		op.mu.Lock()
		ended := op.ended
		op.mu.Unlock()
		code := 103
		if ended {
			code = 200
		}
		syncReply(w, code)
	case op.path + "/wait":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-op.done:
			syncReply(w, 200)
		case <-r.Context().Done():
		}
	case op.path + "/websocket":
		secret := r.URL.Query().Get("secret")
		if secret != op.dataSecret && secret != op.controlSecret {
			http.Error(w, "bad secret", 403)
			return
		}
		upgrader := websocket.Upgrader{}
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		if secret == op.dataSecret {
			op.startOnce.Do(func() {
				if n.start(op) != nil {
					op.finish()
				}
				close(op.started)
			})
			<-op.started
			op.mu.Lock()
			master := op.pty
			op.mu.Unlock()
			if master == nil {
				return
			}
			defer op.finish()
			go func() {
				defer op.finish()
				buffer := make([]byte, 4096)
				for {
					count, err := master.Read(buffer)
					if count > 0 {
						if ws.WriteMessage(websocket.BinaryMessage, buffer[:count]) != nil {
							return
						}
					}
					if err != nil {
						_ = ws.WriteMessage(websocket.TextMessage, []byte(""))
						return
					}
				}
			}()
			for {
				_, data, err := ws.ReadMessage()
				if err != nil {
					return
				}
				if _, err = master.Write(data); err != nil {
					return
				}
			}
		} else {
			// Reading starts only once the tmux PTY exists, matching the native
			// start acknowledgement expected by the production helper's ping.
			select {
			case <-op.started:
			case <-op.done:
				return
			}
			defer op.finish()
			for {
				_, data, err := ws.ReadMessage()
				if err != nil {
					return
				}
				var resize struct {
					Command string            `json:"command"`
					Args    map[string]string `json:"args"`
				}
				if json.Unmarshal(data, &resize) == nil && resize.Command == "window-resize" {
					width, e1 := strconv.ParseUint(resize.Args["width"], 10, 16)
					height, e2 := strconv.ParseUint(resize.Args["height"], 10, 16)
					if e1 == nil && e2 == nil && width > 0 && height > 0 {
						op.mu.Lock()
						master := op.pty
						op.mu.Unlock()
						if master != nil {
							_ = pty.Setsize(master, &pty.Winsize{Cols: uint16(width), Rows: uint16(height)})
						}
					}
				}
			}
		}
	default:
		http.NotFound(w, r)
	}
}
