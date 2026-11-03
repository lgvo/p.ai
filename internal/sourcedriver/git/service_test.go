package git

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/conformance"
)

// The service cases' client is a session's clone as a bare repository of
// its own, which a real git client fetches into and pushes from. Git
// reaches ServeGit through its SSH command: the test binary as a relay,
// which connects git's standard streams to the case's Serve over a Unix
// socket of the clone's.

func init() { testBinaryRoles["git-relay"] = relayRole }

// Client makes a new clone beside the fixture's storage.
func (f *fixture) Client(project string) conformance.Client {
	f.mu.Lock()
	f.clients++
	dir := filepath.Join(filepath.Dir(f.storage), "c"+strconv.Itoa(f.clients))
	f.mu.Unlock()
	if err := os.Mkdir(dir, 0o700); err != nil {
		f.t.Fatal(err)
	}
	c := &clone{t: f.t, project: project, dir: dir, repo: filepath.Join(dir, "clone.git")}
	if out, err := exec.Command("git", "init", "--quiet", "--bare", c.repo).CombinedOutput(); err != nil {
		f.t.Fatalf("git init: %v: %s", err, out)
	}
	return c
}

// SetOtherRef writes the ref as an administrator would.
func (f *fixture) SetOtherRef(project, ref string, id sourcedriver.OID) {
	f.git(project, "", "update-ref", ref, string(id))
}

// clone is a session's clone driven by the git client.
type clone struct {
	t       *testing.T
	project string
	dir     string // holds the relay's sockets
	repo    string

	mu      sync.Mutex
	counter int
}

var _ conformance.Client = (*clone)(nil)

// next numbers the clone's commits and sockets.
func (c *clone) next() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counter++
	return c.counter
}

func (c *clone) url() string { return "ssh://git@p/" + c.project }

// command is git on the clone, as a session's client runs it.
func (c *clone) command(input string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"--git-dir=" + c.repo}, args...)...)
	cmd.Stdin = strings.NewReader(input)
	// GORACE keeps a race-enabled relay from sleeping a second at its exit.
	cmd.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=P Session", "GIT_AUTHOR_EMAIL=session@example.test",
		"GIT_COMMITTER_NAME=P Session", "GIT_COMMITTER_EMAIL=session@example.test")
	return cmd
}

