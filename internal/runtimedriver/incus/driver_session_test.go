package incus

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

const testRuntime = "p-" + testUUID

func TestDescribeOffersPublicEgressOnlyWithItsSubstrate(t *testing.T) {
	w := newIncusWorld(t)
	got := w.driver().Describe()
	if got.ImageBuilder != ImageBuilderVersion || !slices.Equal(got.Networks, []string{"none"}) || got.EgressAddresses != 0 || !got.Services {
		t.Fatalf("without egress: %+v", got)
	}
	w.public = testEgressSubstrate()
	c := w.public
	slots := 0
	for {
		if _, err := (control.PublicEgressConfig{Network: c.Network, ACL: c.ACL, BridgeIPv4: c.BridgeIPv4, DNS: c.DNS, SudoBinary: c.SudoBinary, NftBinary: c.NftBinary, BridgeProofBinary: c.BridgeProofBinary}).PublicEgressGuestIPv4(slots); err != nil {
			break
		}
		slots++
	}
	got = w.driver().Describe()
	if !slices.Equal(got.Networks, []string{"none", "public-egress"}) || got.EgressAddresses != slots || slots == 0 {
		t.Fatalf("with egress: %+v, P reserves %d addresses", got, slots)
	}
}

func TestCheckProvesTheProjectConfinement(t *testing.T) {
	w := newIncusWorld(t)
	if err := w.driver().Check(context.Background()); err != nil {
		t.Fatalf("confined project refused: %v", err)
	}
	b := w.backend()
	b.config.DiskSourceCeilings = []string{"/var/lib/p-vm/endpoints"}
	if err := NewDriver(b).Check(context.Background()); err == nil {
		t.Fatal("a project whose disk ceilings differ from the trusted ones passed")
	}
}

func TestSessionSpecMustBeOneProjectsSession(t *testing.T) {
	for name, edit := range map[string]func(*runtimedriver.Session){
		"build reference":       func(s *runtimedriver.Session) { s.Ref.Kind = runtimedriver.KindBuild },
		"no project label":      func(s *runtimedriver.Session) { s.Labels = runtimedriver.Labels{} },
		"another label":         func(s *runtimedriver.Session) { s.Labels["build"] = testUUID },
		"another repository":    func(s *runtimedriver.Session) { s.Workspace.Repository = "team/other" },
		"no endpoint directory": func(s *runtimedriver.Session) { s.Endpoints = "" },
		"endpoints elsewhere":   func(s *runtimedriver.Session) { s.Endpoints = "/srv/endpoints/" + testUUID },
		"image alias":           func(s *runtimedriver.Session) { s.Image = "nixos" },
	} {
		t.Run(name, func(t *testing.T) {
			w := newIncusWorld(t)
			s := testSessionSpec(t)
			edit(&s)
			if _, err := w.driver().Inspect(context.Background(), s); err == nil || len(w.commands) != 0 {
				t.Fatalf("invalid spec reached Incus: %v %v", err, w.commands)
			}
		})
	}
}

func TestCreateGatesBeforeInitAndLeavesTheRuntimeStoppedAndComplete(t *testing.T) {
	w := newIncusWorld(t)
	s := testSessionSpec(t)
	s.Grants = []runtimedriver.Grant{testGrant()}
	gates := 0
	got, err := w.driver().Create(context.Background(), s, "", func() error {
		if gates++; len(w.mutations()) != 0 || w.count("list", "--format", "json") == 0 {
			t.Error("gate called before the whole project was proven free, or after an effect")
		}
		return nil
	})
	if err != nil || gates != 1 {
		t.Fatalf("create: gates=%d %v", gates, err)
	}
	in := w.instance(testRuntime)
	want := runtimedriver.Incarnation(in.config["volatile.uuid"] + "/" + in.config["volatile.uuid.generation"])
	if got.State != runtimedriver.StateStopped || !got.Complete || got.Incarnation != want || got.Host.Ready {
		t.Fatalf("created runtime: %+v, want stopped and complete at %s", got, want)
	}
	if mutations := w.mutations(); !slices.Equal(mutations, []string{"init", "device p-grant-data", "device p-endpoint"}) {
		t.Fatalf("effects: %v", mutations)
	}
	identity := in.files["/etc/p/git/identity"]
	if identity.uid != 1000 || identity.mode != 0400 || !bytes.Equal(identity.data, s.GitKey) || !bytes.Contains(in.files["/etc/p/session.json"].data, []byte(`"filesystem_mounts"`)) {
		t.Fatalf("guest files not installed: identity %+v session %s", identity, in.files["/etc/p/session.json"].data)
	}
}

