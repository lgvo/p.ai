package git

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/gitnames"
)

// A receive service's pre-receive hook checks the push inside receive-pack,
// where the pushed objects are still kept apart, and asks the driver to
// approve it over a socket of that push only. The hook executes the program
// running the driver as `<program> git-hook`, which calls
// RunPreReceiveHook: `p git-hook` in the daemon.

// hookRequest is the hook's one request.
type hookRequest struct {
	Token string `json:"token"`
	Ref   string `json:"ref"`
	Old   string `json:"old"`
	New   string `json:"new"`
}

// errPushRefused is the hook's refusal, which receive-pack reports to the
// client.
var errPushRefused = errors.New("P's Git service refused the push")

// hooksDirectory holds the pre-receive hook and the per-push sockets.
func (d *Driver) hooksDirectory() string { return filepath.Join(d.storage, "git-hooks") }

// installHook writes the pre-receive hook, which executes the program
// running the driver as `<program> git-hook`, into the hooks directory, a
// private directory of the storage. Writing it again is harmless: a
// receive-pack reads either copy whole.
func (d *Driver) installHook() error {
	dir := d.hooksDirectory()
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := privateDirectory(dir); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if strings.ContainsAny(executable, "'\n\r") {
		return errors.New("the driver's program path cannot be quoted for the hook")
	}
	temp, err := os.CreateTemp(dir, ".pre-receive-")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, err = temp.WriteString("#!/bin/sh\nexec '" + executable + "' git-hook\n")
	if err = errors.Join(err, temp.Chmod(0o700), temp.Close()); err != nil {
		return err
	}
	return os.Rename(temp.Name(), filepath.Join(dir, "pre-receive"))
}

// approval is the driver's end of one receive service's hook: a private
// socket that takes one request, checks that it names the service's head
// with valid IDs, and calls Approve once.
type approval struct {
	path, token string
	listener    net.Listener
	done        chan struct{}

	mu   sync.Mutex
	conn net.Conn
}

// startApproval opens the socket of one receive service whose one head is
// branch.
func (d *Driver) startApproval(ctx context.Context, branch string, approve sourcedriver.Approve) (*approval, error) {
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(random[:])
	path := filepath.Join(d.hooksDirectory(), "h-"+token[:16]+".sock")
	if len(path) >= 100 {
		return nil, errors.New("the hook socket's path is too long for a Unix socket")
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		return nil, err
	}
	a := &approval{path: path, token: token, listener: listener, done: make(chan struct{})}
	go a.serve(ctx, branch, approve)
	return a, nil
}

// serve answers the first connection only: a later one finds no listener.
func (a *approval) serve(ctx context.Context, branch string, approve sourcedriver.Approve) {
	defer close(a.done)
	conn, err := a.listener.Accept()
	a.listener.Close()
	if err != nil {
		return
	}
	a.mu.Lock()
	a.conn = conn
	a.mu.Unlock()
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	answer := "deny\n"
	if a.approves(ctx, conn, branch, approve) {
		answer = "ok\n"
	}
	_, _ = io.WriteString(conn, answer)
}

// approves reads the hook's request and calls Approve once if the request is
// this push's, for the service's head, with valid IDs.
func (a *approval) approves(ctx context.Context, conn net.Conn, branch string, approve sourcedriver.Approve) bool {
	line, err := bufio.NewReaderSize(conn, 1025).ReadSlice('\n')
	if err != nil || len(line) > 1024 {
		return false
	}
	var request hookRequest
	if json.Unmarshal(line, &request) != nil || subtle.ConstantTimeCompare([]byte(request.Token), []byte(a.token)) != 1 ||
		request.Ref != "refs/heads/"+branch || !oldValue(request.Old) || !gitnames.OID(request.New) || ctx.Err() != nil {
		return false
	}
	old := request.Old
	if zeroOID(old) {
		old = ""
	}
	return approve(branch, sourcedriver.OID(old), sourcedriver.OID(request.New)) == nil
}

