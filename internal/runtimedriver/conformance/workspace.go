package conformance

import (
	"bytes"
	"errors"
	"testing"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// The workspace's guest path, and the guest paths of grants and endpoints,
// as the design places them.
const (
	guestWorkspace = "/workspace"
	guestGrants    = "/mnt/p/"
	guestEndpoints = "/opt/p/endpoints"
)

var workspaceCases = append([]testCase{
	{"a write keeps exact owner, mode and bytes, and a read returns them", func(t *testing.T, f Fixture) {
		w := stoppedWorkspace(t, f, SessionOptions{})
		dir := runtimedriver.File{Type: runtimedriver.FileDirectory, UID: 1000, GID: 1000, Mode: 0o750}
		file := runtimedriver.File{Type: runtimedriver.FileRegular, UID: 0, GID: 1000, Mode: 0o640, Data: []byte("binary\x00\xff\nbytes")}
		w.write(t, "exact", dir)
		w.write(t, "exact/file", file)
		w.wantFile(t, "exact", dir)
		w.wantFile(t, "exact/file", file)
		replaced := runtimedriver.File{Type: runtimedriver.FileRegular, UID: 1000, GID: 1000, Mode: 0o600, Data: []byte("replaced")}
		w.write(t, "exact/file", replaced)
		w.wantFile(t, "exact/file", replaced)
	}},
	{"a missing path reads as absent", func(t *testing.T, f Fixture) {
		w := stoppedWorkspace(t, f, SessionOptions{})
		for _, p := range []string{"missing", "missing/deeper"} {
			w.wantFile(t, p, runtimedriver.File{Type: runtimedriver.FileAbsent})
		}
	}},
	{"remove removes the path", func(t *testing.T, f Fixture) {
		w := stoppedWorkspace(t, f, SessionOptions{})
		w.write(t, "gone", regular("to remove"))
		if err := f.Driver().RemoveWorkspace(t.Context(), w.target, "gone", func() error { return nil }); err != nil {
			t.Fatalf("RemoveWorkspace: %v", err)
		}
		w.wantFile(t, "gone", runtimedriver.File{Type: runtimedriver.FileAbsent})
	}},
	{"write and remove call the gate once before their effect, and a failing gate changes nothing", func(t *testing.T, f Fixture) {
		w := stoppedWorkspace(t, f, SessionOptions{})
		before := regular("before")
		w.write(t, "file", before)
		g := &gate{before: func() { w.wantFile(t, "file", before) }}
		if err := f.Driver().WriteWorkspace(t.Context(), w.target, "file", regular("after"), g.call); err != nil || g.count() != 1 {
			t.Fatalf("WriteWorkspace = %v after %d gate calls, want success after 1", err, g.count())
		}
		g = &gate{before: func() { w.wantFile(t, "file", regular("after")) }}
		if err := f.Driver().RemoveWorkspace(t.Context(), w.target, "file", g.call); err != nil || g.count() != 1 {
			t.Fatalf("RemoveWorkspace = %v after %d gate calls, want success after 1", err, g.count())
		}
		w.write(t, "file", before)
		failing := &gate{err: errGate}
		if err := f.Driver().WriteWorkspace(t.Context(), w.target, "file", regular("after"), failing.call); !errors.Is(err, errGate) {
			t.Fatalf("WriteWorkspace with a failing gate = %v, want the gate's error", err)
		}
		if err := f.Driver().RemoveWorkspace(t.Context(), w.target, "file", failing.call); !errors.Is(err, errGate) {
			t.Fatalf("RemoveWorkspace with a failing gate = %v, want the gate's error", err)
		}
		w.wantFile(t, "file", before)
	}},
	{"a read refuses a file larger than its limit", func(t *testing.T, f Fixture) {
		w := stoppedWorkspace(t, f, SessionOptions{})
		file := regular("twelve bytes")
		w.write(t, "file", file)
		if got, err := f.Driver().ReadWorkspace(t.Context(), w.target, "file", int64(len(file.Data))-1); err == nil {
			t.Fatalf("ReadWorkspace over its limit = %+v, want a refusal", got)
		}
		got, err := f.Driver().ReadWorkspace(t.Context(), w.target, "file", int64(len(file.Data)))
		if err != nil || !bytes.Equal(got.Data, file.Data) {
			t.Fatalf("ReadWorkspace at its limit = %+v, %v; want the file", got, err)
		}
	}},
	{"workspace operations refuse a running runtime", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		at := running(t, f, s).Incarnation
		wantRefused(t, f, target(s, at), "file", regular("refused"))
	}},
	{"workspace operations refuse another incarnation", func(t *testing.T, f Fixture) {
		w := stoppedWorkspace(t, f, SessionOptions{})
		other := created(t, f, f.Session(SessionOptions{})).Incarnation
		w.write(t, "file", regular("kept"))
		wantRefused(t, f, target(w.target.Session, other), "file", regular("refused"))
		w.wantFile(t, "file", regular("kept"))
	}},
	{"workspace operations refuse a target without an incarnation", func(t *testing.T, f Fixture) {
		w := stoppedWorkspace(t, f, SessionOptions{})
		w.write(t, "file", regular("kept"))
		wantRefused(t, f, target(w.target.Session, ""), "file", regular("refused"))
		w.wantFile(t, "file", regular("kept"))
	}},
	{"workspace operations refuse paths that are not relative and clean", func(t *testing.T, f Fixture) {
		w := stoppedWorkspace(t, f, SessionOptions{})
		for _, p := range []string{"", ".", "/etc/passwd", "/workspace/file", "..", "../file", "dir/../file", "dir//file", "dir/", "./file"} {
			wantRefused(t, f, w.target, p, regular("refused"))
		}
	}},
	{"a symlink at the path is reported, never followed", func(t *testing.T, f Fixture) {
		w := stoppedWorkspace(t, f, SessionOptions{})
		inner := regular("inside the workspace")
		w.write(t, "inner", inner)
		f.Workspace().Symlink(w.ref(), "to-inner", "inner")
		f.Workspace().Symlink(w.ref(), "to-host", "/etc/hostname")
		for _, p := range []string{"to-inner", "to-host"} {
			got, err := f.Driver().ReadWorkspace(t.Context(), w.target, p, 1<<10)
			if err != nil || got.Type != runtimedriver.FileSymlink || len(got.Data) != 0 {
				t.Fatalf("ReadWorkspace(%s) = %+v, %v; want a symlink without data", p, got, err)
			}
		}
		// A write at the link may replace the link or be refused; either
		// way it never reaches the link's target.
		_ = f.Driver().WriteWorkspace(t.Context(), w.target, "to-inner", regular("written at the link"), func() error { return nil })
		w.wantFile(t, "inner", inner)
	}},
	{"a symlink above the path is refused", func(t *testing.T, f Fixture) {
		w := stoppedWorkspace(t, f, SessionOptions{})
		w.write(t, "dir", directory())
		inner := regular("inside the workspace")
		w.write(t, "dir/file", inner)
		f.Workspace().Symlink(w.ref(), "linked", "dir")
		f.Workspace().Symlink(w.ref(), "escape", "/etc")
		for _, p := range []string{"linked/file", "linked/new", "escape/hostname"} {
			wantRefused(t, f, w.target, p, regular("refused"))
		}
		w.wantFile(t, "dir/file", inner)
	}},
	{"remove of a symlink removes only the link", func(t *testing.T, f Fixture) {
		w := stoppedWorkspace(t, f, SessionOptions{})
		inner := regular("inside the workspace")
		w.write(t, "inner", inner)
		f.Workspace().Symlink(w.ref(), "link", "inner")
		if err := f.Driver().RemoveWorkspace(t.Context(), w.target, "link", func() error { return nil }); err != nil {
			t.Fatalf("RemoveWorkspace of a symlink: %v", err)
		}
		w.wantFile(t, "link", runtimedriver.File{Type: runtimedriver.FileAbsent})
		w.wantFile(t, "inner", inner)
	}},
	{"a foreign mount beside the workspace, at /workspace-sibling, is not confused with it", func(t *testing.T, f Fixture) {
		w := stoppedWorkspace(t, f, SessionOptions{})
		w.write(t, "dir", directory())
		w.write(t, "dir/file", regular("in the private root"))
		f.Workspace().ForeignMount(w.ref(), guestWorkspace+"-sibling")
		w.wantFile(t, "dir/file", regular("in the private root"))
		w.write(t, "dir/file", regular("written"))
		if err := f.Driver().RemoveWorkspace(t.Context(), w.target, "dir/file", func() error { return nil }); err != nil {
			t.Fatalf("RemoveWorkspace beside a sibling mount: %v", err)
		}
	}},
	{"markers in grant sources and the endpoint directory are never returned", func(t *testing.T, f Fixture) {
		w := stoppedWorkspace(t, f, SessionOptions{Grant: true})
		s := w.target.Session
		marker := f.Workspace().Markers(s)
		grant := guestGrants + s.Grants[0].Name
		f.Workspace().Symlink(w.ref(), "grant", grant)
		f.Workspace().Symlink(w.ref(), "grant-marker", grant+"/marker")
		f.Workspace().Symlink(w.ref(), "endpoints", guestEndpoints)
		f.Workspace().Symlink(w.ref(), "endpoints-marker", guestEndpoints+"/marker")
		for _, p := range []string{"grant", "grant-marker", "endpoints", "endpoints-marker", "grant/marker", "endpoints/marker", "marker"} {
			got, err := f.Driver().ReadWorkspace(t.Context(), w.target, p, 1<<20)
			if err == nil && bytes.Contains(got.Data, marker) {
				t.Fatalf("ReadWorkspace(%s) returned a marker from outside the private root", p)
			}
		}
	}},
	{"a workspace file survives stop and start", func(t *testing.T, f Fixture) {
		w := stoppedWorkspace(t, f, SessionOptions{})
		kept := regular("kept across stop and start")
		w.write(t, "kept", kept)
		if _, err := f.Driver().Start(t.Context(), w.target.Session); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if _, err := f.Driver().Stop(t.Context(), w.target.Session); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		w.wantFile(t, "kept", kept)
	}},
	{"a write settles applied only with its content in place exactly", func(t *testing.T, f Fixture) {
		w := stoppedWorkspace(t, f, SessionOptions{})
		content := regular("written")
		w.write(t, "file", content)
		effect := runtimedriver.Effect{Kind: runtimedriver.EffectWrite, Target: w.target, Path: "file", Content: content}
		wantSettled(t, f, effect, runtimedriver.Applied)
		effect.Content = regular("other content")
		if got := settle(t, f, effect); got == runtimedriver.Applied {
			t.Fatal("Settle(write) of content that is not there = applied")
		}
	}},
	{"a remove settles applied once the path is absent", func(t *testing.T, f Fixture) {
		w := stoppedWorkspace(t, f, SessionOptions{})
		w.write(t, "file", regular("to remove"))
		if err := f.Driver().RemoveWorkspace(t.Context(), w.target, "file", func() error { return nil }); err != nil {
			t.Fatalf("RemoveWorkspace: %v", err)
		}
		wantSettled(t, f, runtimedriver.Effect{Kind: runtimedriver.EffectRemove, Target: w.target, Path: "file"}, runtimedriver.Applied)
	}},
}, foreignMountCases()...)

