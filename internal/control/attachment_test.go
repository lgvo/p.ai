package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

func TestOrdinaryHostProcessCannotClaimPresence(t *testing.T) {
	c := &Connection{peerPID: os.Getpid(), peerUID: uint32(os.Geteuid())}
	if c.trustedHelper() {
		t.Fatal("test/API process accepted as fixed helper")
	}
	ctx := context.WithValue(context.Background(), connectionKey{}, c)
	// A nil implementation is intentional: denial must happen before lifecycle.
	table := NewMethodTable()
	addAttachmentMethods(table, nil)
	_, err := table.Handler()(ctx, "attachment.claim", json.RawMessage(`{"token":"`+strings.Repeat("a", 64)+`","width":80,"height":24}`))
	if want := api.NewError(api.KindInvalidRequest, api.ReasonConnection); !reflect.DeepEqual(err, want) {
		t.Fatalf("attachment.claim from an ordinary process answered %v, want %v", err, want)
	}
	if c.takeTerminal() != nil {
		t.Fatal("a refused claim left a terminal to carry")
	}
}

// fakeTerminal is an open terminal: what the relay writes to it arrives on
// input, resizes on resized, and what the test writes to write is its
// output. A refusing terminal refuses input; closing it ends its output with
// endErr.
type fakeTerminal struct {
	output    *io.PipeReader
	write     *io.PipeWriter
	input     chan string
	resized   chan runtimedriver.Size
	closed    chan struct{}
	once      sync.Once
	refusing  bool
	endErr    error
	closeHook func()
}

func newFakeTerminal() *fakeTerminal {
	r, w := io.Pipe()
	return &fakeTerminal{output: r, write: w, input: make(chan string, 16), resized: make(chan runtimedriver.Size, 4), closed: make(chan struct{})}
}

func (f *fakeTerminal) Read(p []byte) (int, error) { return f.output.Read(p) }

func (f *fakeTerminal) Write(p []byte) (int, error) {
	select {
	case <-f.closed:
		return 0, io.ErrClosedPipe
	default:
	}
	if f.refusing {
		return 0, errors.New("the terminal's channel closed")
	}
	f.input <- string(p)
	return len(p), nil
}

func (f *fakeTerminal) Resize(s runtimedriver.Size) error { f.resized <- s; return nil }

// Close ends the terminal: its output ends.
func (f *fakeTerminal) Close() error {
	f.once.Do(func() {
		close(f.closed)
		f.write.CloseWithError(f.endErr)
	})
	if f.closeHook != nil {
		f.closeHook()
	}
	return nil
}

// relayFixture is a connection served by ServeConn whose attachment.claim
// opens terminal, unless claimErr is set; other serves the other methods.
type relayFixture struct {
	t      *testing.T
	client net.Conn
	reader *bufio.Reader
	frames *api.FrameConn
	served chan struct{}
	claims int
}

func newRelayFixture(t *testing.T, terminal *fakeTerminal, claimErr error, other Handler) *relayFixture {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close() })
	f := &relayFixture{t: t, client: client, reader: bufio.NewReader(client), served: make(chan struct{})}
	table := NewMethodTable()
	RegisterClaim(table, func(context.Context, api.AttachmentClaimRequest) (Terminal, error) {
		f.claims++
		if claimErr != nil {
			return nil, claimErr
		}
		return terminal, nil
	})
	claim := table.Handler()
	go func() {
		defer close(f.served)
		ServeConn(context.Background(), server, withHello(func(ctx context.Context, method string, params json.RawMessage) (any, *api.Error) {
			if method == api.AttachmentClaim.Name || other == nil {
				return claim(ctx, method, params)
			}
			return other(ctx, method, params)
		}))
	}()
	agree(t, client, f.reader)
	f.frames = api.NewFrameConn(client, f.reader)
	return f
}

var claimLine = `{"jsonrpc":"2.0","id":7,"method":"attachment.claim","params":{"token":"` + strings.Repeat("a", 64) + `","width":80,"height":24}}` + "\n"

// answer reads the next answer.
func (f *relayFixture) answer() string {
	f.t.Helper()
	f.client.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := f.reader.ReadString('\n')
	if err != nil {
		f.t.Fatalf("answer: %q %v", line, err)
	}
	return line
}

func (f *relayFixture) frame() api.Frame {
	f.t.Helper()
	f.client.SetReadDeadline(time.Now().Add(2 * time.Second))
	frame, err := f.frames.ReadFrame()
	if err != nil {
		f.t.Fatalf("frame: %v", err)
	}
	return frame
}

// claimed sends the claim and reads its answer.
func (f *relayFixture) claimed() {
	f.t.Helper()
	if _, err := f.client.Write([]byte(claimLine)); err != nil {
		f.t.Fatal(err)
	}
	if answer := f.answer(); answer != `{"jsonrpc":"2.0","id":7,"result":{}}`+"\n" {
		f.t.Fatalf("claim answered %s", answer)
	}
}

