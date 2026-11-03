package supervise

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimedriver/memory"
	"github.com/lgvo/p.ai/internal/runtimedriver/protocol"
)

// The test binary is also the driver the tests start: copied under one of
// these names, it serves a driver instead of running the tests.
const (
	// fixtureDriver serves the memory driver, which dies in Start and
	// HostLog of a session whose reference is crashRef and answers HostLog
	// with what it was started with.
	fixtureDriver = "p-driver-fixture"
	// fixtureOtherVersion answers the hello as a driver of another
	// protocol version.
	fixtureOtherVersion = "p-driver-fixture-v0"
	// fixtureDurable serves the durable driver, whose backend's state
	// outlives its process.
	fixtureDurable = "p-driver-fixture-durable"
)

const crashRef = "crash"

func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case fixtureDriver:
		os.Exit(serveFixture())
	case fixtureOtherVersion:
		os.Exit(answerOtherVersion())
	case fixtureDurable:
		os.Exit(serveDriver(openDurable))
	}
	os.Exit(m.Run())
}

var fixtureCapabilities = runtimedriver.Capabilities{
	ImageBuilder: "memory/1", Networks: []string{runtimedriver.NetworkNone}, Services: true,
}

func serveFixture() int {
	world := memory.NewWorld(memory.Config{Capacity: 64, Capabilities: fixtureCapabilities})
	return serveDriver(func(context.Context, protocol.Hello) (runtimedriver.Driver, error) {
		return crashing{memory.NewDriver(world)}, nil
	})
}

