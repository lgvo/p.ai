package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

// The daemon leases the managed packages host configuration selects for its
// lifetime: none is removed while it runs, and another instance cannot
// select them.
func TestManagedDaemonLifetimeLeaseAndForeignInstanceRefusal(t *testing.T) {
	state := t.TempDir()
	if e := os.Chmod(state, 0700); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(state, func(path string, d os.DirEntry, e error) error {
			if e == nil && d.IsDir() {
				_ = os.Chmod(path, 0700)
			}
			return nil
		})
	})
	source := filepath.Join(state, "source")
	if e := os.Mkdir(source, 0700); e != nil {
		t.Fatal(e)
	}
	manifest := `{"schema":"p.plugin/v2","id":"example.log","version":"1.0.0","description":"test","capability":"event-handler","entry":"handler"}`
	if e := os.WriteFile(filepath.Join(source, "plugin.json"), []byte(manifest), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(source, "handler"), []byte("#!/bin/sh\nexit 0\n"), 0700); e != nil {
		t.Fatal(e)
	}
	p, e := plugin.Conformance(source)
	if e != nil {
		t.Fatal(e)
	}
	staged, e := plugin.StagePackage(context.Background(), state, "one", source, p.SHA256, "")
	if e != nil {
		t.Fatal(e)
	}
	cfg := control.HostConfig{StateDir: state, Events: &control.EventsConfig{Path: staged.Selection.Path, SHA256: staged.Selection.SHA256}}
	if _, e = leaseConfiguredPackages(cfg, "two"); e == nil {
		t.Fatal("foreign daemon accepted another instance registry")
	}
	release, e := leaseConfiguredPackages(cfg, "one")
	if e != nil {
		t.Fatal(e)
	}
	if e = plugin.RemoveStaged(state, "one", p.SHA256, func() error { return nil }); e == nil {
		t.Fatal("removed a package the running daemon selects")
	}
	release()
	if e = plugin.RemoveStaged(state, "one", p.SHA256, func() error { return nil }); e != nil {
		t.Fatal(e)
	}
	if _, e = leaseConfiguredPackages(cfg, "one"); e == nil {
		t.Fatal("a daemon leased a removed package")
	}
}
