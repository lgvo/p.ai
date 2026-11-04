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
	"os"
	"path/filepath"
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/memory"
	"github.com/lgvo/p.ai/internal/sourcedriver/protocol"
)

// The test binary is also the driver the tests start: copied under one of
// these names, it serves a driver instead of running the tests.
const (
	// fixtureDriver serves the fixture driver.
	fixtureDriver = "p-source-fixture"
	// fixtureOtherVersion answers the hello as a driver of another
	// protocol version.
	fixtureOtherVersion = "p-source-fixture-v0"
)

// The projects whose calls kill the fixture driver's process: a read, and
// a head creation once its gate admitted it.
const crash = "crash"

// whoami is the project whose InspectRepository reports how the fixture
// driver's process was started, in the repository's incarnation.
const whoami = "whoami"

func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case fixtureDriver:
		os.Exit(serveFixture())
	case fixtureOtherVersion:
		os.Exit(answerOtherVersion())
	}
	os.Exit(m.Run())
}

// started is what the fixture driver reports of how it was started: its
// process, and the settings of core's its hello carried.
type started struct {
	PID  int           `json:"pid"`
	Args []string      `json:"args"`
	Env  []string      `json:"env"`
	Core protocol.Core `json:"core"`
}

// fixture is the memory driver, over a World of its process's own, with a
// state directory its hello's config names, which outlives its process:
// Describe names the backend in the file backend there, if any, Check
// fails while the file unchecked is there and logs each check, and the
// projects crash and whoami behave as their constants say.
type fixture struct {
	*memory.Driver
	state string
	core  protocol.Core
}

func serveFixture() int {
	c, err := protocol.Inherited()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	err = protocol.Serve(context.Background(), c, func(_ context.Context, h protocol.Hello) (sourcedriver.Driver, error) {
		var config struct {
			State string `json:"state"`
		}
		if json.Unmarshal(h.Config, &config) != nil || config.State == "" {
			return nil, fmt.Errorf("the fixture's config %s names no state directory", h.Config)
		}
		return &fixture{Driver: memory.NewDriver(memory.NewWorld()), state: config.State, core: h.Core}, nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func (d *fixture) Describe() sourcedriver.Description {
	description := d.Driver.Describe()
	if backend, err := os.ReadFile(filepath.Join(d.state, "backend")); err == nil {
		description.Backend = string(backend)
	}
	return description
}

func (d *fixture) Check(ctx context.Context) error {
	f, err := os.OpenFile(filepath.Join(d.state, "checks"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, os.Getpid())
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(d.state, "unchecked")); err == nil {
		return errors.New("the fixture's storage is unchecked")
	}
	return d.Driver.Check(ctx)
}

func (d *fixture) InspectRepository(ctx context.Context, project string) (sourcedriver.Repository, error) {
	if project == whoami {
		raw, err := json.Marshal(started{PID: os.Getpid(), Args: os.Args, Env: os.Environ(), Core: d.core})
		return sourcedriver.Repository{Project: whoami, Exists: true, Incarnation: string(raw)}, err
	}
	return d.Driver.InspectRepository(ctx, project)
}

func (d *fixture) Branches(ctx context.Context, project string) ([]sourcedriver.Head, error) {
	if project == crash {
		os.Exit(3)
	}
	return d.Driver.Branches(ctx, project)
}

func (d *fixture) CreateBranch(ctx context.Context, project, name string, at sourcedriver.OID, g sourcedriver.Gate) error {
	if project == crash {
		if err := g(); err != nil {
			return err
		}
		os.Exit(3)
	}
	return d.Driver.CreateBranch(ctx, project, name, at, g)
}

// answerOtherVersion reads the hello and answers it as a driver of
// protocol p.source-driver/v0 would.
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
	fmt.Fprintf(c, `{"jsonrpc":"2.0","id":%d,"result":{"protocol":"p.source-driver/v0","capabilities":{"backend":"memory","version":"1","commands":[]}}}`+"\n", hello.ID)
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
	src, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	path := filepath.Join(t.TempDir(), name)
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
