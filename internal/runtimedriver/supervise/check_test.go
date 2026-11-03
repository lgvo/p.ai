package supervise

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const account = 1000

func TestDirectoryRules(t *testing.T) {
	dir := func(mode, uid uint32) fileStat { return fileStat{mode: syscall.S_IFDIR | mode, uid: uid, nlink: 2} }
	for _, c := range []struct {
		name string
		stat fileStat
		want string
	}{
		{"owned by the account", dir(0o755, account), ""},
		{"owned by root", dir(0o755, 0), ""},
		{"owned by root, sticky and world-writable", dir(0o1777, 0), ""},
		{"owned by root, sticky and group-writable", dir(0o1775, 0), ""},
		{"owned by another account", dir(0o755, account+1), "is owned by another account"},
		{"group-writable", dir(0o775, account), "is group- or world-writable"},
		{"world-writable", dir(0o757, account), "is group- or world-writable"},
		{"group-writable and owned by root", dir(0o775, 0), "is group- or world-writable"},
		{"sticky and world-writable but not root's", dir(0o1777, account), "is group- or world-writable"},
		{"a symbolic link", fileStat{mode: syscall.S_IFLNK | 0o777, uid: account, nlink: 1}, "is a symbolic link"},
		{"a regular file", fileStat{mode: syscall.S_IFREG | 0o755, uid: account, nlink: 1}, "is not a directory"},
	} {
		if got := directoryRefusal(c.stat, account); got != c.want {
			t.Errorf("a directory %s: refusal %q, want %q", c.name, got, c.want)
		}
	}
}

func TestExecutableRules(t *testing.T) {
	file := func(mode, uid uint32, nlink uint64) fileStat {
		return fileStat{mode: syscall.S_IFREG | mode, uid: uid, nlink: nlink}
	}
	for _, c := range []struct {
		name string
		stat fileStat
		want string
	}{
		{"owned by the account", file(0o700, account, 1), ""},
		{"owned by root", file(0o755, 0, 1), ""},
		{"owned by root, read-only, with other links", file(0o555, 0, 3), ""},
		{"owned by another account", file(0o700, account+1, 1), "is owned by another account"},
		{"group-writable", file(0o720, account, 1), "is group- or world-writable"},
		{"world-writable", file(0o702, account, 1), "is group- or world-writable"},
		{"owned by root, writable, with another link", file(0o755, 0, 2), "has another link while it can be written"},
		{"owned by the account, read-only, with another link", file(0o500, account, 2), "has another link while it can be written"},
		{"without its owner's execute bit", file(0o644, account, 1), "lacks its owner's execute bit"},
		{"executable by others only", file(0o611, account, 1), "lacks its owner's execute bit"},
		{"a directory", fileStat{mode: syscall.S_IFDIR | 0o755, uid: account, nlink: 2}, "is not a regular file"},
		{"a FIFO", fileStat{mode: syscall.S_IFIFO | 0o700, uid: account, nlink: 1}, "is not a regular file"},
		{"a symbolic link", fileStat{mode: syscall.S_IFLNK | 0o777, uid: account, nlink: 1}, "is a symbolic link"},
	} {
		if got := executableRefusal(c.stat, account); got != c.want {
			t.Errorf("an executable %s: refusal %q, want %q", c.name, got, c.want)
		}
	}
}

// executable is a trusted executable in a private directory of its own.
type executable struct {
	dir, path, digest string
}

func newExecutable(t *testing.T) executable {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "driver")
	return executable{dir: dir, path: path, digest: script(t, path, "#!/bin/sh\nexit 0\n")}
}

