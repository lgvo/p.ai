package protocol_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimedriver/protocol"
)

// waitLimit bounds every wait, so a call that never returns fails its test.
const waitLimit = 10 * time.Second

// scripted is a driver whose calls a test defines; a call it did not
// define panics.
type scripted struct {
	runtimedriver.Driver
	check        func(context.Context) error
	inventory    func(context.Context) (runtimedriver.Inventory, error)
	start        func(context.Context, runtimedriver.Session) (runtimedriver.Observation, error)
	create       func(context.Context, runtimedriver.Session, runtimedriver.Incarnation, runtimedriver.Gate) (runtimedriver.Observation, error)
	openTerminal func(context.Context, runtimedriver.Target, runtimedriver.Size) (runtimedriver.Terminal, error)
	write        func(context.Context, runtimedriver.Target, string, runtimedriver.File, runtimedriver.Gate) error
}

var scriptedCapabilities = runtimedriver.Capabilities{ImageBuilder: "scripted/1", BuilderPolicy: "none", Networks: []string{runtimedriver.NetworkNone}, Services: true}

func (d *scripted) Describe() runtimedriver.Capabilities { return scriptedCapabilities }
func (d *scripted) Check(ctx context.Context) error      { return d.check(ctx) }
func (d *scripted) Inventory(ctx context.Context) (runtimedriver.Inventory, error) {
	return d.inventory(ctx)
}
func (d *scripted) Start(ctx context.Context, s runtimedriver.Session) (runtimedriver.Observation, error) {
	return d.start(ctx, s)
}
func (d *scripted) Create(ctx context.Context, s runtimedriver.Session, at runtimedriver.Incarnation, g runtimedriver.Gate) (runtimedriver.Observation, error) {
	return d.create(ctx, s, at, g)
}
func (d *scripted) OpenTerminal(ctx context.Context, t runtimedriver.Target, size runtimedriver.Size) (runtimedriver.Terminal, error) {
	return d.openTerminal(ctx, t, size)
}
func (d *scripted) WriteWorkspace(ctx context.Context, t runtimedriver.Target, path string, f runtimedriver.File, g runtimedriver.Gate) error {
	return d.write(ctx, t, path, f, g)
}

var session = runtimedriver.Session{Ref: runtimedriver.Ref{Kind: runtimedriver.KindSession, ID: "00000000-0000-4000-8000-000000000001"}}

func TestTheHelloAgreesTheVersionAndCarriesTheConfiguration(t *testing.T) {
	core, driverEnd := pair(t)
	hello := protocol.Hello{
		Config: json.RawMessage(`{"binary":"/run/current-system/sw/bin/runtime","project":"user-1000"}`),
		Core: protocol.Core{InstanceID: "11111111-1111-4111-8111-111111111111", EndpointPrefix: "/var/lib/p/endpoints",
			DiskSourceCeilings: []string{"/srv/p"}, BuilderStoragePool: "builders",
			PublicEgress: &protocol.PublicEgress{Network: "p-public", ACL: "p-public-acl", BridgeIPv4: "198.18.0.1/24", DNS: []string{"1.1.1.1"},
				SudoBinary: "/run/wrappers/bin/sudo", NftBinary: "/run/current-system/sw/bin/nft", BridgeProofBinary: "/run/current-system/sw/bin/p-bridge-proof"}},
	}
	got := make(chan protocol.Hello, 1)
	go protocol.Serve(t.Context(), driverEnd, func(_ context.Context, h protocol.Hello) (runtimedriver.Driver, error) {
		got <- h
		return &scripted{}, nil
	})
	client, err := protocol.Connect(t.Context(), core, hello)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()
	h := <-got
	if string(h.Config) != string(hello.Config) || !reflect.DeepEqual(h.Core, hello.Core) {
		t.Fatalf("the driver got hello %+v (config %s), want %+v (config %s)", h.Core, h.Config, hello.Core, hello.Config)
	}
	if c := client.Describe(); !reflect.DeepEqual(c, scriptedCapabilities) {
		t.Fatalf("Describe = %+v, want the driver's %+v", c, scriptedCapabilities)
	}
}

