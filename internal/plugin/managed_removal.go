package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

type removalIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}
type removalFile struct {
	Name     string          `json:"name"`
	Identity removalIdentity `json:"identity"`
	Size     int64           `json:"size"`
	Mode     uint32          `json:"mode"`
	SHA256   string          `json:"sha256"`
}
type removalReceipt struct {
	Schema    string          `json:"schema"`
	Digest    string          `json:"digest"`
	Parent    removalIdentity `json:"parent"`
	Directory removalIdentity `json:"directory"`
	Files     []removalFile   `json:"files"`
}

const removalSchema = "p.plugin-removal/v1"

func removalFileIdentity(i os.FileInfo) removalIdentity {
	if st, ok := i.Sys().(*syscall.Stat_t); ok {
		return removalIdentity{uint64(st.Dev), st.Ino}
	}
	return removalIdentity{}
}
func removalBytesSHA(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func captureRemovalReceipt(target, digest string) (removalReceipt, error) {
	r := removalReceipt{Schema: removalSchema, Digest: digest, Files: []removalFile{}}
	parent, e := os.Lstat(filepath.Dir(target))
	if e != nil {
		return r, e
	}
	r.Parent = removalFileIdentity(parent)
	i, e := os.Lstat(target)
	if errors.Is(e, os.ErrNotExist) {
		return r, nil
	}
	if e != nil {
		return r, e
	}
	if e = verifyStaged(target, digest); e != nil {
		return r, e
	}
	r.Directory = removalFileIdentity(i)
	pkg, captured, e := snapshotWithoutLease(context.Background(), target, map[string]bool{"*": true})
	if e != nil || pkg.SHA256 != digest {
		return r, errors.Join(e, errors.New("approved removal snapshot unavailable"))
	}
	dir, e := openRemovalDirectory(target, r)
	if e != nil {
		return r, e
	}
	defer dir.Close()
	names := make([]string, 0, len(captured))
	for name := range captured {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		f, e := openPackageFile(int(dir.Fd()), name)
		if e != nil {
			return r, e
		}
		info, e := f.Stat()
		if e != nil || !privateManaged(info, false) || info.Mode().Perm()&0200 != 0 {
			f.Close()
			return r, errors.New("removal file identity unavailable")
		}
		data, e := io.ReadAll(io.LimitReader(f, (8<<20)+1))
		after, statErr := f.Stat()
		f.Close()
		if e != nil || statErr != nil || !bytes.Equal(data, captured[name]) || removalFileIdentity(info) != removalFileIdentity(after) || info.Size() != after.Size() {
			return r, errors.New("removal opened bytes changed")
		}
		r.Files = append(r.Files, removalFile{Name: name, Identity: removalFileIdentity(info), Size: info.Size(), Mode: uint32(info.Mode().Perm()), SHA256: removalBytesSHA(data)})
	}
	if e = validateRemovalSurvivors(target, r); e != nil {
		return r, e
	}
	return r, nil
}
func openRemovalDirectory(target string, r removalReceipt) (*os.File, error) {
	if e := checkedManagedDir(filepath.Dir(target)); e != nil {
		return nil, e
	}
	parent, e := os.Lstat(filepath.Dir(target))
	if e != nil || removalFileIdentity(parent) != r.Parent {
		return nil, errors.New("removal parent identity changed; preserve and investigate")
	}
	fd, e := syscall.Open(target, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	dir := os.NewFile(uintptr(fd), target)
	i, e := dir.Stat()
	if e != nil || !privateManaged(i, true) || removalFileIdentity(i) != r.Directory {
		dir.Close()
		return nil, errors.New("removal package directory substituted; preserve and investigate")
	}
	return dir, nil
}
func verifyRemovalFile(dir *os.File, expected removalFile) error {
	f, e := openPackageFile(int(dir.Fd()), expected.Name)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	defer f.Close()
	i, e := f.Stat()
	if e != nil || !privateManaged(i, false) || removalFileIdentity(i) != expected.Identity || i.Size() != expected.Size || uint32(i.Mode().Perm()) != expected.Mode {
		return errors.New("removal file substituted or modified; preserve and investigate")
	}
	data, e := io.ReadAll(io.LimitReader(f, expected.Size+1))
	if e != nil || int64(len(data)) != expected.Size || removalBytesSHA(data) != expected.SHA256 {
		return errors.New("removal survivor bytes changed; preserve and investigate")
	}
	after, e := f.Stat()
	if e != nil || removalFileIdentity(after) != expected.Identity || after.Size() != expected.Size {
		return errors.New("removal survivor changed during verification")
	}
	return nil
}
func validateRemovalSurvivors(target string, r removalReceipt) error {
	parent, e := os.Lstat(filepath.Dir(target))
	if e != nil || removalFileIdentity(parent) != r.Parent {
		return errors.New("removal parent identity changed")
	}
	dir, e := openRemovalDirectory(target, r)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	defer dir.Close()
	entries, e := dir.ReadDir(129)
	if e != nil && !errors.Is(e, io.EOF) || len(entries) > 128 {
		return errors.New("removal survivor inventory unavailable")
	}
	approved := map[string]removalFile{}
	for _, f := range r.Files {
		approved[f.Name] = f
	}
	for _, entry := range entries {
		expected, ok := approved[entry.Name()]
		if !ok {
			return errors.New("unknown removal survivor; preserve all remaining content and investigate")
		}
		if e = verifyRemovalFile(dir, expected); e != nil {
			return e
		}
	}
	return nil
}
func validRemovalReceipt(r removalReceipt, digest string) bool {
	if r.Schema != removalSchema || r.Digest != digest || r.Parent.Inode == 0 || len(r.Files) > 128 || r.Directory.Inode == 0 && len(r.Files) != 0 {
		return false
	}
	seen := map[string]bool{}
	total := int64(0)
	for _, f := range r.Files {
		total += f.Size
		if safeRelative(f.Name) != nil || filepath.Base(f.Name) != f.Name || f.Name == "." || f.Name == ".." || seen[f.Name] || f.Identity.Inode == 0 || f.Size < 0 || f.Size > 8<<20 || f.Mode != 0400 || !validDigest(f.SHA256) {
			return false
		}
		seen[f.Name] = true
	}
	return total <= 16<<20 && (r.Directory.Inode == 0 || seen["plugin.json"])
}
func loadRemovalReceipt(marker, digest string) (removalReceipt, error) {
	var r removalReceipt
	f, e := managedFile(marker, syscall.O_RDONLY)
	if e != nil {
		return r, e
	}
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if e != nil || len(raw) > 64<<10 || strictJSON(raw, &r) != nil || !validRemovalReceipt(r, digest) {
		return r, errors.New("disable cleanup receipt unavailable; preserve package and investigate exact identity")
	}
	return r, nil
}
func publishRemovalReceipt(root, marker string, r removalReceipt) error {
	raw, e := json.Marshal(r)
	if e != nil || len(raw) > 64<<10 || !validRemovalReceipt(r, r.Digest) {
		return errors.New("invalid bounded removal receipt")
	}
	f, e := os.CreateTemp(root, ".disable-pending-")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, e = f.Write(raw); e == nil {
		e = f.Sync()
	}
	f.Close()
	if e != nil {
		return e
	}
	if e = os.Rename(tmp, marker); e != nil {
		return e
	}
	return syncDirectory(root)
}

// RemoveStaged syncs the exact cleanup receipt before any deletion. A resumed
// attempt admits only missing approved files or exact unchanged survivors.
func RemoveStaged(state, instance, digest string, verify func() error) error {
	return removeStaged(state, instance, digest, verify, func(fd int, name string) error { return syscall.Unlinkat(fd, name) })
}
func removeStaged(state, instance, digest string, verify func() error, unlink func(int, string) error) error {
	if !validDigest(digest) {
		return errors.New("invalid package digest")
	}
	root, e := ensureRegistry(state, instance)
	if e != nil {
		return e
	}
	lock, e := registryLock(root)
	if e != nil {
		return e
	}
	defer lock.Close()
	lease, e := digestLease(root, digest, true)
	if e != nil {
		return e
	}
	defer lease.Close()
	target := filepath.Join(root, "packages", digest)
	marker := filepath.Join(root, ".disabled-"+digest)
	r, e := loadRemovalReceipt(marker, digest)
	fresh := errors.Is(e, os.ErrNotExist)
	if fresh {
		r, e = captureRemovalReceipt(target, digest)
	}
	if e != nil {
		return e
	}
	if e = validateRemovalSurvivors(target, r); e != nil {
		return e
	}
	if verify == nil {
		return errors.New("removal dependency proof required")
	}
	if e = verify(); e != nil {
		return e
	}
	if fresh {
		if e = publishRemovalReceipt(root, marker, r); e != nil {
			return e
		}
	}
	dir, e := openRemovalDirectory(target, r)
	if errors.Is(e, os.ErrNotExist) {
		return syncDirectory(filepath.Dir(target))
	}
	if e != nil {
		return e
	}
	defer dir.Close()
	if e = syscall.Fchmod(int(dir.Fd()), 0700); e != nil {
		return e
	}
	for _, file := range r.Files {
		if e = verifyRemovalFile(dir, file); e != nil {
			return e
		}
		if e = unlink(int(dir.Fd()), file.Name); e != nil && !errors.Is(e, os.ErrNotExist) {
			return fmt.Errorf("package disabled, exact cleanup incomplete; retry removal after filesystem availability returns: %w", e)
		}
		if e = dir.Sync(); e != nil {
			return e
		}
	}
	if e = validateRemovalSurvivors(target, r); e != nil {
		return e
	}
	i, e := os.Lstat(target)
	if e != nil || removalFileIdentity(i) != r.Directory {
		return errors.New("package directory changed before final removal")
	}
	if e = os.Remove(target); e != nil {
		return fmt.Errorf("package disabled, final directory removal incomplete; retry exact removal: %w", e)
	}
	return syncDirectory(filepath.Dir(target))
}