// must runs git on the clone and returns its trimmed output.
func (c *clone) must(input string, args ...string) string {
	c.t.Helper()
	out, err := c.command(input, args...).CombinedOutput()
	if err != nil {
		c.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (c *clone) Commit(parents ...sourcedriver.OID) sourcedriver.OID {
	args := []string{"commit-tree", string(c.Tree())}
	for _, p := range parents {
		args = append(args, "-p", string(p))
	}
	return sourcedriver.OID(c.must(fmt.Sprintf("session commit %d\n", c.next()), args...))
}

func (c *clone) Tree() sourcedriver.OID {
	return sourcedriver.OID(c.must("", "hash-object", "-w", "-t", "tree", "--stdin"))
}

func (c *clone) Has(id sourcedriver.OID) bool {
	return c.command("", "cat-file", "-e", string(id)).Run() == nil
}

func (c *clone) Advertised(serve conformance.Serve) (map[string]sourcedriver.OID, error) {
	out, err := c.overRelay(serve, "ls-remote", c.url())
	if err != nil {
		return nil, err
	}
	advertised := map[string]sourcedriver.OID{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if id, ref, ok := strings.Cut(line, "\t"); ok {
			advertised[ref] = sourcedriver.OID(id)
		}
	}
	return advertised, nil
}

func (c *clone) Fetch(serve conformance.Serve, name string) (sourcedriver.OID, error) {
	tracking := "refs/remotes/p/" + name
	if _, err := c.overRelay(serve, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", c.url(), "+refs/heads/"+name+":"+tracking); err != nil {
		return "", err
	}
	out, err := c.command("", "rev-parse", "--verify", tracking).Output()
	return sourcedriver.OID(strings.TrimSpace(string(out))), err
}

func (c *clone) Push(serve conformance.Serve, updates ...conformance.Update) error {
	args := []string{"push", "--quiet", c.url()}
	for _, u := range updates {
		if u.New == "" {
			args = append(args, ":"+u.Ref)
		} else {
			args = append(args, "+"+string(u.New)+":"+u.Ref)
		}
	}
	_, err := c.overRelay(serve, args...)
	return err
}

// overRelay runs git with args, its SSH command the test binary relaying to
// a socket of the clone's, and serves the one connection it makes. It
// returns git's output once git and serve are done.
func (c *clone) overRelay(serve conformance.Serve, args ...string) (string, error) {
	socket := filepath.Join(c.dir, "r"+strconv.Itoa(c.next())+".sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return "", err
	}
	defer listener.Close()
	cmd, err := c.relayed(socket, args...)
	if err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return "", err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		if conn, err := listener.Accept(); err == nil {
			accepted <- conn
		}
	}()
	select {
	case conn := <-accepted:
		serveRelayed(serve, conn)
		err = <-exited
	case err = <-exited:
		listener.Close()
		select {
		case conn := <-accepted:
			conn.Close()
		default:
		}
	}
	if err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// relayed is git with args on the clone, its SSH command the test binary
// relaying to socket.
func (c *clone) relayed(socket string, args ...string) (*exec.Cmd, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := c.command("", args...)
	cmd.Env = append(cmd.Env, "GIT_SSH_VARIANT=simple", "GIT_SSH_COMMAND="+relayQuote(executable)+" git-relay "+relayQuote(socket))
	return cmd, nil
}

// serveRelayed reads the SSH command the relay forwards, then hands serve
// the connection as the channel, and closes it once serve returns.
func serveRelayed(serve conformance.Serve, conn net.Conn) {
	defer conn.Close()
	var command []byte
	for one := make([]byte, 1); len(command) < 4096; command = append(command, one[0]) {
		if _, err := io.ReadFull(conn, one); err != nil {
			return
		}
		if one[0] == '\n' {
			_ = serve(string(command), sourcedriver.Stream{In: conn, Out: conn, Err: io.Discard})
			return
		}
	}
}

// relayRole is the test binary as git's SSH command: its arguments are the
// socket, then what Git passes a simple SSH command, the host and the
// command. It sends the command, then relays git's streams both ways.
func relayRole(args []string) int {
	if len(args) < 2 {
		return 2
	}
	conn, err := net.Dial("unix", args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 255
	}
	if _, err := io.WriteString(conn, args[len(args)-1]+"\n"); err != nil {
		return 255
	}
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		_ = conn.(*net.UnixConn).CloseWrite()
	}()
	_, _ = io.Copy(os.Stdout, conn)
	return 0
}

func relayQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// WantID speaks Git's protocol v0 itself, since git refuses to send a want
// the service did not advertise: it reads the advertisement, wants id
// alone, and succeeds only if the service starts a pack.
func (c *clone) WantID(serve conformance.Serve, id sourcedriver.OID) error {
	toService, fromClient := io.Pipe()
	toClient, fromService := io.Pipe()
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = serve("git-upload-pack '/"+c.project+"'", sourcedriver.Stream{In: toService, Out: fromService, Err: io.Discard})
		fromService.Close()
		toService.Close()
	}()
	err := wantByID(bufio.NewReader(toClient), fromClient, id)
	fromClient.Close()
	_, _ = io.Copy(io.Discard, toClient)
	<-served
	return err
}

func wantByID(in *bufio.Reader, out io.Writer, id sourcedriver.OID) error {
	for {
		line, err := readPacket(in)
		if err != nil {
			return err
		}
		if line == nil {
			break
		}
	}
	go func() { _, _ = io.WriteString(out, packet("want "+string(id)+"\n")+"0000"+packet("done\n")) }()
	line, err := readPacket(in)
	switch {
	case err != nil:
		return err
	case strings.HasPrefix(string(line), "ERR "):
		return errors.New(strings.TrimSpace(string(line)))
	case string(line) != "NAK\n":
		return fmt.Errorf("the service answered %q", line)
	}
	signature := make([]byte, 4)
	if _, err := io.ReadFull(in, signature); err != nil || string(signature) != "PACK" {
		return errors.New("the service sent no pack")
	}
	return nil
}

