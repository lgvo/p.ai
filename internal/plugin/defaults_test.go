package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/eventfile"
)

// defaultsCatalog copies the bundled packages' sources into a catalog, with
// a stand-in for the file-log's entry, which is built. VM48 exercises the
// installed packages.
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
		if manifest.Entry != "" {
			if err := os.WriteFile(filepath.Join(dir, manifest.Entry), []byte(handlerScript), 0700); err != nil {
				t.Fatal(err)
			}
		}
	}
	return catalog
}

// The defaults select each bundled package by its directory and digest, as
// host configuration does, with the file-log writing the event log named.
func TestDefaultSelectionsPinTheBundledPackages(t *testing.T) {
	catalog := defaultsCatalog(t)
	log := filepath.Join(t.TempDir(), "events.ndjson")
	defaults, err := DefaultSelections(catalog, log)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("read-only defaults created event log: %v", err)
	}
	for _, c := range []struct {
		got       Selection
		directory string
	}{{defaults.Events, "file-log"}, {defaults.Runtime.AgentAdapter, "codex-adapter"}} {
		pkg, err := Conformance(filepath.Join(catalog, c.directory))
		if err != nil {
			t.Fatal(err)
		}
		if c.got.Path != pkg.Path || c.got.SHA256 != pkg.SHA256 {
			t.Fatalf("the %s selection is %+v, want path %s and digest %s", c.directory, c.got, pkg.Path, pkg.SHA256)
		}
	}
	config, err := eventfile.ParseConfig(defaults.Events.Config)
	if err != nil || config != (eventfile.Config{Path: log, MaxBytes: 4 << 20}) {
		t.Fatalf("the file-log config is %s (%v), want the event log %s with 4 MiB segments", defaults.Events.Config, err, log)
	}
	if len(defaults.Runtime.AgentAdapter.Config) != 0 {
		t.Fatalf("the agent adapter selection carries config %s", defaults.Runtime.AgentAdapter.Config)
	}
	raw, err := json.Marshal(defaults)
	if err != nil {
		t.Fatal(err)
	}
	var blocks map[string]map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil || len(blocks) != 2 || blocks["events"] == nil || blocks["runtime"]["agent_adapter"] == nil {
		t.Fatalf("the defaults are not the events and runtime.agent_adapter blocks: %s", raw)
	}
}

func TestDefaultSelectionsRejectUnsafeOrSubstitutedPackages(t *testing.T) {
	for _, name := range []string{"relative log", "unclean log", "wrong identity", "missing asset", "symlink asset", "missing entry"} {
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
			case "missing entry":
				if err := os.Remove(filepath.Join(catalog, "file-log", "p-event-file")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := DefaultSelections(catalog, log); err == nil {
				t.Fatal("unsafe or substituted defaults accepted")
			}
		})
	}
}
