package protocol_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/protocol"
)

// waitLimit bounds every wait, so a call that never returns fails its test.
const waitLimit = 10 * time.Second

// scripted is a driver whose calls a test defines; a call it did not
// define panics.
type scripted struct {
	sourcedriver.Driver
	check           func(context.Context) error
	checkNames      func(context.Context, []sourcedriver.Name) ([]sourcedriver.Name, error)
	branches        func(context.Context, string) ([]sourcedriver.Head, error)
	createBranch    func(context.Context, string, string, sourcedriver.OID, sourcedriver.Gate) error
	losingCommits   func(context.Context, string, []string) (sourcedriver.Loss, error)
	renameWorkspace func(context.Context, sourcedriver.WorkspaceRename, []sourcedriver.WorkspaceFile) (sourcedriver.WorkspaceEdits, error)
	serveGit        func(context.Context, string, sourcedriver.Stream, sourcedriver.Open) error
	settle          func(context.Context, sourcedriver.Effect) (sourcedriver.Settlement, error)
}

var description = sourcedriver.Description{Backend: "scripted", Version: "7", Commands: []string{"git-upload-pack", "git-receive-pack"}}

func (d *scripted) Describe() sourcedriver.Description { return description }
func (d *scripted) Check(ctx context.Context) error    { return d.check(ctx) }
func (d *scripted) CheckNames(ctx context.Context, names []sourcedriver.Name) ([]sourcedriver.Name, error) {
	return d.checkNames(ctx, names)
}
func (d *scripted) Branches(ctx context.Context, project string) ([]sourcedriver.Head, error) {
	return d.branches(ctx, project)
}
func (d *scripted) CreateBranch(ctx context.Context, project, name string, at sourcedriver.OID, g sourcedriver.Gate) error {
	return d.createBranch(ctx, project, name, at, g)
}
func (d *scripted) LosingCommits(ctx context.Context, project string, removed []string) (sourcedriver.Loss, error) {
	return d.losingCommits(ctx, project, removed)
}
func (d *scripted) RenameWorkspace(ctx context.Context, r sourcedriver.WorkspaceRename, files []sourcedriver.WorkspaceFile) (sourcedriver.WorkspaceEdits, error) {
	return d.renameWorkspace(ctx, r, files)
}
func (d *scripted) ServeGit(ctx context.Context, command string, stream sourcedriver.Stream, open sourcedriver.Open) error {
	return d.serveGit(ctx, command, stream, open)
}
func (d *scripted) Settle(ctx context.Context, e sourcedriver.Effect) (sourcedriver.Settlement, error) {
	return d.settle(ctx, e)
}

var anyHello = protocol.Hello{Config: json.RawMessage(`{}`)}

func TestTheHelloAgreesTheVersionAndCarriesTheConfiguration(t *testing.T) {
	core, driverEnd := pair(t)
	hello := protocol.Hello{Config: json.RawMessage(`{"git":"/run/current-system/sw/bin/git","ssh":"/run/current-system/sw/bin/ssh"}`),
		Core: protocol.Core{Storage: "/var/lib/p", Executable: "/nix/store/0000-p/bin/p-source-git"}}
	got := make(chan protocol.Hello, 1)
	go protocol.Serve(t.Context(), driverEnd, func(_ context.Context, h protocol.Hello) (sourcedriver.Driver, error) {
		got <- h
		return &scripted{}, nil
	})
	client, err := protocol.Connect(t.Context(), core, hello)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()
	h := <-got
	if string(h.Config) != string(hello.Config) || h.Core != hello.Core {
		t.Fatalf("the driver got hello %+v (config %s), want %+v (config %s)", h.Core, h.Config, hello.Core, hello.Config)
	}
	if d := client.Describe(); !reflect.DeepEqual(d, description) {
		t.Fatalf("Describe = %+v, want the driver's %+v", d, description)
	}
}

