package conformance

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// HeadControls disturb the driver's reads of heads. A Fixture's
// Repositories controls provide them.
type HeadControls interface {
	// MoveDuringRead points refs/heads/<name> at tip, or removes it when tip
	// is empty, between the two reads of the driver's next observation of
	// every head, as a session's push could. On the bundled driver: a seam
	// of the driver between the reads.
	MoveDuringRead(project, name string, tip sourcedriver.OID)
	// SetHeads points refs/heads/<name> at tip for each name, at once.
	SetHeads(project string, names []string, tip sourcedriver.OID)
	// DamageHead points refs/heads/<name> at tip, which need not be a
	// commit of the repository, as a damaged repository could. On the
	// bundled driver: the loose ref file written directly, since Git
	// refuses a head on anything but a commit.
	DamageHead(project, name string, tip sourcedriver.OID)
}

// missingCommit is a well-formed object ID no repository of the suite
// holds.
const missingCommit = sourcedriver.OID("ffffffffffffffffffffffffffffffffffffffff")

// inspectBranch reads one head through the driver.
func inspectBranch(t *testing.T, f Fixture, project, name string) (sourcedriver.OID, bool) {
	t.Helper()
	tip, ok, err := f.Driver().InspectBranch(t.Context(), project, name)
	if err != nil {
		t.Fatalf("InspectBranch(%q): %v", name, err)
	}
	return tip, ok
}

// branches reads every head through the driver.
func branches(t *testing.T, f Fixture, project string) []sourcedriver.Head {
	t.Helper()
	heads, err := f.Driver().Branches(t.Context(), project)
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	return heads
}

// withHead makes the project's repository with the head name at a new
// commit, and returns that commit.
func withHead(t *testing.T, f Fixture, project, name string) sourcedriver.OID {
	t.Helper()
	repository(t, f, project)
	tip := f.Natives().Commit(project)
	f.Natives().SetHead(project, name, tip)
	return tip
}

func headNames(n int) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("h%04d", i)
	}
	return names
}