func (f *relayFixture) awaitServed() {
	f.t.Helper()
	select {
	case <-f.served:
	case <-time.After(2 * time.Second):
		f.t.Fatal("the connection was still served")
	}
}

func receive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	panic("unreachable")
}

// Once the claim is answered the connection carries the terminal: input and
// resizes reach it, including a frame the helper sent with the claim, its
// output comes back, and an end frame follows the end of its output.
func TestAClaimedConnectionCarriesTheTerminal(t *testing.T) {
	terminal := newFakeTerminal()
	f := newRelayFixture(t, terminal, nil, nil)
	early := []byte{byte(api.FrameInput), 0, 0, 0, 5, 'e', 'a', 'r', 'l', 'y'}
	if _, err := f.client.Write(append([]byte(claimLine), early...)); err != nil {
		t.Fatal(err)
	}
	if answer := f.answer(); answer != `{"jsonrpc":"2.0","id":7,"result":{}}`+"\n" {
		t.Fatalf("claim answered %s", answer)
	}
	if got := receive(t, terminal.input, "the early input"); got != "early" {
		t.Fatalf("the terminal read %q", got)
	}
	if err := f.frames.WriteFrame(api.FrameInput, []byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, terminal.input, "input"); got != "ls\r" {
		t.Fatalf("the terminal read %q", got)
	}
	if err := f.frames.WriteJSON(api.FrameResize, api.TerminalSize{Width: 132, Height: 43}); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, terminal.resized, "a resize"); got != (runtimedriver.Size{Width: 132, Height: 43}) {
		t.Fatalf("the terminal was resized to %+v", got)
	}
	go terminal.write.Write([]byte("output"))
	if got := f.frame(); got.Kind != api.FrameOutput || string(got.Data) != "output" {
		t.Fatalf("output frame %+v", got)
	}
	terminal.write.Close()
	if got := f.frame(); got.Kind != api.FrameEnd || len(got.Data) != 0 {
		t.Fatalf("end frame %+v", got)
	}
	f.awaitServed()
	receive(t, terminal.closed, "the terminal to be closed")
}

// When the helper goes, the relay closes the terminal.
func TestTheRelayClosesTheTerminalWhenTheHelperGoes(t *testing.T) {
	terminal := newFakeTerminal()
	f := newRelayFixture(t, terminal, nil, nil)
	f.claimed()
	f.client.Close()
	receive(t, terminal.closed, "the terminal to be closed")
	f.awaitServed()
}

// A frame the relay does not take from the helper ends the relay.
func TestTheRelayEndsOnAFrameItDoesNotTake(t *testing.T) {
	for name, frame := range map[string]func(*api.FrameConn) error{
		"output from the helper":    func(c *api.FrameConn) error { return c.WriteFrame(api.FrameOutput, []byte("x")) },
		"a resize to no size":       func(c *api.FrameConn) error { return c.WriteJSON(api.FrameResize, api.TerminalSize{}) },
		"a resize that is not JSON": func(c *api.FrameConn) error { return c.WriteFrame(api.FrameResize, []byte("{")) },
	} {
		t.Run(name, func(t *testing.T) {
			terminal := newFakeTerminal()
			f := newRelayFixture(t, terminal, nil, nil)
			f.claimed()
			if err := frame(f.frames); err != nil {
				t.Fatal(err)
			}
			receive(t, terminal.closed, "the terminal to be closed")
			if got := f.frame(); got.Kind != api.FrameEnd {
				t.Fatalf("last frame %+v, want the end", got)
			}
			f.awaitServed()
			if len(terminal.resized) != 0 {
				t.Fatal("the terminal was resized")
			}
		})
	}
}

// A terminal that refuses input, such as one whose channel closed, is closed,
// and its end still reaches the helper with why it ended.
func TestARefusedInputEndsTheTerminalWithItsEndFrame(t *testing.T) {
	terminal := newFakeTerminal()
	terminal.refusing = true
	terminal.endErr = errors.New("the terminal ended unproven")
	f := newRelayFixture(t, terminal, nil, nil)
	f.claimed()
	if err := f.frames.WriteFrame(api.FrameInput, []byte("late")); err != nil {
		t.Fatal(err)
	}
	if got := f.frame(); got.Kind != api.FrameEnd || string(got.Data) != "the terminal ended unproven" {
		t.Fatalf("end frame %+v", got)
	}
	f.awaitServed()
}

// An output that ends in an error tells the helper why in the end frame.
func TestAFailedTerminalTellsTheHelperWhy(t *testing.T) {
	terminal := newFakeTerminal()
	f := newRelayFixture(t, terminal, nil, nil)
	f.claimed()
	terminal.write.CloseWithError(errors.New("the attachment ended without proof"))
	if got := f.frame(); got.Kind != api.FrameEnd || string(got.Data) != "the attachment ended without proof" {
		t.Fatalf("end frame %+v", got)
	}
	f.awaitServed()
}

