package plugin

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/lgvo/p.ai/internal/driverwire/launch"
)

// OpenExecutable applies the launch checks for account, the account P runs
// as, to the package of an executable capability at path, pinned by digest,
// and returns its manifest and its entry, open: the file it checked and
// hashed, which is the one to start. Before it hashes anything, the package
// directory and its ancestors must pass launch.OpenDirectory, every file
// launch.CheckFile, and the entry its manifest names its owner's execute
// bit; it then hashes the files it checked. Every refusal is of kind
// launch.ErrLaunchCheck.
func OpenExecutable(path, digest string, account uint32) (Package, *os.File, error) {
	if !validDigest(digest) {
		return Package{}, nil, launch.Refusal(path, "is pinned by a SHA-256 that is not 64 lowercase hex digits")
	}
	dir, err := launch.OpenDirectory(path, account)
	if err != nil {
		return Package{}, nil, err
	}
	defer dir.Close()
	files, err := checkFiles(dir, path, account)
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	if err != nil {
		return Package{}, nil, err
	}
	entry, err := checkEntry(files, path, account)
	if err != nil {
		return Package{}, nil, err
	}
	if _, err := dir.Seek(0, io.SeekStart); err != nil {
		return Package{}, nil, launch.Refusal(path, "cannot be read: "+err.Error())
	}
	pkg, _, err := snapshotOpen(context.Background(), path, dir, nil, func(name string, f *os.File) error {
		if checked := files[name]; checked == nil || !sameFile(checked, f) {
			return launch.Refusal(filepath.Join(path, name), "changed while it was checked")
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, launch.ErrLaunchCheck) {
			return Package{}, nil, err
		}
		return Package{}, nil, launch.Refusal(path, "is not a valid package: "+err.Error())
	}
	if pkg.SHA256 != digest {
		return Package{}, nil, launch.Refusal(path, "has SHA-256 "+pkg.SHA256+", not the pinned "+digest)
	}
	delete(files, pkg.Manifest.Entry)
	return pkg, entry, nil
}

// checkFiles opens every file of the package directory dir, at path, without
// following a symbolic link, and applies launch.CheckFile to it. It returns
// the files it opened, by name, also on an error.
func checkFiles(dir *os.File, path string, account uint32) (map[string]*os.File, error) {
	files := map[string]*os.File{}
	entries, err := dir.ReadDir(maxPackageFiles + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return files, launch.Refusal(path, "cannot be read: "+err.Error())
	}
	if len(entries) > maxPackageFiles {
		return files, launch.Refusal(path, "is not a valid package: it has more than 128 files")
	}
	for _, entry := range entries {
		name := filepath.Join(path, entry.Name())
		fd, err := syscall.Openat(int(dir.Fd()), entry.Name(), syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if errors.Is(err, syscall.ELOOP) {
			return files, launch.Refusal(name, "is a symbolic link")
		}
		if err != nil {
			return files, launch.Refusal(name, "cannot be opened: "+err.Error())
		}
		f := os.NewFile(uintptr(fd), name)
		files[entry.Name()] = f
		if err := launch.CheckFile(f, name, account, false); err != nil {
			return files, err
		}
	}
	return files, nil
}

// checkEntry reads the manifest among the checked files and returns the
// entry it names, which needs its owner's execute bit.
func checkEntry(files map[string]*os.File, path string, account uint32) (*os.File, error) {
	manifest := files["plugin.json"]
	if manifest == nil {
		return nil, launch.Refusal(path, "is not a valid package: missing plugin.json")
	}
	data, err := io.ReadAll(io.LimitReader(manifest, maxManifestBytes+1))
	if err != nil || len(data) > maxManifestBytes {
		return nil, launch.Refusal(manifest.Name(), "cannot be read within 64 KiB")
	}
	m, err := decodeManifest(data)
	if err != nil {
		return nil, launch.Refusal(path, "is not a valid package: "+err.Error())
	}
	if m.Entry == "" {
		return nil, launch.Refusal(path, "provides "+m.Capability+", which P does not start")
	}
	entry := files[m.Entry]
	if entry == nil {
		return nil, launch.Refusal(path, "is not a valid package: missing entry "+m.Entry)
	}
	if err := launch.CheckFile(entry, entry.Name(), account, true); err != nil {
		return nil, err
	}
	return entry, nil
}

// sameFile reports whether a and b are the same file.
func sameFile(a, b *os.File) bool {
	var sa, sb syscall.Stat_t
	return syscall.Fstat(int(a.Fd()), &sa) == nil && syscall.Fstat(int(b.Fd()), &sb) == nil &&
		sa.Dev == sb.Dev && sa.Ino == sb.Ino
}