func TestADriverRefusesCoreSpeakingAnotherVersion(t *testing.T) {
	core, driverEnd := pair(t)
	defer core.Close()
	var opened atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- protocol.Serve(context.Background(), driverEnd, func(context.Context, protocol.Hello) (sourcedriver.Driver, error) {
			opened.Add(1)
			return &scripted{}, nil
		})
	}()
	core.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"hello","params":{"protocol":"p.source-driver/v0","config":{},"core":{"storage":"/var/lib/p"}}}` + "\n"))
	core.SetReadDeadline(time.Now().Add(waitLimit))
	line, err := bufio.NewReader(core).ReadBytes('\n')
	var res struct {
		Error *struct {
			Code int `json:"code"`
			Data struct {
				Supported []string `json:"supported"`
			} `json:"data"`
		} `json:"error"`
	}
	if err != nil || json.Unmarshal(line, &res) != nil || res.Error == nil || res.Error.Code != -32001 || !slices.Equal(res.Error.Data.Supported, []string{protocol.Version}) {
		t.Fatalf("a hello of v0 answered %q, %v; want a version refusal naming %s", line, err, protocol.Version)
	}
	if err := wait(t, done); err == nil || !strings.Contains(err.Error(), "p.source-driver/v0") {
		t.Fatalf("Serve = %v, want the refused version", err)
	}
	if opened.Load() != 0 {
		t.Fatal("the driver was made for a refused hello")
	}
}

func TestCoreRefusesADriverSpeakingAnotherVersion(t *testing.T) {
	core, driverEnd := pair(t)
	go func() {
		if _, err := bufio.NewReader(driverEnd).ReadBytes('\n'); err != nil {
			return
		}
		driverEnd.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocol":"p.source-driver/v0","capabilities":{"backend":"git","version":"1","commands":[]}}}` + "\n"))
	}()
	if client, err := protocol.Connect(t.Context(), core, anyHello); err == nil || !strings.Contains(err.Error(), "p.source-driver/v0") {
		if client != nil {
			client.Close()
		}
		t.Fatalf("Connect to a driver of v0 = %v, want a refusal naming the version", err)
	}
}

func TestADriverThatCannotBeConfiguredRefusesTheHello(t *testing.T) {
	core, driverEnd := pair(t)
	done := make(chan error, 1)
	go func() {
		done <- protocol.Serve(t.Context(), driverEnd, func(context.Context, protocol.Hello) (sourcedriver.Driver, error) {
			return nil, fmt.Errorf("the git source driver's config must name git and ssh: %w", sourcedriver.ErrInvalid)
		})
	}()
	client, err := protocol.Connect(t.Context(), core, anyHello)
	if err == nil || !strings.Contains(err.Error(), "must name git and ssh") || !errors.Is(err, sourcedriver.ErrInvalid) {
		if client != nil {
			client.Close()
		}
		t.Fatalf("Connect = %v, want the driver's configuration error with its kind", err)
	}
	if err := wait(t, done); err == nil {
		t.Fatal("Serve returned no error for a refused configuration")
	}
}