func TestADriverRefusesCoreSpeakingAnotherVersion(t *testing.T) {
	r, opened, done := rawCore(t)
	r.send(helloFrame("p.runtime-driver/v0"))
	res := r.receive()
	if res.Error == nil || res.Error.Code != -32001 || res.Error.Data == nil || !reflect.DeepEqual(res.Error.Data.Supported, []string{protocol.Version}) {
		t.Fatalf("hello of v0 answered %+v, want a version refusal naming %s", res, protocol.Version)
	}
	r.wantClosed()
	if err := wait(t, done); err == nil || !strings.Contains(err.Error(), "p.runtime-driver/v0") {
		t.Fatalf("Serve = %v, want the refused version", err)
	}
	if opened.Load() != 0 {
		t.Fatal("the driver was made for a refused hello")
	}
}

func TestCoreRefusesADriverSpeakingAnotherVersion(t *testing.T) {
	core, driverEnd := pair(t)
	go func() {
		r := bufio.NewReader(driverEnd)
		if _, err := r.ReadBytes('\n'); err != nil {
			return
		}
		driverEnd.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocol":"p.runtime-driver/v0","capabilities":{"image_builder":"x","builder_policy":"","networks":[],"egress_addresses":0,"services":false}}}` + "\n"))
	}()
	if client, err := protocol.Connect(t.Context(), core, anyHello); err == nil || !strings.Contains(err.Error(), "p.runtime-driver/v0") {
		if client != nil {
			client.Close()
		}
		t.Fatalf("Connect to a driver of v0 = %v, want a refusal naming the version", err)
	}
	driverEnd.SetReadDeadline(time.Now().Add(waitLimit))
	if _, err := io.ReadAll(driverEnd); err != nil {
		t.Fatalf("core did not close the refused connection: %v", err)
	}
}

func TestACallBeforeTheHelloIsRefused(t *testing.T) {
	r, opened, done := rawCore(t)
	r.send(`{"jsonrpc":"2.0","id":1,"method":"check","params":{}}`)
	if res := r.receive(); res.Error == nil || res.Error.Code != -32002 {
		t.Fatalf("a check before the hello answered %+v, want hello_required", res)
	}
	r.wantClosed()
	if err := wait(t, done); err == nil {
		t.Fatal("Serve accepted a call before the hello")
	}
	if opened.Load() != 0 {
		t.Fatal("the driver was made without a hello")
	}
}

func TestADriverThatCannotBeConfiguredRefusesTheHello(t *testing.T) {
	core, driverEnd := pair(t)
	done := make(chan error, 1)
	go func() {
		done <- protocol.Serve(t.Context(), driverEnd, func(context.Context, protocol.Hello) (runtimedriver.Driver, error) {
			return nil, errors.New("confined user socket unavailable")
		})
	}()
	if client, err := protocol.Connect(t.Context(), core, anyHello); err == nil || !strings.Contains(err.Error(), "confined user socket unavailable") {
		if client != nil {
			client.Close()
		}
		t.Fatalf("Connect = %v, want the driver's configuration error", err)
	}
	if err := wait(t, done); err == nil || !strings.Contains(err.Error(), "confined user socket unavailable") {
		t.Fatalf("Serve = %v, want the configuration error", err)
	}
}

