package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// killAttachmentHelper selects only an immediate child of the live CLI with
// the trusted executable and exact helper mode. Linux records children against
// the thread which forked them; Go need not fork on the thread-group leader.
func killAttachmentHelper(parent *os.Process, executable os.FileInfo) (bool, error) {
	if err := parent.Signal(syscall.Signal(0)); err != nil {
		return false, fmt.Errorf("attachment client is no longer live: %w", err)
	}
	paths, err := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", parent.Pid))
	if err != nil {
		return false, err
	}
	children := make(map[int]bool)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) { // A Go runtime thread may exit.
			continue
		}
		if err != nil {
			return false, fmt.Errorf("attachment child discovery: %w", err)
		}
		for _, field := range strings.Fields(string(data)) {
			pid, err := strconv.Atoi(field)
			if err != nil || pid <= 0 {
				return false, errors.New("invalid attachment child PID")
			}
			children[pid] = true
		}
	}
	selected := -1
	defer func() {
		if selected >= 0 {
			unix.Close(selected)
		}
	}()
	for pid := range children {
		// Pin before inspecting /proc: if the child exits and its PID is reused
		// during inspection, this fd can never signal the replacement process.
		// The pinned VM supports pidfds; never fall back to a numeric PID kill.
		fd, err := unix.PidfdOpen(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("pin attachment child: %w", err)
		}
		matches, err := attachmentHelperMatches(pid, parent.Pid, executable)
		if err != nil || !matches {
			unix.Close(fd)
			if err != nil {
				return false, err
			}
			continue
		}
		if selected >= 0 {
			unix.Close(fd)
			return false, errors.New("multiple trusted attachment helper children")
		}
		selected = fd
	}
	if selected < 0 {
		return false, nil
	}
	// os/exec retains the original process handle. Refuse a stale client even
	// if its numeric PID was recycled while we were enumerating tasks.
	if err := parent.Signal(syscall.Signal(0)); err != nil {
		return false, fmt.Errorf("attachment client exited during discovery: %w", err)
	}
	if err := unix.PidfdSendSignal(selected, unix.SIGKILL, nil, 0); err != nil {
		return false, fmt.Errorf("kill pinned attachment helper: %w", err)
	}
	return true, nil
}

func attachmentHelperMatches(pid, parentPID int, executable os.FileInfo) (bool, error) {
	prefix := "/proc/" + strconv.Itoa(pid)
	peer, err := os.Stat(prefix + "/exe")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("attachment child executable: %w", err)
	}
	if !os.SameFile(executable, peer) {
		return false, nil
	}
	raw, err := os.ReadFile(prefix + "/cmdline")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("attachment child mode: %w", err)
	}
	args := bytes.Split(bytes.TrimSuffix(raw, []byte{0}), []byte{0})
	if len(args) != 2 || string(args[1]) != "attach-helper" {
		return false, nil
	}
	stat, err := os.ReadFile(prefix + "/stat")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("attachment child parent: %w", err)
	}
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 {
		return false, errors.New("invalid attachment child stat")
	}
	fields := strings.Fields(string(stat[end+1:]))
	return len(fields) >= 2 && fields[1] == strconv.Itoa(parentPID), nil
}