func packet(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

// readPacket reads one pkt-line: its payload, or nil for a flush.
func readPacket(in *bufio.Reader) ([]byte, error) {
	var size [4]byte
	if _, err := io.ReadFull(in, size[:]); err != nil {
		return nil, err
	}
	n, err := strconv.ParseUint(string(size[:]), 16, 16)
	if err != nil || n != 0 && n < 4 {
		return nil, fmt.Errorf("a malformed pkt-line length %q", size)
	}
	if n == 0 {
		return nil, nil
	}
	line := make([]byte, n-4)
	_, err = io.ReadFull(in, line)
	return line, err
}

func TestTheProtectiveOptionsOverrideEarlierOnesAndTheRepositorysConfig(t *testing.T) {
	f := newFixture(t).(*fixture)
	if _, err := f.driver.CreateRepository(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}
	repo, err := f.driver.repository("app")
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"gc.auto": "0", "maintenance.auto": "false", "receive.autogc": "false"} {
		f.git("app", "", "config", key, "true")
		for _, kind := range []sourcedriver.ServiceKind{sourcedriver.ServiceUpload, sourcedriver.ServiceReceive} {
			args := append([]string{"-c", key + "=true"}, transportGitConfigArgs(kind, f.driver.hooksDirectory(), 1024)...)
			got, err := f.driver.run(t.Context(), repo, nil, 4096, append(args, "--git-dir="+repo, "config", "--get", key)...)
			if err != nil || got.Exit != 0 || strings.TrimSpace(got.Stdout) != want {
				t.Errorf("the %s service reads %s as %q (%v), want %q", kind, key, got.Stdout, err, want)
			}
		}
	}
}

func TestAClosedDriverEndsItsServicesAndRefusesNewOnesBeforeOpen(t *testing.T) {
	f := newFixture(t).(*fixture)
	if _, err := f.driver.CreateRepository(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}
	never, _ := net.Pipe()
	t.Cleanup(func() { never.Close() })
	started := &firstWrite{started: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		result <- f.driver.ServeGit(context.Background(), "git-upload-pack '/app'", sourcedriver.Stream{In: never, Out: started}, openService("closing"))
	}()
	<-started.started
	if got := settleService(t, f.driver, "closing"); got != sourcedriver.Unknown {
		t.Fatalf("a running service settles %v", got)
	}
	if err := f.driver.Close(); err != nil {
		t.Fatal(err)
	}
	if got := settleService(t, f.driver, "closing"); got != sourcedriver.Applied {
		t.Errorf("after Close its service settles %v, want applied", got)
	}
	select {
	case err := <-result:
		if !errors.Is(err, sourcedriver.ErrUnavailable) {
			t.Errorf("a service Close ended returned %v, want ErrUnavailable", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a service Close ended did not return")
	}
	err := f.driver.ServeGit(t.Context(), "git-upload-pack '/app'", sourcedriver.Stream{}, func(sourcedriver.ServiceKind, string) (sourcedriver.Service, sourcedriver.Approve, error) {
		t.Error("a closed driver opened a service")
		return sourcedriver.Service{}, nil, errors.New("refused")
	})
	if !errors.Is(err, sourcedriver.ErrUnavailable) {
		t.Errorf("ServeGit on a closed driver = %v, want ErrUnavailable", err)
	}
}

func TestServeGitRefusesAServiceOpenAuthorizedForAnotherCommand(t *testing.T) {
	f := newFixture(t).(*fixture)
	if _, err := f.driver.CreateRepository(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}
	valid := sourcedriver.Service{ID: "s", Project: "app", Kind: sourcedriver.ServiceReceive, Branch: "work", MaxInput: 1 << 20, Deadline: time.Minute}
	for _, c := range []struct {
		what   string
		change func(*sourcedriver.Service)
	}{
		{"another kind", func(s *sourcedriver.Service) { s.Kind = sourcedriver.ServiceUpload }},
		{"another project", func(s *sourcedriver.Service) { s.Project = "other" }},
		{"no ID", func(s *sourcedriver.Service) { s.ID = "" }},
		{"no input ceiling", func(s *sourcedriver.Service) { s.MaxInput = 0 }},
		{"no deadline", func(s *sourcedriver.Service) { s.Deadline = 0 }},
		{"an invalid branch", func(s *sourcedriver.Service) { s.Branch = "../main" }},
	} {
		service := valid
		c.change(&service)
		stream := &usedStream{}
		err := f.driver.ServeGit(t.Context(), "git-receive-pack '/app'", sourcedriver.Stream{In: stream, Out: stream}, func(sourcedriver.ServiceKind, string) (sourcedriver.Service, sourcedriver.Approve, error) {
			return service, func(string, sourcedriver.OID, sourcedriver.OID) error { return nil }, nil
		})
		if err == nil || stream.used {
			t.Errorf("a service with %s: %v, stream used %t", c.what, err, stream.used)
		}
	}
}

func TestAServiceDeadlineKillsGitsWholeProcessGroup(t *testing.T) {
	f := newFixture(t).(*fixture)
	if _, err := f.driver.CreateRepository(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}
	tip := f.Commit("app")
	f.SetHead("app", "work", tip)
	client := f.Client("app")
	fetch := func(command string, stream sourcedriver.Stream) error {
		return f.driver.ServeGit(t.Context(), command, stream, openService("fetch"))
	}
	if _, err := client.Fetch(fetch, "work"); err != nil {
		t.Fatal(err)
	}
	next := client.Commit(tip)
	approving, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	push := func(command string, stream sourcedriver.Stream) error {
		return f.driver.ServeGit(t.Context(), command, stream, func(kind sourcedriver.ServiceKind, project string) (sourcedriver.Service, sourcedriver.Approve, error) {
			service := sourcedriver.Service{ID: "push", Project: project, Kind: kind, Branch: "work", MaxInput: 1 << 20, Deadline: time.Second}
			return service, func(string, sourcedriver.OID, sourcedriver.OID) error {
				close(approving)
				<-release
				return nil
			}, nil
		})
	}
	pushed := make(chan error, 1)
	go func() { pushed <- client.Push(push, conformance.Update{Ref: "refs/heads/work", New: next}) }()
	<-approving
	repo, _ := f.driver.repository("app")
	group := receivePackGroup(t, repo)
	if members := liveGroupMembers(group); len(members) < 2 {
		t.Fatalf("receive-pack's process group %d holds %v; want receive-pack and its hook", group, members)
	}
	// The deadline passes a second after the service started; the hook
	// alone would wait 5 seconds for its answer.
	for limit := time.Now().Add(3 * time.Second); len(liveGroupMembers(group)) > 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(limit) {
			t.Fatalf("processes %v of the service outlived its deadline", liveGroupMembers(group))
		}
	}
	once.Do(func() { close(release) })
	if err := <-pushed; err == nil {
		t.Error("a push cut off by its deadline succeeded")
	}
	if got := f.git("app", "", "rev-parse", "refs/heads/work"); got != string(tip) {
		t.Errorf("refs/heads/work moved to %s", got)
	}
}

// openService authorizes any command as the service id, for the head work.
func openService(id string) sourcedriver.Open {
	return func(kind sourcedriver.ServiceKind, project string) (sourcedriver.Service, sourcedriver.Approve, error) {
		service := sourcedriver.Service{ID: id, Project: project, Kind: kind, Branch: "work", MaxInput: 1 << 30, Deadline: time.Minute}
		return service, func(string, sourcedriver.OID, sourcedriver.OID) error { return nil }, nil
	}
}

func settleService(t *testing.T, d *Driver, id string) sourcedriver.Settlement {
	t.Helper()
	got, err := d.Settle(t.Context(), sourcedriver.Effect{Kind: sourcedriver.EffectService, Project: "app", Service: id})
	if err != nil {
		t.Fatalf("Settle(%s): %v", id, err)
	}
	return got
}

// firstWrite discards a service's output and closes started at its first
// byte.
type firstWrite struct {
	once    sync.Once
	started chan struct{}
}

func (w *firstWrite) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	return len(p), nil
}

