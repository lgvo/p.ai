package sshserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/sourcedriver"
	sourcememory "github.com/lgvo/p.ai/internal/sourcedriver/memory"
)

// memoryProject is an in-memory source driver whose project app has the
// head main.
func memoryProject(t *testing.T) (*sourcememory.World, *sourcememory.Driver, sourcedriver.OID) {
	t.Helper()
	w := sourcememory.NewWorld()
	driver := sourcememory.NewDriver(w)
	if _, err := driver.CreateRepository(t.Context(), "app"); err != nil {
		t.Fatal(err)
	}
	tip := w.Commit("app")
	w.SetHead("app", "main", tip)
	return w, driver, tip
}

// sessionKey is the fingerprint of the established session of project app
// on branch, created by choice.
func sessionKey(t *testing.T, store *control.Store, branch, choice string) string {
	t.Helper()
	ctx := t.Context()
	signer, _ := newKey(t)
	fingerprint := Fingerprint(signer.PublicKey())
	op, session, err := store.ReserveSession(ctx, control.ReserveSessionRequest{Key: branch, Project: "app", Branch: branch, Choice: choice})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error {
			return store.AdvanceOperation(ctx, op.ID, "running", "principals-ready", true, nil, "", "")
		},
		func() error { return store.RegisterGitPrincipal(ctx, fingerprint, "session", "app", session.UUID) },
		func() error { return store.AdvanceSessionRegistry(ctx, session.UUID, "creating", "established") },
		func() error { return store.AdvanceOperation(ctx, op.ID, "completed", "established", true, nil, "", "") },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	return fingerprint
}

// authorityChangeWaits reports whether a change of Git authority, the
// creation of another project, still waits after a moment, as it does while
// a lease holds Git authority's read lock.
func authorityChangeWaits(t *testing.T, store *control.Store, project string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	err := store.CreateProject(ctx, project, json.RawMessage(`{}`))
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	return err != nil
}

// changeAuthority changes Git authority, creating another project, which it
// can once no lease holds Git authority.
func changeAuthority(t *testing.T, store *control.Store, project string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	return store.CreateProject(ctx, project, json.RawMessage(`{}`))
}

func TestAServiceHoldsItsLeaseFromOpenUntilServeGitReturns(t *testing.T) {
	for _, c := range []struct {
		name, request string
		want          error
	}{
		{"the service ends", "done\n", nil},
		{"the driver refuses", "want 0000000000000000000000000000000000000001\n", sourcedriver.ErrConflict},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := openStore(t)
			_, driver, tip := memoryProject(t)
			key := hostKey(t, store)
			route := Git(t.Context(), store, driver)
			in, client := io.Pipe()
			fromService, out := io.Pipe()
			served := make(chan error, 1)
			go func() {
				served <- route.Service.Serve(t.Context(), Exec{Command: "git-upload-pack 'app'", Fingerprint: Fingerprint(key.PublicKey()), In: in, Out: out, Err: io.Discard})
				out.Close()
			}()
			lines := bufio.NewReader(fromService)
			for _, want := range []string{"head " + string(tip) + " refs/heads/main\n", "end\n"} {
				if line, err := lines.ReadString('\n'); err != nil || line != want {
					t.Fatalf("the service advertised %q, %v; want %q", line, err, want)
				}
			}
			if !authorityChangeWaits(t, store, "during") {
				t.Fatal("Git authority changed while the service ran")
			}
			if _, err := io.WriteString(client, c.request); err != nil {
				t.Fatal(err)
			}
			go io.Copy(io.Discard, lines)
			if err := <-served; !errors.Is(err, c.want) {
				t.Fatalf("the service ended with %v, want %v", err, c.want)
			}
			if err := changeAuthority(t, store, "after"); err != nil {
				t.Fatalf("the service's lease outlived ServeGit: %v", err)
			}
		})
	}
}

// endingDriver opens an upload service of app and then ends the call with
// answer, as a driver that refuses the service or that dies does. Settle
// answers that service ended only once ended is closed.
type endingDriver struct {
	*sourcememory.Driver
	answer error
	opened sourcedriver.Service
	ended  chan struct{}
}