func TestCreateWithoutIncarnationNeverAdoptsAnExistingRuntime(t *testing.T) {
	w := newIncusWorld(t)
	s := testSessionSpec(t)
	if _, err := w.driver().Create(context.Background(), s, "", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	for name, setup := range map[string]func(){
		"at its address": func() {},
		"renamed":        func() { w.change(testRuntime, func(in *worldInstance) { in.name = "renamed-by-hand" }) },
	} {
		setup()
		before := w.mutations()
		_, err := w.driver().Create(context.Background(), s, "", func() error { t.Fatalf("%s: gate called", name); return nil })
		if !errors.Is(err, runtimedriver.ErrMismatch) || !slices.Equal(w.mutations(), before) {
			t.Fatalf("%s: %v, effects %v", name, err, w.mutations()[len(before):])
		}
	}
}

func TestCreateRefusesBeforeItsGateWhenAHostCheckFails(t *testing.T) {
	w := newIncusWorld(t)
	s := testSessionSpec(t)
	s.Grants = []runtimedriver.Grant{testGrant()}
	w.grantErr = errors.New("runtime grant data source identity changed")
	refused := errors.New("core could not record its intent")
	if _, err := w.driver().Create(context.Background(), s, "", func() error { t.Fatal("gate called"); return nil }); err == nil || len(w.mutations()) != 0 {
		t.Fatalf("changed grant source: %v %v", err, w.mutations())
	}
	w.grantErr = nil
	if _, err := w.driver().Create(context.Background(), s, "", func() error { return refused }); err != refused || len(w.mutations()) != 0 {
		t.Fatalf("gate error not returned unchanged before any effect: %v %v", err, w.mutations())
	}
}

func TestCreateFailingAfterItsGateIsAnUnknownOutcome(t *testing.T) {
	w := newIncusWorld(t)
	w.before = func(args []string) error {
		if args[0] == "init" {
			return errors.New("Incus connection lost")
		}
		return nil
	}
	_, err := w.driver().Create(context.Background(), testSessionSpec(t), "", func() error { return nil })
	if !errors.Is(err, runtimedriver.ErrUnknownOutcome) {
		t.Fatalf("lost init is not an unknown outcome: %v", err)
	}
}

// interruptedCreate leaves the runtime of s initialized without its endpoint
// device and guest files, as a driver that died after the init would.
func interruptedCreate(t *testing.T, w *incusWorld, s runtimedriver.Session) runtimedriver.Observation {
	t.Helper()
	w.before = func(args []string) error {
		if args[0] == "config" && args[4] == "p-endpoint" {
			return errors.New("driver died")
		}
		return nil
	}
	if _, err := w.driver().Create(context.Background(), s, "", func() error { return nil }); !errors.Is(err, runtimedriver.ErrUnknownOutcome) {
		t.Fatalf("interrupted create: %v", err)
	}
	w.before = nil
	got, err := w.driver().Inspect(context.Background(), s)
	if err != nil || got.State != runtimedriver.StateStopped || got.Complete || got.Incarnation == "" {
		t.Fatalf("interrupted runtime: %+v %v", got, err)
	}
	return got
}

func TestCreateAtAnIncarnationCompletesThatRuntimeWithoutInit(t *testing.T) {
	w := newIncusWorld(t)
	s := testSessionSpec(t)
	interrupted := interruptedCreate(t, w, s)
	before := w.mutations()
	if _, err := w.driver().Create(context.Background(), s, "00000000-0000-4000-8000-000000000000/00000000-0000-4000-8000-000000000000", nil); !errors.Is(err, runtimedriver.ErrMismatch) || !slices.Equal(w.mutations(), before) {
		t.Fatalf("another incarnation: %v %v", err, w.mutations())
	}
	got, err := w.driver().Create(context.Background(), s, interrupted.Incarnation, nil)
	if err != nil || got.State != runtimedriver.StateStopped || !got.Complete || got.Incarnation != interrupted.Incarnation {
		t.Fatalf("completion: %+v %v", got, err)
	}
	if added := w.mutations()[len(before):]; !slices.Equal(added, []string{"device p-endpoint"}) {
		t.Fatalf("completion effects: %v", added)
	}
	if _, ok := w.instance(testRuntime).files["/etc/p/session.json"]; !ok {
		t.Fatal("guest files not installed")
	}
	if _, err := w.driver().Create(context.Background(), s, interrupted.Incarnation, nil); err != nil || w.count("init") != 1 {
		t.Fatalf("completing a complete runtime: %v, %d inits", err, w.count("init"))
	}
}

func TestCreateAtAnIncarnationRefusesADifferentGuestFile(t *testing.T) {
	w := newIncusWorld(t)
	s := testSessionSpec(t)
	interrupted := interruptedCreate(t, w, s)
	changed := guestFile{typ: "file", uid: 0, gid: 0, mode: 0644, data: []byte(`{"schema":"other"}`)}
	w.change(testRuntime, func(in *worldInstance) {
		for _, path := range []string{"/etc", "/etc/p"} {
			in.files[path] = guestFile{typ: "directory", uid: 0, gid: 0, mode: 0755}
		}
		in.files["/etc/p/session.json"] = changed
	})
	if _, err := w.driver().Create(context.Background(), s, interrupted.Incarnation, nil); err == nil {
		t.Fatal("a different guest file was accepted")
	}
	if got := w.instance(testRuntime).files["/etc/p/session.json"]; !bytes.Equal(got.data, changed.data) {
		t.Fatal("a different guest file was overwritten")
	}
	w.change(testRuntime, func(in *worldInstance) { in.status = "Running" })
	if _, err := w.driver().Create(context.Background(), s, interrupted.Incarnation, nil); err == nil {
		t.Fatal("a running runtime was completed")
	}
}

func TestInspectReportsAbsentIncompleteAndDrift(t *testing.T) {
	w := newIncusWorld(t)
	s := testSessionSpec(t)
	s.Grants = []runtimedriver.Grant{testGrant()}
	ctx := context.Background()
	if got, err := w.driver().Inspect(ctx, s); err != nil || got.State != runtimedriver.StateAbsent {
		t.Fatalf("nothing at the address: %+v %v", got, err)
	}
	if _, err := w.driver().Create(ctx, s, "", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	var grant map[string]string
	w.change(testRuntime, func(in *worldInstance) { grant = in.devices["p-grant-data"]; delete(in.devices, "p-grant-data") })
	if got, err := w.driver().Inspect(ctx, s); err != nil || got.State != runtimedriver.StateStopped || got.Complete {
		t.Fatalf("missing grant device: %+v %v", got, err)
	}
	for name, drift := range map[string]func(*worldInstance){
		"changed grant device": func(in *worldInstance) { in.devices["p-grant-data"]["readonly"] = "false" },
		"extra device":         func(in *worldInstance) { in.devices["eth0"] = map[string]string{"type": "nic", "network": "incusbr0"} },
		"another project":      func(in *worldInstance) { in.config["user.p.project_path"] = "team/other" },
		"privileged":           func(in *worldInstance) { in.config["security.privileged"] = "true" },
	} {
		w.change(testRuntime, func(in *worldInstance) {
			in.devices = map[string]map[string]string{"p-endpoint": in.devices["p-endpoint"], "p-grant-data": maps.Clone(grant)}
			in.config["user.p.project_path"] = "team/app"
			delete(in.config, "security.privileged")
		})
		if _, err := w.driver().Inspect(ctx, s); err != nil {
			t.Fatalf("restored runtime: %v", err)
		}
		w.change(testRuntime, drift)
		if _, err := w.driver().Inspect(ctx, s); !errors.Is(err, runtimedriver.ErrMismatch) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestInspectReadsTheIncarnationAndTheSessionHost(t *testing.T) {
	w := newIncusWorld(t)
	s := testSessionSpec(t)
	ctx := context.Background()
	created, err := w.driver().Create(ctx, s, "", func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	w.change(testRuntime, func(in *worldInstance) { in.status = "Running" })
	if got, err := w.driver().Inspect(ctx, s); err != nil || got.State != runtimedriver.StateRunning || !got.Host.Ready || got.Incarnation != created.Incarnation {
		t.Fatalf("running with a ready host: %+v %v", got, err)
	}
	w.change(testRuntime, func(in *worldInstance) {
		in.host, in.journal = "failed/failed/exit-code", "p-interactive.service: tmux exited"
	})
	if got, err := w.driver().Inspect(ctx, s); err != nil || got.Host.Ready || !strings.Contains(got.Host.Diagnostic, "tmux exited") {
		t.Fatalf("failed host: %+v %v", got, err)
	}
	w.before = func(args []string) error {
		if args[0] == "exec" {
			return errors.New("guest systemd not accepting exec")
		}
		return nil
	}
	if got, err := w.driver().Inspect(ctx, s); err != nil || !got.Host.Unknown || got.State != runtimedriver.StateRunning {
		t.Fatalf("unreadable host: %+v %v", got, err)
	}
	w.before = nil
	w.change(testRuntime, func(in *worldInstance) { in.config["volatile.uuid.generation"] = w.newUUID() })
	if got, err := w.driver().Inspect(ctx, s); err != nil || got.Incarnation == created.Incarnation || got.Incarnation == "" {
		t.Fatalf("restored runtime kept its incarnation: %+v %v", got, err)
	}
}

// createdSession is the stopped, complete runtime of s.
func createdSession(t *testing.T, w *incusWorld, s runtimedriver.Session) runtimedriver.Observation {
	t.Helper()
	got, err := w.driver().Create(context.Background(), s, "", func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestStartWaitsUntilTheSessionHostIsReady(t *testing.T) {
	w := newIncusWorld(t)
	s := testSessionSpec(t)
	created := createdSession(t, w, s)
	w.change(testRuntime, func(in *worldInstance) { in.hostReads = 3 })
	got, err := w.driver().Start(context.Background(), s)
	if err != nil || got.State != runtimedriver.StateRunning || !got.Host.Ready || got.Incarnation != created.Incarnation {
		t.Fatalf("start: %+v %v", got, err)
	}
	if reads := w.instance(testRuntime).reads; reads != 4 {
		t.Fatalf("returned after %d host reads, want the 4th, the first ready one", reads)
	}
	before := w.mutations()
	if got, err := w.driver().Start(context.Background(), s); err != nil || !got.Host.Ready || !slices.Equal(w.mutations(), before) {
		t.Fatalf("start of a ready runtime: %+v %v %v", got, err, w.mutations())
	}
}

func TestStartOfAFailedHostStopsTheRuntimeWithItsDiagnostic(t *testing.T) {
	w := newIncusWorld(t)
	s := testSessionSpec(t)
	createdSession(t, w, s)
	w.change(testRuntime, func(in *worldInstance) {
		in.host, in.journal = "failed/failed/exit-code", "p-interactive.service: /bin/bash: not found"
	})
	_, err := w.driver().Start(context.Background(), s)
	var host *runtimedriver.HostError
	if !errors.As(err, &host) || !strings.Contains(host.Diagnostic, "/bin/bash: not found") {
		t.Fatalf("failed host: %v", err)
	}
	if in := w.instance(testRuntime); in.status != "Stopped" || !slices.Equal(w.mutations()[len(w.mutations())-2:], []string{"start", "stop"}) {
		t.Fatalf("failed host left %s after %v", in.status, w.mutations())
	}
}

func TestStartThatIncusRefusesIsAHostErrorAndLeavesTheRuntimeStopped(t *testing.T) {
	w := newIncusWorld(t)
	s := testSessionSpec(t)
	createdSession(t, w, s)
	w.before = func(args []string) error {
		if args[0] == "start" {
			return errors.New("Failed to start device \"p-grant-data\"")
		}
		return nil
	}
	_, err := w.driver().Start(context.Background(), s)
	var host *runtimedriver.HostError
	if !errors.As(err, &host) || !strings.Contains(host.Diagnostic, "Failed to start device") || w.instance(testRuntime).status != "Stopped" {
		t.Fatalf("refused start: %v", err)
	}
}

func TestStartChecksTheEndpointDirectoryBeforeAnyEffect(t *testing.T) {
	w := newIncusWorld(t)
	s := testSessionSpec(t)
	createdSession(t, w, s)
	before := w.mutations()
	w.endpointErr = errors.New("endpoint socket must be daemon-owned mode 0666")
	_, err := w.driver().Start(context.Background(), s)
	var host *runtimedriver.HostError
	if err == nil || errors.As(err, &host) || !slices.Equal(w.mutations(), before) {
		t.Fatalf("changed endpoint directory: %v %v", err, w.mutations())
	}
}

func TestStopStopsTheRuntimeGracefully(t *testing.T) {
	w := newIncusWorld(t)
	s := testSessionSpec(t)
	createdSession(t, w, s)
	if _, err := w.driver().Start(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	got, err := w.driver().Stop(context.Background(), s)
	if err != nil || got.State != runtimedriver.StateStopped || got.Host.Ready {
		t.Fatalf("stop: %+v %v", got, err)
	}
	if w.count("stop", testRuntime, "--timeout", "30") != 1 || w.count("stop", testRuntime, "--force") != 0 {
		t.Fatalf("not one graceful stop: %v", w.commands)
	}
	if _, err := w.driver().Stop(context.Background(), s); err != nil || w.count("stop") != 1 {
		t.Fatalf("stop of a stopped runtime: %v %v", err, w.commands)
	}
}

func TestHostReadinessRetriesItsObservationAndStopsAFailure(t *testing.T) {
	ctx := context.Background()
	attempts, stops := 0, 0
	stop := func(context.Context) error { stops++; return nil }
	_, err := awaitHostReady(ctx, time.Second, func(context.Context) (HostObservation, error) {
		if attempts++; attempts == 1 {
			return HostObservation{}, errors.New("guest systemd not yet accepting exec")
		}
		return HostObservation{Observation: Observation{Exists: true, Status: "Running"}, Ready: true}, nil
	}, stop)
	if err != nil || attempts != 2 || stops != 0 {
		t.Fatalf("transient boot observation: attempts=%d stops=%d err=%v", attempts, stops, err)
	}
	_, err = awaitHostReady(ctx, 25*time.Millisecond, func(context.Context) (HostObservation, error) {
		return HostObservation{Observation: Observation{Exists: true, Status: "Running"}, Unit: "activating/start-post"}, nil
	}, stop)
	if err == nil || !strings.Contains(err.Error(), "timed out") || stops != 1 {
		t.Fatalf("readiness timeout left running: stops=%d err=%v", stops, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = awaitHostReady(canceled, time.Second, func(context.Context) (HostObservation, error) {
		return HostObservation{Observation: Observation{Exists: true, Status: "Running"}, Unit: "activating/start-post"}, nil
	}, stop)
	if err == nil || stops != 1 {
		t.Fatalf("a canceled caller stopped the runtime: stops=%d err=%v", stops, err)
	}
}

func TestHostDiagnosticIsBoundedValidUTF8(t *testing.T) {
	got := hostError(errors.New("\xff" + strings.Repeat("é", 4000) + "; stopping failed")).Diagnostic
	if len(got) > 4096 || len(got) < 4095 || !utf8.ValidString(got) || !strings.HasPrefix(got, "�é") {
		t.Fatalf("diagnostic of %d bytes, valid %t: %.20q", len(got), utf8.ValidString(got), got)
	}
	if got := hostError(errors.New("tmux exited")).Diagnostic; got != "tmux exited" {
		t.Fatalf("short diagnostic changed: %q", got)
	}
}

// restoreAfterDevice makes Incus restore the runtime, a new generation,
// right after the named device is added to it.
func restoreAfterDevice(w *incusWorld, device string) {
	w.after = func(args []string) error {
		if args[0] == "config" && args[4] == device {
			w.mu.Lock()
			w.instance(testRuntime).config["volatile.uuid.generation"] = w.newUUID()
			w.mu.Unlock()
		}
		return nil
	}
}

func TestCreateAddsDevicesOnlyToTheRuntimeItInitialized(t *testing.T) {
	w := newIncusWorld(t)
	s := testSessionSpec(t)
	s.Grants = []runtimedriver.Grant{testGrant()}
	restoreAfterDevice(w, "p-grant-data")
	_, err := w.driver().Create(context.Background(), s, "", func() error { return nil })
	if !errors.Is(err, runtimedriver.ErrMismatch) || !errors.Is(err, runtimedriver.ErrUnknownOutcome) {
		t.Fatalf("restore between two device additions: %v", err)
	}
	if mutations := w.mutations(); !slices.Equal(mutations, []string{"init", "device p-grant-data"}) {
		t.Fatalf("effects on the restored runtime: %v", mutations)
	}
	if _, ok := w.instance(testRuntime).files["/etc/p/session.json"]; ok {
		t.Fatal("guest files written into the restored runtime")
	}
}

func TestCreateAtAnIncarnationAddsDevicesOnlyToThatRuntime(t *testing.T) {
	w := newIncusWorld(t)
	s := testSessionSpec(t)
	s.Grants = []runtimedriver.Grant{testGrant()}
	w.before = func(args []string) error {
		if args[0] == "config" {
			return errors.New("driver died")
		}
		return nil
	}
	if _, err := w.driver().Create(context.Background(), s, "", func() error { return nil }); !errors.Is(err, runtimedriver.ErrUnknownOutcome) {
		t.Fatalf("interrupted create: %v", err)
	}
	w.before = nil
	interrupted, err := w.driver().Inspect(context.Background(), s)
	if err != nil || interrupted.Complete {
		t.Fatalf("interrupted runtime: %+v %v", interrupted, err)
	}
	before := w.mutations()
	restoreAfterDevice(w, "p-grant-data")
	if _, err := w.driver().Create(context.Background(), s, interrupted.Incarnation, nil); !errors.Is(err, runtimedriver.ErrMismatch) {
		t.Fatalf("restore between two device additions: %v", err)
	}
	if added := w.mutations()[len(before):]; !slices.Equal(added, []string{"device p-grant-data"}) {
		t.Fatalf("effects on the restored runtime: %v", added)
	}
}

// A runtime that starts between two device additions gets no further device.
func TestCreateAddsNoDeviceToARuntimeThatStarted(t *testing.T) {
	w := newIncusWorld(t)
	s := testSessionSpec(t)
	second := testGrant()
	second.Name, second.Source, second.Inode = "more", "/var/lib/p-vm/grants/team/more", 12
	s.Grants = []runtimedriver.Grant{testGrant(), second}
	w.after = func(args []string) error {
		if args[0] == "config" && args[4] == "p-grant-data" {
			w.mu.Lock()
			w.instance(testRuntime).status = "Running"
			w.mu.Unlock()
		}
		return nil
	}
	if _, err := w.driver().Create(context.Background(), s, "", func() error { return nil }); err == nil || !errors.Is(err, runtimedriver.ErrUnknownOutcome) {
		t.Fatalf("runtime started between two device additions: %v", err)
	}
	if mutations := w.mutations(); !slices.Equal(mutations, []string{"init", "device p-grant-data"}) {
		t.Fatalf("effects on the running runtime: %v", mutations)
	}
}

// While Incus cleans up after a poweroff the failed host started, it refuses
// the stop and lists a placeholder for the instance for a moment; the startup
// failure is reported once the runtime is seen stopped, without the refused
// stop.
func TestStartOfAFailedHostIsReportedOnceTheRuntimeIsSeenStopped(t *testing.T) {
	w := newIncusWorld(t)
	s := testSessionSpec(t)
	createdSession(t, w, s)
	w.change(testRuntime, func(in *worldInstance) {
		in.host, in.journal = "failed/failed/exit-code", "p-interactive.service: exited"
	})
	placeholders := 0
	w.before = func(args []string) error {
		if args[0] == "stop" {
			w.change(testRuntime, func(in *worldInstance) { in.status = "Stopped" })
			placeholders = 3
			return errors.New("Incus instance state unavailable")
		}
		return nil
	}
	b := w.backend()
	run := b.run
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		if len(argv) > 3 && argv[3] == "list" && placeholders > 0 {
			placeholders--
			return []byte(unrenderedInstance), nil
		}
		return run(ctx, binary, argv, env)
	}
	_, err := NewDriver(b).Start(context.Background(), s)
	var host *runtimedriver.HostError
	if !errors.As(err, &host) || !strings.Contains(host.Diagnostic, "exited") || strings.Contains(host.Diagnostic, "stopping failed") || placeholders != 0 {
		t.Fatalf("startup failure not reported once the runtime was seen stopped: %v (%d placeholders left)", err, placeholders)
	}
}

// A stop that is refused while the runtime keeps running, or whose runtime
// goes missing, is named in the startup failure, within the stop's bound.
func TestStartOfAFailedHostNamesAStopNotSeenToTakeEffect(t *testing.T) {
	for _, c := range []struct {
		name string
		gone bool
	}{{"still running", false}, {"gone", true}} {
		t.Run(c.name, func(t *testing.T) {
			w := newIncusWorld(t)
			s := testSessionSpec(t)
			createdSession(t, w, s)
			w.change(testRuntime, func(in *worldInstance) { in.host = "failed/failed/exit-code" })
			w.before = func(args []string) error {
				if args[0] == "stop" {
					if c.gone {
						w.mu.Lock()
						w.instances = slices.DeleteFunc(w.instances, func(in *worldInstance) bool { return in.name == testRuntime })
						w.mu.Unlock()
					}
					return errors.New("Incus state postcondition failed")
				}
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			answer := make(chan error, 1)
			go func() {
				_, err := w.driver().Start(ctx, s)
				answer <- err
			}()
			select {
			case err := <-answer:
				if err == nil || !strings.Contains(err.Error(), "stopping failed: Incus state postcondition failed") {
					t.Fatalf("a stop not seen to take effect is not named: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Start kept observing after the stop's bound")
			}
		})
	}
}

// A failed host powers its runtime off by itself; a stop Incus refuses
// meanwhile does not make Start return while the runtime still runs.
func TestStartOfAFailedHostReturnsOnlyOnceTheRuntimeStopped(t *testing.T) {
	w := newIncusWorld(t)
	s := testSessionSpec(t)
	createdSession(t, w, s)
	w.change(testRuntime, func(in *worldInstance) {
		in.host, in.journal = "failed/failed/exit-code", "p-interactive.service: exited"
	})
	w.before = func(args []string) error {
		if args[0] == "stop" {
			go func() {
				time.Sleep(300 * time.Millisecond)
				w.change(testRuntime, func(in *worldInstance) { in.status = "Stopped" })
			}()
			return errors.New("The instance is already stopping")
		}
		return nil
	}
	_, err := w.driver().Start(context.Background(), s)
	var host *runtimedriver.HostError
	if !errors.As(err, &host) || w.instance(testRuntime).status != "Stopped" {
		t.Fatalf("failed host: %v, runtime %s", err, w.instance(testRuntime).status)
	}
}