// foreignMountCases refuse each workspace operation on a path that a mount
// which is not the private root lies at, below or above, escaped names
// included. Each is a case of its own, so a fixture holds one runtime.
func foreignMountCases() []testCase {
	var cases []testCase
	for _, c := range []struct {
		where string
		mount string // the foreign mount's guest path
		dir   string // a directory holding a file named file
		path  string // the path the operations are refused
	}{
		{"above the workspace", "/", "dir", "dir/file"},
		{"at the workspace", guestWorkspace, "dir", "dir/file"},
		{"above the path", guestWorkspace + "/dir", "dir", "dir/file"},
		{"at the path", guestWorkspace + "/dir/file", "dir", "dir/file"},
		{"below the path", guestWorkspace + "/dir/below", "dir", "dir"},
		{"with a space in its name", guestWorkspace + "/with space", "with space", "with space/file"},
		{"with a tab in its name", guestWorkspace + "/with\ttab", "with\ttab", "with\ttab/file"},
		{"with a newline in its name", guestWorkspace + "/with\nnewline", "with\nnewline", "with\nnewline/file"},
		{"with an escape sequence in its name", guestWorkspace + `/with\040escape`, `with\040escape`, `with\040escape/file`},
	} {
		cases = append(cases, testCase{"a foreign mount " + c.where + " is refused", func(t *testing.T, f Fixture) {
			w := stoppedWorkspace(t, f, SessionOptions{})
			w.write(t, c.dir, directory())
			w.write(t, c.dir+"/file", regular("in the private root"))
			f.Workspace().ForeignMount(w.ref(), c.mount)
			written := regular("refused")
			if c.path == c.dir {
				written = directory()
			}
			wantRefused(t, f, w.target, c.path, written)
		}})
	}
	return cases
}