func TestErrorsKeepTheirKindsAndFactsAcrossTheConnection(t *testing.T) {
	kinds := []error{sourcedriver.ErrUnknownOutcome, sourcedriver.ErrInvalid, sourcedriver.ErrConflict, sourcedriver.ErrNotFound,
		sourcedriver.ErrLimit, sourcedriver.ErrUnavailable, context.Canceled, context.DeadlineExceeded}
	answers := make(chan error, 1)
	invalid := &sourcedriver.InvalidName{Index: 2, Name: sourcedriver.Name{Kind: sourcedriver.NameOriginURL, Value: "file:///etc"}}
	mismatch := &sourcedriver.WorkspaceMismatch{Field: "branch", Expected: "refs/heads/old", Actual: "refs/heads/other"}
	s := serve(t, &scripted{
		check: func(context.Context) error { return <-answers },
		checkNames: func(context.Context, []sourcedriver.Name) ([]sourcedriver.Name, error) {
			return nil, fmt.Errorf("check names: %w", invalid)
		},
		renameWorkspace: func(context.Context, sourcedriver.WorkspaceRename, []sourcedriver.WorkspaceFile) (sourcedriver.WorkspaceEdits, error) {
			return sourcedriver.WorkspaceEdits{}, mismatch
		},
	})
	for _, kind := range kinds {
		answer := fmt.Errorf("the backend said no: %w", kind)
		answers <- answer
		err := s.client.Check(t.Context())
		if err == nil || err.Error() != answer.Error() {
			t.Fatalf("Check = %v, want %v", err, answer)
		}
		for _, other := range kinds {
			if errors.Is(err, other) != (other == kind) {
				t.Fatalf("errors.Is(%v, %v) = %t", err, other, errors.Is(err, other))
			}
		}
	}
	answers <- errors.New("plain")
	if err := s.client.Check(t.Context()); err == nil || slices.ContainsFunc(kinds, func(k error) bool { return errors.Is(err, k) }) {
		t.Fatalf("a plain error came back as %v, with a kind", err)
	}
	var gotInvalid *sourcedriver.InvalidName
	if _, err := s.client.CheckNames(t.Context(), []sourcedriver.Name{{Kind: sourcedriver.NameOriginURL, Value: "file:///etc"}}); !errors.As(err, &gotInvalid) ||
		!reflect.DeepEqual(gotInvalid, invalid) || !errors.Is(err, sourcedriver.ErrInvalid) {
		t.Fatalf("CheckNames = %v, want the invalid name %+v matching ErrInvalid", err, invalid)
	}
	var gotMismatch *sourcedriver.WorkspaceMismatch
	if _, err := s.client.RenameWorkspace(t.Context(), sourcedriver.WorkspaceRename{}, nil); !errors.As(err, &gotMismatch) || *gotMismatch != *mismatch {
		t.Fatalf("RenameWorkspace = %v, want the mismatch %+v", err, mismatch)
	}
}

// Values cross the connection unchanged, a nil list apart from an empty
// one: LosingCommits of nil removes every head, of none removes nothing.
func TestValuesCrossTheConnectionUnchanged(t *testing.T) {
	got := make(chan []string, 1)
	loss := sourcedriver.Loss{Heads: []sourcedriver.Head{{Name: "main", Tip: "a1"}}, Commits: []sourcedriver.OID{}}
	edits := sourcedriver.WorkspaceEdits{Read: map[string]int64{".git/HEAD": 4096},
		Edits: []sourcedriver.WorkspaceFile{{Path: ".git/HEAD", File: sourcedriver.File{Type: sourcedriver.FileRegular, UID: 1000, GID: 100, Mode: 0o644, Data: []byte{}}}}, Plan: "p1"}
	s := serve(t, &scripted{
		losingCommits: func(_ context.Context, _ string, removed []string) (sourcedriver.Loss, error) {
			got <- removed
			return loss, nil
		},
		renameWorkspace: func(_ context.Context, r sourcedriver.WorkspaceRename, files []sourcedriver.WorkspaceFile) (sourcedriver.WorkspaceEdits, error) {
			if r != (sourcedriver.WorkspaceRename{ID: "op", Old: "a", New: "b", Goal: sourcedriver.GoalRenamed, Plan: "p1", User: 1000}) || len(files) != 1 || files[0].File.Data != nil {
				return sourcedriver.WorkspaceEdits{}, fmt.Errorf("the driver got %+v and %+v", r, files)
			}
			return edits, nil
		},
	})
	for _, removed := range [][]string{nil, {}, {"work"}} {
		l, err := s.client.LosingCommits(t.Context(), "app", removed)
		if r := <-got; err != nil || (r == nil) != (removed == nil) || !slices.Equal(r, removed) || !reflect.DeepEqual(l, loss) {
			t.Fatalf("LosingCommits(%#v) reached the driver as %#v and answered %+v, %v; want %+v", removed, r, l, err, loss)
		}
	}
	e, err := s.client.RenameWorkspace(t.Context(), sourcedriver.WorkspaceRename{ID: "op", Old: "a", New: "b", Goal: sourcedriver.GoalRenamed, Plan: "p1", User: 1000},
		[]sourcedriver.WorkspaceFile{{Path: ".git/HEAD", File: sourcedriver.File{Type: sourcedriver.FileAbsent}}})
	if err != nil || !reflect.DeepEqual(e, edits) {
		t.Fatalf("RenameWorkspace = %+v, %v; want %+v", e, err, edits)
	}
}