func (d *endingDriver) ServeGit(_ context.Context, _ string, _ sourcedriver.Stream, open sourcedriver.Open) error {
	s, _, err := open(sourcedriver.ServiceUpload, "app")
	if err != nil {
		return err
	}
	d.opened = s
	return d.answer
}

func (d *endingDriver) Settle(_ context.Context, e sourcedriver.Effect) (sourcedriver.Settlement, error) {
	select {
	case <-d.ended:
		if e.Kind == sourcedriver.EffectService && e.Service == d.opened.ID {
			return sourcedriver.Applied, nil
		}
	default:
	}
	return sourcedriver.Unknown, nil
}

func TestALostServiceKeepsItsLeaseUntilTheDriverSettlesItEnded(t *testing.T) {
	for _, c := range []struct {
		name   string
		answer error
		lost   bool
	}{
		{"the service's end", nil, false},
		{"a lost call", fmt.Errorf("the driver's connection dropped: %w", sourcedriver.ErrUnknownOutcome), true},
		{"a driver that cannot be reached", fmt.Errorf("the driver is not running: %w", sourcedriver.ErrUnavailable), true},
		{"a refusal", fmt.Errorf("the client's input passed the service's ceiling: %w", sourcedriver.ErrLimit), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := openStore(t)
			_, memory, _ := memoryProject(t)
			driver := &endingDriver{Driver: memory, answer: c.answer, ended: make(chan struct{})}
			key := hostKey(t, store)
			route := Git(t.Context(), store, driver)
			err := route.Service.Serve(t.Context(), Exec{Command: "git-upload-pack 'app'", Fingerprint: Fingerprint(key.PublicKey()), In: strings.NewReader(""), Out: io.Discard, Err: io.Discard})
			if err != c.answer {
				t.Fatalf("the call answered %v", err)
			}
			if !c.lost {
				if err := changeAuthority(t, store, "ended"); err != nil {
					t.Fatalf("the service's lease outlived its refusal: %v", err)
				}
				return
			}
			if !authorityChangeWaits(t, store, "ended") {
				t.Fatal("a lost service's lease was released before the driver settled it ended")
			}
			close(driver.ended)
			if err := changeAuthority(t, store, "settled"); err != nil {
				t.Fatalf("a settled service's lease was kept: %v", err)
			}
		})
	}
}

// reopeningDriver calls open opens times during ServeGit, keeping the
// first refusal, and keeps open, as a driver that breaks the contract
// might.
type reopeningDriver struct {
	*sourcememory.Driver
	opens   int
	open    sourcedriver.Open
	refusal error
}

func (d *reopeningDriver) ServeGit(_ context.Context, _ string, _ sourcedriver.Stream, open sourcedriver.Open) error {
	d.open = open
	for range d.opens {
		if _, _, err := open(sourcedriver.ServiceUpload, "app"); err != nil && d.refusal == nil {
			d.refusal = err
		}
	}
	return nil
}

func TestOpenAuthorizesOneServiceAndOnlyDuringServeGit(t *testing.T) {
	for _, c := range []struct {
		name  string
		opens int
	}{
		{"a second open during the call", 2},
		{"an open after a call that made none", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := openStore(t)
			_, memory, _ := memoryProject(t)
			driver := &reopeningDriver{Driver: memory, opens: c.opens}
			key := hostKey(t, store)
			route := Git(t.Context(), store, driver)
			if err := route.Service.Serve(t.Context(), Exec{Command: "git-upload-pack 'app'", Fingerprint: Fingerprint(key.PublicKey()), In: strings.NewReader(""), Out: io.Discard, Err: io.Discard}); err != nil {
				t.Fatal(err)
			}
			if c.opens > 1 && driver.refusal == nil {
				t.Fatal("open authorized a second service in one call")
			}
			if _, _, err := driver.open(sourcedriver.ServiceUpload, "app"); err == nil {
				t.Fatal("open authorized a service after ServeGit returned")
			}
			if err := changeAuthority(t, store, "after"); err != nil {
				t.Fatalf("a refused open took a lease: %v", err)
			}
		})
	}
}

