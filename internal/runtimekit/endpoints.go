package runtimekit

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

func privateKey(path string, uid uint32) error {
	if err := regularOwned(path, uid); err != nil {
		return err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if st.Mode().Perm() != 0400 {
		return fmt.Errorf("%s: private key requires mode 0400", path)
	}
	return nil
}

func validateEndpoints() (err error) {
	defer func() { err = endpointFailure(err) }()
	mounts, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	defer mounts.Close()
	if err := readonlyEndpointMount(mounts); err != nil {
		return err
	}
	return endpointSockets(endpointRuntimePath)
}

const endpointStagingPath = "/opt/p/endpoints"
const endpointRuntimePath = "/run/p"

var errEndpointMountMissing = errors.New("endpoint mount is missing")

// PrepareEndpoints binds the validated Incus mount into systemd's /run after
// NixOS has mounted that tmpfs. Incus cannot attach directly below /run because
// image activation would hide its mount. Both paths expose the same two sockets.
func PrepareEndpoints() (err error) {
	defer func() { err = endpointFailure(err) }()
	if os.Geteuid() != 0 {
		return errors.New("endpoint preparation requires container root")
	}
	for _, path := range []string{"/run", "/opt", "/opt/p"} {
		if err := endpointParent(path); err != nil {
			return err
		}
	}
	return prepareEndpointBind(
		func() (io.ReadCloser, error) { return os.Open("/proc/self/mountinfo") },
		endpointSockets,
		func() error { return endpointTarget(endpointRuntimePath) },
		func() error { return sameEndpointDirectory(endpointStagingPath, endpointRuntimePath) },
		func(source, target string, flags uintptr) error { return syscall.Mount(source, target, "", flags, "") },
	)
}

func endpointParent(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect endpoint parent %s: %w", path, err)
	}
	if !st.IsDir() || owner(st) != 0 || st.Mode().Perm()&022 != 0 {
		return fmt.Errorf("%s must be a root-owned non-writable directory (%s)", path, endpointStat(st))
	}
	return nil
}

func endpointTarget(path string) error {
	if err := os.Mkdir(path, 0755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("create endpoint target: %w", err)
	}
	if err := endpointParent(path); err != nil {
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("read endpoint target: %w", err)
	}
	if len(entries) != 0 {
		return errors.New("unmounted endpoint target must be empty")
	}
	return nil
}

func sameEndpointDirectory(staging, target string) error {
	source, err := os.Lstat(staging)
	if err != nil {
		return fmt.Errorf("inspect endpoint staging identity: %w", err)
	}
	dest, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("inspect endpoint target identity: %w", err)
	}
	if !source.IsDir() || !dest.IsDir() || !os.SameFile(source, dest) {
		return errors.New("endpoint target does not match staging directory")
	}
	return nil
}

func prepareEndpointBind(
	openMounts func() (io.ReadCloser, error),
	checkSockets func(string) error,
	prepareTarget func() error,
	checkIdentity func() error,
	mount func(source, target string, flags uintptr) error,
) error {
	checkMount := func(path string, requirePrivate bool) error {
		mounts, err := openMounts()
		if err != nil {
			return err
		}
		defer mounts.Close()
		return endpointMountAt(mounts, path, requirePrivate)
	}
	if err := checkMount(endpointStagingPath, false); err != nil {
		return fmt.Errorf("endpoint staging precondition: %w", err)
	}
	if err := checkSockets(endpointStagingPath); err != nil {
		return fmt.Errorf("endpoint staging precondition: %w", err)
	}
	finalErr := checkMount(endpointRuntimePath, true)
	if finalErr == nil {
		if err := checkIdentity(); err != nil {
			return err
		}
		if err := checkSockets(endpointRuntimePath); err != nil {
			return err
		}
	} else if errors.Is(finalErr, errEndpointMountMissing) {
		if err := prepareTarget(); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("endpoint target precondition: %w", finalErr)
	}
	if err := mount("", endpointStagingPath, syscall.MS_PRIVATE|syscall.MS_REC); err != nil {
		return fmt.Errorf("make endpoint staging private: %w", err)
	}
	if err := checkMount(endpointStagingPath, true); err != nil {
		return fmt.Errorf("endpoint staging after preparation: %w", err)
	}
	if finalErr != nil {
		// MS_BIND clones the source's per-mount flags, including read-only and
		// any locked security flags. Remounting could inadvertently clear them.
		if err := mount(endpointStagingPath, endpointRuntimePath, syscall.MS_BIND); err != nil {
			return fmt.Errorf("bind endpoint runtime target: %w", err)
		}
		if err := mount("", endpointRuntimePath, syscall.MS_PRIVATE|syscall.MS_REC); err != nil {
			return fmt.Errorf("make endpoint runtime target private: %w", err)
		}
	}
	for _, path := range []string{endpointStagingPath, endpointRuntimePath} {
		if err := checkMount(path, true); err != nil {
			return fmt.Errorf("endpoint mount after preparation: %w", err)
		}
		if err := checkSockets(path); err != nil {
			return fmt.Errorf("endpoint sockets after preparation: %w", err)
		}
	}
	return checkIdentity()
}

