package protocol_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// recordingInput is the client's input, which records whether anything read
// it.
type recordingInput struct {
	r     io.Reader
	reads atomic.Int32
}

func (in *recordingInput) Read(p []byte) (int, error) {
	in.reads.Add(1)
	return in.r.Read(p)
}

// syncBuffer is an output the test reads once the call returned.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

var service = sourcedriver.Service{ID: "s1", Project: "app", Kind: sourcedriver.ServiceReceive, Branch: "work", MaxInput: 1 << 30, Deadline: 10 * time.Minute}

// The channel's input, output and standard error each reach the other
// side, and the driver's open and approve are core's, called back during
// the call with their parameters and answers.
func TestServeGitRelaysTheChannelAndCallsBackOpenAndApprove(t *testing.T) {
	type approval struct {
		branch   string
		old, new sourcedriver.OID
	}
	approvals := make(chan approval, 2)
	s := serve(t, &scripted{serveGit: func(_ context.Context, command string, stream sourcedriver.Stream, open sourcedriver.Open) error {
		if command != "git-receive-pack 'app'" {
			return errors.New("another command")
		}
		got, approve, err := open(sourcedriver.ServiceReceive, "app")
		if err != nil {
			return err
		}
		if got != service {
			return errors.New("open answered another service")
		}
		input, err := io.ReadAll(stream.In)
		if err != nil || string(input) != "the client's pack" {
			return errors.New("the driver read " + string(input))
		}
		io.WriteString(stream.Out, "Git's report")
		io.WriteString(stream.Err, "Git's diagnostics")
		if err := approve("work", "", "b1"); err != nil {
			return err
		}
		return approve("work", "a1", "refused")
	}})
	out, diagnostics := &syncBuffer{}, &syncBuffer{}
	errRefused := errors.New("the update is not the service's to make")
	err := s.client.ServeGit(t.Context(), "git-receive-pack 'app'", sourcedriver.Stream{In: strings.NewReader("the client's pack"), Out: out, Err: diagnostics},
		func(kind sourcedriver.ServiceKind, project string) (sourcedriver.Service, sourcedriver.Approve, error) {
			if kind != sourcedriver.ServiceReceive || project != "app" {
				return sourcedriver.Service{}, nil, errors.New("open of another service")
			}
			return service, func(branch string, old, new sourcedriver.OID) error {
				approvals <- approval{branch, old, new}
				if new == "refused" {
					return errRefused
				}
				return nil
			}, nil
		})
	if err == nil || err.Error() != errRefused.Error() {
		t.Fatalf("ServeGit = %v, want Approve's second answer", err)
	}
	if out.String() != "Git's report" || diagnostics.String() != "Git's diagnostics" {
		t.Fatalf("the client got output %q and errors %q", out.String(), diagnostics.String())
	}
	if first, second := <-approvals, <-approvals; first != (approval{"work", "", "b1"}) || second != (approval{"work", "a1", "refused"}) {
		t.Fatalf("Approve was called with %+v and %+v", first, second)
	}
}

// Core reads the client's input only once the driver reads it, as a driver
// in its own process would: a service that ends first leaves it untouched.
func TestServeGitReadsNoInputTheDriverDidNotRead(t *testing.T) {
	s := serve(t, &scripted{serveGit: func(_ context.Context, _ string, _ sourcedriver.Stream, open sourcedriver.Open) error {
		if _, _, err := open(sourcedriver.ServiceUpload, "app"); err != nil {
			return err
		}
		return sourcedriver.ErrNotFound
	}})
	in := &recordingInput{r: strings.NewReader("want")}
	err := s.client.ServeGit(t.Context(), "git-upload-pack 'app'", sourcedriver.Stream{In: in, Out: io.Discard, Err: io.Discard}, admit)
	if !errors.Is(err, sourcedriver.ErrNotFound) {
		t.Fatalf("ServeGit = %v, want the driver's ErrNotFound", err)
	}
	if in.reads.Load() != 0 {
		t.Fatalf("core read the client's input %d times for a service that never did", in.reads.Load())
	}
}

// A ServeGit whose connection is lost once open admitted its service may
// have changed a head: it is unknown, which core's SSH server does not take
// for the driver's own answer, so the lease stays until the service is
// settled ended. Lost before open ran, nothing happened; open's refusal is
// the call's.
func TestALostServeGitEndsByWhetherOpenAdmittedIt(t *testing.T) {
	errOpen := errors.New("the key's principal may not push to app")
	for _, c := range []struct {
		name    string
		open    bool  // whether the driver calls open before it blocks
		refuse  error // open's answer
		want    error
		unknown bool
	}{
		{name: "after open admitted the service", open: true, want: sourcedriver.ErrUnknownOutcome, unknown: true},
		{name: "after open refused", open: true, refuse: errOpen, want: errOpen},
		{name: "before open", want: sourcedriver.ErrUnavailable},
	} {
		t.Run(c.name, func(t *testing.T) {
			entered := make(chan struct{})
			s := serve(t, &scripted{serveGit: func(ctx context.Context, _ string, _ sourcedriver.Stream, open sourcedriver.Open) error {
				var err error
				if c.open {
					_, _, err = open(sourcedriver.ServiceReceive, "app")
				}
				close(entered)
				<-ctx.Done()
				return err
			}})
			var opens atomic.Int32
			done := make(chan error, 1)
			go func() {
				done <- s.client.ServeGit(context.Background(), "git-receive-pack 'app'", sourcedriver.Stream{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard},
					func(sourcedriver.ServiceKind, string) (sourcedriver.Service, sourcedriver.Approve, error) {
						opens.Add(1)
						if c.refuse != nil {
							return sourcedriver.Service{}, nil, c.refuse
						}
						return service, func(string, sourcedriver.OID, sourcedriver.OID) error { return nil }, nil
					})
			}()
			waitFor(t, entered, "the call to reach the driver")
			s.driver.Close()
			err := wait(t, done)
			if !errors.Is(err, c.want) || errors.Is(err, sourcedriver.ErrUnknownOutcome) != c.unknown {
				t.Fatalf("the lost ServeGit = %v, want %v (unknown %t)", err, c.want, c.unknown)
			}
			if c.unknown {
				for _, refusal := range []error{sourcedriver.ErrInvalid, sourcedriver.ErrLimit, sourcedriver.ErrConflict, sourcedriver.ErrNotFound} {
					if errors.Is(err, refusal) {
						t.Fatalf("the lost ServeGit = %v, which reads as the driver's refusal %v", err, refusal)
					}
				}
			}
			if want := map[bool]int32{true: 1, false: 0}[c.open]; opens.Load() != want {
				t.Fatalf("core's open ran %d times, want %d", opens.Load(), want)
			}
		})
	}
}