// Every frame that is not one strict JSON-RPC message ends the connection,
// and nothing it names reaches the driver.
func TestStrictFramingEndsTheConnectionOnAMalformedFrame(t *testing.T) {
	for name, frame := range map[string]string{
		"not JSON":                   `check please`,
		"a duplicate key":            `{"jsonrpc":"2.0","id":2,"id":3,"method":"check","params":{}}`,
		"a duplicate nested key":     `{"jsonrpc":"2.0","id":2,"method":"check","params":{"a":1,"a":2}}`,
		"an unknown field":           `{"jsonrpc":"2.0","id":2,"method":"check","params":{},"extra":1}`,
		"an inexact field name":      `{"jsonrpc":"2.0","ID":2,"method":"check","params":{}}`,
		"another JSON-RPC version":   `{"jsonrpc":"1.0","id":2,"method":"check","params":{}}`,
		"a trailing value":           `{"jsonrpc":"2.0","id":2,"method":"check","params":{}} {}`,
		"a request without params":   `{"jsonrpc":"2.0","id":2,"method":"check"}`,
		"a request with null params": `{"jsonrpc":"2.0","id":2,"method":"check","params":null}`,
		"a request with a result":    `{"jsonrpc":"2.0","id":2,"method":"check","params":{},"result":null}`,
		"a response of both kinds":   `{"jsonrpc":"2.0","id":2,"result":null,"error":{"code":1,"message":"x"}}`,
		"a notification not known":   `{"jsonrpc":"2.0","method":"check","params":{}}`,
		"a negative ID":              `{"jsonrpc":"2.0","id":-2,"method":"check","params":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var checks atomic.Int32
			r, _, done := rawCore(t, &scripted{check: func(context.Context) error { checks.Add(1); return nil }})
			r.hello()
			r.send(frame)
			r.wantClosed()
			if err := wait(t, done); err == nil {
				t.Fatal("Serve returned no error for a malformed frame")
			}
			if checks.Load() != 0 {
				t.Fatal("a malformed frame reached the driver")
			}
		})
	}
}

func TestAFrameOverTheBoundEndsTheConnection(t *testing.T) {
	r, _, done := rawCore(t, &scripted{})
	r.hello()
	go r.c.Write([]byte(`{"jsonrpc":"2.0","id":2,"method":"check","params":{"pad":"` + strings.Repeat("x", protocol.MaxFrameBytes) + `"}}` + "\n"))
	r.wantClosed()
	if err := wait(t, done); err == nil || !strings.Contains(err.Error(), "frame bound") {
		t.Fatalf("Serve = %v, want the frame bound", err)
	}
}

func TestAFrameCutShortEndsTheConnection(t *testing.T) {
	r, _, done := rawCore(t, &scripted{})
	r.hello()
	r.c.Write([]byte(`{"jsonrpc":"2.0","id":2,"method":"ch`))
	r.c.CloseWrite()
	if err := wait(t, done); err == nil || !strings.Contains(err.Error(), "inside a frame") {
		t.Fatalf("Serve = %v, want a frame cut short", err)
	}
}

func TestAFrameAtTheBoundIsRead(t *testing.T) {
	r, opened, _ := rawCore(t)
	prefix := `{"jsonrpc":"2.0","id":1,"method":"hello","params":{"protocol":"` + protocol.Version + `","config":{"pad":"`
	suffix := `"},"core":{"instance_id":"","endpoint_prefix":"","disk_source_ceilings":null,"builder_storage_pool":"","public_egress":null}}}`
	go r.c.Write([]byte(prefix + strings.Repeat("x", protocol.MaxFrameBytes-len(prefix)-len(suffix)-1) + suffix + "\n"))
	if res := r.receive(); res.Error != nil || opened.Load() != 1 {
		t.Fatalf("a hello of exactly %d bytes answered %+v", protocol.MaxFrameBytes, res)
	}
}

func TestCallsWithParametersOfAnotherShapeAreRefusedAndTheConnectionStays(t *testing.T) {
	var checks atomic.Int32
	r, _, _ := rawCore(t, &scripted{check: func(context.Context) error { checks.Add(1); return nil }})
	r.hello()
	r.send(`{"jsonrpc":"2.0","id":2,"method":"check","params":{"deep":true}}`)
	if res := r.receive(); res.Error == nil || res.Error.Code != -32602 || checks.Load() != 0 {
		t.Fatalf("check with unknown parameters answered %+v after %d checks, want invalid params and none", res, checks.Load())
	}
	r.send(`{"jsonrpc":"2.0","id":3,"method":"reboot","params":{}}`)
	if res := r.receive(); res.Error == nil || res.Error.Code != -32601 {
		t.Fatalf("an unknown method answered %+v, want method not found", res)
	}
	r.send(`{"jsonrpc":"2.0","id":4,"method":"check","params":{}}`)
	if res := r.receive(); res.Error != nil || string(res.Result) != "null" || checks.Load() != 1 {
		t.Fatalf("a check after the refusals answered %+v", res)
	}
}

func TestOpenTerminalWithoutItsConnectionEndsTheConnection(t *testing.T) {
	r, _, done := rawCore(t, &scripted{openTerminal: func(context.Context, runtimedriver.Target, runtimedriver.Size) (runtimedriver.Terminal, error) {
		t.Error("the driver opened a terminal that has no connection")
		return nil, errors.New("unreachable")
	}})
	r.hello()
	r.send(`{"jsonrpc":"2.0","id":2,"method":"open_terminal","params":{"target":{"session":{"ref":{"kind":"session","id":"x"},"labels":null,"image":"","command":null,"workspace":{"repository":"","branch":"","commit":""},"git_key":null,"git_host_key":"","endpoints":"","grants":null,"network":{"public_ipv4":""},"assets":null},"at":""},"size":{"width":80,"height":24}}}`)
	if res := r.receive(); res.Error == nil {
		t.Fatalf("open_terminal without a connection answered %+v", res)
	}
	r.wantClosed()
	if err := wait(t, done); err == nil {
		t.Fatal("Serve returned no error")
	}
}

func TestCoreSendsNoRequestOverTheFrameBound(t *testing.T) {
	var checks atomic.Int32
	s := serve(t, &scripted{
		check: func(context.Context) error { checks.Add(1); return nil },
		write: func(context.Context, runtimedriver.Target, string, runtimedriver.File, runtimedriver.Gate) error {
			t.Error("a write over the frame bound reached the driver")
			return nil
		},
	}, anyHello)
	huge := runtimedriver.File{Type: runtimedriver.FileRegular, Mode: 0o644, Data: make([]byte, protocol.MaxFrameBytes)}
	err := s.client.WriteWorkspace(t.Context(), runtimedriver.Target{Session: session}, "file", huge, func() error {
		t.Error("the gate of a write that was never sent ran")
		return nil
	})
	if err == nil || errors.Is(err, runtimedriver.ErrUnknownOutcome) || !strings.Contains(err.Error(), "frame bound") {
		t.Fatalf("WriteWorkspace over the bound = %v, want a refusal before sending", err)
	}
	if err := s.client.Check(t.Context()); err != nil || checks.Load() != 1 {
		t.Fatalf("Check after the refusal = %v", err)
	}
}

func TestErrorsKeepTheirKindsAcrossTheConnection(t *testing.T) {
	kinds := []error{runtimedriver.ErrUnknownOutcome, runtimedriver.ErrMismatch, runtimedriver.ErrUnavailable,
		runtimedriver.ErrUnsupported, runtimedriver.ErrNoService, runtimedriver.ErrNotReady, context.Canceled, context.DeadlineExceeded}
	answers := make(chan error, 1)
	s := serve(t, &scripted{
		check: func(context.Context) error { return <-answers },
		start: func(context.Context, runtimedriver.Session) (runtimedriver.Observation, error) {
			return runtimedriver.Observation{}, fmt.Errorf("start: %w", &runtimedriver.HostError{Diagnostic: "p-interactive.service failed: exit 3"})
		},
		openTerminal: func(context.Context, runtimedriver.Target, runtimedriver.Size) (runtimedriver.Terminal, error) {
			return nil, &runtimedriver.UnendedTerminal{ID: "0d3c-terminal", Err: errors.New("the exec was admitted")}
		},
	}, anyHello)
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
	answers <- errors.Join(runtimedriver.ErrMismatch, runtimedriver.ErrNotReady)
	if err := s.client.Check(t.Context()); !errors.Is(err, runtimedriver.ErrMismatch) || !errors.Is(err, runtimedriver.ErrNotReady) {
		t.Fatalf("an error of two kinds came back as %v", err)
	}
	answers <- errors.New("plain")
	if err := s.client.Check(t.Context()); err == nil || slices.ContainsFunc(kinds, func(k error) bool { return errors.Is(err, k) }) {
		t.Fatalf("a plain error came back as %v, with a kind", err)
	}
	var host *runtimedriver.HostError
	if _, err := s.client.Start(t.Context(), session); !errors.As(err, &host) || host.Diagnostic != "p-interactive.service failed: exit 3" {
		t.Fatalf("Start = %v, want the host error and its diagnostic", err)
	}
	var unended *runtimedriver.UnendedTerminal
	if _, err := s.client.OpenTerminal(t.Context(), runtimedriver.Target{Session: session}, runtimedriver.Size{Width: 80, Height: 24}); !errors.As(err, &unended) ||
		unended.ID != "0d3c-terminal" || !errors.Is(err, runtimedriver.ErrUnknownOutcome) {
		t.Fatalf("OpenTerminal = %v, want the unended terminal 0d3c-terminal", err)
	}
}

// A call whose connection is lost after it was sent: an effect that may
// have happened is unknown; a read, and an effect whose gate never ran,
// changed nothing.
func TestACallWhoseConnectionIsLostEndsByWhatMayHaveHappened(t *testing.T) {
	errGate := errors.New("core refused")
	for _, c := range []struct {
		name string
		// call makes the call; the driver signals entered and blocks.
		call    func(cl runtimedriver.Driver, entered func(), g runtimedriver.Gate) error
		gate    error // what core's gate answers
		want    error
		gates   int
		unknown bool
	}{
		{name: "an effect without a gate", want: runtimedriver.ErrUnknownOutcome, unknown: true,
			call: func(cl runtimedriver.Driver, _ func(), _ runtimedriver.Gate) error {
				_, err := cl.Start(context.Background(), session)
				return err
			}},
		{name: "an effect whose gate ran", want: runtimedriver.ErrUnknownOutcome, unknown: true, gates: 1,
			call: func(cl runtimedriver.Driver, _ func(), g runtimedriver.Gate) error {
				_, err := cl.Create(context.Background(), session, "", g)
				return err
			}},
		{name: "an effect whose gate refused", gate: errGate, want: errGate, gates: 1,
			call: func(cl runtimedriver.Driver, _ func(), g runtimedriver.Gate) error {
				_, err := cl.Create(context.Background(), session, "", g)
				return err
			}},
		{name: "an effect whose gate never ran", want: runtimedriver.ErrUnavailable,
			call: func(cl runtimedriver.Driver, _ func(), g runtimedriver.Gate) error {
				_, err := cl.Create(context.Background(), session, "", g)
				return err
			}},
		{name: "a read", want: runtimedriver.ErrUnavailable,
			call: func(cl runtimedriver.Driver, _ func(), _ runtimedriver.Gate) error {
				_, err := cl.Inventory(context.Background())
				return err
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			entered := make(chan struct{})
			block := func(ctx context.Context) { close(entered); <-ctx.Done() }
			s := serve(t, &scripted{
				start: func(ctx context.Context, _ runtimedriver.Session) (runtimedriver.Observation, error) {
					block(ctx)
					return runtimedriver.Observation{}, ctx.Err()
				},
				create: func(ctx context.Context, _ runtimedriver.Session, _ runtimedriver.Incarnation, g runtimedriver.Gate) (runtimedriver.Observation, error) {
					if c.gates > 0 {
						if err := g(); err != nil {
							close(entered)
							<-ctx.Done()
							return runtimedriver.Observation{}, err
						}
					}
					block(ctx)
					return runtimedriver.Observation{}, ctx.Err()
				},
				inventory: func(ctx context.Context) (runtimedriver.Inventory, error) {
					block(ctx)
					return runtimedriver.Inventory{}, ctx.Err()
				},
			}, anyHello)
			var gates atomic.Int32
			done := make(chan error, 1)
			go func() {
				done <- c.call(s.client, nil, func() error { gates.Add(1); return c.gate })
			}()
			select {
			case <-entered:
			case <-time.After(waitLimit):
				t.Fatal("the call did not reach the driver")
			}
			s.driver.Close()
			err := wait(t, done)
			if !errors.Is(err, c.want) || errors.Is(err, runtimedriver.ErrUnknownOutcome) != c.unknown {
				t.Fatalf("the lost call = %v, want %v (unknown %t)", err, c.want, c.unknown)
			}
			if int(gates.Load()) != c.gates {
				t.Fatalf("core's gate ran %d times, want %d", gates.Load(), c.gates)
			}
		})
	}
}

// A call never returns while a gate it started still runs, since its caller
// and the gate share state: a connection lost meanwhile ends the call once
// the gate returned, by what the gate decided.
func TestACallWhoseConnectionIsLostWaitsForItsRunningGate(t *testing.T) {
	errGate := errors.New("core refused")
	for _, c := range []struct {
		name    string
		gate    error // what core's gate decides once it is released
		want    error
		unknown bool
	}{
		{name: "a gate that admits the effect", want: runtimedriver.ErrUnknownOutcome, unknown: true},
		{name: "a gate that refuses it", gate: errGate, want: errGate},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := serve(t, &scripted{create: func(ctx context.Context, _ runtimedriver.Session, _ runtimedriver.Incarnation, g runtimedriver.Gate) (runtimedriver.Observation, error) {
				if err := g(); err != nil {
					return runtimedriver.Observation{}, err
				}
				<-ctx.Done()
				return runtimedriver.Observation{}, ctx.Err()
			}}, anyHello)
			held, release := make(chan struct{}), make(chan struct{})
			var record []string // what the gate records, which its caller reads once the call returned
			done := make(chan error, 1)
			go func() {
				_, err := s.client.Create(context.Background(), session, "", func() error {
					close(held)
					<-release
					record = append(record, "the creation's init state")
					return c.gate
				})
				done <- err
			}()
			waitFor(t, held, "the gate to run")
			s.driver.Close()
			waitFor(t, s.client.Done(), "the client to lose the connection")
			select {
			case err := <-done:
				close(release)
				t.Fatalf("the call returned %v while its gate still ran", err)
			case <-time.After(100 * time.Millisecond):
			}
			close(release)
			err := wait(t, done)
			if !errors.Is(err, c.want) || errors.Is(err, runtimedriver.ErrUnknownOutcome) != c.unknown {
				t.Fatalf("the lost call = %v, want %v (unknown %t)", err, c.want, c.unknown)
			}
			if len(record) != 1 {
				t.Fatalf("the gate recorded %q by the time the call returned, want its one record", record)
			}
		})
	}
}

