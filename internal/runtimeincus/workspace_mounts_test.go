package runtimeincus

import (
	"errors"
	"strings"
	"testing"
)

func TestFrozenProcMetadataKeepsExactPathAndObservedEvidence(t *testing.T) {
	if err := frozenProcMetadata("/proc", guestFile{typ: "directory", uid: 0, mode: 0555}, nil, "directory"); err != nil {
		t.Fatal(err)
	}
	if err := frozenProcMetadata("/proc", guestFile{typ: "directory", uid: 65534, gid: 65534, mode: 0555}, nil, "directory"); err != nil {
		t.Fatalf("kernel overflow owner on exact proc root refused: %v", err)
	}
	for _, bad := range []guestFile{
		{typ: "directory", uid: 65534, gid: 0, mode: 0555},
		{typ: "directory", uid: 65534, gid: 65534, mode: 0755},
		{typ: "symlink", uid: 65534, gid: 65534, mode: 0555},
	} {
		if err := frozenProcMetadata("/proc", bad, nil, "directory"); err == nil {
			t.Fatalf("unsafe proc root accepted: %+v", bad)
		}
	}
	for _, tc := range []struct {
		path  string
		meta  guestFile
		cause error
		part  string
	}{
		{"/proc/1", guestFile{typ: "directory", uid: 1000000, gid: 1000000, mode: 0555}, nil, "uid=1000000"},
		{"/proc/1", guestFile{typ: "directory", uid: 65534, gid: 65534, mode: 0555}, nil, "uid=65534"},
		{"/proc/1/mountinfo", guestFile{typ: "", uid: 0}, errors.New("SFTP permission denied"), "SFTP permission denied"},
	} {
		err := frozenProcMetadata(tc.path, tc.meta, tc.cause, "file")
		if err == nil || !strings.Contains(err.Error(), tc.path) || !strings.Contains(err.Error(), tc.part) {
			t.Fatalf("frozen proc evidence lost: %v", err)
		}
	}
}

func TestWorkspaceMountinfoRejectsNestedAndEscapedMounts(t *testing.T) {
	line := func(mountpoint string) []byte {
		return []byte("40 1 0:1 / /proc rw,nosuid - proc proc rw\n42 1 0:1 / " + mountpoint + " rw,relatime - tmpfs tmpfs rw\n")
	}
	for _, mount := range []string{"/workspace", "/workspace/sub", `/work\163pace`, `/workspace/dir\040name`, `/workspace/dir\011name`} {
		if err := verifyWorkspaceMountinfo(line(mount)); err == nil {
			t.Fatalf("nested mount %q accepted", mount)
		}
	}
	for _, mount := range []string{"/", "/workspace-sibling", "/tmp"} {
		if err := verifyWorkspaceMountinfo(line(mount)); err != nil {
			t.Fatalf("safe mount %q refused: %v", mount, err)
		}
	}
	for _, bad := range [][]byte{nil, []byte("42 1 0:1 / /workspace rw - tmpfs tmpfs rw"), []byte("bad\n"), line(`/work\16`), line(`/work\777space`)} {
		if err := verifyWorkspaceMountinfo(bad); err == nil {
			t.Fatalf("malformed mountinfo accepted: %q", bad)
		}
	}
	for _, bad := range [][]byte{
		[]byte("42 1 0:1 / / rw - tmpfs tmpfs rw\n"),
		[]byte("40 1 0:1 / /proc rw - tmpfs tmpfs rw\n"),
		[]byte("40 1 0:1 / /proc rw - proc proc rw\n41 1 0:1 / /proc/1 rw - tmpfs tmpfs rw\n"),
	} {
		if err := verifyWorkspaceMountinfo(bad); err == nil {
			t.Fatalf("untrusted proc source accepted: %q", bad)
		}
	}
}
