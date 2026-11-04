package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// defaultsCatalog copies the bundled packages' sources into a catalog. VM48
// exercises the installed packages.
func defaultsCatalog(t *testing.T) string {
	t.Helper()
	catalog := t.TempDir()
	for _, role := range bundledRoles {
		dir := filepath.Join(catalog, role.directory)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		source := filepath.Join("..", "..", "plugins", "bundled", role.directory)
		data, err := os.ReadFile(filepath.Join(source, "plugin.json"))
		if err != nil {
			t.Fatal(err)
		}
		var manifest Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "plugin.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		for _, asset := range manifest.Assets {
			data, err := os.ReadFile(filepath.Join(source, asset))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, asset), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return catalog
}

func TestDefaultActivationReadOnlyAndPinned(t *testing.T) {
	catalog := defaultsCatalog(t)
	log := filepath.Join(t.TempDir(), "events.ndjson")
	selection, err := DefaultActivation(catalog, log)
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Plugins) != 2 || selection.Schema != ActivationSchema {
		t.Fatalf("incomplete composition: %+v", selection)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("read-only defaults created event log: %v", err)
	}
	data, err := json.Marshal(selection)
	if err != nil {
		t.Fatal(err)
	}
	activation := filepath.Join(t.TempDir(), "activation.json")
	if err := os.WriteFile(activation, data, 0600); err != nil {
		t.Fatal(err)
	}
	active, err := LoadActivation(activation)
	if err != nil || len(active) != 2 {
		t.Fatalf("defaults cannot activate: %v, %+v", err, active)
	}
	for _, selected := range active {
		if len(selected.Grants) != 1 || selected.Grants[0] != selected.Package.Manifest.Requests[0] {
			t.Fatalf("unexpected authority: %+v", selected)
		}
		if selected.Package.Manifest.Capability == "event-handler" {
			var config FileLogConfig
			if err := json.Unmarshal(selected.Config, &config); err != nil || config.Path != log {
				t.Fatalf("log selection not bound to explicit path: %v %+v", err, config)
			}
		}
	}
	// Selection is immutable: later package edits require a new reviewed digest.
	asset := filepath.Join(catalog, "codex-adapter", "p-codex-adapter")
	if err := os.WriteFile(asset, []byte("#!/bin/sh\nexit 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadActivation(activation); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("changed package accepted with old selection: %v", err)
	}
}

func TestDefaultActivationRejectsUnsafeOrSubstitutedPackages(t *testing.T) {
	for _, name := range []string{"relative log", "unclean log", "wrong identity", "missing asset", "symlink asset"} {
		t.Run(name, func(t *testing.T) {
			catalog := defaultsCatalog(t)
			log := filepath.Join(t.TempDir(), "events.ndjson")
			asset := filepath.Join(catalog, "codex-adapter", "p-codex-adapter")
			switch name {
			case "relative log":
				log = "events.ndjson"
			case "unclean log":
				log = "/tmp/example/../events.ndjson"
			case "wrong identity":
				path := filepath.Join(catalog, "codex-adapter", "plugin.json")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				data = []byte(strings.Replace(string(data), "org.p.codex-adapter", "org.p.alternate.codex-adapter", 1))
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "missing asset", "symlink asset":
				if err := os.Remove(asset); err != nil {
					t.Fatal(err)
				}
				if name == "symlink asset" {
					if err := os.Symlink("/etc/passwd", asset); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := DefaultActivation(catalog, log); err == nil {
				t.Fatal("unsafe or substituted defaults accepted")
			}
		})
	}
}