// usedStream records whether a service read or wrote it.
type usedStream struct{ used bool }

func (s *usedStream) Read([]byte) (int, error)    { s.used = true; return 0, io.EOF }
func (s *usedStream) Write(p []byte) (int, error) { s.used = true; return len(p), nil }

// receivePackGroup finds the receive-pack serving repo, the leader of its
// process group.
func receivePackGroup(t *testing.T, repo string) int {
	t.Helper()
	paths, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	for _, path := range paths {
		data, err := os.ReadFile(path)
		args := strings.Split(string(data), "\x00")
		if err != nil || !slices.Contains(args, "receive-pack") || !slices.Contains(args, repo) {
			continue
		}
		pid, _ := strconv.Atoi(filepath.Base(filepath.Dir(path)))
		if group, _ := processGroupOf(pid); group == pid {
			return pid
		}
	}
	t.Fatal("no receive-pack leads a process group of its own")
	return 0
}

// liveGroupMembers are the processes of the group that have not ended.
func liveGroupMembers(group int) []int {
	var members []int
	paths, _ := filepath.Glob("/proc/[0-9]*/stat")
	for _, path := range paths {
		pid, _ := strconv.Atoi(filepath.Base(filepath.Dir(path)))
		if g, state := processGroupOf(pid); g == group && state != "Z" {
			members = append(members, pid)
		}
	}
	return members
}

// processGroupOf reads a process's group and state from /proc.
func processGroupOf(pid int) (group int, state string) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, ""
	}
	// The command name is in parentheses and may hold spaces: the fields
	// after it are the state, the parent and the group.
	fields := strings.Fields(string(data[bytes.LastIndexByte(data, ')')+1:]))
	if len(fields) < 3 {
		return 0, ""
	}
	group, _ = strconv.Atoi(fields[2])
	return group, fields[0]
}
