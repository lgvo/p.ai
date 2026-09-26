package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

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
	manifest := `{"schema":"p.plugin/v1","id":"example.log","version":"1.0.0","api":"1.0","capability":"event-handler","placement":"host","description":"test","runtime":{"kind":"declarative","entry":"event.file.append"},"requests":["event.file.append"]}`
	if e := os.WriteFile(filepath.Join(source, "plugin.json"), []byte(manifest), 0600); e != nil {
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
	staged.Selection.Config = json.RawMessage(`{"path":"` + filepath.Join(state, "events.ndjson") + `","max_bytes":4096}`)
	activation := filepath.Join(state, "active.json")
	raw, _ := json.Marshal(plugin.Activation{Schema: plugin.ActivationSchema, Plugins: []plugin.SelectedPackage{staged.Selection}})
	if e = os.WriteFile(activation, raw, 0600); e != nil {
		t.Fatal(e)
	}
	cfg := control.HostConfig{StateDir: state, Events: &control.EventsConfig{ActivationPath: activation, PluginID: p.Manifest.ID}}
	if _, e = leaseConfiguredPackages(cfg, "two"); e == nil {
		t.Fatal("foreign daemon accepted another instance registry")
	}
	release, e := leaseConfiguredPackages(cfg, "one")
	if e != nil {
		t.Fatal(e)
	}
	// Removing the selection file does not revoke the already-cached runner.
	raw, _ = json.Marshal(plugin.Activation{Schema: plugin.ActivationSchema, Plugins: []plugin.SelectedPackage{}})
	if e = os.WriteFile(activation, raw, 0600); e != nil {
		t.Fatal(e)
	}
	if e = plugin.RemoveStaged(state, "one", p.SHA256, func() error { return nil }); e == nil {
		t.Fatal("removed a cached daemon runner")
	}
	release()
	if e = plugin.RemoveStaged(state, "one", p.SHA256, func() error { return nil }); e != nil {
		t.Fatal(e)
	}
	if _, e = leaseConfiguredPackages(control.HostConfig{StateDir: state, Events: &control.EventsConfig{ActivationPath: activation}}, "one"); e != nil {
		t.Fatal("empty deselected authority failed", e)
	}
}

func TestManagedAssetPlanBindsExactInstanceLeasedPath(t *testing.T) {
	source, e := filepath.Abs("../../plugins/bundled/tmux-host")
	if e != nil {
		t.Fatal(e)
	}
	pkg, e := plugin.Conformance(source)
	if e != nil {
		t.Fatal(e)
	}
	staged := []plugin.InstallResult{}
	for _, instance := range []string{"one", "foreign"} {
		state := t.TempDir()
		if e = os.Chmod(state, 0700); e != nil {
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
		r, e := plugin.StagePackage(context.Background(), state, instance, source, pkg.SHA256, "")
		if e != nil {
			t.Fatal(e)
		}
		staged = append(staged, r)
	}
	active := plugin.Active{Package: staged[0].Package, Grants: staged[0].Selection.Grants}
	release, e := plugin.LeaseInstancePackage(active.Package.Path, filepath.Dir(filepath.Dir(filepath.Dir(active.Package.Path))), "one")
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	leased := map[string]plugin.Active{active.Package.Path: active}
	original, e := plugin.PlanAssets(active)
	if e != nil {
		t.Fatal(e)
	}
	if !sameLeasedAsset(leased, original) {
		t.Fatal("actual approved asset path refused")
	}
	foreign, e := plugin.PlanAssets(plugin.Active{Package: staged[1].Package, Grants: staged[1].Selection.Grants})
	if e != nil {
		t.Fatal(e)
	}
	if original.PackageID != foreign.PackageID || original.PackageSHA256 != foreign.PackageSHA256 || original.PackagePath == foreign.PackagePath {
		t.Fatal("fixture does not model same digest different bound registry")
	}
	if sameLeasedAsset(leased, foreign) {
		t.Fatal("foreign registry asset activation swap bypassed exact lifetime lease")
	}
}
