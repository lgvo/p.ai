package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/eventhandler"
	"github.com/lgvo/p.ai/internal/plugin"
)

// handlerEntry is the name the tests start this test binary under: an event
// handler that handles every event.
const handlerEntry = "event-fixture"

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == handlerEntry {
		conn, err := eventhandler.Inherited()
		if err != nil {
			os.Exit(2)
		}
		eventhandler.Serve(context.Background(), conn, eventhandler.Identity{ID: "example.log", Version: "1.0.0"},
			func(json.RawMessage) (eventhandler.Handler, error) {
				return func(context.Context, eventhandler.Event) (eventhandler.Status, error) {
					return eventhandler.Handled, nil
				}, nil
			})
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// privateDir is a private directory whose read-only staged packages the
// test can still remove.
func privateDir(t *testing.T) string {
	t.Helper()
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
	return private
}

// handlerPackage writes an event-handler package whose entry is this test
// binary, as handlerEntry, into a new directory of private.
func handlerPackage(t *testing.T, private string) plugin.Package {
	t.Helper()
	source := filepath.Join(private, "source")
	if e := os.Mkdir(source, 0700); e != nil {
		t.Fatal(e)
	}
	manifest := `{"schema":"p.plugin/v2","id":"example.log","version":"1.0.0","description":"test","capability":"event-handler","entry":"` + handlerEntry + `"}`
	if e := os.WriteFile(filepath.Join(source, "plugin.json"), []byte(manifest), 0600); e != nil {
		t.Fatal(e)
	}
	self, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(self)
	if e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(source, handlerEntry), data, 0700); e != nil {
		t.Fatal(e)
	}
	pkg, e := plugin.Conformance(source)
	if e != nil {
		t.Fatal(e)
	}
	return pkg
}

func writeHost(t *testing.T, path string, cfg control.HostConfig) {
	t.Helper()
	raw, _ := json.Marshal(cfg)
	if e := os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
}

func TestPluginManagementCLIExplicitApprovalOfflineSelectionAndRemoval(t *testing.T) {
	private := privateDir(t)
	pkg := handlerPackage(t, private)
	state := filepath.Join(private, "state")
	host := filepath.Join(private, "host.json")
	cfg := control.HostConfig{Schema: control.HostSchema, StateDir: state}
	writeHost(t, host, cfg)
	if e := run([]string{"plugins", "install", host, pkg.Path, strings.Repeat("f", 64)}); e == nil {
		t.Fatal("CLI omitted approved digest comparison")
	}
	if e := run([]string{"plugins", "install", host, pkg.Path, pkg.SHA256}); e != nil {
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
	cfg.Events = &control.EventsConfig{Path: staged, SHA256: pkg.SHA256, Config: json.RawMessage(`{}`)}
	writeHost(t, host, cfg)
	if e = run([]string{"plugins", "remove", host, pkg.SHA256}); e == nil || !strings.Contains(e.Error(), "selected in host configuration") {
		t.Fatalf("CLI removed a package host configuration selects: %v", e)
	}
	if _, e = plugin.Conformance(staged); e != nil {
		t.Fatal("selected refusal deleted bytes", e)
	}
	// The trusted owner deselects the package before removal. A package cannot
	// change host configuration by supplying a new manifest or source path.
	cfg.Events = nil
	writeHost(t, host, cfg)
	if e = run([]string{"plugins", "remove", host, pkg.SHA256}); e != nil {
		t.Fatal(e)
	}
	if _, e = plugin.Conformance(staged); e == nil {
		t.Fatal("a removed package still conforms")
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

// p plugins run-event starts the handler that host configuration selects,
// after its launch checks, and prints what it did with the event.
func TestRunEventStartsTheSelectedHandler(t *testing.T) {
	private := privateDir(t)
	pkg := handlerPackage(t, private)
	host := filepath.Join(private, "host.json")
	event := filepath.Join(private, "event.json")
	if e := os.WriteFile(event, []byte(`{"schema":"p.event/v1","id":"e-1","kind":"session.condition_changed","occurred_at":"2026-11-03T00:00:00Z","instance":"p","fields":{"condition":"ready"}}`), 0600); e != nil {
		t.Fatal(e)
	}
	cfg := control.HostConfig{Schema: control.HostSchema, StateDir: filepath.Join(private, "state")}
	writeHost(t, host, cfg)
	if e := run([]string{"plugins", "run-event", host, event}); e == nil || !strings.Contains(e.Error(), "selects no event handler") {
		t.Fatalf("run-event without a selection: %v", e)
	}
	cfg.Events = &control.EventsConfig{Path: pkg.Path, SHA256: pkg.SHA256, Config: json.RawMessage(`{}`)}
	writeHost(t, host, cfg)
	out := captureStdout(t, func() {
		if e := run([]string{"plugins", "run-event", host, event}); e != nil {
			t.Fatalf("run-event: %v", e)
		}
	})
	if strings.TrimSpace(out) != `{"event_id":"e-1","status":"handled"}` {
		t.Fatalf("run-event printed %q", out)
	}
	if e := os.Chmod(filepath.Join(pkg.Path, handlerEntry), 0770); e != nil {
		t.Fatal(e)
	}
	if e := run([]string{"plugins", "run-event", host, event}); e == nil || !strings.Contains(e.Error(), "failed its launch checks") {
		t.Fatalf("run-event with a loosened entry: %v", e)
	}
}

func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	read := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		read <- string(data)
	}()
	defer func() {
		os.Stdout = saved
	}()
	f()
	os.Stdout = saved
	w.Close()
	return <-read
}
