package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type environmentFixture struct {
	resolve, realize int
	failed           bool
}

func (f *environmentFixture) Resolve(context.Context) error {
	f.resolve++
	if f.failed {
		return errors.New("native resolution failed")
	}
	return nil
}
func (f *environmentFixture) Realize(context.Context) error {
	f.realize++
	if f.failed {
		return errors.New("native realization failed")
	}
	return nil
}

func buildEnvironmentPackage(t *testing.T, source, mode string) Active {
	t.Helper()
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("Go compiler unavailable")
	}
	dir := t.TempDir()
	manifest, err := os.ReadFile(filepath.Join("../../plugins/bundled/environment-nix", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"build", "-o", filepath.Join(dir, "environment.wasm")}
	if mode != "" {
		args = append(args, "-ldflags", "-X main.mode="+mode)
	}
	args = append(args, "./"+source)
	cmd := exec.Command(goPath, args...)
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOTOOLCHAIN=local")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build environment WASM: %v: %s", err, out)
	}
	pkg, err := Conformance(dir)
	if err != nil {
		t.Fatal(err)
	}
	return Active{Package: pkg, Grants: []string{"environment.nix"}, Config: json.RawMessage(`{}`)}
}

func TestEnvironmentWASIClosedStages(t *testing.T) {
	active := buildEnvironmentPackage(t, "../../plugins/bundled/environment-nix", "")
	f := &environmentFixture{}
	if err := RunEnvironment(context.Background(), active, "environment.resolve", f); err != nil {
		t.Fatal(err)
	}
	if err := RunEnvironment(context.Background(), active, "environment.realize", f); err != nil {
		t.Fatal(err)
	}
	if f.resolve != 1 || f.realize != 1 {
		t.Fatalf("wrong native calls: %+v", f)
	}
	f.failed = true
	if err := RunEnvironment(context.Background(), active, "environment.resolve", f); err == nil || !strings.Contains(err.Error(), "native resolution failed") {
		t.Fatalf("swallowed native failure: %v", err)
	}
}

func TestEnvironmentWASIDigestConfigGrantAndKind(t *testing.T) {
	active := buildEnvironmentPackage(t, "../../plugins/bundled/environment-nix", "")
	for _, tc := range []struct {
		name   string
		modify func(*Active)
	}{
		{"grant", func(a *Active) { a.Grants = nil }},
		{"config", func(a *Active) { a.Config = json.RawMessage(`{"command":"nix build"}`) }},
		{"null config", func(a *Active) { a.Config = json.RawMessage(`null`) }},
		{"capability", func(a *Active) { a.Package.Manifest.Capability = "runtime" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := active
			tc.modify(&copy)
			f := &environmentFixture{}
			if err := RunEnvironment(context.Background(), copy, "environment.resolve", f); err == nil || f.resolve != 0 {
				t.Fatalf("invalid activation reached broker: %v %+v", err, f)
			}
		})
	}
	f := &environmentFixture{}
	if err := RunEnvironment(context.Background(), active, "environment.delete", f); err == nil || f.resolve != 0 {
		t.Fatalf("wrong command reached broker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(active.Package.Path, "environment.wasm"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RunEnvironment(context.Background(), active, "environment.resolve", f); err == nil || f.resolve != 0 {
		t.Fatalf("changed WASM reached broker: %v", err)
	}
}

func TestEnvironmentWASIHostileCallsAndResults(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		expected int
	}{
		{"wrong-scope", 0}, {"wrong-kind", 0}, {"extra-field", 0}, {"repeat", 1}, {"refuse-after", 1}, {"extra-result", 1}, {"oversize-result", 1}, {"no-call", 0}, {"bad-pointer", 0},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			active := buildEnvironmentPackage(t, "./testdata/environment-hostile", tc.mode)
			f := &environmentFixture{}
			if err := RunEnvironment(context.Background(), active, "environment.resolve", f); err == nil || f.resolve != tc.expected {
				t.Fatalf("hostile module accepted: mode=%s calls=%d err=%v", tc.mode, f.resolve, err)
			}
		})
	}
}

func TestEnvironmentWASINoAmbientAuthority(t *testing.T) {
	t.Setenv("P_ENV_WASI_SECRET", "host-only")
	active := buildEnvironmentPackage(t, "./testdata/environment-hostile", "ambient")
	f := &environmentFixture{}
	if err := RunEnvironment(context.Background(), active, "environment.resolve", f); err != nil || f.resolve != 1 {
		t.Fatalf("WASI inherited ambient authority or skipped scoped broker: %v %+v", err, f)
	}
}
