package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/driverwire/launch"
)

// The launch checks refuse a package that someone other than root or P's
// account could change, before they trust its digest.
func TestOpenExecutableRefuses(t *testing.T) {
	euid := uint32(os.Geteuid())
	for _, c := range []struct {
		name string
		// arrange changes the package at dir and returns the digest and
		// account to check it with.
		arrange func(t *testing.T, dir, digest string) (string, uint32)
		want    string
	}{
		{"a group-writable package directory", func(t *testing.T, dir, digest string) (string, uint32) {
			chmod(t, dir, 0o770)
			return digest, euid
		}, "package is group- or world-writable"},
		{"a world-writable file", func(t *testing.T, dir, digest string) (string, uint32) {
			chmod(t, filepath.Join(dir, "plugin.json"), 0o606)
			return digest, euid
		}, "plugin.json is group- or world-writable"},
		{"a group-writable entry", func(t *testing.T, dir, digest string) (string, uint32) {
			chmod(t, filepath.Join(dir, "handler"), 0o770)
			return digest, euid
		}, "handler is group- or world-writable"},
		{"a package owned by another account", func(t *testing.T, dir, digest string) (string, uint32) {
			return digest, euid + 1
		}, "is owned by another account"},
		{"a second link to a file P's account owns", func(t *testing.T, dir, digest string) (string, uint32) {
			if err := os.Link(filepath.Join(dir, "plugin.json"), filepath.Join(t.TempDir(), "alias")); err != nil {
				t.Fatal(err)
			}
			return digest, euid
		}, "plugin.json has another link while it can be written"},
		{"an entry without its execute bit", func(t *testing.T, dir, digest string) (string, uint32) {
			chmod(t, filepath.Join(dir, "handler"), 0o600)
			return digest, euid
		}, "handler lacks its owner's execute bit"},
		{"an entry without its execute bit, pinned by another digest", func(t *testing.T, dir, digest string) (string, uint32) {
			chmod(t, filepath.Join(dir, "handler"), 0o600)
			return strings.Repeat("0", 64), euid
		}, "handler lacks its owner's execute bit"},
		{"a world-writable file, pinned by another digest", func(t *testing.T, dir, digest string) (string, uint32) {
			chmod(t, filepath.Join(dir, "plugin.json"), 0o606)
			return strings.Repeat("0", 64), euid
		}, "plugin.json is group- or world-writable"},
		{"another digest", func(t *testing.T, dir, digest string) (string, uint32) {
			return strings.Repeat("0", 64), euid
		}, "not the pinned " + strings.Repeat("0", 64)},
		{"a changed file", func(t *testing.T, dir, digest string) (string, uint32) {
			if err := os.WriteFile(filepath.Join(dir, "handler"), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			return digest, euid
		}, "not the pinned"},
		{"a symbolic link in the package", func(t *testing.T, dir, digest string) (string, uint32) {
			if err := os.Symlink("/etc/passwd", filepath.Join(dir, "other")); err != nil {
				t.Fatal(err)
			}
			return digest, euid
		}, "other is a symbolic link"},
		{"a digest that is not lowercase hex", func(t *testing.T, dir, digest string) (string, uint32) {
			return strings.ToUpper(digest), euid
		}, "not 64 lowercase hex digits"},
	} {
		t.Run(c.name, func(t *testing.T) {
			pkg := testPackage(t)
			digest, account := c.arrange(t, pkg.Path, pkg.SHA256)
			_, entry, err := OpenExecutable(pkg.Path, digest, account)
			if err == nil {
				entry.Close()
				t.Fatal("the launch checks passed")
			}
			if !errors.Is(err, launch.ErrLaunchCheck) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("the launch checks refused with %v, want %q", err, c.want)
			}
		})
	}
}

// A package of files is never started.
func TestOpenExecutableRefusesAPackageOfFiles(t *testing.T) {
	pkg, err := Conformance(writePackage(t, adapterManifest, map[string]string{"p-codex-adapter": "#!/bin/sh\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, entry, err := OpenExecutable(pkg.Path, pkg.SHA256, uint32(os.Geteuid())); err == nil || !errors.Is(err, launch.ErrLaunchCheck) {
		if entry != nil {
			entry.Close()
		}
		t.Fatalf("OpenExecutable of an agent adapter = %v, want a refusal", err)
	}
}

// A trusted package passes, and the file returned is its entry, as hashed.
func TestOpenExecutableReturnsTheEntryChecked(t *testing.T) {
	pkg := testPackage(t)
	got, entry, err := OpenExecutable(pkg.Path, pkg.SHA256, uint32(os.Geteuid()))
	if err != nil {
		t.Fatalf("the launch checks refused a trusted package: %v", err)
	}
	defer entry.Close()
	if !reflect.DeepEqual(got.Manifest, pkg.Manifest) || got.SHA256 != pkg.SHA256 || entry.Name() != filepath.Join(pkg.Path, "handler") {
		t.Fatalf("OpenExecutable = %+v, %s; want %+v and its entry", got, entry.Name(), pkg)
	}
	if _, err := entry.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(entry)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(handlerScript))
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != hex.EncodeToString(want[:]) {
		t.Fatalf("the entry returned holds %q", data)
	}
}

func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
