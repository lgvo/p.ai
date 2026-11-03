package runtimeincus

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"testing"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// workspaceTarget creates the test session's runtime from an image holding
// files besides its root and workspace, and returns the stopped runtime's
// target.
func workspaceTarget(t *testing.T, w *incusWorld, files map[string]guestFile) runtimedriver.Target {
	t.Helper()
	image := w.images[0].files
	image[guestWorkspace] = guestFile{typ: "directory", uid: 1000, gid: 1000, mode: 0755}
	maps.Copy(image, files)
	s, at := createdRuntime(t, w)
	return runtimedriver.Target{Session: s, At: at}
}

// instanceFiles is a copy of the runtime's files.
func instanceFiles(w *incusWorld) map[string]guestFile {
	w.mu.Lock()
	defer w.mu.Unlock()
	return maps.Clone(w.instance(testRuntime).files)
}

func TestWorkspaceWriteRefusesBeforeItsGateWhatIncusWouldNotWriteExactly(t *testing.T) {
	regular := func(mode uint32) runtimedriver.File {
		return runtimedriver.File{Type: runtimedriver.FileRegular, UID: 1000, GID: 1000, Mode: mode, Data: []byte("written\n")}
	}
	for _, c := range []struct {
		name, path string
		file       runtimedriver.File
	}{
		{"mode bits beyond the permissions", "tool", regular(0o4755)},
		{"a missing parent", "missing/file", regular(0o644)},
		{"a file over a directory", "dir", regular(0o644)},
		{"another mode for an existing directory", "dir", runtimedriver.File{Type: runtimedriver.FileDirectory, UID: 1000, GID: 1000, Mode: 0o700}},
		{"a file over a symlink", "link", regular(0o644)},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newIncusWorld(t)
			target := workspaceTarget(t, w, map[string]guestFile{
				"/workspace/dir":  {typ: "directory", uid: 1000, gid: 1000, mode: 0755},
				"/workspace/link": {typ: "symlink", uid: 1000, gid: 1000, mode: 0777, data: []byte("dir")},
			})
			before := instanceFiles(w)
			err := w.driver().WriteWorkspace(context.Background(), target, c.path, c.file, func() error { t.Error("gate called"); return nil })
			if err == nil || !reflect.DeepEqual(instanceFiles(w), before) {
				t.Fatalf("WriteWorkspace(%s) = %v, files changed: %t", c.path, err, !reflect.DeepEqual(instanceFiles(w), before))
			}
		})
	}
}

func TestWorkspaceWriteThatIsNotInPlaceAfterwardsIsAnUnknownOutcome(t *testing.T) {
	w := newIncusWorld(t)
	target := workspaceTarget(t, w, nil)
	w.write = func(*guestFile) (land, hang bool) { return false, false }
	gates := 0
	file := runtimedriver.File{Type: runtimedriver.FileRegular, UID: 1000, GID: 1000, Mode: 0o644, Data: []byte("lost\n")}
	err := w.driver().WriteWorkspace(context.Background(), target, "file", file, func() error { gates++; return nil })
	if !errors.Is(err, runtimedriver.ErrUnknownOutcome) || gates != 1 {
		t.Fatalf("WriteWorkspace = %v after %d gate calls, want an unknown outcome after 1", err, gates)
	}
}

