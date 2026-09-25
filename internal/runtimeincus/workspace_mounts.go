package runtimeincus

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// A frozen container's SFTP endpoint joins its mount namespace. A mount below
// /workspace could otherwise make a bounded read include storage outside the
// exact root device. Stopped roots have no live nested mount namespace.
func (a *unixFileAPI) verifyFrozenWorkspaceMounts(ctx context.Context) error {
	return a.verifyFrozenWorkspaceMountsAt(ctx, []string{"/workspace"})
}

func (a *unixFileAPI) verifyFrozenWorkspaceMountsAt(ctx context.Context, roots []string) error {
	const file = frozenMountinfoPath
	for _, name := range []string{"/proc", "/proc/1"} {
		meta, err := a.lstat(ctx, name)
		if err := frozenProcMetadata(name, meta, err, "directory"); err != nil {
			return err
		}
	}
	meta, err := a.lstat(ctx, file)
	if err := frozenProcMetadata(file, meta, err, "file"); err != nil {
		return err
	}
	got, err := a.readFrozenMountinfoSFTP(ctx)
	if err != nil {
		return errors.Join(err, errors.New("frozen mountinfo unreadable"))
	}
	return verifyWorkspaceMountinfoRoots(got, roots)
}

func frozenProcMetadata(name string, meta guestFile, readErr error, wantType string) error {
	if name == "/proc" {
		// proc_root is mode 0555 and globally root-owned in Linux 6.18.
		// Incus forkfile may join the mount namespace without the matching
		// user namespace, so stat reports both unmapped IDs as overflow 65534.
		if readErr == nil && meta.typ == "directory" && meta.mode == 0555 &&
			(meta.uid == 0 && meta.gid == 0 || meta.uid == 65534 && meta.gid == 65534) {
			return nil
		}
	} else if readErr == nil && meta.typ == wantType && meta.uid == 0 {
		return nil
	}
	return errors.Join(readErr, fmt.Errorf("frozen proc path %s metadata type=%s uid=%d gid=%d mode=%#o", name, meta.typ, meta.uid, meta.gid, meta.mode))
}

func verifyWorkspaceMountinfo(raw []byte) error {
	return verifyWorkspaceMountinfoRoots(raw, []string{"/workspace"})
}

func verifyWorkspaceMountinfoRoots(raw []byte, roots []string) error {
	if len(roots) == 0 || len(roots) > 9 {
		return errors.New("workspace mount roots outside bound")
	}
	for _, root := range roots {
		if !supportedWorkspaceTreeRoot(root) {
			return errors.New("workspace mount root outside closed paths")
		}
	}
	if len(raw) == 0 || len(raw) > 256<<10 || raw[len(raw)-1] != '\n' {
		return errors.New("mountinfo size or terminator invalid")
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) == 0 || len(lines) > 4096 {
		return errors.New("mountinfo line count invalid")
	}
	procSeen := false
	for _, line := range lines {
		if line == "" || len(line) > 4096 {
			return errors.New("mountinfo record invalid")
		}
		left, right, ok := strings.Cut(line, " - ")
		if !ok || right == "" {
			return errors.New("mountinfo separator missing")
		}
		fields := strings.Fields(left)
		rightFields := strings.Fields(right)
		if len(fields) < 6 || len(rightFields) < 3 {
			return errors.New("mountinfo fields incomplete")
		}
		for _, field := range fields[:3] {
			if field == "" || strings.ContainsAny(field, "\x00\n\r\\") {
				return errors.New("mountinfo identity malformed")
			}
		}
		mountpoint, err := unescapeMountinfoPath(fields[4])
		if err != nil || mountpoint == "" || mountpoint[0] != '/' || strings.Contains(mountpoint, "//") || strings.Contains(mountpoint, "/../") || strings.HasSuffix(mountpoint, "/..") {
			return errors.New("mountinfo mountpoint malformed")
		}
		for _, root := range roots {
			if mountpoint != "/" && (mountpoint == root || strings.HasPrefix(mountpoint, root+"/") || root != "/workspace" && strings.HasPrefix(root, mountpoint+"/")) {
				return errors.New("workspace contains a nested or ancestor mount")
			}
		}
		if mountpoint == "/proc" {
			if procSeen || rightFields[0] != "proc" {
				return errors.New("proc mount identity ambiguous")
			}
			procSeen = true
		}
		if mountpoint == "/proc/1" || strings.HasPrefix(mountpoint, "/proc/1/") {
			return errors.New("proc mountinfo path overmounted")
		}
	}
	if !procSeen {
		return errors.New("proc mount identity unavailable")
	}
	return nil
}

func unescapeMountinfoPath(raw string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			if raw[i] == 0 || raw[i] == '\r' || raw[i] == '\n' {
				return "", errors.New("mountpoint control byte")
			}
			out.WriteByte(raw[i])
			continue
		}
		if i+3 >= len(raw) {
			return "", errors.New("mountpoint escape truncated")
		}
		var value byte
		for j := 1; j <= 3; j++ {
			if raw[i+j] < '0' || raw[i+j] > '7' {
				return "", errors.New("mountpoint escape malformed")
			}
			value = value*8 + raw[i+j] - '0'
		}
		if value != ' ' && value != '\t' && value != '\n' && value != '\\' && value != 's' {
			return "", errors.New("mountpoint escape unsupported")
		}
		out.WriteByte(value)
		i += 3
	}
	return out.String(), nil
}