// workspaceUnderTest is the workspace of a created, stopped session.
type workspaceUnderTest struct {
	f      Fixture
	target runtimedriver.Target
}

func stoppedWorkspace(t *testing.T, f Fixture, o SessionOptions) workspaceUnderTest {
	t.Helper()
	s := f.Session(o)
	return workspaceUnderTest{f: f, target: target(s, created(t, f, s).Incarnation)}
}

func (w workspaceUnderTest) ref() runtimedriver.Ref { return w.target.Session.Ref }

func (w workspaceUnderTest) write(t *testing.T, path string, file runtimedriver.File) {
	t.Helper()
	if err := w.f.Driver().WriteWorkspace(t.Context(), w.target, path, file, func() error { return nil }); err != nil {
		t.Fatalf("WriteWorkspace(%s): %v", path, err)
	}
}

func (w workspaceUnderTest) wantFile(t *testing.T, path string, want runtimedriver.File) {
	t.Helper()
	got, err := w.f.Driver().ReadWorkspace(t.Context(), w.target, path, 1<<20)
	if err != nil {
		t.Fatalf("ReadWorkspace(%s): %v", path, err)
	}
	if got.Type != want.Type || got.Type != runtimedriver.FileAbsent && (got.UID != want.UID || got.GID != want.GID || got.Mode != want.Mode) || !bytes.Equal(got.Data, want.Data) {
		t.Fatalf("ReadWorkspace(%s) = %+v, want %+v", path, got, want)
	}
}

// wantRefused checks that each workspace operation refuses path at target,
// without calling its gate; the write would write written.
func wantRefused(t *testing.T, f Fixture, at runtimedriver.Target, path string, written runtimedriver.File) {
	t.Helper()
	if got, err := f.Driver().ReadWorkspace(t.Context(), at, path, 1<<20); err == nil {
		t.Errorf("ReadWorkspace(%q) = %+v, want a refusal", path, got)
	}
	if err := f.Driver().WriteWorkspace(t.Context(), at, path, written, unexpectedGate(t)); err == nil {
		t.Errorf("WriteWorkspace(%q) succeeded, want a refusal", path)
	}
	if err := f.Driver().RemoveWorkspace(t.Context(), at, path, unexpectedGate(t)); err == nil {
		t.Errorf("RemoveWorkspace(%q) succeeded, want a refusal", path)
	}
}

func regular(data string) runtimedriver.File {
	return runtimedriver.File{Type: runtimedriver.FileRegular, UID: 1000, GID: 1000, Mode: 0o644, Data: []byte(data)}
}

func directory() runtimedriver.File {
	return runtimedriver.File{Type: runtimedriver.FileDirectory, UID: 1000, GID: 1000, Mode: 0o755}
}
