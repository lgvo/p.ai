package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/protocol"
	"github.com/lgvo/p.ai/internal/sourcedriver/supervise"
)

// The names the tests start this test binary under: as executableName it
// runs main, the driver itself, without a separate build; as relayName it
// is the ssh a Git client runs, which relays the command and its channel
// to the Unix socket in P_TEST_RELAY.
const (
	executableName = "p-source-git"
	relayName      = "ssh-relay"
)

func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case executableName:
		main()
		os.Exit(0)
	case relayName:
		os.Exit(relay(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func TestTheExecutableTakesNoArgumentsButTheHooks(t *testing.T) {
	out, err := exec.Command(installed(t, shortDir(t), executableName), "--storage", "/var/lib/p").CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), "takes no arguments") {
		t.Fatalf("started with arguments: %v, %q", err, out)
	}
}

// The built driver, started as the daemon starts it, takes its settings from
// the hello: the bundled driver's refusal of a config that names no git or
// ssh comes back, and the driver exits.
func TestTheExecutableServesTheBundledDriverConfiguredByTheHello(t *testing.T) {
	path := installed(t, shortDir(t), executableName)
	_, err := supervise.Start(t.Context(), supervise.Config{Path: path, SHA256: digest(t, path),
		Hello: protocol.Hello{Config: json.RawMessage(`{}`), Core: protocol.Core{Storage: filepath.Join(shortDir(t), "source")}}})
	if err == nil || !strings.Contains(err.Error(), "the git source driver's config") {
		t.Fatalf("Start with a config naming no git or ssh = %v, want the bundled driver's refusal", err)
	}
}

// A push through the driver's process, started as the daemon starts it,
// runs the driver's pre-receive hook, which executes the driver's own file
// as `p-source-git git-hook`: core's Approve is called with the update,
// and the head moves.
func TestAPushThroughTheExecutableRunsItsHook(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git unavailable")
	}
	dir := shortDir(t)
	path := installed(t, dir, executableName)
	config, err := json.Marshal(map[string]string{"git": git, "ssh": "/nonexistent/ssh"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	driver, err := supervise.Start(t.Context(), supervise.Config{Path: path, SHA256: digest(t, path),
		Hello: protocol.Hello{Config: config, Core: protocol.Core{Storage: filepath.Join(dir, "source")}}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer driver.Close()
	if _, err := driver.CreateRepository(t.Context(), "app"); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	clone := filepath.Join(dir, "clone")
	gitIn(t, "", "init", "--quiet", clone)
	gitIn(t, clone, "commit", "--quiet", "--allow-empty", "--message=first")
	tip := sourcedriver.OID(strings.TrimSpace(gitIn(t, clone, "rev-parse", "HEAD")))

	socket := filepath.Join(dir, "relay.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	type approval struct {
		branch   string
		old, new sourcedriver.OID
	}
	var mu sync.Mutex
	var approvals []approval
	served := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			served <- err
			return
		}
		defer conn.Close()
		in := bufio.NewReader(conn)
		command, err := in.ReadString('\n')
		if err != nil {
			served <- err
			return
		}
		served <- driver.ServeGit(t.Context(), strings.TrimSuffix(command, "\n"), sourcedriver.Stream{In: in, Out: conn, Err: os.Stderr},
			func(kind sourcedriver.ServiceKind, project string) (sourcedriver.Service, sourcedriver.Approve, error) {
				service := sourcedriver.Service{ID: "push", Project: project, Kind: kind, Branch: "main", MaxInput: 1 << 30, Deadline: time.Minute}
				return service, func(branch string, old, new sourcedriver.OID) error {
					mu.Lock()
					defer mu.Unlock()
					approvals = append(approvals, approval{branch, old, new})
					return nil
				}, nil
			})
	}()
	push := exec.Command(git, "-C", clone, "push", "--quiet", "ssh://p/app", "HEAD:refs/heads/main")
	push.Env = append(os.Environ(), "GIT_SSH_COMMAND="+installed(t, dir, relayName), "GIT_SSH_VARIANT=simple", "P_TEST_RELAY="+socket)
	if out, err := push.CombinedOutput(); err != nil {
		t.Fatalf("git push: %v: %s", err, out)
	}
	if err := <-served; err != nil {
		t.Fatalf("ServeGit: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(approvals) != 1 || approvals[0] != (approval{"main", "", tip}) {
		t.Fatalf("Approve was called with %+v, want once with main from nothing to %s", approvals, tip)
	}
	if got, ok, err := driver.InspectBranch(t.Context(), "app", "main"); err != nil || !ok || got != tip {
		t.Fatalf("after the push refs/heads/main is %q, %t, %v; want %s", got, ok, err, tip)
	}
}

// relay is the ssh a Git client runs as `ssh host command`: it sends the
// command as one line to the socket in P_TEST_RELAY, then relays its input
// and output until the socket ends.
func relay(args []string) int {
	if len(args) < 2 {
		return 2
	}
	conn, err := net.Dial("unix", os.Getenv("P_TEST_RELAY"))
	if err != nil {
		return 255
	}
	if _, err := io.WriteString(conn, args[len(args)-1]+"\n"); err != nil {
		return 255
	}
	go func() {
		io.Copy(conn, os.Stdin)
		conn.(*net.UnixConn).CloseWrite()
	}()
	io.Copy(os.Stdout, conn)
	return 0
}

// gitIn runs git in dir, as a session's client would, and returns its
// output.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=P Test", "GIT_AUTHOR_EMAIL=p@example.test",
		"GIT_COMMITTER_NAME=P Test", "GIT_COMMITTER_EMAIL=p@example.test", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

// shortDir is a private temporary directory with a short path: the Git
// service's per-push sockets live in the driver's storage, and a Unix
// socket's path must stay under 108 bytes.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "psg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// installed copies this test binary into dir as name, with mode 0700, and
// returns its path.
func installed(t *testing.T, dir, name string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func digest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
