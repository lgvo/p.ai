package plugin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func wasiPackage(t *testing.T) Package {
	t.Helper()
	dir := t.TempDir()
	manifest, err := os.ReadFile("../../plugins/examples/filter-log/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "filter.wasm"), "./plugins/examples/filter-log")
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build example: %v: %s", err, out)
	}
	pkg, err := Conformance(dir)
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func TestWASIEventFilterAndDigestPin(t *testing.T) {
	pkg := wasiPackage(t)
	private := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(private, "events.ndjson")
	path := filepath.Join(t.TempDir(), "activation.json")
	activationFile(t, pkg, path, []string{"event.file.append"}, FileLogConfig{Path: log, MaxBytes: 4096})
	active, err := LoadActivation(path)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{Schema: "p.event/v1", ID: "e-1", Kind: "session.condition_changed", OccurredAt: time.Now().UTC().Format(time.RFC3339), Instance: "p-one", Fields: map[string]string{"condition": "stopped"}}
	result, err := RunEvent(context.Background(), active[0], event)
	if err != nil || result.Status != "skipped" {
		t.Fatalf("filtered event: %+v %v", result, err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("filtered event wrote log: %v", err)
	}
	event.Fields["condition"] = "ready"
	result, err = RunEvent(context.Background(), active[0], event)
	if err != nil || result.Status != "appended" {
		t.Fatalf("ready event: %+v %v", result, err)
	}
	data, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(data), `"condition":"ready"`) {
		t.Fatalf("log: %q %v", data, err)
	}
	bytes, err := os.ReadFile(filepath.Join(pkg.Path, "filter.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	bytes[len(bytes)-1] ^= 1
	if err := os.WriteFile(filepath.Join(pkg.Path, "filter.wasm"), bytes, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := RunEvent(context.Background(), active[0], event); err == nil || !strings.Contains(err.Error(), "digest changed") {
		t.Fatalf("mutated module ran: %v", err)
	}
}

func TestAssetPlanFixedRolesAndPins(t *testing.T) {
	dir := t.TempDir()
	manifest := Manifest{Schema: ManifestSchema, ID: "org.p.example.host", Version: "1.0.0", API: APIVersion, Capability: "interactive-host", Placement: "internal-session", Description: "Example host", Runtime: Runtime{Kind: "assets"}, Requests: []string{"session.asset.install"}, Assets: []string{"p-session.target", "p-interactive.service", "p-attach"}}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range manifest.Assets {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("asset:"+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	pkg, err := Conformance(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "activation.json")
	activationFile(t, pkg, path, manifest.Requests, struct{}{})
	active, err := LoadActivation(path)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanAssets(active[0])
	if err != nil || len(plan.Files) != 3 {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	for _, file := range plan.Files {
		if file.Destination != assetRoles[file.Role].destination || file.Mode != assetRoles[file.Role].mode || string(file.Data) != "asset:"+file.Role {
			t.Fatalf("wrong role plan: %+v", file)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "p-attach"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanAssets(active[0]); err == nil || !strings.Contains(err.Error(), "digest changed") {
		t.Fatalf("mutated asset accepted: %v", err)
	}
	manifest.Assets[2] = "other"
	data, _ = json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Conformance(dir); err == nil {
		t.Fatal("invalid destination role accepted")
	}
}

func TestAssetScopeAndGrantRefusal(t *testing.T) {
	dir := t.TempDir()
	m := Manifest{Schema: ManifestSchema, ID: "org.p.example.adapter", Version: "1.0.0", API: APIVersion, Capability: "agent-adapter", Placement: "internal-session", Description: "Adapter", Runtime: Runtime{Kind: "assets"}, Requests: []string{"agent.status.report"}, Assets: []string{"p-codex-adapter"}}
	writeManifest := func() {
		data, _ := json.Marshal(m)
		if err := os.WriteFile(filepath.Join(dir, "plugin.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeManifest()
	if err := os.WriteFile(filepath.Join(dir, "p-codex-adapter"), []byte("adapter"), 0600); err != nil {
		t.Fatal(err)
	}
	pkg, err := Conformance(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "activation.json")
	activationFile(t, pkg, path, []string{"agent.status.report"}, struct{}{})
	active, err := LoadActivation(path)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanAssets(active[0])
	if err != nil || len(plan.Files) != 1 || plan.Files[0].Destination != "/usr/libexec/p/codex-adapter" {
		t.Fatalf("adapter plan: %+v %v", plan, err)
	}
	activationFile(t, pkg, path, []string{"session.asset.install"}, struct{}{})
	if _, err := LoadActivation(path); err == nil {
		t.Fatal("wrong grant activated")
	}
	m.Placement = "host"
	writeManifest()
	if _, err := Conformance(dir); err == nil {
		t.Fatal("host placement accepted")
	}
	m.Placement = "internal-session"
	m.Assets = []string{"../other"}
	writeManifest()
	if _, err := Conformance(dir); err == nil {
		t.Fatal("traversal asset accepted")
	}
	m.Assets = []string{"p-codex-adapter"}
	writeManifest()
	if err := os.WriteFile(filepath.Join(dir, "p-codex-adapter"), make([]byte, (1<<20)+1), 0600); err != nil {
		t.Fatal(err)
	}
	pkg, err = Conformance(dir)
	if err != nil {
		t.Fatal(err)
	}
	activationFile(t, pkg, path, []string{"agent.status.report"}, struct{}{})
	active, err = LoadActivation(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PlanAssets(active[0]); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("oversized asset planned: %v", err)
	}
}

func TestRuntimeHostCapabilityCanActivate(t *testing.T) {
	dir := t.TempDir()
	m := Manifest{Schema: ManifestSchema, ID: "org.p.example.runtime", Version: "1.0.0", API: APIVersion, Capability: "runtime", Placement: "host", Description: "Runtime example", Runtime: Runtime{Kind: "wasi-command", Entry: "runtime.wasm"}, Requests: []string{"runtime.incus"}}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime.wasm"), []byte{0, 97, 115, 109, 1, 0, 0, 0}, 0600); err != nil {
		t.Fatal(err)
	}
	pkg, err := Conformance(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "activation.json")
	activationFile(t, pkg, path, []string{"runtime.incus"}, struct{}{})
	if _, err := LoadActivation(path); err != nil {
		t.Fatalf("runtime capability activation: %v", err)
	}
}

func TestBrokerImportWithoutLinearMemoryIsRefused(t *testing.T) {
	// Minimal WebAssembly module assembled from the core binary format: one
	// imported (i32,i32,i32,i32)->i32 broker function and an exported _start
	// that calls it with zeroes. It deliberately has no memory section/export.
	// Keeping the bytes here makes the malformed boundary case inspectable.
	wasm := []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, // header
		0x01, 0x0c, 0x02, 0x60, 0x04, 0x7f, 0x7f, 0x7f, 0x7f, 0x01, 0x7f, 0x60, 0x00, 0x00, // types
		0x02, 0x14, 0x01, 0x0b, 'p', '_', 'b', 'r', 'o', 'k', 'e', 'r', '_', 'v', '1', 0x04, 'c', 'a', 'l', 'l', 0x00, 0x00, // import
		0x03, 0x02, 0x01, 0x01, // one local function, type 1
		0x07, 0x0a, 0x01, 0x06, '_', 's', 't', 'a', 'r', 't', 0x00, 0x01, // _start export
		0x0a, 0x0f, 0x01, 0x0d, 0x00, 0x41, 0x00, 0x41, 0x01, 0x41, 0x00, 0x41, 0x01, 0x10, 0x00, 0x1a, 0x0b, // call and drop
	}
	dir := t.TempDir()
	m := Manifest{Schema: ManifestSchema, ID: "org.p.example.nomemory", Version: "1.0.0", API: APIVersion, Capability: "event-handler", Placement: "host", Description: "No memory fixture", Runtime: Runtime{Kind: "wasi-command", Entry: "filter.wasm"}, Requests: []string{"event.file.append"}}
	manifest, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "filter.wasm"), wasm, 0600); err != nil {
		t.Fatal(err)
	}
	pkg, err := Conformance(dir)
	if err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "activation.json")
	activationFile(t, pkg, path, []string{"event.file.append"}, FileLogConfig{Path: filepath.Join(private, "events.ndjson"), MaxBytes: 4096})
	active, err := LoadActivation(path)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{Schema: "p.event/v1", ID: "e-1", Kind: "session.condition_changed", OccurredAt: time.Now().UTC().Format(time.RFC3339), Instance: "p-one", Fields: map[string]string{"condition": "ready"}}
	if _, err := RunEvent(context.Background(), active[0], event); err == nil || !strings.Contains(err.Error(), "outside module memory") {
		t.Fatalf("memoryless broker call: %v", err)
	}
	if _, err := os.Stat(filepath.Join(private, "events.ndjson")); !os.IsNotExist(err) {
		t.Fatalf("memoryless module caused effect: %v", err)
	}
}

func TestWASIIsolationAndLimits(t *testing.T) {
	dir := t.TempDir()
	manifest, err := os.ReadFile("../../plugins/examples/filter-log/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "filter.wasm"), "./internal/plugin/testdata/wasi-hostile")
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build hostile fixture: %v: %s", err, out)
	}
	pkg, err := Conformance(dir)
	if err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "activation.json")
	activationFile(t, pkg, path, []string{"event.file.append"}, FileLogConfig{Path: filepath.Join(private, "events.ndjson"), MaxBytes: 4096})
	active, err := LoadActivation(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("P_PLUGIN_PROBE_SECRET", "must-not-be-inherited")
	tests := []struct {
		name, kind, key, value, want string
		timeout                      time.Duration
	}{
		{"host filesystem environment network", "session.condition_changed", "condition", "ready", "", time.Second},
		{"undeclared broker call", "session.attachment_changed", "count", "1", "broker method refused", time.Second},
		{"broker call count", "session.attachment_changed", "count", "2", "broker request exceeds limit", time.Second},
		{"extra broker field", "session.condition_changed", "condition", "starting", "broker method refused", time.Second},
		{"false appended status", "session.condition_changed", "condition", "stopped", "invalid plugin result", time.Second},
		{"out of bounds broker response", "session.condition_changed", "condition", "missing", "response outside module memory", time.Second},
		{"out of bounds broker memory", "session.unattended_changed", "unattended_condition", "running", "outside module memory", time.Second},
		{"oversized output", "session.policy_changed", "policy_condition", "current", "output exceeds limit", time.Second},
		{"oversized stderr", "session.policy_changed", "policy_condition", "outdated", "output exceeds limit", time.Second},
		{"infinite loop cancelled", "operation.progress", "phase", "running", "timed out or was cancelled", 100 * time.Millisecond},
		// Go emits a panic trace on guest OOM; the host bounds that stderr too.
		{"memory limit", "session.condition_changed", "condition", "creating", "output exceeds limit", time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tt.timeout)
			defer cancel()
			event := Event{Schema: "p.event/v1", ID: "e-test", Kind: tt.kind, OccurredAt: time.Now().UTC().Format(time.RFC3339), Instance: "p-one", Fields: map[string]string{tt.key: tt.value}}
			result, err := RunEvent(ctx, active[0], event)
			if tt.want == "" {
				if err != nil || result.Status != "skipped" {
					t.Fatalf("host access: %+v %v", result, err)
				}
			} else if err == nil || (!strings.Contains(err.Error(), tt.want) && !(tt.name == "memory limit" && strings.Contains(err.Error(), "plugin execution failed"))) {
				t.Fatalf("got %+v %v, want %q", result, err, tt.want)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(private, "events.ndjson")); !os.IsNotExist(err) {
		t.Fatalf("hostile module caused effect: %v", err)
	}
}
