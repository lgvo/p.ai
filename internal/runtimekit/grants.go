package runtimekit

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
)

var guestGrantName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// validateFilesystemMounts observes effective kernel mount flags before the
// interactive command can run. Pinned Incus 7.4 remounts readonly bind devices
// separately, so its config alone cannot prove noexec/nosuid/nodev survived.
func validateFilesystemMounts(grants []FilesystemGrant) error {
	return inspectFilesystemMounts(grants, true)
}

func inspectFilesystemMounts(grants []FilesystemGrant, requireFlags bool) error {
	if len(grants) == 0 {
		return nil
	}
	for _, parent := range []string{"/mnt", "/mnt/p"} {
		st, err := os.Lstat(parent)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || owner(st) != 0 || st.Mode().Perm()&022 != 0 {
			return fmt.Errorf("grant target parent %s is unsafe", parent)
		}
	}
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	defer f.Close()
	return inspectFilesystemMountsFrom(grants, f, os.Lstat, requireFlags)
}

func validateFilesystemMountsFrom(grants []FilesystemGrant, r io.Reader, lstat func(string) (os.FileInfo, error)) error {
	return inspectFilesystemMountsFrom(grants, r, lstat, true)
}

func inspectFilesystemMountsFrom(grants []FilesystemGrant, r io.Reader, lstat func(string) (os.FileInfo, error), requireFlags bool) error {
	if len(grants) == 0 {
		return nil
	}
	wanted := make(map[string]FilesystemGrant, len(grants))
	for _, grant := range grants {
		if !guestGrantName.MatchString(grant.Name) || (grant.Type != "file" && grant.Type != "directory") ||
			(grant.Access != "read-only" && grant.Access != "read-write") || grant.Inode == 0 {
			return errors.New("invalid grant attestation input")
		}
		path := "/mnt/p/" + grant.Name
		if _, exists := wanted[path]; exists {
			return errors.New("duplicate grant attestation input")
		}
		wanted[path] = grant
	}
	seen := map[string]bool{}
	data, err := io.ReadAll(io.LimitReader(r, 256<<10+1))
	if err != nil || len(data) > 256<<10 {
		return errors.New("grant mount inventory exceeds byte bound")
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return errors.New("grant mount inventory lacks terminator")
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 256<<10)
	lines := 0
	for scanner.Scan() {
		lines++
		if lines > 4096 {
			return errors.New("grant mount inventory exceeds line bound")
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 {
			return errors.New("grant mount inventory malformed")
		}
		mountpoint, err := decodeGrantMountpoint(fields[4])
		if err != nil {
			return err
		}
		if !strings.HasPrefix(mountpoint, "/mnt/p/") {
			continue
		}
		grant, ok := wanted[mountpoint]
		if !ok || seen[mountpoint] {
			return errors.New("unexpected or duplicate mount below grant target")
		}
		seen[mountpoint] = true
		sep := slices.Index(fields, "-")
		if sep < 6 || sep+3 >= len(fields) {
			return errors.New("grant mount record malformed")
		}
		if requireFlags {
			for _, opt := range fields[6:sep] {
				if strings.HasPrefix(opt, "shared:") || strings.HasPrefix(opt, "master:") || strings.HasPrefix(opt, "propagate_from:") || opt == "unbindable" {
					return errors.New("grant mount propagation is not private")
				}
			}
		}
		if requireFlags {
			options := strings.Split(fields[5], ",")
			if !slices.Contains(options, "nosuid") || !slices.Contains(options, "nodev") ||
				(grant.Access == "read-only") != slices.Contains(options, "ro") ||
				grant.Executable == slices.Contains(options, "noexec") {
				return errors.New("grant mount flags differ from policy")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if lines == 0 || len(seen) != len(wanted) {
		return errors.New("grant mount inventory incomplete")
	}
	for path, grant := range wanted {
		st, err := lstat(path)
		if err != nil || st.Mode()&os.ModeSymlink != 0 ||
			(grant.Type == "file" && !st.Mode().IsRegular()) ||
			(grant.Type == "directory" && !st.IsDir()) || filepath.Clean(path) != path {
			return fmt.Errorf("grant target %s type changed", path)
		}
		identity, ok := st.Sys().(*syscall.Stat_t)
		if !ok || uint64(identity.Dev) != grant.Device || identity.Ino != grant.Inode {
			return fmt.Errorf("grant target %s source generation changed", path)
		}
	}
	return nil
}

// PrepareFilesystemMounts repairs only the mount flags of exact already-bound
// Incus devices. It never resolves or mounts a host source path. The service
// calls this as container root before workspace initialization or user code.
func PrepareFilesystemMounts() error {
	if os.Geteuid() != 0 {
		return errors.New("filesystem grant preparation requires container root")
	}
	cfg, err := ReadConfig()
	if err != nil {
		return err
	}
	return prepareFilesystemMountsWith(cfg.FilesystemMounts,
		func(requireFlags bool) error { return inspectFilesystemMounts(cfg.FilesystemMounts, requireFlags) },
		func(path string, flags uintptr) error { return syscall.Mount("", path, "", flags, "") })
}

func prepareFilesystemMountsWith(grants []FilesystemGrant, inspect func(bool) error, mount func(string, uintptr) error) error {
	if err := inspect(false); err != nil {
		return err
	}
	for _, grant := range grants {
		path := "/mnt/p/" + grant.Name
		if err := mount(path, syscall.MS_PRIVATE); err != nil {
			return fmt.Errorf("make grant private: %w", err)
		}
		flags := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_NOSUID | syscall.MS_NODEV)
		if grant.Access == "read-only" {
			flags |= syscall.MS_RDONLY
		}
		if !grant.Executable {
			flags |= syscall.MS_NOEXEC
		}
		if err := mount(path, flags); err != nil {
			return fmt.Errorf("confine grant mount: %w", err)
		}
	}
	return inspect(true)
}

func decodeGrantMountpoint(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		if i+3 >= len(s) || s[i+1] < '0' || s[i+1] > '7' || s[i+2] < '0' || s[i+2] > '7' || s[i+3] < '0' || s[i+3] > '7' {
			return "", errors.New("grant mountpoint escape malformed")
		}
		b.WriteByte((s[i+1]-'0')*64 + (s[i+2]-'0')*8 + s[i+3] - '0')
		i += 3
	}
	path := b.String()
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("grant mountpoint path malformed")
	}
	return path, nil
}