var headCases = []testCase{
	{"inspect reads a head exactly and tells it absent", func(t *testing.T, f Fixture) {
		tip := withHead(t, f, "p", "work")
		if got, ok := inspectBranch(t, f, "p", "work"); !ok || got != tip {
			t.Fatalf("InspectBranch(work) = %q, %v; want %q", got, ok, tip)
		}
		if got, ok := inspectBranch(t, f, "p", "other"); ok || got != "" {
			t.Fatalf("InspectBranch of an absent head = %q, %v; want absent", got, ok)
		}
		if got, ok := inspectBranch(t, f, "p", "wor"); ok || got != "" {
			t.Fatalf("InspectBranch of a prefix of a head = %q, %v; want absent", got, ok)
		}
	}},
	{"inspect refuses a symbolic ref, dangling or not", func(t *testing.T, f Fixture) {
		withHead(t, f, "p", "work")
		f.Natives().SetSymbolic("p", "alias", "work")
		f.Natives().SetSymbolic("p", "dangling", "nowhere")
		for _, name := range []string{"alias", "dangling"} {
			if _, _, err := f.Driver().InspectBranch(t.Context(), "p", name); !errors.Is(err, sourcedriver.ErrConflict) {
				t.Errorf("InspectBranch of the symbolic ref %s = %v, want ErrConflict", name, err)
			}
		}
	}},
	{"branches are every head in byte order with their tips", func(t *testing.T, f Fixture) {
		repository(t, f, "p")
		base := f.Natives().Commit("p")
		next := f.Natives().Commit("p", base)
		want := []sourcedriver.Head{{Name: "B", Tip: base}, {Name: "a-b", Tip: next}, {Name: "a.b", Tip: base}, {Name: "a/b", Tip: next}, {Name: "main", Tip: next}}
		for _, h := range []sourcedriver.Head{want[4], want[2], want[0], want[3], want[1]} {
			f.Natives().SetHead("p", h.Name, h.Tip)
		}
		if got := branches(t, f, "p"); !reflect.DeepEqual(got, want) {
			t.Fatalf("Branches = %v, want %v", got, want)
		}
	}},
	{"branches refuse a head whose tip is not a commit of the repository", func(t *testing.T, f Fixture) {
		withHead(t, f, "p", "work")
		withHead(t, f, "q", "work")
		f.Repositories().DamageHead("p", "damaged", f.Natives().Tree("p"))
		f.Repositories().DamageHead("q", "damaged", missingCommit)
		for _, project := range []string{"p", "q"} {
			if heads, err := f.Driver().Branches(t.Context(), project); err == nil {
				t.Errorf("Branches of %s with a head on a tree or a missing object = %v, want an error", project, heads)
			}
		}
	}},
	{"more than 1024 heads is ErrLimit", func(t *testing.T, f Fixture) {
		tip := withHead(t, f, "p", "h0000")
		f.Repositories().SetHeads("p", headNames(sourcedriver.MaxHeads), tip)
		if heads := branches(t, f, "p"); len(heads) != sourcedriver.MaxHeads {
			t.Fatalf("Branches of %d heads returned %d", sourcedriver.MaxHeads, len(heads))
		}
		f.Natives().SetHead("p", "one-more", tip)
		if heads, err := f.Driver().Branches(t.Context(), "p"); !errors.Is(err, sourcedriver.ErrLimit) {
			t.Fatalf("Branches of %d heads = %d heads, %v; want ErrLimit", sourcedriver.MaxHeads+1, len(heads), err)
		}
	}},
	{"a head moved between two reads is an error, not an observation", func(t *testing.T, f Fixture) {
		tip := withHead(t, f, "p", "work")
		moved := f.Natives().Commit("p", tip)
		f.Repositories().MoveDuringRead("p", "work", moved)
		if heads, err := f.Driver().Branches(t.Context(), "p"); err == nil {
			t.Fatalf("Branches across a moving head = %v, want an error", heads)
		}
		if got := branches(t, f, "p"); !reflect.DeepEqual(got, []sourcedriver.Head{{Name: "work", Tip: moved}}) {
			t.Fatalf("a new read = %v, want work at %s", got, moved)
		}
	}},
	{"creation creates the head after calling the gate once", func(t *testing.T, f Fixture) {
		repository(t, f, "p")
		at := f.Natives().Commit("p")
		g := &gate{before: func() {
			if _, ok := inspectBranch(t, f, "p", "work"); ok {
				t.Error("at the gate the head already existed")
			}
		}}
		if err := f.Driver().CreateBranch(t.Context(), "p", "work", at, g.call); err != nil {
			t.Fatalf("CreateBranch: %v", err)
		}
		if g.count() != 1 {
			t.Fatalf("CreateBranch called the gate %d times, want 1", g.count())
		}
		if got, ok := inspectBranch(t, f, "p", "work"); !ok || got != at {
			t.Fatalf("after CreateBranch the head is %q, %v; want %q", got, ok, at)
		}
	}},
	{"creation refuses an existing head even at the same tip", func(t *testing.T, f Fixture) {
		tip := withHead(t, f, "p", "work")
		other := f.Natives().Commit("p")
		for _, at := range []sourcedriver.OID{tip, other} {
			if err := f.Driver().CreateBranch(t.Context(), "p", "work", at, unexpectedGate(t)); !errors.Is(err, sourcedriver.ErrConflict) {
				t.Errorf("CreateBranch over an existing head = %v, want ErrConflict", err)
			}
		}
		if got, _ := inspectBranch(t, f, "p", "work"); got != tip {
			t.Fatalf("the refused creation moved the head to %q", got)
		}
	}},
	{"creation refuses a dangling symbolic ref and creates nothing through it", func(t *testing.T, f Fixture) {
		repository(t, f, "p")
		at := f.Natives().Commit("p")
		f.Natives().SetSymbolic("p", "work", "nowhere")
		if err := f.Driver().CreateBranch(t.Context(), "p", "work", at, unexpectedGate(t)); !errors.Is(err, sourcedriver.ErrConflict) {
			t.Fatalf("CreateBranch over a dangling symbolic ref = %v, want ErrConflict", err)
		}
		if _, ok := inspectBranch(t, f, "p", "nowhere"); ok {
			t.Fatal("the refused creation created the symbolic ref's target")
		}
		if _, _, err := f.Driver().InspectBranch(t.Context(), "p", "work"); !errors.Is(err, sourcedriver.ErrConflict) {
			t.Fatalf("after the refusal InspectBranch(work) = %v, want the symbolic ref still there", err)
		}
	}},
	{"creation refuses a commit not in the repository and a non-commit", func(t *testing.T, f Fixture) {
		repository(t, f, "p")
		repository(t, f, "q")
		for name, at := range map[string]sourcedriver.OID{
			"missing":           missingCommit,
			"another project's": f.Natives().Commit("q"),
			"a tree":            f.Natives().Tree("p"),
		} {
			if err := f.Driver().CreateBranch(t.Context(), "p", "work", at, unexpectedGate(t)); err == nil {
				t.Errorf("CreateBranch at %s object succeeded", name)
			}
		}
		if heads := branches(t, f, "p"); len(heads) != 0 {
			t.Fatalf("the refused creations left heads %v", heads)
		}
	}},
	{"a failing gate creates no head", func(t *testing.T, f Fixture) {
		repository(t, f, "p")
		at := f.Natives().Commit("p")
		g := &gate{err: errGate}
		if err := f.Driver().CreateBranch(t.Context(), "p", "work", at, g.call); !errors.Is(err, errGate) {
			t.Fatalf("CreateBranch with a failing gate = %v, want the gate's error", err)
		}
		if _, ok := inspectBranch(t, f, "p", "work"); ok {
			t.Fatal("a failing gate left the head created")
		}
	}},
	{"deletion deletes the head at its tip after calling the gate once", func(t *testing.T, f Fixture) {
		tip := withHead(t, f, "p", "work")
		f.Natives().SetHead("p", "other", tip)
		g := &gate{before: func() {
			if got, ok := inspectBranch(t, f, "p", "work"); !ok || got != tip {
				t.Error("at the gate the head was already gone")
			}
		}}
		if err := f.Driver().DeleteBranch(t.Context(), "p", "work", tip, g.call); err != nil {
			t.Fatalf("DeleteBranch: %v", err)
		}
		if g.count() != 1 {
			t.Fatalf("DeleteBranch called the gate %d times, want 1", g.count())
		}
		if got := branches(t, f, "p"); !reflect.DeepEqual(got, []sourcedriver.Head{{Name: "other", Tip: tip}}) {
			t.Fatalf("after DeleteBranch the heads are %v, want only other", got)
		}
	}},
	{"deletion refuses a moved tip, an absent head and a symbolic ref", func(t *testing.T, f Fixture) {
		tip := withHead(t, f, "p", "work")
		moved := f.Natives().Commit("p", tip)
		f.Natives().SetHead("p", "work", moved)
		f.Natives().SetSymbolic("p", "alias", "work")
		for _, name := range []string{"work", "absent", "alias"} {
			if err := f.Driver().DeleteBranch(t.Context(), "p", name, tip, unexpectedGate(t)); !errors.Is(err, sourcedriver.ErrConflict) {
				t.Errorf("DeleteBranch(%s) at a tip it does not name = %v, want ErrConflict", name, err)
			}
		}
		if got, _ := inspectBranch(t, f, "p", "work"); got != moved {
			t.Fatalf("the refused deletion changed the head to %q", got)
		}
		if _, _, err := f.Driver().InspectBranch(t.Context(), "p", "alias"); !errors.Is(err, sourcedriver.ErrConflict) {
			t.Fatalf("the refused deletion removed the symbolic ref: %v", err)
		}
	}},
	{"a failing gate deletes no head", func(t *testing.T, f Fixture) {
		tip := withHead(t, f, "p", "work")
		g := &gate{err: errGate}
		if err := f.Driver().DeleteBranch(t.Context(), "p", "work", tip, g.call); !errors.Is(err, errGate) {
			t.Fatalf("DeleteBranch with a failing gate = %v, want the gate's error", err)
		}
		if got, ok := inspectBranch(t, f, "p", "work"); !ok || got != tip {
			t.Fatal("a failing gate left the head deleted")
		}
	}},
	{"head operations on a project without a repository are ErrNotFound", func(t *testing.T, f Fixture) {
		ops := map[string]error{}
		_, ops["Branches"] = f.Driver().Branches(t.Context(), "p")
		_, _, ops["InspectBranch"] = f.Driver().InspectBranch(t.Context(), "p", "work")
		ops["CreateBranch"] = f.Driver().CreateBranch(t.Context(), "p", "work", missingCommit, unexpectedGate(t))
		ops["DeleteBranch"] = f.Driver().DeleteBranch(t.Context(), "p", "work", missingCommit, unexpectedGate(t))
		_, ops["InspectCommit"] = f.Driver().InspectCommit(t.Context(), "p", missingCommit)
		_, ops["LosingCommits"] = f.Driver().LosingCommits(t.Context(), "p", nil)
		for op, err := range ops {
			if !errors.Is(err, sourcedriver.ErrNotFound) {
				t.Errorf("%s without a repository = %v, want ErrNotFound", op, err)
			}
		}
		if r := inspectRepository(t, f, "p"); r.Exists {
			t.Fatalf("the refused operations made the repository: %+v", r)
		}
	}},
}