// A call whose context ends while it waits for its running gate, after the
// connection was lost, returns once the gate did, with the gate's outcome
// and its own cancellation.
func TestACallCancelledWhileItWaitsForItsGateCarriesItsCancellation(t *testing.T) {
	s := serve(t, &scripted{create: func(ctx context.Context, _ runtimedriver.Session, _ runtimedriver.Incarnation, g runtimedriver.Gate) (runtimedriver.Observation, error) {
		if err := g(); err != nil {
			return runtimedriver.Observation{}, err
		}
		<-ctx.Done()
		return runtimedriver.Observation{}, ctx.Err()
	}}, anyHello)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := s.client.Create(ctx, session, "", func() error {
			close(held)
			<-release
			return nil
		})
		done <- err
	}()
	waitFor(t, held, "the gate to run")
	s.driver.Close()
	waitFor(t, s.client.Done(), "the client to lose the connection")
	// The call now waits for its gate; it ends with the connection lost.
	time.Sleep(50 * time.Millisecond)
	cancel()
	close(release)
	err := wait(t, done)
	if !errors.Is(err, runtimedriver.ErrUnknownOutcome) || !errors.Is(err, context.Canceled) {
		t.Fatalf("a call cancelled while it waited for its admitting gate = %v, want an unknown outcome and its cancellation", err)
	}
}

