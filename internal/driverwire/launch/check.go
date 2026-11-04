package launch

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// ErrLaunchCheck is the error of a driver executable that failed its launch
// checks: the daemon does not start it. A refusal names the driver first, as
// in "the runtime driver failed its launch checks: ...".
var ErrLaunchCheck = errors.New("failed its launch checks")

// fileStat is what the launch checks read of a file: its type and
// permission bits, owner and link count, and its identity.
type fileStat struct {
	mode     uint32 // st_mode
	uid      uint32
	nlink    uint64
	dev, ino uint64
}

func statOf(st *syscall.Stat_t) fileStat {
	return fileStat{mode: st.Mode, uid: st.Uid, nlink: uint64(st.Nlink), dev: uint64(st.Dev), ino: st.Ino}
}

func (s fileStat) fileType() uint32 { return s.mode & syscall.S_IFMT }
func (s fileStat) perm() uint32     { return s.mode & 0o777 }

// trustedOwner is root or account, the account P runs as.
func trustedOwner(uid, account uint32) bool { return uid == 0 || uid == account }

// directoryRefusal says why a directory on the executable's path is not
// trusted: it must be a directory, not a symbolic link, owned by root or
// account and writable by neither group nor others, unless root owns it and
// it is sticky. It is empty for a trusted directory.
func directoryRefusal(s fileStat, account uint32) string {
	switch {
	case s.fileType() == syscall.S_IFLNK:
		return "is a symbolic link"
	case s.fileType() != syscall.S_IFDIR:
		return "is not a directory"
	case !trustedOwner(s.uid, account):
		return "is owned by another account"
	case s.perm()&0o022 != 0 && !(s.uid == 0 && s.mode&syscall.S_ISVTX != 0):
		return "is group- or world-writable"
	}
	return ""
}

// fileRefusal says why a file is not trusted: it must be a regular file
// owned by root or account, writable by neither group nor others, with one
// link unless root owns it and no one may write it, so no writable alias
// shares its bytes. It is empty for a trusted file.
func fileRefusal(s fileStat, account uint32) string {
	switch {
	case s.fileType() == syscall.S_IFLNK:
		return "is a symbolic link"
	case s.fileType() != syscall.S_IFREG:
		return "is not a regular file"
	case !trustedOwner(s.uid, account):
		return "is owned by another account"
	case s.perm()&0o022 != 0:
		return "is group- or world-writable"
	case s.nlink != 1 && !(s.uid == 0 && s.perm()&0o222 == 0):
		return "has another link while it can be written"
	}
	return ""
}

// executableRefusal says why the executable is not trusted: it must be a
// trusted file with its owner's execute bit. It is empty for a trusted
// executable.
func executableRefusal(s fileStat, account uint32) string {
	if reason := fileRefusal(s, account); reason != "" {
		return reason
	}
	if s.perm()&0o100 == 0 {
		return "lacks its owner's execute bit"
	}
	return ""
}

// validDigest reports whether digest is a SHA-256 in lowercase hex.
func validDigest(digest string) bool {
	if len(digest) != 2*sha256.Size {
		return false
	}
	for _, c := range digest {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// Refusal is the error of a launch check that refused path for reason, as
// in "failed its launch checks: /path is group- or world-writable".
func Refusal(path, reason string) error {
	return fmt.Errorf("%w: %s %s", ErrLaunchCheck, path, reason)
}

func refused(path, reason string) error { return Refusal(path, reason) }

// checkExecutable applies the launch checks to the driver executable at
// path for account and returns it open: the file it checked and hashed,
// which is the one to start. It opens the executable's directory as
// OpenDirectory does, opens the executable from that directory without
// following a symbolic link, checks the open file, and only then hashes it
// against digest.
func checkExecutable(path, digest string, account uint32) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, refused(path, "is not an absolute clean path")
	}
	if !validDigest(digest) {
		return nil, fmt.Errorf("%w: the pinned SHA-256 of %s is not 64 lowercase hex digits", ErrLaunchCheck, path)
	}
	dir, err := OpenDirectory(filepath.Dir(path), account)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	fd, err := syscall.Openat(int(dir.Fd()), filepath.Base(path), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, refused(path, "is a symbolic link")
	}
	if err != nil {
		return nil, refused(path, "cannot be opened: "+err.Error())
	}
	f := os.NewFile(uintptr(fd), path)
	if err := CheckFile(f, path, account, true); err != nil {
		f.Close()
		return nil, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		f.Close()
		return nil, refused(path, "cannot be hashed: "+err.Error())
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != digest {
		f.Close()
		return nil, refused(path, "has SHA-256 "+got+", not the pinned "+digest)
	}
	return f, nil
}

// OpenDirectory applies the launch checks to the directory dir and its
// ancestors for account and returns dir open: it opens dir first, checks it
// and every ancestor by path, and confirms that the opened directory is the
// one checked. Each must be a directory, not a symbolic link, owned by root
// or account and writable by neither group nor others, unless root owns it
// and it is sticky.
func OpenDirectory(dir string, account uint32) (*os.File, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, refused(dir, "is not an absolute clean path")
	}
	dirfd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, refused(dir, "is a symbolic link")
	}
	if err != nil {
		return nil, refused(dir, "cannot be opened: "+err.Error())
	}
	f := os.NewFile(uintptr(dirfd), dir)
	var opened syscall.Stat_t
	if err := syscall.Fstat(dirfd, &opened); err != nil {
		f.Close()
		return nil, refused(dir, "cannot be read: "+err.Error())
	}
	named, err := checkDirectories(dir, account)
	if err != nil {
		f.Close()
		return nil, err
	}
	if named.dev != uint64(opened.Dev) || named.ino != opened.Ino {
		f.Close()
		return nil, refused(dir, "changed while it was checked")
	}
	return f, nil
}

// checkDirectories checks dir and each of its ancestors and returns what it
// read of dir.
func checkDirectories(dir string, account uint32) (fileStat, error) {
	var first fileStat
	for d := dir; ; d = filepath.Dir(d) {
		var st syscall.Stat_t
		if err := syscall.Lstat(d, &st); err != nil {
			return fileStat{}, refused(d, "cannot be read: "+err.Error())
		}
		s := statOf(&st)
		if d == dir {
			first = s
		}
		if reason := directoryRefusal(s, account); reason != "" {
			return fileStat{}, refused(d, reason)
		}
		if d == "/" {
			return first, nil
		}
	}
}

// CheckFile applies the launch checks for account to f, a file opened from
// a directory OpenDirectory checked, which path names in a refusal: a
// regular file owned by root or account, writable by neither group nor
// others, with one link unless root owns it and no one may write it, so no
// writable alias shares its bytes. An executable also needs its owner's
// execute bit.
func CheckFile(f *os.File, path string, account uint32, executable bool) error {
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return refused(path, "cannot be read: "+err.Error())
	}
	reason := fileRefusal(statOf(&st), account)
	if executable {
		reason = executableRefusal(statOf(&st), account)
	}
	if reason != "" {
		return refused(path, reason)
	}
	return nil
}
