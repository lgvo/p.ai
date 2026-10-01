package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const goodManifest = `{
  "schema":"p.plugin/v1", "id":"example.filelog", "version":"1.2.3",
  "api":"1.0", "capability":"event-handler", "placement":"host",
  "description":"Test log handler", "runtime":{"kind":"declarative","entry":"event.file.append"},
  "requests":["event.file.append"]
}`

func testPackage(t *testing.T) Package {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "filelog")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(goodManifest), 0600); err != nil {
		t.Fatal(err)
	}
	pkg, err := Conformance(dir)
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func activationFile(t *testing.T, pkg Package, path string, grants []string, config any) string {
	t.Helper()
	conf, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(Activation{Schema: ActivationSchema, Plugins: []SelectedPackage{{
		ID: pkg.Manifest.ID, Path: pkg.Path, SHA256: pkg.SHA256, Grants: grants, Config: conf,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConformanceRejectsUnsafePackage(t *testing.T) {
	tests := []struct {
		name, manifest string
		extra          func(string) error
		want           string
	}{
		{"unknown grant", strings.Replace(goodManifest, "event.file.append\"]", "runtime.incus\"]", 1), nil, "grant"},
		{"unknown field", strings.Replace(goodManifest, `"schema":"p.plugin/v1"`, `"schema":"p.plugin/v1","surprise":true`, 1), nil, "unknown field"},
		{"wrong api", strings.Replace(goodManifest, `"api":"1.0"`, `"api":"2.0"`, 1), nil, "unsupported plugin API"},
		{"symlink", goodManifest, func(dir string) error { return os.Symlink("/etc/passwd", filepath.Join(dir, "other")) }, "symlink"},
		{"traversal", strings.Replace(goodManifest, `"runtime":{"kind":"declarative","entry":"event.file.append"}`, `"runtime":{"kind":"wasi-command","entry":"../escape.wasm"}`, 1), nil, "unsafe relative path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(tt.manifest), 0600); err != nil {
				t.Fatal(err)
			}
			if tt.extra != nil {
				if err := tt.extra(dir); err != nil {
					t.Fatal(err)
				}
			}
			_, err := Conformance(dir)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
		})
	}
}

func TestConformanceBoundsPackageEntries(t *testing.T) {
	pkg := testPackage(t)
	for i := 0; i < 128; i++ {
		name := filepath.Join(pkg.Path, fmt.Sprintf("asset-%03d", i))
		if err := os.WriteFile(name, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Conformance(pkg.Path); err == nil || !strings.Contains(err.Error(), "more than 128") {
		t.Fatalf("oversize package accepted: %v", err)
	}
}

func TestActivationPinsDigestAndGrants(t *testing.T) {
	pkg := testPackage(t)
	path := filepath.Join(t.TempDir(), "activation.json")
	log := filepath.Join(t.TempDir(), "events.ndjson")
	config := FileLogConfig{Path: log, MaxBytes: 1024}
	activationFile(t, pkg, path, []string{"event.file.append"}, config)
	active, err := LoadActivation(path)
	if err != nil || len(active) != 1 {
		t.Fatalf("activation: %v, %+v", err, active)
	}
	activationFile(t, pkg, path, nil, config)
	if _, err := LoadActivation(path); err == nil || !strings.Contains(err.Error(), "grants") {
		t.Fatalf("missing grant accepted: %v", err)
	}
	activationFile(t, pkg, path, []string{"event.file.append", "runtime.incus"}, config)
	if _, err := LoadActivation(path); err == nil || !strings.Contains(err.Error(), "grants") {
		t.Fatalf("extra grant accepted: %v", err)
	}
	activationFile(t, pkg, path, []string{"event.file.append"}, config)
	if err := os.WriteFile(filepath.Join(pkg.Path, "new-file"), []byte("change"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadActivation(path); err == nil || !(strings.Contains(err.Error(), "digest mismatch") || strings.Contains(err.Error(), "undeclared package file")) {
		t.Fatalf("changed package accepted: %v", err)
	}
}

func TestActivationRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activation.fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadActivation(path); err == nil {
		t.Fatal("FIFO activation accepted")
	}
}

func TestEmitRejectsPackageMutationAfterActivation(t *testing.T) {
	pkg := testPackage(t)
	private := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(private, "events.ndjson")
	path := filepath.Join(t.TempDir(), "activation.json")
	activationFile(t, pkg, path, []string{"event.file.append"}, FileLogConfig{Path: log, MaxBytes: 1024})
	active, err := LoadActivation(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg.Path, "extra"), []byte("mutation"), 0600); err != nil {
		t.Fatal(err)
	}
	event := Event{Schema: "p.event/v1", ID: "e-1", Kind: "session.condition_changed", OccurredAt: "2026-09-23T00:00:00Z", Instance: "p-one", Fields: map[string]string{"condition": "ready"}}
	if err := Emit(active, event); err == nil || !(strings.Contains(err.Error(), "digest changed") || strings.Contains(err.Error(), "undeclared package file")) {
		t.Fatalf("mutated package emitted: %v", err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("event log created after mutation: %v", err)
	}
}

func TestWASIConformanceActivates(t *testing.T) {
	pkg := testPackage(t)
	manifest := strings.Replace(goodManifest, `"runtime":{"kind":"declarative","entry":"event.file.append"}`, `"runtime":{"kind":"wasi-command","entry":"plugin.wasm"}`, 1)
	if err := os.WriteFile(filepath.Join(pkg.Path, "plugin.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg.Path, "plugin.wasm"), []byte{0, 97, 115, 109, 1, 0, 0, 0}, 0600); err != nil {
		t.Fatal(err)
	}
	pkg, err := Conformance(pkg.Path)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "activation.json")
	activationFile(t, pkg, path, []string{"event.file.append"}, FileLogConfig{Path: "/tmp/example", MaxBytes: 1024})
	if _, err := LoadActivation(path); err != nil {
		t.Fatalf("WASI activation: %v", err)
	}
}

func TestFileLogBrokerAndEventBoundary(t *testing.T) {
	pkg := testPackage(t)
	private := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(private, "events.ndjson")
	path := filepath.Join(t.TempDir(), "activation.json")
	activationFile(t, pkg, path, []string{"event.file.append"}, FileLogConfig{Path: log, MaxBytes: 1024})
	active, err := LoadActivation(path)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{Schema: "p.event/v1", ID: "e-1", Kind: "session.condition_changed", OccurredAt: "2026-09-23T00:00:00Z", Instance: "p-one", Fields: map[string]string{"condition": "ready"}}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := EmitContext(cancelled, active, event); err == nil {
		t.Fatal("cancelled event was delivered")
	}
	if _, err := os.Lstat(log); !os.IsNotExist(err) {
		t.Fatalf("cancelled event changed log: %v", err)
	}
	if err := Emit(active, event); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"condition":"ready"`) || !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("invalid NDJSON: %q", data)
	}
	if info, err := os.Stat(log); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("unsafe file mode: %v, %v", info, err)
	}
	if _, err := ParseEvent([]byte(`{"schema":"p.event/v1","id":"e-2","kind":"session.condition_changed","occurred_at":"now","instance":"p-one","secret":"x"}`)); err == nil {
		t.Fatal("unrecognized event payload accepted")
	}
	bad := event
	bad.Fields = map[string]string{"phase": "running"}
	if err := bad.Validate(); err == nil {
		t.Fatal("wrong event field accepted")
	}
	bad.Fields = map[string]string{"condition": "secret-value"}
	if err := bad.Validate(); err == nil {
		t.Fatal("unknown condition accepted")
	}
	bad.Kind = "session.attachment_changed"
	bad.Fields = map[string]string{"count": "-1"}
	if err := bad.Validate(); err == nil {
		t.Fatal("negative attachment count accepted")
	}
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "target"), log); err != nil {
		t.Fatal(err)
	}
	if err := Emit(active, event); err == nil {
		t.Fatal("symlink log accepted")
	}
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(log, 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := Emit(active, event); err == nil {
		t.Fatal("FIFO log accepted")
	}
	if time.Since(start) > time.Second {
		t.Fatal("FIFO log open blocked")
	}
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(private, "target")
	if err := os.WriteFile(target, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, log); err != nil {
		t.Fatal(err)
	}
	if err := Emit(active, event); err == nil {
		t.Fatal("hard-linked log accepted")
	}
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	linkedParent := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(private, linkedParent); err != nil {
		t.Fatal(err)
	}
	activationFile(t, pkg, path, []string{"event.file.append"}, FileLogConfig{Path: filepath.Join(linkedParent, "events.ndjson"), MaxBytes: 1024})
	active, err = LoadActivation(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Emit(active, event); err == nil {
		t.Fatal("symlinked parent accepted")
	}
}

func TestFileLogRotation(t *testing.T) {
	pkg := testPackage(t)
	private := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(private, "events.ndjson")
	path := filepath.Join(t.TempDir(), "activation.json")
	activationFile(t, pkg, path, []string{"event.file.append"}, FileLogConfig{Path: log, MaxBytes: 1024})
	active, err := LoadActivation(path)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{Schema: "p.event/v1", ID: "e-1", Kind: "session.condition_changed", OccurredAt: "2026-09-23T00:00:00Z", Instance: "p-one", Fields: map[string]string{"condition": "ready"}}
	for i := 0; i < 20; i++ {
		if err := Emit(active, event); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{log, log + ".1"} {
		if info, err := os.Stat(p); err != nil || info.Size() > 1024 {
			t.Fatalf("rotation failed for %s: %v %v", p, info, err)
		}
	}
}
