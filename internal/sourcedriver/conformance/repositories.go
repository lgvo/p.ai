package conformance

import (
	"errors"
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// RepositoryControls act on the driver's storage. They hold the controls of
// the heads and commits groups too, whose cases reach them through the
// Fixture's Repositories.
type RepositoryControls interface {
	HeadControls
	CommitControls

	// LoosenStorage makes the driver's storage writable by others, as a
	// careless administrator would. On the bundled driver: the storage
	// directory becomes group-writable.
	LoosenStorage()
	// DefaultBranch is the branch the HEAD of the project's repository
	// names.
	DefaultBranch(project string) string
	// SetDefaultBranch points the HEAD of the project's repository at
	// refs/heads/<name>.
	SetDefaultBranch(project, name string)
	// LeavePartial leaves at the project's address, where no repository
	// is, something that is not a complete repository, as an interrupted
	// copy or an administrator could. On the bundled driver: a directory
	// with objects and refs and no HEAD.
	LeavePartial(project string)
}

// inspectRepository reads the project's repository through the driver.
func inspectRepository(t *testing.T, f Fixture, project string) sourcedriver.Repository {
	t.Helper()
	r, err := f.Driver().InspectRepository(t.Context(), project)
	if err != nil {
		t.Fatalf("InspectRepository(%q): %v", project, err)
	}
	return r
}

var repositoryCases = []testCase{
	{"creation converges and leaves main with no heads", func(t *testing.T, f Fixture) {
		first := repository(t, f, "p")
		if !first.Exists || first.Project != "p" || first.Incarnation == "" {
			t.Fatalf("CreateRepository = %+v, want p existing with an incarnation", first)
		}
		if again := repository(t, f, "p"); again != first {
			t.Fatalf("a repeated CreateRepository = %+v, want %+v", again, first)
		}
		if inspected := inspectRepository(t, f, "p"); inspected != first {
			t.Fatalf("InspectRepository = %+v, want %+v", inspected, first)
		}
		if head := f.Repositories().DefaultBranch("p"); head != "main" {
			t.Fatalf("the new repository's HEAD names %q, want main", head)
		}
		heads, err := f.Driver().Branches(t.Context(), "p")
		if err != nil || len(heads) != 0 {
			t.Fatalf("Branches of a new repository = %v, %v; want none", heads, err)
		}
	}},
	{"inspect of a project never created proves it absent", func(t *testing.T, f Fixture) {
		if r := inspectRepository(t, f, "p"); r.Exists || r.Project != "p" {
			t.Fatalf("InspectRepository of a project never created = %+v, want absent", r)
		}
	}},
	{"creation keeps an existing repository and its heads", func(t *testing.T, f Fixture) {
		first := repository(t, f, "p")
		tip := f.Natives().Commit("p")
		f.Natives().SetHead("p", "work", tip)
		if again := repository(t, f, "p"); again != first {
			t.Fatalf("CreateRepository of an existing repository = %+v, want %+v", again, first)
		}
		if got, ok, err := f.Driver().InspectBranch(t.Context(), "p", "work"); err != nil || !ok || got != tip {
			t.Fatalf("after a repeated creation the head is %q, %v, %v; want %q", got, ok, err, tip)
		}
	}},
	{"creation refuses an existing repository whose HEAD is not main", func(t *testing.T, f Fixture) {
		first := repository(t, f, "p")
		f.Repositories().SetDefaultBranch("p", "trunk")
		if r, err := f.Driver().CreateRepository(t.Context(), "p"); err == nil {
			t.Fatalf("CreateRepository of a repository whose HEAD names trunk = %+v, want an error", r)
		}
		if r := inspectRepository(t, f, "p"); r != first {
			t.Fatalf("the refused creation changed the repository: %+v, want %+v", r, first)
		}
		if head := f.Repositories().DefaultBranch("p"); head != "trunk" {
			t.Fatalf("the refused creation moved HEAD to %q", head)
		}
	}},
	{"something incomplete at the address is an error, never absent", func(t *testing.T, f Fixture) {
		f.Repositories().LeavePartial("p")
		if r, err := f.Driver().InspectRepository(t.Context(), "p"); err == nil {
			t.Fatalf("InspectRepository of an incomplete repository = %+v, want an error", r)
		}
		if r, err := f.Driver().CreateRepository(t.Context(), "p"); err == nil {
			t.Fatalf("CreateRepository over an incomplete repository = %+v, want an error", r)
		}
		if err := f.Driver().DeleteRepository(t.Context(), sourcedriver.Repository{Project: "p"}, unexpectedGate(t)); err == nil {
			t.Fatal("DeleteRepository of an incomplete repository succeeded")
		}
		if r, err := f.Driver().InspectRepository(t.Context(), "p"); err == nil {
			t.Fatalf("after the refusals InspectRepository = %+v, want the incomplete repository's error", r)
		}
	}},
	{"deletion removes only its incarnation, even one made again while the gate runs", func(t *testing.T, f Fixture) {
		old := repository(t, f, "p")
		var current sourcedriver.Repository
		g := func() error {
			if err := f.Driver().DeleteRepository(t.Context(), old, func() error { return nil }); err != nil {
				t.Errorf("DeleteRepository inside the gate: %v", err)
			}
			current = repository(t, f, "p")
			return nil
		}
		if err := f.Driver().DeleteRepository(t.Context(), old, g); !errors.Is(err, sourcedriver.ErrConflict) {
			t.Fatalf("DeleteRepository across a re-creation = %v, want ErrConflict", err)
		}
		if r := inspectRepository(t, f, "p"); r != current || !r.Exists {
			t.Fatalf("after the refused deletion the repository is %+v, want the new one %+v", r, current)
		}
	}},
	{"a re-created repository has a new incarnation", func(t *testing.T, f Fixture) {
		first := repository(t, f, "p")
		if err := f.Driver().DeleteRepository(t.Context(), first, func() error { return nil }); err != nil {
			t.Fatalf("DeleteRepository: %v", err)
		}
		if second := repository(t, f, "p"); second.Incarnation == first.Incarnation {
			t.Fatalf("the re-created repository kept incarnation %q", first.Incarnation)
		}
	}},
	{"deletion refuses another incarnation without calling the gate", func(t *testing.T, f Fixture) {
		old := repository(t, f, "p")
		if err := f.Driver().DeleteRepository(t.Context(), old, func() error { return nil }); err != nil {
			t.Fatalf("DeleteRepository: %v", err)
		}
		current := repository(t, f, "p")
		if err := f.Driver().DeleteRepository(t.Context(), old, unexpectedGate(t)); !errors.Is(err, sourcedriver.ErrConflict) {
			t.Fatalf("DeleteRepository at an earlier incarnation = %v, want ErrConflict", err)
		}
		if r := inspectRepository(t, f, "p"); r != current {
			t.Fatalf("the refused deletion changed the repository: %+v, want %+v", r, current)
		}
	}},
	{"deletion calls the gate once before removing, leaves nothing and is safe to repeat", func(t *testing.T, f Fixture) {
		r := repository(t, f, "p")
		f.Natives().SetHead("p", "work", f.Natives().Commit("p"))
		g := &gate{before: func() {
			if at := inspectRepository(t, f, "p"); !at.Exists {
				t.Error("at the gate the repository was already gone")
			}
		}}
		if err := f.Driver().DeleteRepository(t.Context(), r, g.call); err != nil {
			t.Fatalf("DeleteRepository: %v", err)
		}
		if g.count() != 1 {
			t.Fatalf("DeleteRepository called the gate %d times, want 1", g.count())
		}
		if after := inspectRepository(t, f, "p"); after.Exists {
			t.Fatalf("after DeleteRepository the repository is %+v, want absent", after)
		}
		if err := f.Driver().DeleteRepository(t.Context(), r, unexpectedGate(t)); err != nil {
			t.Fatalf("a repeated DeleteRepository = %v, want success without the gate", err)
		}
		if _, err := f.Driver().Branches(t.Context(), "p"); !errors.Is(err, sourcedriver.ErrNotFound) {
			t.Fatalf("Branches of a deleted repository = %v, want ErrNotFound", err)
		}
	}},
	{"a failing gate leaves the repository", func(t *testing.T, f Fixture) {
		r := repository(t, f, "p")
		g := &gate{err: errGate}
		if err := f.Driver().DeleteRepository(t.Context(), r, g.call); !errors.Is(err, errGate) {
			t.Fatalf("DeleteRepository with a failing gate = %v, want the gate's error", err)
		}
		if after := inspectRepository(t, f, "p"); after != r {
			t.Fatalf("after a failing gate the repository is %+v, want %+v", after, r)
		}
	}},
	{"deletion of an absent repository succeeds without the gate", func(t *testing.T, f Fixture) {
		if err := f.Driver().DeleteRepository(t.Context(), sourcedriver.Repository{Project: "p"}, unexpectedGate(t)); err != nil {
			t.Fatalf("DeleteRepository of an absent repository: %v", err)
		}
	}},
	{"projects whose names nest keep repositories of their own", func(t *testing.T, f Fixture) {
		outer := repository(t, f, "a")
		repository(t, f, "a.git/b")
		f.Natives().SetHead("a", "work", f.Natives().Commit("a"))
		if heads, err := f.Driver().Branches(t.Context(), "a.git/b"); err != nil || len(heads) != 0 {
			t.Fatalf("Branches of a.git/b = %v, %v; want none", heads, err)
		}
		if err := f.Driver().DeleteRepository(t.Context(), outer, func() error { return nil }); err != nil {
			t.Fatalf("DeleteRepository(a): %v", err)
		}
		if r := inspectRepository(t, f, "a.git/b"); !r.Exists {
			t.Fatal("deleting a removed a.git/b")
		}
	}},
	{"an unsafe storage fails Check and every mutation refuses", func(t *testing.T, f Fixture) {
		r := repository(t, f, "p")
		tip := f.Natives().Commit("p")
		f.Natives().SetHead("p", "work", tip)
		url := f.Origin().NewOrigin()
		f.Origin().SetOriginRef(url, "refs/heads/main", f.Origin().OriginCommit(url))
		main := observe(t, f, url)["refs/heads/main"]
		if err := f.Driver().Check(t.Context()); err != nil {
			t.Fatalf("Check of a private storage: %v", err)
		}
		f.Repositories().LoosenStorage()
		if err := f.Driver().Check(t.Context()); err == nil {
			t.Fatal("Check of a storage others may write succeeded")
		}
		if _, err := f.Driver().CreateRepository(t.Context(), "q"); err == nil {
			t.Error("CreateRepository in an unsafe storage succeeded")
		}
		if err := f.Driver().DeleteRepository(t.Context(), r, unexpectedGate(t)); err == nil {
			t.Error("DeleteRepository in an unsafe storage succeeded")
		}
		if err := f.Driver().CreateBranch(t.Context(), "p", "other", tip, unexpectedGate(t)); err == nil {
			t.Error("CreateBranch in an unsafe storage succeeded")
		}
		if err := f.Driver().DeleteBranch(t.Context(), "p", "work", tip, unexpectedGate(t)); err == nil {
			t.Error("DeleteBranch in an unsafe storage succeeded")
		}
		if _, err := f.Driver().FetchOrigin(t.Context(), "p", url, main); err == nil {
			t.Error("FetchOrigin into an unsafe storage succeeded")
		}
		if _, err := f.Driver().ComparePublication(t.Context(), "p", url, tip, &main); err == nil {
			t.Error("ComparePublication, which fetches, in an unsafe storage succeeded")
		}
		publication := sourcedriver.Publication{URL: url, Branch: "work", Commit: tip, Destination: "new", Relation: sourcedriver.RelationAbsent}
		if _, err := f.Driver().Publish(t.Context(), "p", publication, unexpectedGate(t)); err == nil {
			t.Error("Publish from an unsafe storage succeeded")
		}
		wantRef(t, f, url, "refs/heads/new", "")
	}},
}