// serveDriver serves the driver open makes on the connection the
// supervisor gave, and returns the process's exit status.
func serveDriver(open protocol.Open) int {
	c, err := protocol.Inherited()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := protocol.Serve(context.Background(), c, open); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// started is what the fixture driver reports of how it was started.
type started struct {
	PID  int      `json:"pid"`
	Args []string `json:"args"`
	Env  []string `json:"env"`
}

// crashing is the memory driver, but its process dies inside Start and
// HostLog of the session crashRef, and HostLog reports how it was started.
type crashing struct{ *memory.Driver }

func (d crashing) Start(ctx context.Context, s runtimedriver.Session) (runtimedriver.Observation, error) {
	if s.Ref.ID == crashRef {
		os.Exit(3)
	}
	return d.Driver.Start(ctx, s)
}

func (d crashing) HostLog(_ context.Context, t runtimedriver.Target) (string, error) {
	if t.Session.Ref.ID == crashRef {
		os.Exit(3)
	}
	return startedJSON()
}

// startedJSON is how this process was started.
func startedJSON() (string, error) {
	raw, err := json.Marshal(started{PID: os.Getpid(), Args: os.Args, Env: os.Environ()})
	return string(raw), err
}

// The sessions whose create kills the durable driver's process once its
// gate admitted it: before the create's effect, or after it and before the
// answer.
const (
	dieBeforeCreating = "die-before-creating"
	dieAfterCreating  = "die-after-creating"
)

// durable is a driver whose backend keeps its state in a directory outside
// the driver's process, which its hello's config names, so that a
// restarted driver settles what one that died left: a session's runtime is
// a file named by its reference, an attachment a file named by its
// terminal's ID that says whether its guest process ended, and the log has a
// line for every create asked and every create applied. A create holds the
// session's admission lock from before its gate until it returns or its
// process dies, so while no process holds it no create can still apply its
// effect. HostLog reports how the process was started.
type durable struct {
	runtimedriver.Driver // nil: the operations the tests do not use
	state                string
}

func openDurable(_ context.Context, h protocol.Hello) (runtimedriver.Driver, error) {
	var config struct {
		State string `json:"state"`
	}
	if json.Unmarshal(h.Config, &config) != nil || config.State == "" {
		return nil, fmt.Errorf("the durable driver's config %s names no state directory", h.Config)
	}
	return durable{state: config.State}, nil
}

func (d durable) Describe() runtimedriver.Capabilities { return fixtureCapabilities }
func (d durable) Check(context.Context) error          { return nil }
func (d durable) HostLog(context.Context, runtimedriver.Target) (string, error) {
	return startedJSON()
}

func (d durable) Create(_ context.Context, s runtimedriver.Session, _ runtimedriver.Incarnation, g runtimedriver.Gate) (runtimedriver.Observation, error) {
	id := s.Ref.ID
	if err := d.log("create asked " + id); err != nil {
		return runtimedriver.Observation{}, err
	}
	admission, err := d.admission(id, syscall.LOCK_EX)
	if err != nil {
		return runtimedriver.Observation{}, err
	}
	defer admission.Close()
	if err := g(); err != nil {
		return runtimedriver.Observation{}, err
	}
	if id == dieBeforeCreating {
		die()
	}
	if err := os.WriteFile(d.path(id), nil, 0o600); err != nil {
		return runtimedriver.Observation{}, err
	}
	if err := d.log("create applied " + id); err != nil {
		return runtimedriver.Observation{}, err
	}
	if id == dieAfterCreating {
		die()
	}
	return runtimedriver.Observation{State: runtimedriver.StateStopped, Incarnation: "1"}, nil
}

func (d durable) OpenTerminal(_ context.Context, t runtimedriver.Target, _ runtimedriver.Size) (runtimedriver.Terminal, error) {
	id := "terminal-" + t.Session.Ref.ID
	if err := os.WriteFile(d.path(id), []byte("attached"), 0o600); err != nil {
		return nil, err
	}
	return newAttachment(id), nil
}

// Settle answers from the state directory only. A create is applied when
// its runtime is there, and not applied when it is not and no process holds
// its admission; an attachment is applied once its guest process ended.
func (d durable) Settle(_ context.Context, e runtimedriver.Effect) (runtimedriver.Settlement, error) {
	switch e.Kind {
	case runtimedriver.EffectCreate:
		id := e.Target.Session.Ref.ID
		admission, err := d.admission(id, syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case errors.Is(err, syscall.EWOULDBLOCK):
			return runtimedriver.Unknown, nil // a running create may still apply it
		case err != nil:
			return runtimedriver.Unknown, err
		}
		defer admission.Close()
		switch _, err := os.Stat(d.path(id)); {
		case err == nil:
			return runtimedriver.Applied, nil
		case errors.Is(err, fs.ErrNotExist):
			return runtimedriver.NotApplied, nil
		default:
			return runtimedriver.Unknown, err
		}
	case runtimedriver.EffectTerminal:
		state, err := os.ReadFile(d.path(e.Terminal))
		if err != nil || string(state) != "ended" {
			return runtimedriver.Unknown, err
		}
		return runtimedriver.Applied, nil
	}
	return runtimedriver.Unknown, fmt.Errorf("the durable driver settles no %s", e.Kind)
}

func (d durable) path(name string) string { return filepath.Join(d.state, name) }

// admission takes the admission lock of the create of session id, as how
// says.
func (d durable) admission(id string, how int) (*os.File, error) {
	f, err := os.OpenFile(d.path(id+".admission"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (d durable) log(line string) error {
	f, err := os.OpenFile(d.path("log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, line)
	return errors.Join(err, f.Close())
}

// die kills the driver's process at once, as a crash would.
func die() {
	syscall.Kill(os.Getpid(), syscall.SIGKILL)
	select {}
}

// logged counts the lines of the durable driver's log in state that are
// line.
func logged(t *testing.T, state, line string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(state, "log"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	n := 0
	for _, l := range strings.Split(string(raw), "\n") {
		if l == line {
			n++
		}
	}
	return n
}

// endGuest ends the guest process of the durable driver's attachment of
// terminal id, in the backend's state.
func endGuest(t *testing.T, state, id string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(state, id), []byte("ended"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// attachment is a durable driver's terminal: it has no output, discards
// its input, and ends unproven when it is closed.
type attachment struct {
	id     string
	closed chan struct{}
	ended  chan runtimedriver.Completion
	close  func()
}

func newAttachment(id string) *attachment {
	a := &attachment{id: id, closed: make(chan struct{}), ended: make(chan runtimedriver.Completion, 1)}
	a.close = sync.OnceFunc(func() {
		close(a.closed)
		a.ended <- runtimedriver.Completion{}
		close(a.ended)
	})
	return a
}

func (a *attachment) ID() string                             { return a.id }
func (a *attachment) Read([]byte) (int, error)               { <-a.closed; return 0, io.EOF }
func (a *attachment) Write(p []byte) (int, error)            { return len(p), nil }
func (a *attachment) Resize(runtimedriver.Size) error        { return nil }
func (a *attachment) Ended() <-chan runtimedriver.Completion { return a.ended }
func (a *attachment) Close() error                           { a.close(); return nil }

// answerOtherVersion reads the hello and answers it as a driver of
// protocol p.runtime-driver/v0 would.
func answerOtherVersion() int {
	c, err := protocol.Inherited()
	if err != nil {
		return 2
	}
	r := bufio.NewReader(c)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return 1
	}
	var hello struct {
		ID uint64 `json:"id"`
	}
	if json.Unmarshal(line, &hello) != nil {
		return 1
	}
	fmt.Fprintf(c, `{"jsonrpc":"2.0","id":%d,"result":{"protocol":"p.runtime-driver/v0","capabilities":{"image_builder":"x","builder_policy":"","networks":[],"egress_addresses":0,"services":false}}}`+"\n", hello.ID)
	io.Copy(io.Discard, r)
	return 0
}

// installed copies this test binary into a new private directory as name,
// with mode 0700, and returns its path and SHA-256.
func installed(t *testing.T, name string) (string, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return install(t, filepath.Join(t.TempDir(), name), self)
}

// install copies the file at from to path with mode 0700 and returns path
// and its SHA-256.
func install(t *testing.T, path, from string) (string, string) {
	t.Helper()
	src, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(dst, h), src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	return path, hex.EncodeToString(h.Sum(nil))
}

// script writes a shell script to path with mode 0700 and returns its
// SHA-256.
func script(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

var anyHello = protocol.Hello{Config: json.RawMessage(`{}`)}