// A call whose connection is lost after it was sent: an effect that may
// have happened is unknown; a read, and an effect whose gate never ran,
// changed nothing; a gate's refusal is the call's.
func TestACallWhoseConnectionIsLostEndsByWhatMayHaveHappened(t *testing.T) {
	errGate := errors.New("core refused")
	for _, c := range []struct {
		name    string
		call    func(cl sourcedriver.Driver, g sourcedriver.Gate) error
		gates   int   // how often the driver calls the gate before it blocks
		gate    error // what core's gate answers
		want    error
		unknown bool
	}{
		{name: "an effect whose gate ran", gates: 1, want: sourcedriver.ErrUnknownOutcome, unknown: true,
			call: func(cl sourcedriver.Driver, g sourcedriver.Gate) error {
				return cl.CreateBranch(context.Background(), "app", "work", "a1", g)
			}},
		{name: "an effect whose gate refused", gates: 1, gate: errGate, want: errGate,
			call: func(cl sourcedriver.Driver, g sourcedriver.Gate) error {
				return cl.CreateBranch(context.Background(), "app", "work", "a1", g)
			}},
		{name: "an effect whose gate never ran", want: sourcedriver.ErrUnavailable,
			call: func(cl sourcedriver.Driver, g sourcedriver.Gate) error {
				return cl.CreateBranch(context.Background(), "app", "work", "a1", g)
			}},
		{name: "a read", want: sourcedriver.ErrUnavailable,
			call: func(cl sourcedriver.Driver, _ sourcedriver.Gate) error {
				_, err := cl.Branches(context.Background(), "app")
				return err
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			entered := make(chan struct{})
			s := serve(t, &scripted{
				createBranch: func(ctx context.Context, _, _ string, _ sourcedriver.OID, g sourcedriver.Gate) error {
					var err error
					if c.gates > 0 {
						err = g()
					}
					close(entered)
					<-ctx.Done()
					return err
				},
				branches: func(ctx context.Context, _ string) ([]sourcedriver.Head, error) {
					close(entered)
					<-ctx.Done()
					return nil, ctx.Err()
				},
			})
			var gates atomic.Int32
			done := make(chan error, 1)
			go func() { done <- c.call(s.client, func() error { gates.Add(1); return c.gate }) }()
			waitFor(t, entered, "the call to reach the driver")
			s.driver.Close()
			err := wait(t, done)
			if !errors.Is(err, c.want) || errors.Is(err, sourcedriver.ErrUnknownOutcome) != c.unknown {
				t.Fatalf("the lost call = %v, want %v (unknown %t)", err, c.want, c.unknown)
			}
			if int(gates.Load()) != c.gates {
				t.Fatalf("core's gate ran %d times, want %d", gates.Load(), c.gates)
			}
		})
	}
}

func TestACancelledCallCancelsTheDriversContext(t *testing.T) {
	sawCancel := make(chan struct{})
	s := serve(t, &scripted{branches: func(ctx context.Context, _ string) ([]sourcedriver.Head, error) {
		<-ctx.Done()
		close(sawCancel)
		return nil, ctx.Err()
	}})
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := s.client.Branches(ctx, "app"); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled Branches = %v, want the cancellation", err)
	}
	waitFor(t, sawCancel, "the driver's context to be cancelled")
}