// A driver that answers a call while the call's gate still runs does not
// make the call return before its gate did.
func TestACallAnsweredWhileItsGateRunsWaitsForTheGate(t *testing.T) {
	client, d := rawDriver(t)
	held, release := make(chan struct{}), make(chan struct{})
	var record []string // what the gate records, which its caller reads once the call returned
	done := make(chan error, 1)
	go func() {
		_, err := client.Create(context.Background(), session, "", func() error {
			close(held)
			<-release
			record = append(record, "the creation's init state")
			return nil
		})
		done <- err
	}()
	create := d.receive()
	d.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"gate","params":{"call":%d}}`, create.ID))
	waitFor(t, held, "the gate to run")
	d.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":{"code":-32000,"message":"create: the driver gave up","data":{"kinds":[]}}}`, create.ID))
	select {
	case err := <-done:
		close(release)
		t.Fatalf("the call returned %v while its gate still ran", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := wait(t, done); err == nil || err.Error() != "create: the driver gave up" {
		t.Fatalf("the answered call = %v, want the driver's answer", err)
	}
	if len(record) != 1 {
		t.Fatalf("the gate recorded %q by the time the call returned, want its one record", record)
	}
}

func TestACancelledCallCancelsTheDriversContext(t *testing.T) {
	sawCancel := make(chan struct{})
	s := serve(t, &scripted{
		inventory: func(ctx context.Context) (runtimedriver.Inventory, error) {
			<-ctx.Done()
			close(sawCancel)
			return runtimedriver.Inventory{}, ctx.Err()
		},
		start: func(ctx context.Context, _ runtimedriver.Session) (runtimedriver.Observation, error) {
			<-ctx.Done()
			return runtimedriver.Observation{}, errors.New("the start was abandoned before its first effect")
		},
	}, anyHello)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := s.client.Inventory(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled Inventory = %v, want the cancellation", err)
	}
	select {
	case <-sawCancel:
	case <-time.After(waitLimit):
		t.Fatal("the driver's context was not cancelled")
	}
	// An effect waits for the driver's answer, which says what happened, and
	// keeps its cancellation.
	ctx, cancel = context.WithCancel(t.Context())
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := s.client.Start(ctx, session); err == nil || errors.Is(err, runtimedriver.ErrUnknownOutcome) ||
		!strings.Contains(err.Error(), "abandoned before its first effect") || !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled Start = %v, want the driver's answer and the cancellation", err)
	}
}