// pushingDriver opens a service of kind on app and asks its Approve for
// one update, as a driver does once its own checks pass, and answers with
// Approve's answer. A client that leaves first cancels the call's context
// before the update.
type pushingDriver struct {
	*sourcememory.Driver
	kind         sourcedriver.ServiceKind
	branch       string
	old, new     sourcedriver.OID
	clientLeaves bool

	leave context.CancelFunc
}

func (d *pushingDriver) ServeGit(_ context.Context, _ string, _ sourcedriver.Stream, open sourcedriver.Open) error {
	_, approve, err := open(d.kind, "app")
	if err != nil {
		return err
	}
	if d.clientLeaves {
		d.leave()
	}
	return approve(d.branch, d.old, d.new)
}

func TestApproveAllowsOnlyTheServicesHeadAndACreationOnlyByTheUnbornMainGrant(t *testing.T) {
	store := openStore(t)
	w, memory, tip := memoryProject(t)
	next := w.Commit("app", tip)
	work := sessionKey(t, store, "work", "existing")
	blank := sessionKey(t, store, "main", "blank")
	for _, c := range []struct {
		name    string
		key     string
		push    pushingDriver
		allowed bool
	}{
		{"an update of the session's head", work, pushingDriver{kind: sourcedriver.ServiceReceive, branch: "work", old: tip, new: next}, true},
		{"an update of another head", work, pushingDriver{kind: sourcedriver.ServiceReceive, branch: "main", old: tip, new: next}, false},
		{"an update by an upload service", work, pushingDriver{kind: sourcedriver.ServiceUpload, branch: "work", old: tip, new: next}, false},
		{"an update of no head by an upload service", work, pushingDriver{kind: sourcedriver.ServiceUpload, old: tip, new: next}, false},
		{"a deletion of the session's head", work, pushingDriver{kind: sourcedriver.ServiceReceive, branch: "work", old: tip}, false},
		{"an update after the client left", work, pushingDriver{kind: sourcedriver.ServiceReceive, branch: "work", old: tip, new: next, clientLeaves: true}, false},
		{"a creation without the unborn grant", work, pushingDriver{kind: sourcedriver.ServiceReceive, branch: "work", new: next}, false},
		{"a creation of main with the unborn grant", blank, pushingDriver{kind: sourcedriver.ServiceReceive, branch: "main", new: next}, true},
		{"a second creation of main, the grant consumed", blank, pushingDriver{kind: sourcedriver.ServiceReceive, branch: "main", new: next}, false},
	} {
		ctx, cancel := context.WithCancel(t.Context())
		c.push.Driver, c.push.leave = memory, cancel
		route := Git(t.Context(), store, &c.push)
		err := route.Service.Serve(ctx, Exec{Command: "git-receive-pack 'app'", Fingerprint: c.key, In: strings.NewReader(""), Out: io.Discard, Err: io.Discard})
		if (err == nil) != c.allowed {
			t.Errorf("%s: Approve answered %v", c.name, err)
		}
		cancel()
	}
}

// uploadOnly is the in-memory driver declaring only git-upload-pack.
type uploadOnly struct{ *sourcememory.Driver }

func (d uploadOnly) Describe() sourcedriver.Description {
	description := d.Driver.Describe()
	description.Commands = []string{"git-upload-pack"}
	return description
}

func TestTheGitServiceServesTheCommandsTheDriverDeclares(t *testing.T) {
	store := openStore(t)
	_, memory, tip := memoryProject(t)
	address := serve(t, store, Git(t.Context(), store, uploadOnly{memory}))
	client, err := dial(t, address, "git", hostKey(t, store))
	if err != nil {
		t.Fatal(err)
	}
	upload, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer upload.Close()
	upload.Stdin = strings.NewReader("done\n")
	output, err := upload.Output("git-upload-pack 'app'")
	if want := "head " + string(tip) + " refs/heads/main\nend\nend\n"; err != nil || string(output) != want {
		t.Fatalf("the declared command answered %q, %v; want %q", output, err, want)
	}
	receive, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer receive.Close()
	if err := receive.Start("git-receive-pack 'app'"); err == nil {
		t.Fatal("a command the driver did not declare was served")
	}
}
