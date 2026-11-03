package memory_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimedriver/conformance"
	"github.com/lgvo/p.ai/internal/runtimedriver/memory"
)

// These cases show what the conformance suite leaves to each driver: the
// memory driver's own answers, which core's tests rely on.

func memoryFixture(t *testing.T, c memory.Config) *fixture {
	w := memory.NewWorld(c)
	f := &fixture{t: t, world: w, driver: memory.NewDriver(w)}
	t.Cleanup(func() { f.driver.Kill() })
	return f
}

func pass() error { return nil }

func TestDroppedAdmissionSettlesNotApplied(t *testing.T) {
	f := memoryFixture(t, memory.Config{Capabilities: capabilities})
	s := f.Session(conformance.SessionOptions{})
	delayed := f.world.Delay(runtimedriver.EffectCreate)
	if _, err := f.driver.Create(t.Context(), s, "", pass); !errors.Is(err, runtimedriver.ErrUnknownOutcome) {
		t.Fatalf("delayed Create = %v, want ErrUnknownOutcome", err)
	}
	delayed.Drop()
	effect := runtimedriver.Effect{Kind: runtimedriver.EffectCreate, Target: runtimedriver.Target{Session: s}}
	if got, err := f.driver.Settle(t.Context(), effect); got != runtimedriver.NotApplied || err != nil {
		t.Fatalf("Settle(create) of a dropped admission = %v, %v; want not applied", got, err)
	}
	if o, err := f.driver.Create(t.Context(), s, "", pass); err != nil || !o.Complete {
		t.Fatalf("Create after a dropped admission = %+v, %v; want a complete runtime", o, err)
	}
}

func TestDelayedWorkspaceWriteSettlesAppliedOnceLanded(t *testing.T) {
	f := memoryFixture(t, memory.Config{Capabilities: capabilities})
	s := f.Session(conformance.SessionOptions{})
	o, err := f.driver.Create(t.Context(), s, "", pass)
	if err != nil {
		t.Fatal(err)
	}
	at := runtimedriver.Target{Session: s, At: o.Incarnation}
	content := runtimedriver.File{Type: runtimedriver.FileRegular, UID: 1000, GID: 1000, Mode: 0o644, Data: []byte("ref: refs/heads/renamed\n")}
	delayed := f.world.Delay(runtimedriver.EffectWrite)
	if err := f.driver.WriteWorkspace(t.Context(), at, ".git/HEAD", content, pass); !errors.Is(err, runtimedriver.ErrUnknownOutcome) {
		t.Fatalf("delayed WriteWorkspace = %v, want ErrUnknownOutcome", err)
	}
	effect := runtimedriver.Effect{Kind: runtimedriver.EffectWrite, Target: at, Path: ".git/HEAD", Content: content}
	if got, _ := f.driver.Settle(t.Context(), effect); got != runtimedriver.Unknown {
		t.Fatalf("Settle(write) before it landed = %v, want unknown", got)
	}
	delayed.Land()
	if got, err := f.driver.Settle(t.Context(), effect); got != runtimedriver.Applied || err != nil {
		t.Fatalf("Settle(write) once landed = %v, %v; want applied", got, err)
	}
}

func TestRemoveOfAnAbsentWorkspacePathNeedsNoGate(t *testing.T) {
	f := memoryFixture(t, memory.Config{Capabilities: capabilities})
	s := f.Session(conformance.SessionOptions{})
	o, err := f.driver.Create(t.Context(), s, "", pass)
	if err != nil {
		t.Fatal(err)
	}
	gate := func() error { t.Error("the gate was called with nothing to remove"); return nil }
	if err := f.driver.RemoveWorkspace(t.Context(), runtimedriver.Target{Session: s, At: o.Incarnation}, "missing", gate); err != nil {
		t.Fatalf("RemoveWorkspace of an absent path: %v", err)
	}
}

func TestCreateAndBuildRefuseBeforeTheGateWhenTheScopeIsFull(t *testing.T) {
	f := memoryFixture(t, memory.Config{Capacity: 1, Capabilities: capabilities})
	if _, err := f.driver.Create(t.Context(), f.Session(conformance.SessionOptions{}), "", pass); err != nil {
		t.Fatal(err)
	}
	refuse := func() error { t.Error("the gate was called in a full scope"); return nil }
	if _, err := f.driver.Create(t.Context(), f.Session(conformance.SessionOptions{}), "", refuse); err == nil {
		t.Fatal("Create in a full scope succeeded")
	}
	if _, err := f.driver.BuildImage(t.Context(), f.Build(), refuse); err == nil {
		t.Fatal("BuildImage in a full scope succeeded")
	}
}

func TestCreateRefusesAPublicAddressAnotherRuntimeHolds(t *testing.T) {
	f := memoryFixture(t, memory.Config{Capabilities: capabilities})
	first := f.Session(conformance.SessionOptions{PublicEgress: true})
	second := f.Session(conformance.SessionOptions{})
	second.Network = first.Network
	if _, err := f.driver.Create(t.Context(), first, "", pass); err != nil {
		t.Fatal(err)
	}
	if _, err := f.driver.Create(t.Context(), second, "", func() error { t.Error("gate called"); return nil }); err == nil {
		t.Fatal("two runtimes were given the same public-egress address")
	}
}