func TestAGateRunsOnceAndOnlyForACallThatTakesOne(t *testing.T) {
	second := make(chan error, 1)
	s := serve(t, &scripted{
		create: func(_ context.Context, _ runtimedriver.Session, at runtimedriver.Incarnation, g runtimedriver.Gate) (runtimedriver.Observation, error) {
			if err := g(); err != nil {
				return runtimedriver.Observation{}, err
			}
			second <- g()
			return runtimedriver.Observation{State: runtimedriver.StateStopped, Incarnation: "1"}, nil
		},
	}, anyHello)
	var gates atomic.Int32
	if _, err := s.client.Create(t.Context(), session, "", func() error { gates.Add(1); return nil }); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := <-second; err == nil || gates.Load() != 1 {
		t.Fatalf("a second call of the gate = %v after %d runs, want a refusal after 1", err, gates.Load())
	}
	if _, err := s.client.Create(t.Context(), session, "1", nil); err == nil {
		t.Fatal("a driver called a gate core did not give and was answered")
	}
}

// rawResponse is a response as a test reads it.
type rawResponse struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    *struct {
			Supported []string `json:"supported"`
		} `json:"data"`
	} `json:"error"`
}

// raw is core's end of a connection that a test writes frames to itself.
type raw struct {
	t *testing.T
	c *net.UnixConn
	r *bufio.Reader
}