func TestCoreSendsNoRequestOverTheFrameBound(t *testing.T) {
	var checks atomic.Int32
	s := serve(t, &scripted{
		check: func(context.Context) error { checks.Add(1); return nil },
		renameWorkspace: func(context.Context, sourcedriver.WorkspaceRename, []sourcedriver.WorkspaceFile) (sourcedriver.WorkspaceEdits, error) {
			t.Error("a rename over the frame bound reached the driver")
			return sourcedriver.WorkspaceEdits{}, nil
		},
	})
	huge := []sourcedriver.WorkspaceFile{{Path: ".git/config", File: sourcedriver.File{Type: sourcedriver.FileRegular, Data: make([]byte, protocol.MaxFrameBytes)}}}
	if _, err := s.client.RenameWorkspace(t.Context(), sourcedriver.WorkspaceRename{}, huge); err == nil || !strings.Contains(err.Error(), "frame bound") {
		t.Fatalf("RenameWorkspace over the bound = %v, want a refusal before sending", err)
	}
	if err := s.client.Check(t.Context()); err != nil || checks.Load() != 1 {
		t.Fatalf("Check after the refusal = %v", err)
	}
}

func TestAFrameOverTheBoundEndsTheConnection(t *testing.T) {
	core, driverEnd := pair(t)
	defer core.Close()
	done := make(chan error, 1)
	go func() {
		done <- protocol.Serve(context.Background(), driverEnd, func(context.Context, protocol.Hello) (sourcedriver.Driver, error) { return &scripted{}, nil })
	}()
	r := bufio.NewReader(core)
	core.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"hello","params":{"protocol":"` + protocol.Version + `","config":{},"core":{"storage":"/var/lib/p"}}}` + "\n"))
	if _, err := r.ReadBytes('\n'); err != nil {
		t.Fatalf("hello: %v", err)
	}
	go core.Write([]byte(`{"jsonrpc":"2.0","id":2,"method":"check","params":{"pad":"` + strings.Repeat("x", protocol.MaxFrameBytes) + `"}}` + "\n"))
	if err := wait(t, done); err == nil || !strings.Contains(err.Error(), "frame bound") {
		t.Fatalf("Serve = %v, want the frame bound", err)
	}
}

func TestServeGitWithoutItsStreamsEndsTheConnection(t *testing.T) {
	core, driverEnd := pair(t)
	defer core.Close()
	done := make(chan error, 1)
	go func() {
		done <- protocol.Serve(context.Background(), driverEnd, func(context.Context, protocol.Hello) (sourcedriver.Driver, error) {
			return &scripted{serveGit: func(context.Context, string, sourcedriver.Stream, sourcedriver.Open) error {
				t.Error("the driver served a command that came without its streams")
				return nil
			}}, nil
		})
	}()
	r := bufio.NewReader(core)
	core.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"hello","params":{"protocol":"` + protocol.Version + `","config":{},"core":{"storage":"/var/lib/p"}}}` + "\n"))
	if _, err := r.ReadBytes('\n'); err != nil {
		t.Fatalf("hello: %v", err)
	}
	core.Write([]byte(`{"jsonrpc":"2.0","id":2,"method":"serve_git","params":{"command":"git-upload-pack 'app'"}}` + "\n"))
	if err := wait(t, done); err == nil || !strings.Contains(err.Error(), "without its Git stream") {
		t.Fatalf("Serve = %v, want serve_git refused without its streams", err)
	}
}

// pair is a connected pair of Unix stream sockets: core's end and the
// driver's.
func pair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	conns := make([]*net.UnixConn, 2)
	for i, fd := range fds {
		f := os.NewFile(uintptr(fd), "pair")
		c, err := net.FileConn(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		conns[i] = c.(*net.UnixConn)
	}
	return conns[0], conns[1]
}

// served is a driver served on one end of a socket pair, with a client
// connected to it on the other.
type served struct {
	client *protocol.Client
	driver *net.UnixConn // the driver's end, to lose the connection with
}

func serve(t *testing.T, d sourcedriver.Driver) served {
	t.Helper()
	core, driverEnd := pair(t)
	go protocol.Serve(context.Background(), driverEnd, func(context.Context, protocol.Hello) (sourcedriver.Driver, error) { return d, nil })
	client, err := protocol.Connect(t.Context(), core, anyHello)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { client.Close(); driverEnd.Close() })
	return served{client: client, driver: driverEnd}
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(waitLimit):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func wait(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(waitLimit):
		t.Fatal("timed out")
	}
	return nil
}
