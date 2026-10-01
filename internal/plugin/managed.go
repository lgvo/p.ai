package plugin

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// ManagedRegistry binds staged packages to one durable P instance. It is not an
// activation catalog: only explicit trusted configuration grants invocation.
type ManagedRegistry struct {
	Schema   string `json:"schema"`
	Instance string `json:"instance"`
	StateDir string `json:"state_dir"`
}
type InstallResult struct {
	Package            Package         `json:"package"`
	Selection          SelectedPackage `json:"selection"`
	ActivationRequired bool            `json:"explicit_trusted_selection_required"`
	PreviousSHA256     string          `json:"previous_sha256,omitempty"`
}

const registrySchema = "p.plugin-registry/v1"

func validDigest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func managedRoot(state string) string { return filepath.Join(state, "plugins") }
func checkManagedAncestors(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("managed path must be absolute and clean")
	}
	for d := filepath.Dir(path); ; d = filepath.Dir(d) {
		i, e := os.Lstat(d)
		if e != nil {
			return e
		}
		st, ok := i.Sys().(*syscall.Stat_t)
		if !ok || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) {
			return errors.New("untrusted managed path ancestry")
		}
		if i.Mode().Perm()&0022 != 0 && !(st.Uid == 0 && i.Mode()&os.ModeSticky != 0) {
			return errors.New("writable managed path ancestry")
		}
		if d == "/" {
			return nil
		}
	}
}
func privateManaged(i os.FileInfo, directory bool) bool {
	st, ok := i.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid()) && i.Mode().Perm()&0077 == 0 && ((directory && i.IsDir()) || (!directory && i.Mode().IsRegular() && st.Nlink == 1))
}
func checkedManagedDir(path string) error {
	if e := checkManagedAncestors(path); e != nil {
		return e
	}
	i, e := os.Lstat(path)
	if e != nil {
		return e
	}
	if !privateManaged(i, true) || i.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed registry directory must be private and owned")
	}
	return nil
}
func managedFile(path string, flags int) (*os.File, error) {
	if e := checkManagedAncestors(path); e != nil {
		return nil, e
	}
	fd, e := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0600)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	i, e := f.Stat()
	if e != nil || !privateManaged(i, false) {
		f.Close()
		return nil, errors.New("managed metadata must be private, singly linked and owned")
	}
	return f, nil
}
func syncDirectory(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func readRegistry(root string) (ManagedRegistry, error) {
	var r ManagedRegistry
	if e := checkedManagedDir(root); e != nil {
		return r, e
	}
	f, e := managedFile(filepath.Join(root, ".p-registry.json"), syscall.O_RDONLY)
	if e != nil {
		return r, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 4097))
	if e != nil || len(b) > 4096 {
		return r, errors.New("invalid registry metadata size")
	}
	if e = strictJSON(b, &r); e != nil {
		return r, e
	}
	if r.Schema != registrySchema || r.Instance == "" || managedRoot(r.StateDir) != root {
		return r, errors.New("invalid managed registry binding")
	}
	return r, nil
}
func registryLock(root string) (*os.File, error) {
	f, e := managedFile(filepath.Join(root, ".registry.lock"), syscall.O_CREAT|syscall.O_RDWR)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("managed registry operation already active")
	}
	return f, nil
}
func ensureRegistry(state, instance string) (string, error) {
	root := managedRoot(state)
	if e := checkedManagedDir(state); e != nil {
		return "", e
	}
	if e := os.Mkdir(root, 0700); e != nil && !errors.Is(e, os.ErrExist) {
		return "", e
	}
	if e := checkedManagedDir(root); e != nil {
		return "", e
	}
	r, e := readRegistry(root)
	if errors.Is(e, os.ErrNotExist) {
		entries, readErr := os.ReadDir(root)
		if readErr != nil {
			return "", readErr
		}
		if len(entries) != 0 {
			return "", errors.New("managed registry binding lost with retained metadata/packages; preserve and investigate")
		}
		f, e := managedFile(filepath.Join(root, ".p-registry.json"), syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL)
		if e != nil {
			return "", e
		}
		r = ManagedRegistry{registrySchema, instance, state}
		b, _ := json.Marshal(r)
		_, e = f.Write(b)
		if e == nil {
			e = f.Sync()
		}
		f.Close()
		if e != nil {
			return "", e
		}
		if e = syncDirectory(root); e != nil {
			return "", e
		}
	} else if e != nil {
		return "", e
	}
	if r.Instance != instance || r.StateDir != state {
		return "", errors.New("registry belongs to another P instance")
	}
	packages := filepath.Join(root, "packages")
	if e = os.Mkdir(packages, 0700); e != nil && !errors.Is(e, os.ErrExist) {
		return "", e
	}
	if e = checkedManagedDir(packages); e != nil {
		return "", e
	}
	if e = syncDirectory(root); e != nil {
		return "", e
	}
	return root, syncDirectory(state)
}
func packageRegistry(path string) (string, string, bool, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", "", false, errors.New("package path must be absolute and clean")
	}
	root := filepath.Dir(filepath.Dir(path))
	digest := filepath.Base(path)
	if filepath.Base(root) != "plugins" || filepath.Base(filepath.Dir(path)) != "packages" || !validDigest(digest) {
		return "", "", false, nil
	}
	// The reserved layout is managed even when its binding is lost. Metadata loss
	// never converts retained or disabled packages into unmanaged authority.
	var e error
	if _, e = readRegistry(root); e != nil {
		return "", "", true, e
	}
	if e = checkedManagedDir(filepath.Dir(path)); e != nil {
		return "", "", true, e
	}
	return root, digest, true, nil
}
func digestLease(root, digest string, exclusive bool) (*os.File, error) {
	f, e := managedFile(filepath.Join(root, ".lease-"+digest), syscall.O_CREAT|syscall.O_RDWR)
	if e != nil {
		return nil, e
	}
	op := syscall.LOCK_SH
	if exclusive {
		op = syscall.LOCK_EX
	}
	if e = syscall.Flock(int(f.Fd()), op|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("managed package has active holders; stop invocations before removal")
	}
	return f, nil
}
func disabled(root, digest string) error {
	_, e := os.Lstat(filepath.Join(root, ".disabled-"+digest))
	if e == nil {
		return errors.New("managed package permanently disabled; approve and select a new digest")
	}
	if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	return nil
}