func TestWorkspaceRemoveRefusesANonEmptyDirectoryBeforeItsGate(t *testing.T) {
	w := newIncusWorld(t)
	target := workspaceTarget(t, w, map[string]guestFile{
		"/workspace/full":      {typ: "directory", uid: 1000, gid: 1000, mode: 0755},
		"/workspace/full/file": {typ: "file", uid: 1000, gid: 1000, mode: 0644, data: []byte("kept\n")},
		"/workspace/empty":     {typ: "directory", uid: 1000, gid: 1000, mode: 0755},
	})
	d := w.driver()
	if err := d.RemoveWorkspace(context.Background(), target, "full", func() error { t.Error("gate called"); return nil }); err == nil {
		t.Fatal("removed a directory that holds a file")
	}
	if _, kept := instanceFiles(w)["/workspace/full/file"]; !kept {
		t.Fatal("the directory's file is gone")
	}
	gates := 0
	if err := d.RemoveWorkspace(context.Background(), target, "empty", func() error { gates++; return nil }); err != nil || gates != 1 {
		t.Fatalf("RemoveWorkspace of an empty directory = %v after %d gate calls", err, gates)
	}
	if _, kept := instanceFiles(w)["/workspace/empty"]; kept {
		t.Fatal("the empty directory remains")
	}
}

func TestWorkspaceReadReportsTheExactTypeAndFullMode(t *testing.T) {
	w := newIncusWorld(t)
	target := workspaceTarget(t, w, map[string]guestFile{
		"/workspace/fifo": {typ: "other", uid: 1000, gid: 100, mode: 0640},
		"/workspace/tool": {typ: "file", uid: 0, gid: 1000, mode: 04750, data: []byte("#!/bin/sh\n")},
	})
	for p, want := range map[string]runtimedriver.File{
		"fifo": {Type: runtimedriver.FileOther, UID: 1000, GID: 100, Mode: 0o640},
		"tool": {Type: runtimedriver.FileRegular, UID: 0, GID: 1000, Mode: 0o4750, Data: []byte("#!/bin/sh\n")},
	} {
		if got, err := w.driver().ReadWorkspace(context.Background(), target, p, 1<<10); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("ReadWorkspace(%s) = %+v, %v; want %+v", p, got, err, want)
		}
	}
}

func TestWorkspaceOperationsRefuseAWorkspaceThatIsASymlink(t *testing.T) {
	w := newIncusWorld(t)
	target := workspaceTarget(t, w, map[string]guestFile{
		"/home":                {typ: "directory", uid: 0, gid: 0, mode: 0755},
		"/home/elsewhere":      {typ: "directory", uid: 1000, gid: 1000, mode: 0755},
		"/home/elsewhere/file": {typ: "file", uid: 1000, gid: 1000, mode: 0644, data: []byte("not the workspace\n")},
	})
	w.mu.Lock()
	w.instance(testRuntime).files[guestWorkspace] = guestFile{typ: "symlink", uid: 1000, gid: 1000, mode: 0777, data: []byte("/home/elsewhere")}
	w.mu.Unlock()
	for _, p := range []string{".", "file"} {
		if got, err := w.driver().ReadWorkspace(context.Background(), target, p, 1<<10); err == nil {
			t.Fatalf("ReadWorkspace(%s) through a symlinked workspace = %+v", p, got)
		}
	}
}

func TestWorkspaceOperationsReachOnlyTheirSessionsRuntime(t *testing.T) {
	w := newIncusWorld(t)
	target := workspaceTarget(t, w, nil)
	other := testSessionSpec(t)
	other.Ref.ID = "11111111-1111-4111-8111-111111111111"
	other.Endpoints = "/var/lib/p-vm/endpoints/" + other.Ref.ID
	created, err := w.driver().Create(context.Background(), other, "", func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	d := w.driver()
	for i, at := range []runtimedriver.Target{target, {Session: other, At: created.Incarnation}} {
		file := runtimedriver.File{Type: runtimedriver.FileRegular, UID: 1000, GID: 1000, Mode: 0o644, Data: fmt.Appendf(nil, "session %d\n", i)}
		if err := d.WriteWorkspace(context.Background(), at, "file", file, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	for i, at := range []runtimedriver.Target{target, {Session: other, At: created.Incarnation}} {
		if got, err := d.ReadWorkspace(context.Background(), at, "file", 1<<10); err != nil || string(got.Data) != fmt.Sprintf("session %d\n", i) {
			t.Fatalf("session %d reads %q, %v", i, got.Data, err)
		}
	}
}