func TestStartOfARunningRuntimeWhoseHostIsNotReadyStopsIt(t *testing.T) {
	f := memoryFixture(t, memory.Config{Capabilities: capabilities})
	s := f.Session(conformance.SessionOptions{})
	if _, err := f.driver.Create(t.Context(), s, "", pass); err != nil {
		t.Fatal(err)
	}
	if _, err := f.driver.Start(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	f.world.UnreadyHost(s.Ref, "the host is restarting")
	_, err := f.driver.Start(t.Context(), s)
	var hostErr *runtimedriver.HostError
	if !errors.As(err, &hostErr) || hostErr.Diagnostic != "the host is restarting" {
		t.Fatalf("Start of a running runtime whose host is not ready = %v, want its HostError", err)
	}
	if o, _ := f.driver.Inspect(t.Context(), s); o.State != runtimedriver.StateStopped {
		t.Fatalf("after the failed Start the runtime is %s, want stopped", o.State)
	}
}

func TestDeleteRefusesARuntimeRenamedAway(t *testing.T) {
	f := memoryFixture(t, memory.Config{Capabilities: capabilities})
	s := f.Session(conformance.SessionOptions{})
	o, err := f.driver.Create(t.Context(), s, "", pass)
	if err != nil {
		t.Fatal(err)
	}
	f.world.RenameAway(s.Ref)
	if err := f.driver.Delete(t.Context(), s.Ref, o.Incarnation, pass); !errors.Is(err, runtimedriver.ErrMismatch) {
		t.Fatalf("Delete of a renamed runtime = %v, want ErrMismatch", err)
	}
}

func TestAKilledDriverIsUnavailable(t *testing.T) {
	f := memoryFixture(t, memory.Config{Capabilities: capabilities})
	f.driver.Kill()
	if _, err := f.driver.Inspect(t.Context(), f.Session(conformance.SessionOptions{})); !errors.Is(err, runtimedriver.ErrUnavailable) {
		t.Fatalf("Inspect on a killed driver = %v, want ErrUnavailable", err)
	}
}

func TestAnUnsettledBuildHoldsItsReferenceAfterItsBuilderIsDeleted(t *testing.T) {
	f := memoryFixture(t, memory.Config{Capabilities: capabilities})
	b := f.Build()
	delayed := f.world.Delay(runtimedriver.EffectBuild)
	if _, err := f.driver.BuildImage(t.Context(), b, pass); !errors.Is(err, runtimedriver.ErrUnknownOutcome) {
		t.Fatalf("delayed BuildImage = %v, want ErrUnknownOutcome", err)
	}
	inv, err := f.driver.Inventory(t.Context())
	if err != nil || len(inv.Objects) != 1 || inv.Objects[0].Ref != b.Ref {
		t.Fatalf("Inventory during the unsettled build = %+v, %v; want its builder", inv, err)
	}
	if err := f.driver.Delete(t.Context(), b.Ref, "", pass); err != nil {
		t.Fatalf("Delete of the build's builder: %v", err)
	}
	if _, err := f.driver.BuildImage(t.Context(), b, func() error { t.Error("gate called"); return nil }); err == nil {
		t.Fatal("a build of a reference whose publication may still land succeeded")
	}
	delayed.Land()
	if got, err := f.driver.Settle(t.Context(), runtimedriver.Effect{Kind: runtimedriver.EffectBuild, Build: b}); got != runtimedriver.Applied || err != nil {
		t.Fatalf("Settle(build) once landed = %v, %v; want applied", got, err)
	}
}

// runningSession creates and starts a new session, returning its target.
func runningSession(t *testing.T, f *fixture) runtimedriver.Target {
	t.Helper()
	s := f.Session(conformance.SessionOptions{})
	if _, err := f.driver.Create(t.Context(), s, "", pass); err != nil {
		t.Fatal(err)
	}
	o, err := f.driver.Start(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	return runtimedriver.Target{Session: s, At: o.Incarnation}
}

func TestAFailedServiceLogsWhyItFailed(t *testing.T) {
	f := memoryFixture(t, memory.Config{Capabilities: capabilities})
	at := runningSession(t, f)
	broken := f.world.AddService(at.Session.Ref, "broken", "", runtimedriver.ServiceStopped, true)
	if err := f.driver.ServiceAction(t.Context(), at, broken, runtimedriver.ActionStart); err != nil {
		t.Fatal(err)
	}
	f.world.Progress(at.Session.Ref, broken)
	logs, err := f.driver.ServiceLogs(t.Context(), at, broken)
	if err != nil || !strings.Contains(logs.Text, "failed to start") {
		t.Fatalf("ServiceLogs of a failed service = %+v, %v; want why it failed", logs, err)
	}
}

func TestStoppingASessionStopsItsServices(t *testing.T) {
	f := memoryFixture(t, memory.Config{Capabilities: capabilities})
	at := runningSession(t, f)
	f.world.AddService(at.Session.Ref, "web", "", runtimedriver.ServiceRunning, false)
	if _, err := f.driver.Stop(t.Context(), at.Session); err != nil {
		t.Fatal(err)
	}
	if _, err := f.driver.Start(t.Context(), at.Session); err != nil {
		t.Fatal(err)
	}
	list, err := f.driver.Services(t.Context(), at)
	if err != nil || len(list) != 1 || list[0].State != runtimedriver.ServiceStopped {
		t.Fatalf("Services after the session stopped and started = %+v, %v; want web stopped", list, err)
	}
}

func TestAnInventoryHoldingAnOversizedNameOrDescriptionIsAnError(t *testing.T) {
	for _, c := range []struct {
		name, description string
	}{
		{strings.Repeat("n", runtimedriver.MaxServiceName+1), "fits"},
		{"fits", strings.Repeat("d", 257)},
	} {
		f := memoryFixture(t, memory.Config{Capabilities: capabilities})
		at := runningSession(t, f)
		f.world.AddService(at.Session.Ref, c.name, c.description, runtimedriver.ServiceStopped, false)
		if list, err := f.driver.Services(t.Context(), at); err == nil {
			t.Fatalf("Services with a %d-byte name and a %d-byte description = %+v, want an error", len(c.name), len(c.description), list)
		}
	}
}
