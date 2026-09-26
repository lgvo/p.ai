package plugin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type runtimeFixture struct {
	state RuntimeState
	calls []string
}

func (f *runtimeFixture) Inspect(context.Context) (RuntimeState, error) {
	f.calls = append(f.calls, "inspect")
	return f.state, nil
}
func (f *runtimeFixture) Create(context.Context) (RuntimeState, error) {
	f.calls = append(f.calls, "create")
	f.state = RuntimeState{Exists: true, Status: "Stopped"}
	return f.state, nil
}
func (f *runtimeFixture) Start(context.Context) (RuntimeState, error) {
	f.calls = append(f.calls, "start")
	f.state.Status = "Running"
	return f.state, nil
}
func (f *runtimeFixture) Stop(context.Context) (RuntimeState, error) {
	f.calls = append(f.calls, "stop")
	f.state.Status = "Stopped"
	return f.state, nil
}
func (f *runtimeFixture) Delete(context.Context) (RuntimeState, error) {
	f.calls = append(f.calls, "delete")
	f.state = RuntimeState{}
	return f.state, nil
}
func (f *runtimeFixture) Assemble(context.Context) (RuntimeState, error) {
	f.calls = append(f.calls, "assemble")
	return f.state, nil
}
func (f *runtimeFixture) ObserveHost(context.Context) (RuntimeState, error) {
	f.calls = append(f.calls, "observe-host")
	f.state.HostUnit = "active/running"
	f.state.HostReady = true
	return f.state, nil
}

func buildRuntimePackage(t *testing.T, source string) Active {
	t.Helper()
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("Go compiler unavailable")
	}
	dir := t.TempDir()
	manifest, err := os.ReadFile(filepath.Join(source, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "plugin.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(goPath, "build", "-o", filepath.Join(dir, "runtime.wasm"), "./"+source)
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOTOOLCHAIN=local")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build runtime WASI: %v: %s", err, output)
	}
	pkg, err := Conformance(dir)
	if err != nil {
		t.Fatal(err)
	}
	return Active{Package: pkg, Grants: []string{"runtime.incus"}}
}

func TestRuntimeWASISequencesClosedEffects(t *testing.T) {
	active := buildRuntimePackage(t, "../../plugins/bundled/runtime-incus")
	f := &runtimeFixture{}
	ctx := context.Background()
	for _, kind := range []string{"runtime.inspect", "runtime.create", "runtime.create", "runtime.assemble", "runtime.start", "runtime.observe-host", "runtime.stop", "runtime.delete"} {
		if _, err := RunRuntime(ctx, active, kind, f); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	counts := map[string]int{}
	for _, call := range f.calls {
		counts[call]++
	}
	if counts["create"] != 2 || counts["assemble"] != 1 || counts["observe-host"] < 1 || counts["start"] != 1 || counts["stop"] != 1 || counts["delete"] != 1 || counts["inspect"] < 10 {
		t.Fatalf("unexpected effects: %v", f.calls)
	}
}

func TestRuntimeWASIDigestAndGrantDenials(t *testing.T) {
	active := buildRuntimePackage(t, "../../plugins/bundled/runtime-incus")
	f := &runtimeFixture{}
	bad := active
	bad.Grants = nil
	if _, err := RunRuntime(context.Background(), bad, "runtime.delete", f); err == nil {
		t.Fatal("missing grant accepted")
	}
	if len(f.calls) != 0 {
		t.Fatalf("missing grant reached broker: %v", f.calls)
	}
	if err := os.WriteFile(filepath.Join(active.Package.Path, "runtime.wasm"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := RunRuntime(context.Background(), active, "runtime.inspect", f); err == nil || !strings.Contains(err.Error(), "package changed") {
		t.Fatalf("changed package accepted: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("changed package reached broker: %v", f.calls)
	}
}

func TestRuntimeWASIRejectsCrossOperationMutation(t *testing.T) {
	hostile := buildRuntimePackage(t, "testdata/runtime-hostile")
	f := &runtimeFixture{state: RuntimeState{Exists: true, Status: "Stopped"}}
	if _, err := RunRuntime(context.Background(), hostile, "runtime.inspect", f); err == nil {
		t.Fatal("cross-operation delete accepted")
	}
	if len(f.calls) != 0 {
		t.Fatalf("hostile module reached native broker: %v", f.calls)
	}
}

func (f *runtimeFixture) Attach(context.Context) (RuntimeState, error) {
	f.calls = append(f.calls, "attach")
	f.state.AttachSpec = &AttachSpec{Project: "fixed", Instance: "fixed", Argv: []string{"/usr/libexec/p/attach"}}
	return f.state, nil
}
func TestRuntimeAttachUsesSelectedWASIAndNativeSpec(t *testing.T) {
	active := buildRuntimePackage(t, "../../plugins/bundled/runtime-incus")
	f := &runtimeFixture{state: RuntimeState{Exists: true, Status: "Running"}}
	result, err := RunRuntime(context.Background(), active, "runtime.attach", f)
	if err != nil || result.AttachSpec == nil || result.AttachSpec.Argv[0] != "/usr/libexec/p/attach" {
		t.Fatal(result, err)
	}
	count := 0
	for _, call := range f.calls {
		if call == "attach" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("WASI broker and fresh postcondition required: %v", f.calls)
	}
	active.Grants = nil
	f.calls = nil
	if _, err = RunRuntime(context.Background(), active, "runtime.attach", f); err == nil || len(f.calls) != 0 {
		t.Fatal("attachment bypassed capability grant")
	}
}
