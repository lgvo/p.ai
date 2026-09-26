package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func privateManagedState(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	if e := os.Chmod(state, 0700); e != nil {
		t.Fatal(e)
	}
	// The production registry is readonly. Restore only these private fixture
	// directory modes so testing.TempDir can remove its retained packages.
	t.Cleanup(func() {
		_ = filepath.WalkDir(state, func(path string, d os.DirEntry, e error) error {
			if e == nil && d.IsDir() {
				_ = os.Chmod(path, 0700)
			}
			return nil
		})
	})
	return state
}
func managedFixture(t *testing.T) (string, Package, InstallResult) {
	t.Helper()
	state := privateManagedState(t)
	src := testPackage(t)
	result, e := StagePackage(context.Background(), state, "one", src.Path, src.SHA256, "")
	if e != nil {
		t.Fatal(e)
	}
	return state, src, result
}
func TestManagedInstallExactBytesApprovedUpdateAndNoActivation(t *testing.T) {
	state, source, result := managedFixture(t)
	if !result.ActivationRequired || result.Selection.Path != result.Package.Path || result.Selection.SHA256 != source.SHA256 || len(result.Selection.Config) != 0 {
		t.Fatalf("incorrect selection: %+v", result)
	}
	root := managedRoot(state)
	activePath := filepath.Join(state, "activation.json")
	if _, e := os.Lstat(activePath); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("install activated a package")
	}
	if _, e := StagePackage(context.Background(), state, "two", source.Path, source.SHA256, ""); e == nil {
		t.Fatal("different instance adopted registry")
	}
	if _, e := LeaseInstancePackage(result.Package.Path, state, "two"); e == nil {
		t.Fatal("different instance leased registry")
	}
	if e := os.WriteFile(filepath.Join(source.Path, "plugin.json"), []byte(strings.Replace(goodManifest, "1.2.3", "1.2.4", 1)), 0600); e != nil {
		t.Fatal(e)
	}
	newer, e := Conformance(source.Path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = StagePackage(context.Background(), state, "one", source.Path, result.Package.SHA256, result.Package.SHA256); e == nil {
		t.Fatal("stale digest approved changed opened bytes")
	}
	updated, e := StagePackage(context.Background(), state, "one", source.Path, newer.SHA256, result.Package.SHA256)
	if e != nil {
		t.Fatal(e)
	}
	if updated.PreviousSHA256 != result.Package.SHA256 {
		t.Fatal("update lost previous proof")
	}
	if old, e := Conformance(result.Package.Path); e != nil || old.SHA256 != result.Package.SHA256 {
		t.Fatal("update altered old selection", e)
	}
	entries, e := os.ReadDir(filepath.Join(root, "packages"))
	if e != nil || len(entries) != 2 {
		t.Fatal("unexpected published inventory", entries, e)
	}
	for _, pkg := range []Package{result.Package, updated.Package} {
		i, e := os.Lstat(pkg.Path)
		if e != nil || i.Mode().Perm() != 0500 {
			t.Fatal("package directory writable", e)
		}
		i, e = os.Lstat(filepath.Join(pkg.Path, "plugin.json"))
		if e != nil || i.Mode().Perm() != 0400 {
			t.Fatal("package file writable", e)
		}
	}
}
func TestManagedRemovalLeasesDependencyFailureAndDurableDisable(t *testing.T) {
	state, _, result := managedFixture(t)
	path := result.Package.Path
	digest := result.Package.SHA256
	held, e := LeasePackage(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = RemoveStaged(state, "one", digest, func() error { return nil }); e == nil {
		t.Fatal("removed leased package")
	}
	held()
	refusal := errors.New("durable dependency")
	if e = RemoveStaged(state, "one", digest, func() error { return refusal }); !errors.Is(e, refusal) {
		t.Fatal(e)
	}
	if e = disabled(managedRoot(state), digest); e != nil {
		t.Fatal("dependency refusal disabled package", e)
	}
	log := filepath.Join(state, "events.ndjson")
	active := Active{Package: result.Package, Grants: result.Selection.Grants, Config: json.RawMessage(`{"path":"` + log + `","max_bytes":4096}`)}
	if e = RemoveStaged(state, "one", digest, func() error { return nil }); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Lstat(path); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("package still present", e)
	}
	if _, e = Conformance(path); e == nil {
		t.Fatal("disabled snapshot accepted")
	}
	if _, e = DispatchEvent(context.Background(), []Active{active}, Event{Schema: "p.event/v1", ID: "one", Kind: "operation.progress", OccurredAt: "2026-09-26T00:00:00Z", Instance: "one", Fields: map[string]string{"phase": "running"}}); e == nil {
		t.Fatal("cached selection invoked after disable")
	}
	if _, e = os.Lstat(log); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("disabled cached handler wrote log")
	}
	if e = RemoveStaged(state, "one", digest, func() error { return nil }); e != nil {
		t.Fatal("repeat removal failed", e)
	}
	source := testPackage(t)
	if _, e = StagePackage(context.Background(), state, "one", source.Path, digest, ""); e == nil {
		t.Fatal("disabled digest silently reactivated")
	}
}
func TestManagedRegistryRejectsSymlinksHardlinksAndPermissions(t *testing.T) {
	for _, kind := range []string{"registry-symlink", "foreign-mode", "hardlink", "package-symlink"} {
		t.Run(kind, func(t *testing.T) {
			state, src, r := managedFixture(t)
			root := managedRoot(state)
			switch kind {
			case "registry-symlink":
				if e := os.Remove(filepath.Join(root, ".p-registry.json")); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(filepath.Join(src.Path, "plugin.json"), filepath.Join(root, ".p-registry.json")); e != nil {
					t.Fatal(e)
				}
			case "foreign-mode":
				if e := os.Chmod(root, 0770); e != nil {
					t.Fatal(e)
				}
			case "hardlink":
				if e := os.Link(filepath.Join(r.Package.Path, "plugin.json"), filepath.Join(state, "linked")); e != nil {
					t.Fatal(e)
				}
			case "package-symlink":
				if e := os.Chmod(r.Package.Path, 0700); e != nil {
					t.Fatal(e)
				}
				if e := os.Remove(filepath.Join(r.Package.Path, "plugin.json")); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(filepath.Join(src.Path, "plugin.json"), filepath.Join(r.Package.Path, "plugin.json")); e != nil {
					t.Fatal(e)
				}
			}
			if e := RemoveStaged(state, "one", r.Package.SHA256, func() error { return nil }); e == nil {
				t.Fatal("unsafe removal admitted")
			}
			if _, e := os.Lstat(r.Package.Path); e != nil {
				t.Fatal("refusal removed package", e)
			}
		})
	}
}
func TestManagedStageMutationAndConcurrentPublication(t *testing.T) {
	state := privateManagedState(t)
	source := testPackage(t)
	approved := source.SHA256
	// Same-size source mutations during capture can yield refusal or the exact
	// approved snapshot, never a digest/path claiming bytes which were not staged.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.WriteFile(filepath.Join(source.Path, "plugin.json"), []byte(strings.Replace(goodManifest, "1.2.3", "1.2.4", 1)), 0600)
			_ = os.WriteFile(filepath.Join(source.Path, "plugin.json"), []byte(goodManifest), 0600)
		}
	}()
	for i := 0; i < 10; i++ {
		r, e := StagePackage(context.Background(), state, "one", source.Path, approved, "")
		if e == nil {
			p, e := Conformance(r.Package.Path)
			if e != nil || p.SHA256 != approved {
				t.Fatal("publication bytes differ from approval", e)
			}
		}
	}
	close(stop)
	<-done
	if e := os.WriteFile(filepath.Join(source.Path, "plugin.json"), []byte(goodManifest), 0600); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := StagePackage(context.Background(), state, "one", source.Path, approved, "")
			failures <- e
		}()
	}
	wg.Wait()
	close(failures)
	if _, e := StagePackage(context.Background(), state, "one", source.Path, approved, ""); e != nil {
		t.Fatal("final idempotent stage", e)
	}
	entries, e := os.ReadDir(filepath.Join(managedRoot(state), "packages"))
	if e != nil {
		t.Fatal(e)
	}
	if len(entries) != 1 || entries[0].Name() != approved {
		t.Fatal("partial/concurrent publication exposed", entries)
	}
}

