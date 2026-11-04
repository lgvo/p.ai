package launch

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/driverwire"
)

// fixtureDriver is the name under which the test binary, copied, is the
// driver the tests start instead of running the tests. Its hello is one
// line, in which it reports how it was started; it then exits once its
// connection ends.
const fixtureDriver = "p-launch-fixture"

// fixtureParent is the fixture driver, which first starts a child in its
// process group, as a driver runs a program, and reports it. The child is
// the test binary again, as fixtureChild, which sleeps until it is killed.
const (
	fixtureParent = "p-launch-fixture-parent"
	fixtureChild  = "p-launch-fixture-child"
)

func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case fixtureDriver, fixtureParent:
		os.Exit(serveFixture())
	case fixtureChild:
		// Not select{}: with no other goroutine, Go ends that as a
		// deadlock.
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(m.Run())
}

// started is what the fixture driver reports of how it was started, and
// the child it started, if any.
type started struct {
	PID   int      `json:"pid"`
	Args  []string `json:"args"`
	Env   []string `json:"env"`
	Child int      `json:"child"`
}

func serveFixture() int {
	c, err := (&driverwire.Protocol{Name: "fixture driver"}).Inherited()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	report := started{PID: os.Getpid(), Args: os.Args, Env: os.Environ()}
	if filepath.Base(os.Args[0]) == fixtureParent {
		child, err := os.StartProcess("/proc/self/exe", []string{fixtureChild}, &os.ProcAttr{Files: []*os.File{nil, nil, os.Stderr}})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		report.Child = child.Pid
	}
	if err := json.NewEncoder(c).Encode(report); err != nil {
		return 1
	}
	io.Copy(io.Discard, c)
	return 0
}

// fixtureConn is the connection to the fixture driver, with its report.
type fixtureConn struct {
	uc      *net.UnixConn
	started started
	done    chan struct{}
}

func (c *fixtureConn) Close() error          { return c.uc.Close() }
func (c *fixtureConn) Done() <-chan struct{} { return c.done }

// fixtureHello reads the fixture driver's report.
func fixtureHello(ctx context.Context, uc *net.UnixConn) (*fixtureConn, error) {
	if deadline, ok := ctx.Deadline(); ok {
		uc.SetReadDeadline(deadline)
	}
	r := bufio.NewReader(uc)
	line, err := r.ReadBytes('\n')
	c := &fixtureConn{uc: uc, done: make(chan struct{})}
	if err == nil {
		err = json.Unmarshal(line, &c.started)
	}
	if err != nil {
		uc.Close()
		return nil, err
	}
	uc.SetReadDeadline(time.Time{})
	go func() {
		io.Copy(io.Discard, r)
		close(c.done)
	}()
	return c, nil
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