// open is ServeGit's gate: called once; a second call is refused without
// running it.
func TestOpenRunsOnce(t *testing.T) {
	second := make(chan error, 1)
	s := serve(t, &scripted{serveGit: func(_ context.Context, _ string, _ sourcedriver.Stream, open sourcedriver.Open) error {
		if _, _, err := open(sourcedriver.ServiceUpload, "app"); err != nil {
			return err
		}
		_, _, err := open(sourcedriver.ServiceUpload, "app")
		second <- err
		return nil
	}})
	var opens atomic.Int32
	err := s.client.ServeGit(t.Context(), "git-upload-pack 'app'", sourcedriver.Stream{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard},
		func(kind sourcedriver.ServiceKind, project string) (sourcedriver.Service, sourcedriver.Approve, error) {
			opens.Add(1)
			return admit(kind, project)
		})
	if err != nil {
		t.Fatalf("ServeGit: %v", err)
	}
	if err := <-second; err == nil || opens.Load() != 1 {
		t.Fatalf("a second open = %v after %d runs, want a refusal after 1", err, opens.Load())
	}
}

// Nothing a call lent runs once it returned: an Approve the driver calls
// after its ServeGit answered is refused, and core's never runs.
func TestApproveAfterTheCallReturnedIsRefused(t *testing.T) {
	late := make(chan sourcedriver.Approve, 1)
	s := serve(t, &scripted{serveGit: func(_ context.Context, _ string, _ sourcedriver.Stream, open sourcedriver.Open) error {
		_, approve, err := open(sourcedriver.ServiceReceive, "app")
		late <- approve
		return err
	}})
	var approvals atomic.Int32
	err := s.client.ServeGit(t.Context(), "git-receive-pack 'app'", sourcedriver.Stream{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard},
		func(sourcedriver.ServiceKind, string) (sourcedriver.Service, sourcedriver.Approve, error) {
			return service, func(string, sourcedriver.OID, sourcedriver.OID) error { approvals.Add(1); return nil }, nil
		})
	if err != nil {
		t.Fatalf("ServeGit: %v", err)
	}
	if err := (<-late)("work", "a1", "b1"); err == nil || approvals.Load() != 0 {
		t.Fatalf("an Approve after the call returned = %v after %d approvals, want a refusal and none", err, approvals.Load())
	}
}

// A call never returns while a callback it lent still runs, since its
// caller and the callback share state: a connection lost meanwhile ends
// the call once the callback returned.
func TestALostServeGitWaitsForItsRunningApprove(t *testing.T) {
	s := serve(t, &scripted{serveGit: func(ctx context.Context, _ string, _ sourcedriver.Stream, open sourcedriver.Open) error {
		_, approve, err := open(sourcedriver.ServiceReceive, "app")
		if err != nil {
			return err
		}
		approve("main", "", "b1")
		<-ctx.Done()
		return ctx.Err()
	}})
	held, release := make(chan struct{}), make(chan struct{})
	var consumed []string // what Approve records, which its caller reads once the call returned
	done := make(chan error, 1)
	go func() {
		done <- s.client.ServeGit(context.Background(), "git-receive-pack 'app'", sourcedriver.Stream{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard},
			func(sourcedriver.ServiceKind, string) (sourcedriver.Service, sourcedriver.Approve, error) {
				return service, func(string, sourcedriver.OID, sourcedriver.OID) error {
					close(held)
					<-release
					consumed = append(consumed, "the unborn main grant")
					return nil
				}, nil
			})
	}()
	waitFor(t, held, "Approve to run")
	s.driver.Close()
	waitFor(t, s.client.Done(), "the client to lose the connection")
	select {
	case err := <-done:
		close(release)
		t.Fatalf("ServeGit returned %v while its Approve still ran", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := wait(t, done); !errors.Is(err, sourcedriver.ErrUnknownOutcome) {
		t.Fatalf("the lost ServeGit = %v, want ErrUnknownOutcome", err)
	}
	if len(consumed) != 1 {
		t.Fatalf("Approve recorded %q by the time the call returned, want its one record", consumed)
	}
}

// admit is an open that admits every service, approving every update.
func admit(kind sourcedriver.ServiceKind, project string) (sourcedriver.Service, sourcedriver.Approve, error) {
	s := service
	s.Kind, s.Project = kind, project
	return s, func(string, sourcedriver.OID, sourcedriver.OID) error { return nil }, nil
}