// A refused claim leaves an ordinary connection that carries no terminal.
func TestARefusedClaimKeepsTheConnectionServingRequests(t *testing.T) {
	f := newRelayFixture(t, nil, Stale(api.ReasonExpired), nil)
	if _, err := f.client.Write([]byte(claimLine)); err != nil {
		t.Fatal(err)
	}
	if answer := f.answer(); !strings.Contains(answer, `"message":"precondition_failed","data":{"reason":"expired"}`) {
		t.Fatalf("claim answered %s", answer)
	}
	if _, err := f.client.Write([]byte(`{"jsonrpc":"2.0","id":8,"method":"system.hello","params":{"protocol":"` + api.Protocol + `"}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if answer := f.answer(); !strings.Contains(answer, `"id":8,"result"`) {
		t.Fatalf("a request after the refused claim answered %s", answer)
	}
}

// Once a claim's answer is written, nothing else may write answers on the
// connection, so a claim is refused while another request is in flight.
func TestAClaimIsRefusedWhileAnotherRequestIsInFlight(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	f := newRelayFixture(t, newFakeTerminal(), nil, func(context.Context, string, json.RawMessage) (any, *api.Error) {
		close(started)
		<-release
		return api.HealthResult{ControlState: "ready"}, nil
	})
	if _, err := f.client.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"system.health","params":{}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	receive(t, started, "the slow request")
	if _, err := f.client.Write([]byte(claimLine)); err != nil {
		t.Fatal(err)
	}
	if answer := f.answer(); !strings.Contains(answer, `"id":7`) || !strings.Contains(answer, `"message":"retry","data":{"reason":"too_many_requests"}`) {
		t.Fatalf("claim answered %s", answer)
	}
	close(release)
	f.answer()
	if f.claims != 0 {
		t.Fatal("the claim reached its handler")
	}
}

// Closing the terminal happens where nothing is left to answer: a panic in
// it is logged, and the connection's teardown still finishes.
func TestTheRelaySurvivesAPanickingTerminalClose(t *testing.T) {
	terminal := newFakeTerminal()
	terminal.closeHook = func() { panic("close bug") }
	f := newRelayFixture(t, terminal, nil, nil)
	f.claimed()
	f.client.Close()
	f.awaitServed()
}

// A claim can take seconds to open the terminal. When the helper hangs up
// meanwhile, the claim's context ends, so the claim counts no presence for a
// client that is gone.
func TestAHangUpDuringTheClaimCancelsIt(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan error, 1)
	table := NewMethodTable()
	RegisterClaim(table, func(ctx context.Context, _ api.AttachmentClaimRequest) (Terminal, error) {
		close(started)
		<-ctx.Done()
		cancelled <- ctx.Err()
		return nil, ctx.Err()
	})
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close() })
	served := make(chan struct{})
	go func() { defer close(served); ServeConn(context.Background(), server, withHello(table.Handler())) }()
	agree(t, client, bufio.NewReader(client))
	if _, err := client.Write([]byte(claimLine)); err != nil {
		t.Fatal(err)
	}
	receive(t, started, "the claim")
	client.Close()
	if err := receive(t, cancelled, "the claim's cancellation"); !errors.Is(err, context.Canceled) {
		t.Fatalf("the claim ended with %v, want its cancellation", err)
	}
	receive(t, served, "the connection's end")
}

// A claim that panics answers internal and the connection keeps serving: its
// hang-up watch is joined before the read loop reads the next request.
func TestAPanickingClaimLeavesTheConnectionServing(t *testing.T) {
	table := NewMethodTable()
	RegisterClaim(table, func(context.Context, api.AttachmentClaimRequest) (Terminal, error) {
		panic("claim bug")
	})
	Register(table, api.SystemHealth, func(context.Context, api.HealthRequest) (api.HealthResult, error) {
		return api.HealthResult{ControlState: "ready"}, nil
	})
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close() })
	go ServeConn(context.Background(), server, withHello(table.Handler()))
	reader := bufio.NewReader(client)
	agree(t, client, reader)
	if _, err := client.Write([]byte(claimLine)); err != nil {
		t.Fatal(err)
	}
	if answer, err := reader.ReadString('\n'); err != nil || !strings.Contains(answer, `"message":"internal","data":{"reason":"panic"}`) {
		t.Fatalf("panicking claim answered %q, %v", answer, err)
	}
	for id := 2; id < 5; id++ {
		if _, err := fmt.Fprintf(client, `{"jsonrpc":"2.0","id":%d,"method":"system.health","params":{}}`+"\n", id); err != nil {
			t.Fatal(err)
		}
		if answer, err := reader.ReadString('\n'); err != nil || !strings.Contains(answer, `"control_state":"ready"`) {
			t.Fatalf("request %d after the panicking claim answered %q, %v", id, answer, err)
		}
	}
}

func TestAttachmentConfirmationRefusals(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	missing := "11111111-2222-3333-4444-555555555555"
	_, _, _, err := s.ConfirmAttachment(ctx, missing)
	if answer := answerError(ctx, err); answer.Kind() != api.KindNotFound || answer.Reason() != api.ReasonSession || answer.Target() != api.TargetSession(missing) {
		t.Fatalf("confirmation for a missing session answered %v", answer)
	}
}