// stop closes the socket and waits until the approval, if one runs, has
// answered: afterwards Approve is never called.
func (a *approval) stop() {
	a.listener.Close()
	a.mu.Lock()
	if a.conn != nil {
		_ = a.conn.SetReadDeadline(time.Now())
	}
	a.mu.Unlock()
	<-a.done
	os.Remove(a.path)
}

// hookEnv is the environment receive-pack passes the hook: the service's
// head, its socket and token, and the Git to check with.
func (d *Driver) hookEnv(branch string, a *approval) []string {
	return []string{"P_GIT_ALLOWED_REF=refs/heads/" + branch, "P_GIT_HOOK_SOCKET=" + a.path, "P_GIT_HOOK_TOKEN=" + a.token, "P_GIT_EXEC=" + d.gitPath}
}

// RunPreReceiveHook is the check that runs inside receive-pack as its
// pre-receive hook, reading Git's update lines from input: exactly one
// update, of the allowed head, to a commit, and a fast-forward or a
// creation; a deletion is refused. It then asks the driver over the push's
// socket, whose answer is core's approval. The receive service holds core's
// lease meanwhile, so the allowed head cannot change owner.
func RunPreReceiveHook(ctx context.Context, input io.Reader) error {
	allowed := os.Getenv("P_GIT_ALLOWED_REF")
	gitPath := os.Getenv("P_GIT_EXEC")
	if !strings.HasPrefix(allowed, "refs/heads/") || gitPath == "" || !filepath.IsAbs(gitPath) {
		return errPushRefused
	}
	data, err := io.ReadAll(io.LimitReader(input, 1025))
	if err != nil || len(data) > 1024 {
		return errPushRefused
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 1 {
		return errPushRefused
	}
	fields := strings.Split(lines[0], " ")
	if len(fields) != 3 || fields[2] != allowed || !oldValue(fields[0]) || !gitnames.OID(fields[1]) {
		return errPushRefused
	}
	old, new := fields[0], fields[1]
	if err := checkPushedCommit(ctx, gitPath, old, new); err != nil {
		return err
	}
	return requestApproval(ctx, allowed, old, new)
}

// checkPushedCommit proves new a commit and, unless the push creates the
// head, a fast-forward of old, reading the pushed objects where
// receive-pack keeps them.
func checkPushedCommit(ctx context.Context, gitPath, old, new string) error {
	env := []string{"PATH=" + filepath.Dir(gitPath), "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1"}
	for _, key := range []string{"GIT_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_QUARANTINE_PATH"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	kind := exec.CommandContext(ctx, gitPath, "cat-file", "-t", new)
	kind.Env = env
	out, err := kind.Output()
	if err != nil || strings.TrimSpace(string(out)) != "commit" {
		return errPushRefused
	}
	if zeroOID(old) {
		return nil
	}
	forward := exec.CommandContext(ctx, gitPath, "merge-base", "--is-ancestor", old, new)
	forward.Env = env
	if forward.Run() != nil {
		return errPushRefused
	}
	return nil
}

// requestApproval asks the driver over the push's socket.
func requestApproval(ctx context.Context, ref, old, new string) error {
	path, token := os.Getenv("P_GIT_HOOK_SOCKET"), os.Getenv("P_GIT_HOOK_TOKEN")
	if path == "" || len(token) != 48 {
		return fmt.Errorf("the push's approval socket is missing: %w", errPushRefused)
	}
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(conn).Encode(hookRequest{Token: token, Ref: ref, Old: old, New: new}); err != nil {
		return err
	}
	answer := make([]byte, 3)
	if _, err := io.ReadFull(conn, answer); err != nil || string(answer) != "ok\n" {
		return errPushRefused
	}
	return nil
}

// oldValue accepts a pushed update's old value: a commit ID, or zeros for a
// creation.
func oldValue(s string) bool { return gitnames.OID(s) || zeroOID(s) }

// zeroOID reports whether s is Git's all-zero ID of either length.
func zeroOID(s string) bool { return (len(s) == 40 || len(s) == 64) && strings.Trim(s, "0") == "" }