func managedAssetFixture(t *testing.T) (string, InstallResult) {
	t.Helper()
	state := privateManagedState(t)
	source, e := filepath.Abs("../../plugins/bundled/tmux-host")
	if e != nil {
		t.Fatal(e)
	}
	p, e := Conformance(source)
	if e != nil {
		t.Fatal(e)
	}
	r, e := StagePackage(context.Background(), state, "one", source, p.SHA256, "")
	if e != nil {
		t.Fatal(e)
	}
	return state, r
}
func interruptManagedRemoval(t *testing.T, state string, r InstallResult) []byte {
	t.Helper()
	calls := 0
	err := removeStaged(state, "one", r.Package.SHA256, func() error { return nil }, func(fd int, name string) error {
		calls++
		if calls == 2 {
			return syscall.EIO
		}
		return syscall.Unlinkat(fd, name)
	})
	if err == nil || calls != 2 {
		t.Fatal("partial removal injection did not remove one actual file", err, calls)
	}
	entries, e := os.ReadDir(r.Package.Path)
	if e != nil || len(entries) != 3 {
		t.Fatal("expected three exact survivors", entries, e)
	}
	raw, e := os.ReadFile(filepath.Join(managedRoot(state), ".disabled-"+r.Package.SHA256))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Conformance(r.Package.Path); e == nil {
		t.Fatal("partial removal allowed new invocation")
	}
	return raw
}
func TestManagedPartialRemovalReopenExactRetry(t *testing.T) {
	state, r := managedAssetFixture(t)
	original := interruptManagedRemoval(t, state, r)
	// All operation descriptors have closed. Reopen solely through durable
	// registry/receipt bytes, with no process-local snapshot or full package left.
	registry, e := readRegistry(managedRoot(state))
	if e != nil || registry.Instance != "one" {
		t.Fatal(e)
	}
	if e = RemoveStaged(state, "one", r.Package.SHA256, func() error { return nil }); e != nil {
		t.Fatal("exact partial cleanup could not resume", e)
	}
	if _, e = os.Lstat(r.Package.Path); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("partial package remains", e)
	}
	after, e := os.ReadFile(filepath.Join(managedRoot(state), ".disabled-"+r.Package.SHA256))
	if e != nil || !bytes.Equal(original, after) {
		t.Fatal("accepted cleanup identities changed", e)
	}
	if e = RemoveStaged(state, "one", r.Package.SHA256, func() error { return nil }); e != nil {
		t.Fatal("completed receipt replay failed", e)
	}
}
func TestManagedPartialRemovalRejectsSubstitutedAndUnknownSurvivors(t *testing.T) {
	for _, kind := range []string{"same-bytes-new-inode", "changed-bytes", "unknown-file", "unknown-directory", "directory-substitution"} {
		t.Run(kind, func(t *testing.T) {
			state, r := managedAssetFixture(t)
			interruptManagedRemoval(t, state, r)
			entries, e := os.ReadDir(r.Package.Path)
			if e != nil {
				t.Fatal(e)
			}
			survivor := filepath.Join(r.Package.Path, entries[0].Name())
			switch kind {
			case "same-bytes-new-inode":
				data, e := os.ReadFile(survivor)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.Rename(survivor, filepath.Join(state, "original-survivor")); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(survivor, data, 0400); e != nil {
					t.Fatal(e)
				}
			case "changed-bytes":
				if e = os.Chmod(survivor, 0600); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(survivor, []byte("different approved-content impostor"), 0400); e != nil {
					t.Fatal(e)
				}
				if e = os.Chmod(survivor, 0400); e != nil {
					t.Fatal(e)
				}
			case "unknown-file":
				if e = os.WriteFile(filepath.Join(r.Package.Path, "unreviewed"), []byte("preserve"), 0400); e != nil {
					t.Fatal(e)
				}
			case "unknown-directory":
				if e = os.Mkdir(filepath.Join(r.Package.Path, "unreviewed"), 0700); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(r.Package.Path, "unreviewed", "private"), []byte("preserve"), 0600); e != nil {
					t.Fatal(e)
				}
			case "directory-substitution":
				if e = os.Rename(r.Package.Path, filepath.Join(state, "original-package")); e != nil {
					t.Fatal(e)
				}
				if e = os.Mkdir(r.Package.Path, 0700); e != nil {
					t.Fatal(e)
				}
			}
			before, e := os.ReadDir(r.Package.Path)
			if e != nil {
				t.Fatal(e)
			}
			if e = RemoveStaged(state, "one", r.Package.SHA256, func() error { return nil }); e == nil {
				t.Fatal("unsafe survivor admitted")
			}
			after, e := os.ReadDir(r.Package.Path)
			if e != nil || len(before) != len(after) {
				t.Fatal("refusal removed survivors", e)
			}
			if kind == "unknown-directory" {
				data, e := os.ReadFile(filepath.Join(r.Package.Path, "unreviewed", "private"))
				if e != nil || string(data) != "preserve" {
					t.Fatal("recurred into unknown content", e)
				}
			}
		})
	}
}
func TestManagedMissingRegistryNeverAdoptsRetainedOrDisabledPackage(t *testing.T) {
	state, _, r := managedFixture(t)
	if e := removeStaged(state, "one", r.Package.SHA256, func() error { return nil }, func(int, string) error { return syscall.EIO }); e == nil {
		t.Fatal("expected injected interruption")
	}
	if e := os.Remove(filepath.Join(managedRoot(state), ".p-registry.json")); e != nil {
		t.Fatal(e)
	}
	if _, e := StagePackage(context.Background(), state, "other", r.Package.Path, r.Package.SHA256, ""); e == nil {
		t.Fatal("lost registry was silently rebound")
	}
	if _, e := LeaseInstancePackage(r.Package.Path, state, "other"); e == nil {
		t.Fatal("missing binding became unmanaged for foreign instance")
	}
	if _, e := LeasePackage(r.Package.Path); e == nil {
		t.Fatal("missing binding bypassed durable disable")
	}
	if _, e := Conformance(r.Package.Path); e == nil {
		t.Fatal("retained package was admitted without registry")
	}
	unmanaged := testPackage(t)
	if _, e := Conformance(unmanaged.Path); e != nil {
		t.Fatal("ordinary unmanaged fixture refused", e)
	}
}
