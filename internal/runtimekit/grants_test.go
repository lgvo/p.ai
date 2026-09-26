package runtimekit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func TestFilesystemGrantEffectiveMounts(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("read"), 0600); err != nil {
		t.Fatal(err)
	}
	infoDir, _ := os.Lstat(dir)
	infoFile, _ := os.Lstat(file)
	statDir := infoDir.Sys().(*syscall.Stat_t)
	statFile := infoFile.Sys().(*syscall.Stat_t)
	grants := []FilesystemGrant{
		{Name: "data", Type: "directory", Access: "read-only", Device: uint64(statDir.Dev), Inode: statDir.Ino},
		{Name: "scratch", Type: "file", Access: "read-write", Executable: true, Device: uint64(statFile.Dev), Inode: statFile.Ino},
	}
	lstat := func(path string) (os.FileInfo, error) {
		switch path {
		case "/mnt/p/data":
			return infoDir, nil
		case "/mnt/p/scratch":
			return infoFile, nil
		}
		return nil, os.ErrNotExist
	}
	line := func(path, flags, extras string) string {
		return fmt.Sprintf("42 1 0:1 / %s %s %s - ext4 /dev/root rw\n", path, flags, extras)
	}
	valid := line("/mnt/p/data", "ro,nosuid,nodev,noexec", "") + line("/mnt/p/scratch", "rw,nosuid,nodev", "")
	for name, mountinfo := range map[string]string{
		"valid":                   valid,
		"readonly lost noexec":    strings.Replace(valid, "ro,nosuid,nodev,noexec", "ro,nosuid,nodev", 1),
		"readonly writable":       strings.Replace(valid, "ro,nosuid,nodev,noexec", "rw,nosuid,nodev,noexec", 1),
		"nested mount":            valid + line("/mnt/p/data/secret", "ro,nosuid,nodev,noexec", ""),
		"duplicate":               valid + line("/mnt/p/data", "ro,nosuid,nodev,noexec", ""),
		"missing":                 line("/mnt/p/data", "ro,nosuid,nodev,noexec", ""),
		"oversized hidden nested": valid + strings.Repeat(line("/other", "rw", ""), 6000) + line("/mnt/p/data/hidden", "ro,nosuid,nodev,noexec", ""),
		"malformed escape":        strings.Replace(valid, "/mnt/p/data", `/mnt/p/da\9ta`, 1),
		"shared":                  strings.Replace(valid, "noexec  -", "noexec shared:5 -", 1),
	} {
		t.Run(name, func(t *testing.T) {
			err := validateFilesystemMountsFrom(grants, strings.NewReader(mountinfo), lstat)
			if (err == nil) != (name == "valid") {
				t.Fatalf("mount accepted=%v: %v", err == nil, err)
			}
		})
	}
	changed := append([]FilesystemGrant(nil), grants...)
	changed[0].Inode++
	if err := validateFilesystemMountsFrom(changed, strings.NewReader(valid), lstat); err == nil {
		t.Fatal("changed source inode accepted")
	}
}

func TestGrantSessionConfigPinsSchema(t *testing.T) {
	good := `{"schema":"p.runtime-session/v4","activation":"base","command":["/bin/sh"],"filesystem_mounts":[{"name":"data","type":"directory","access":"read-only","executable":false,"device":1,"inode":2}]}`
	if _, err := ParseConfig([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(good, "p.runtime-session/v4", "p.runtime-session/v3", 1),
		strings.Replace(good, `"inode":2`, `"inode":0`, 1),
		strings.Replace(good, `"name":"data"`, `"name":"../data"`, 1),
		strings.Replace(good, `"access":"read-only"`, `"access":"writable"`, 1),
	} {
		if _, err := ParseConfig([]byte(bad)); err == nil {
			t.Fatalf("accepted invalid grant config %s", bad)
		}
	}
}

func TestPrepareFilesystemMountsKeepsClosedOrderAndFlags(t *testing.T) {
	grants := []FilesystemGrant{{Name: "data", Type: "directory", Access: "read-only", Inode: 1},
		{Name: "scratch", Type: "directory", Access: "read-write", Executable: true, Inode: 2}}
	var steps []string
	inspect := func(final bool) error {
		if final {
			steps = append(steps, "verify")
		} else {
			steps = append(steps, "preflight")
		}
		return nil
	}
	mount := func(path string, flags uintptr) error {
		steps = append(steps, fmt.Sprintf("%s:%d", path, flags))
		return nil
	}
	if err := prepareFilesystemMountsWith(grants, inspect, mount); err != nil {
		t.Fatal(err)
	}
	ro := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_RDONLY | syscall.MS_NOEXEC)
	rw := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_NOSUID | syscall.MS_NODEV)
	want := []string{"preflight", fmt.Sprintf("/mnt/p/data:%d", syscall.MS_PRIVATE), fmt.Sprintf("/mnt/p/data:%d", ro),
		fmt.Sprintf("/mnt/p/scratch:%d", syscall.MS_PRIVATE), fmt.Sprintf("/mnt/p/scratch:%d", rw), "verify"}
	if !slices.Equal(steps, want) {
		t.Fatalf("mount sequence: %v, want %v", steps, want)
	}
	steps = nil
	if err := prepareFilesystemMountsWith(grants, func(bool) error { return errors.New("unsafe") }, mount); err == nil || len(steps) != 0 {
		t.Fatalf("preflight failure mutated: %v %v", err, steps)
	}
}