// LeasePackage fences the complete invocation, including already-cached Active
// selections. Unmanaged packages retain the existing invocation revalidation.
func LeasePackage(path string) (func(), error) {
	root, digest, managed, e := packageRegistry(path)
	if e != nil {
		return nil, e
	}
	if !managed {
		return func() {}, nil
	}
	f, e := digestLease(root, digest, false)
	if e != nil {
		return nil, e
	}
	if e = disabled(root, digest); e != nil {
		f.Close()
		return nil, e
	}
	return func() { f.Close() }, nil
}

// LeaseInstancePackage also prevents a second instance from borrowing another
// instance's managed registry. Daemons hold this lease until all runners stop.
func LeaseInstancePackage(path, state, instance string) (func(), error) {
	root, _, managed, e := packageRegistry(path)
	if e != nil {
		return nil, e
	}
	if managed {
		r, e := readRegistry(root)
		if e != nil {
			return nil, e
		}
		if r.StateDir != state || r.Instance != instance {
			return nil, errors.New("managed package belongs to another P instance")
		}
	}
	return LeasePackage(path)
}
func StagePackage(ctx context.Context, state, instance, source, approved, previous string) (InstallResult, error) {
	var result InstallResult
	if !validDigest(approved) || previous != "" && !validDigest(previous) {
		return result, errors.New("explicit lowercase SHA256 approval required")
	}
	root, e := ensureRegistry(state, instance)
	if e != nil {
		return result, e
	}
	lock, e := registryLock(root)
	if e != nil {
		return result, e
	}
	defer lock.Close()
	lease, e := digestLease(root, approved, true)
	if e != nil {
		return result, e
	}
	defer lease.Close()
	if e = disabled(root, approved); e != nil {
		return result, e
	}
	pkg, bytes, e := packageSnapshot(ctx, source, map[string]bool{"*": true})
	if e != nil {
		return result, e
	}
	if pkg.SHA256 != approved {
		return result, errors.New("opened package bytes differ from explicitly approved digest")
	}
	if previous != "" {
		if previous == approved {
			return result, errors.New("update requires a newly approved digest")
		}
		old, e := Conformance(filepath.Join(root, "packages", previous))
		if e != nil {
			return result, e
		}
		if old.Manifest.ID != pkg.Manifest.ID {
			return result, errors.New("update package ID differs from previous package")
		}
	}
	target := filepath.Join(root, "packages", approved)
	if _, e = os.Lstat(target); e == nil {
		if e = verifyStaged(target, approved); e != nil {
			return result, e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return result, e
	} else {
		tmp, e := os.MkdirTemp(filepath.Dir(target), ".staging-")
		if e != nil {
			return result, e
		}
		defer func() { _ = os.Chmod(tmp, 0700); _ = os.RemoveAll(tmp) }()
		for name, data := range bytes {
			f, e := os.OpenFile(filepath.Join(tmp, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
			if e != nil {
				return result, e
			}
			_, e = f.Write(data)
			if e == nil {
				e = f.Sync()
			}
			f.Close()
			if e != nil {
				return result, e
			}
		}
		if e = syncDirectory(tmp); e != nil {
			return result, e
		}
		if e = os.Chmod(tmp, 0500); e != nil {
			return result, e
		}
		if e = os.Rename(tmp, target); e != nil {
			return result, e
		}
		if e = syncDirectory(filepath.Dir(target)); e != nil {
			return result, e
		}
		if e = verifyStaged(target, approved); e != nil {
			return result, e
		}
	}
	pkg.Path = target
	result = InstallResult{Package: pkg, Selection: SelectedPackage{ID: pkg.Manifest.ID, Path: target, SHA256: approved, Grants: pkg.Manifest.Requests}, ActivationRequired: true, PreviousSHA256: previous}
	return result, nil
}
func verifyStaged(path, digest string) error {
	if e := checkedManagedDir(path); e != nil {
		return e
	}
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	entries, e := f.ReadDir(129)
	f.Close()
	if e != nil || len(entries) > 128 {
		return errors.New("invalid staged inventory")
	}
	for _, x := range entries {
		i, e := os.Lstat(filepath.Join(path, x.Name()))
		if e != nil || !privateManaged(i, false) || i.Mode().Perm()&0200 != 0 {
			return errors.New("staged files must be owned, singly linked and readonly")
		}
	} // Caller holds the exclusive lease, so use the internal snapshot bypass only here.
	pkg, _, e := snapshotWithoutLease(context.Background(), path, nil)
	if e != nil {
		return e
	}
	if pkg.SHA256 != digest {
		return errors.New("staged digest mismatch")
	}
	return nil
}