func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// The launch checks refuse what the rules refuse, as the files are on
// disk, before they hash anything.
func TestLaunchChecksRefuse(t *testing.T) {
	euid := uint32(os.Geteuid())
	for _, c := range []struct {
		name string
		// arrange returns the path, digest and account to check.
		arrange func(t *testing.T, e executable) (string, string, uint32)
		want    string
	}{
		{"a directory owned by another account", func(t *testing.T, e executable) (string, string, uint32) {
			return e.path, e.digest, euid + 1
		}, "bin is owned by another account"},
		{"a group-writable executable", func(t *testing.T, e executable) (string, string, uint32) {
			chmod(t, e.path, 0o770)
			return e.path, e.digest, euid
		}, "driver is group- or world-writable"},
		{"a world-writable executable", func(t *testing.T, e executable) (string, string, uint32) {
			chmod(t, e.path, 0o707)
			return e.path, e.digest, euid
		}, "driver is group- or world-writable"},
		{"a group-writable directory", func(t *testing.T, e executable) (string, string, uint32) {
			chmod(t, e.dir, 0o770)
			return e.path, e.digest, euid
		}, "bin is group- or world-writable"},
		{"a world-writable directory", func(t *testing.T, e executable) (string, string, uint32) {
			chmod(t, e.dir, 0o707)
			return e.path, e.digest, euid
		}, "bin is group- or world-writable"},
		{"a sticky world-writable directory not owned by root", func(t *testing.T, e executable) (string, string, uint32) {
			chmod(t, e.dir, os.ModeSticky|0o777)
			return e.path, e.digest, euid
		}, "bin is group- or world-writable"},
		{"a second link while it can be written", func(t *testing.T, e executable) (string, string, uint32) {
			if err := os.Link(e.path, filepath.Join(e.dir, "alias")); err != nil {
				t.Fatal(err)
			}
			return e.path, e.digest, euid
		}, "has another link while it can be written"},
		{"no execute bit for its owner", func(t *testing.T, e executable) (string, string, uint32) {
			chmod(t, e.path, 0o600)
			return e.path, e.digest, euid
		}, "lacks its owner's execute bit"},
		{"another SHA-256", func(t *testing.T, e executable) (string, string, uint32) {
			return e.path, strings.Repeat("0", 64), euid
		}, "has SHA-256 " + "%digest%"},
		{"a pinned digest that is not lowercase hex", func(t *testing.T, e executable) (string, string, uint32) {
			return e.path, strings.ToUpper(e.digest), euid
		}, "is not 64 lowercase hex digits"},
		{"a symbolic link", func(t *testing.T, e executable) (string, string, uint32) {
			link := filepath.Join(e.dir, "link")
			if err := os.Symlink(e.path, link); err != nil {
				t.Fatal(err)
			}
			return link, e.digest, euid
		}, "link is a symbolic link"},
		{"a symbolic link among its directories", func(t *testing.T, e executable) (string, string, uint32) {
			link := filepath.Join(filepath.Dir(e.dir), "linked")
			if err := os.Symlink(e.dir, link); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(e.dir, "sub"), 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(link, "sub", "driver")
			return path, script(t, filepath.Join(e.dir, "sub", "driver"), "#!/bin/sh\n"), euid
		}, "linked is a symbolic link"},
		{"a directory", func(t *testing.T, e executable) (string, string, uint32) {
			return e.dir, e.digest, euid
		}, "is not a regular file"},
		{"a FIFO", func(t *testing.T, e executable) (string, string, uint32) {
			fifo := filepath.Join(e.dir, "fifo")
			if err := syscall.Mkfifo(fifo, 0o700); err != nil {
				t.Fatal(err)
			}
			return fifo, e.digest, euid
		}, "is not a regular file"},
		{"a missing file", func(t *testing.T, e executable) (string, string, uint32) {
			return filepath.Join(e.dir, "missing"), e.digest, euid
		}, "cannot be opened"},
		{"a relative path", func(t *testing.T, e executable) (string, string, uint32) {
			return "bin/driver", e.digest, euid
		}, "is not an absolute clean path"},
		{"an unclean path", func(t *testing.T, e executable) (string, string, uint32) {
			return e.dir + "/./driver", e.digest, euid
		}, "is not an absolute clean path"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newExecutable(t)
			path, digest, owner := c.arrange(t, e)
			f, err := checkExecutable(path, digest, owner)
			if err == nil {
				f.Close()
				t.Fatal("the launch checks passed")
			}
			want := strings.ReplaceAll(c.want, "%digest%", e.digest)
			if !errors.Is(err, ErrLaunchCheck) || !strings.Contains(err.Error(), want) {
				t.Fatalf("the launch checks refused with %v, want %q", err, want)
			}
		})
	}
}

func TestLaunchChecksPassAndReturnTheFileChecked(t *testing.T) {
	e := newExecutable(t)
	f, err := checkExecutable(e.path, e.digest, uint32(os.Geteuid()))
	if err != nil {
		t.Fatalf("the launch checks refused a trusted executable: %v", err)
	}
	defer f.Close()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != e.digest {
		t.Fatalf("the returned file holds other bytes than the executable checked")
	}
}

// Root's read-only files pass, with any number of links: the Nix store
// hard-links identical files.
func TestLaunchChecksPassARootOwnedReadOnlyFile(t *testing.T) {
	path, err := exec.LookPath("true")
	if err == nil {
		path, err = filepath.EvalSymlinks(path)
	}
	var st syscall.Stat_t
	if err != nil || syscall.Stat(path, &st) != nil || st.Uid != 0 || st.Mode&0o222 != 0 {
		t.Skip("no root-owned read-only executable to check")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	f, err := checkExecutable(path, hex.EncodeToString(sum[:]), uint32(os.Geteuid()))
	if err != nil {
		t.Fatalf("the launch checks refused %s (%d links): %v", path, st.Nlink, err)
	}
	f.Close()
}
