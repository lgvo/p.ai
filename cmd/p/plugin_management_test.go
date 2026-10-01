package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

func TestPluginManagementCLIExplicitApprovalOfflineSelectionAndRemoval(t *testing.T) {
	private := t.TempDir()
	if e := os.Chmod(private, 0700); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(private, func(path string, d os.DirEntry, e error) error {
			if e == nil && d.IsDir() {
				_ = os.Chmod(path, 0700)
			}
			return nil
		})
	})
	source := filepath.Join(private, "source")
	if e := os.Mkdir(source, 0700); e != nil {
		t.Fatal(e)
	}
	manifest := `{"schema":"p.plugin/v1","id":"example.log","version":"1.0.0","api":"1.0","capability":"event-handler","placement":"host","description":"test","runtime":{"kind":"declarative","entry":"event.file.append"},"requests":["event.file.append"]}`
	if e := os.WriteFile(filepath.Join(source, "plugin.json"), []byte(manifest), 0600); e != nil {
		t.Fatal(e)
	}
	pkg, e := plugin.Conformance(source)
	if e != nil {
		t.Fatal(e)
	}
	state := filepath.Join(private, "state")
	host := filepath.Join(private, "host.json")
	cfg := control.HostConfig{Schema: control.HostSchema, StateDir: state}
	raw, _ := json.Marshal(cfg)
	if e = os.WriteFile(host, raw, 0600); e != nil {
		t.Fatal(e)
	}
	if e = run([]string{"plugins", "install", host, source, strings.Repeat("f", 64)}); e == nil {
		t.Fatal("CLI omitted approved digest comparison")
	}
	if e = run([]string{"plugins", "install", host, source, pkg.SHA256}); e != nil {
		t.Fatal(e)
	}
	staged := filepath.Join(state, "plugins", "packages", pkg.SHA256)
	if p, e := plugin.Conformance(staged); e != nil || p.SHA256 != pkg.SHA256 {
		t.Fatal(e)
	}
	store, e := control.OpenStore(state)
	if e != nil {
		t.Fatal(e)
	}
	if e = run([]string{"plugins", "remove", host, pkg.SHA256}); e == nil {
		t.Fatal("CLI removed while daemon owned store")
	}
	store.Close()
	selection := plugin.SelectedPackage{ID: pkg.Manifest.ID, Path: staged, SHA256: pkg.SHA256, Grants: pkg.Manifest.Requests, Config: json.RawMessage(`{"path":"` + filepath.Join(private, "events.ndjson") + `","max_bytes":4096}`)}
	active := filepath.Join(private, "active.json")
	raw, _ = json.Marshal(plugin.Activation{Schema: plugin.ActivationSchema, Plugins: []plugin.SelectedPackage{selection}})
	if e = os.WriteFile(active, raw, 0600); e != nil {
		t.Fatal(e)
	}
	cfg.Events = &control.EventsConfig{ActivationPath: active, PluginID: pkg.Manifest.ID}
	raw, _ = json.Marshal(cfg)
	if e = os.WriteFile(host, raw, 0600); e != nil {
		t.Fatal(e)
	}
	if e = run([]string{"plugins", "remove", host, pkg.SHA256}); e == nil {
		t.Fatal("CLI removed explicit trusted selection")
	}
	if _, e = plugin.Conformance(staged); e != nil {
		t.Fatal("selected refusal deleted bytes", e)
	}
	// Trusted owner explicitly removes the role before removal. A package cannot
	// change either authority file by supplying a new manifest or source path.
	cfg.Events = nil
	raw, _ = json.Marshal(cfg)
	if e = os.WriteFile(host, raw, 0600); e != nil {
		t.Fatal(e)
	}
	if e = run([]string{"plugins", "remove", host, pkg.SHA256}); e != nil {
		t.Fatal(e)
	}
	if _, e = plugin.LoadPrivateActivation(active); e == nil {
		t.Fatal("dormant selection invoked disabled package")
	}
	store, e = control.OpenStore(state)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	if _, e = store.InstanceID(context.Background()); e != nil {
		t.Fatal("management broke durable store", e)
	}
}
