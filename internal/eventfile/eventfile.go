// Package eventfile appends P's events to a private host file, with one
// bounded prior segment: the bundled file-log event handler, p-event-file,
// and the example filter write with it.
package eventfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/lgvo/p.ai/internal/strictjson"
)

// Config is the file-log's config in trusted host configuration: the file
// it appends to, and the size at which it rotates.
type Config struct {
	Path     string `json:"path"`
	MaxBytes int64  `json:"max_bytes"`
}

// ParseConfig reads a file-log config strictly: an absolute clean path and
// a rotation size from 1 KiB to 100 MiB.
func ParseConfig(data []byte) (Config, error) {
	var config Config
	if err := strictjson.Decode(data, &config); err != nil {
		return config, fmt.Errorf("file-log config: %w", err)
	}
	if !filepath.IsAbs(config.Path) || filepath.Clean(config.Path) != config.Path {
		return config, errors.New("file-log path must be absolute and clean")
	}
	if config.MaxBytes < 1024 || config.MaxBytes > 100<<20 {
		return config, errors.New("file-log max_bytes must be between 1024 and 104857600")
	}
	return config, nil
}

var logMu sync.Mutex

// Append appends line to the file config names. The file's directory must
// be private and owned by this process's user, reached through no symbolic
// link and below no writable ancestor but a root-owned sticky one; the file
// must be private, owned and singly linked. A file that line would take
// past MaxBytes is first rotated to one prior segment, PATH.1.
func Append(config Config, line []byte) error {
	logMu.Lock()
	defer logMu.Unlock()
	if int64(len(line)) > config.MaxBytes {
		return errors.New("event exceeds file limit")
	}
	dirFD, name, err := openPrivateParent(config.Path)
	if err != nil {
		return err
	}
	defer syscall.Close(dirFD)
	f, err := noFollowOpenAt(dirFD, name, syscall.O_WRONLY|syscall.O_APPEND|syscall.O_CREAT, 0600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		f.Close()
		return errors.New("log file must be private, owned, regular, and unlinked elsewhere")
	}
	if info.Size()+int64(len(line)) > config.MaxBytes {
		if err := f.Close(); err != nil {
			return err
		}
		// One bounded prior segment is retained; events are diagnostic, not truth.
		if err := syscall.Renameat(dirFD, name, dirFD, name+".1"); err != nil {
			return err
		}
		f, err = noFollowOpenAt(dirFD, name, syscall.O_WRONLY|syscall.O_APPEND|syscall.O_CREAT|syscall.O_EXCL, 0600)
		if err != nil {
			return err
		}
	}
	_, err = f.Write(line)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func noFollowOpenAt(dirFD int, name string, flags int, mode uint32) (*os.File, error) {
	fd, err := syscall.Openat(dirFD, name, flags|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

// openPrivateParent walks every ancestor by directory descriptor so a
// symlinked component cannot redirect the append to another host location.
func openPrivateParent(path string) (int, string, error) {
	name := filepath.Base(path)
	if name == "." || name == ".." || name == "/" {
		return -1, "", errors.New("invalid log basename")
	}
	dir := filepath.Dir(path)
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", err
	}
	components := strings.Split(strings.TrimPrefix(dir, "/"), "/")
	for _, component := range components {
		if component == "" {
			continue
		}
		next, openErr := syscall.Openat(fd, component, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		syscall.Close(fd)
		if openErr != nil {
			return -1, "", openErr
		}
		fd = next
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err != nil {
			syscall.Close(fd)
			return -1, "", err
		}
		if st.Uid != 0 && st.Uid != uint32(os.Geteuid()) {
			syscall.Close(fd)
			return -1, "", errors.New("untrusted log directory owner")
		}
		if st.Mode&0022 != 0 && !(st.Uid == 0 && st.Mode&syscall.S_ISVTX != 0) {
			syscall.Close(fd)
			return -1, "", errors.New("writable log directory ancestry")
		}
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		syscall.Close(fd)
		return -1, "", err
	}
	if st.Uid != uint32(os.Geteuid()) || st.Mode&0077 != 0 {
		syscall.Close(fd)
		return -1, "", fmt.Errorf("log directory must be private and owned by P (path=%s uid=%d mode=%#o euid=%d)", dir, st.Uid, st.Mode, os.Geteuid())
	}
	return fd, name, nil
}