// The unshifted host bind has an unmapped owner in the container. Its exact
// read-only mount and socket-only contents establish the runtime boundary;
// requiring container-root ownership would require a forbidden ID-map change.
func readonlyEndpointMount(mounts io.Reader) error {
	return endpointMountAt(mounts, endpointRuntimePath, true)
}

func endpointMountAt(mounts io.Reader, path string, requirePrivate bool) error {
	scanner := bufio.NewScanner(mounts)
	found := false
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 7 {
			return errors.New("invalid mount information")
		}
		if strings.HasPrefix(fields[4], path+"/") {
			return errors.New("endpoint mount contains a nested mount")
		}
		if fields[4] != path {
			continue
		}
		separator := slices.Index(fields[6:], "-") + 6
		if separator < 6 || separator+3 >= len(fields) {
			return errors.New("invalid endpoint mount information")
		}
		if found || !slices.Contains(strings.Split(fields[5], ","), "ro") {
			return fmt.Errorf("endpoints require one read-only mount at %s", path)
		}
		if requirePrivate {
			for _, field := range fields[6:separator] {
				if strings.HasPrefix(field, "shared:") || strings.HasPrefix(field, "master:") || strings.HasPrefix(field, "propagate_from:") || field == "unbindable" {
					return errors.New("endpoint mount must have private propagation")
				}
			}
		}
		found = true
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w at %s", errEndpointMountMissing, path)
	}
	return nil
}

func endpointSockets(path string) error {
	dir, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect endpoint directory: %w", err)
	}
	if !dir.IsDir() {
		return fmt.Errorf("endpoint path must be a directory (%s)", endpointStat(dir))
	}
	if dir.Mode().Perm() != 0755 {
		return fmt.Errorf("endpoint directory requires mode 0755 (%s)", endpointStat(dir))
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("read endpoint directory: %w", err)
	}
	if len(entries) != 2 {
		return errors.New("endpoint directory requires exactly two fixed sockets")
	}
	for _, entry := range entries {
		if entry.Name() != "session.sock" && entry.Name() != "git.sock" {
			return errors.New("unexpected entry in endpoint directory")
		}
		st, err := os.Lstat(filepath.Join(path, entry.Name()))
		if err != nil {
			return fmt.Errorf("inspect endpoint socket %s: %w", entry.Name(), err)
		}
		if st.Mode()&os.ModeSocket == 0 || st.Mode().Perm() != 0666 || owner(st) != owner(dir) {
			return fmt.Errorf("endpoint socket %s has invalid type, mode, or owner (%s; directory uid=%d)", entry.Name(), endpointStat(st), owner(dir))
		}
	}
	return nil
}

func endpointStat(st os.FileInfo) string {
	stat := st.Sys().(*syscall.Stat_t)
	return fmt.Sprintf("type=%s mode=%04o uid=%d gid=%d", st.Mode().Type(), st.Mode().Perm(), stat.Uid, stat.Gid)
}

// Only fixed endpoint metadata is diagnostic: never read socket data, list
// unexpected names, or include mount source paths and filesystem options.
func endpointFailure(err error) error {
	if err == nil {
		return nil
	}
	var errno syscall.Errno
	errors.As(err, &errno)
	summary := "unavailable"
	if mounts, openErr := os.Open("/proc/self/mountinfo"); openErr == nil {
		summary = endpointMountSummary(mounts)
		mounts.Close()
	}
	return fmt.Errorf("%w (euid=%d egid=%d errno=%d; endpoint mountinfo: %s)", err, os.Geteuid(), os.Getegid(), errno, summary)
}

func endpointMountSummary(mounts io.Reader) string {
	scanner := bufio.NewScanner(io.LimitReader(mounts, 1<<20))
	var summary []string
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 7 || (fields[4] != endpointRuntimePath && fields[4] != endpointStagingPath) {
			continue
		}
		separator := slices.Index(fields[6:], "-") + 6
		if separator < 6 || separator+1 >= len(fields) {
			return "invalid"
		}
		// Omit fields 3 (host root), separator+2 (source), and super options.
		summary = append(summary, fmt.Sprintf("id=%s parent=%s device=%s target=%s flags=%s propagation=%s filesystem=%s",
			fields[0], fields[1], fields[2], fields[4], fields[5], strings.Join(fields[6:separator], ","), fields[separator+1]))
		if len(summary) == 2 {
			break
		}
	}
	if scanner.Err() != nil {
		return "unavailable"
	}
	if len(summary) == 0 {
		return "missing"
	}
	return short([]byte(strings.Join(summary, "; ")))
}