// rawCore serves the driver d, or one that is never called, to a raw core
// end. It counts the drivers made and delivers Serve's return.
func rawCore(t *testing.T, d ...runtimedriver.Driver) (raw, *atomic.Int32, chan error) {
	core, driverEnd := pair(t)
	t.Cleanup(func() { core.Close() })
	opened := &atomic.Int32{}
	done := make(chan error, 1)
	go func() {
		done <- protocol.Serve(context.Background(), driverEnd, func(context.Context, protocol.Hello) (runtimedriver.Driver, error) {
			opened.Add(1)
			if len(d) == 0 {
				return &scripted{}, nil
			}
			return d[0], nil
		})
	}()
	return raw{t: t, c: core, r: bufio.NewReader(core)}, opened, done
}

// rawDriver connects a client to a driver end that the test writes frames
// to itself, and answers the hello.
func rawDriver(t *testing.T) (*protocol.Client, raw) {
	t.Helper()
	core, driverEnd := pair(t)
	t.Cleanup(func() { driverEnd.Close() })
	d := raw{t: t, c: driverEnd, r: bufio.NewReader(driverEnd)}
	connected := make(chan *protocol.Client, 1)
	go func() {
		client, err := protocol.Connect(context.Background(), core, anyHello)
		if err != nil {
			t.Errorf("Connect: %v", err)
		}
		connected <- client
	}()
	hello := d.receive()
	d.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"protocol":%q,"capabilities":{"image_builder":"x","builder_policy":"","networks":[],"egress_addresses":0,"services":false}}}`,
		hello.ID, protocol.Version))
	client := <-connected
	if client == nil {
		t.FailNow()
	}
	t.Cleanup(func() { client.Close() })
	return client, d
}

func helloFrame(version string) string {
	return `{"jsonrpc":"2.0","id":1,"method":"hello","params":{"protocol":"` + version + `","config":{},"core":{"instance_id":"","endpoint_prefix":"","disk_source_ceilings":null,"builder_storage_pool":"","public_egress":null}}}`
}

func (r raw) send(frame string) {
	r.t.Helper()
	if _, err := r.c.Write([]byte(frame + "\n")); err != nil {
		r.t.Fatalf("write: %v", err)
	}
}

func (r raw) receive() rawResponse {
	r.t.Helper()
	r.c.SetReadDeadline(time.Now().Add(waitLimit))
	line, err := r.r.ReadBytes('\n')
	if err != nil {
		r.t.Fatalf("read: %v", err)
	}
	var res rawResponse
	if err := json.Unmarshal(line, &res); err != nil {
		r.t.Fatalf("response %s: %v", line, err)
	}
	return res
}

// hello agrees the version.
func (r raw) hello() {
	r.t.Helper()
	r.send(helloFrame(protocol.Version))
	if res := r.receive(); res.Error != nil {
		r.t.Fatalf("hello: %+v", res.Error)
	}
}

// wantClosed checks that the driver closed the connection. A connection
// closed with bytes left unread reads as reset.
func (r raw) wantClosed() {
	r.t.Helper()
	r.c.SetReadDeadline(time.Now().Add(waitLimit))
	if rest, err := io.ReadAll(r.r); err != nil && !errors.Is(err, syscall.ECONNRESET) {
		r.t.Fatalf("the driver kept the connection open: %v (read %q)", err, rest)
	}
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
