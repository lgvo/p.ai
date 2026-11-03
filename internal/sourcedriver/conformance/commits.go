package conformance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// CommitControls read and disturb exports and read a repository whole. A
// Fixture's Repositories controls provide them.
type CommitControls interface {
	// Snapshot is a digest of the project's repository, its refs and its
	// objects: two snapshots differ when anything was written.
	Snapshot(project string) string
	// ExportedHistory reads the export as a session's clone would and
	// returns the commit its ref names, then every other commit the clone
	// holds, in any order. On the bundled driver: a clone of the bundle.
	ExportedHistory(e sourcedriver.Export) []sourcedriver.OID
	// CutExport cuts the driver's next Export off by its deadline once it
	// has begun to write into its directory. On the bundled driver: a seam
	// that expires the export's deadline once its work is staged.
	CutExport()
}

// chain writes n commits into the project's repository, each the child of
// the one before and the first a child of parents, and returns them in
// order.
func chain(f Fixture, project string, n int, parents ...sourcedriver.OID) []sourcedriver.OID {
	commits := make([]sourcedriver.OID, 0, n)
	for range n {
		next := f.Natives().Commit(project, parents...)
		commits = append(commits, next)
		parents = []sourcedriver.OID{next}
	}
	return commits
}

// inspectCommit reads one commit through the driver.
func inspectCommit(t *testing.T, f Fixture, project string, id sourcedriver.OID) sourcedriver.Commit {
	t.Helper()
	c, err := f.Driver().InspectCommit(t.Context(), project, id)
	if err != nil {
		t.Fatalf("InspectCommit(%s): %v", id, err)
	}
	return c
}

// losing reads what removing heads would lose through the driver.
func losing(t *testing.T, f Fixture, project string, removed []string) sourcedriver.Loss {
	t.Helper()
	loss, err := f.Driver().LosingCommits(t.Context(), project, removed)
	if err != nil {
		t.Fatalf("LosingCommits(%q): %v", removed, err)
	}
	return loss
}

// sameCommits reports whether a and b hold the same commits, in any order.
func sameCommits(a, b []sourcedriver.OID) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// stagingDirectory is a private directory such as core makes for an export.
func stagingDirectory(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "staging")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// directoryNames lists the names in dir.
func directoryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// keptFile leaves a file in dir that an export must not touch.
func keptFile(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "kept"), []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
}

var commitCases = []testCase{
	{"inspect tells missing, present and reachable commits", func(t *testing.T, f Fixture) {
		repository(t, f, "p")
		repository(t, f, "q")
		base := f.Natives().Commit("p")
		tip := f.Natives().Commit("p", base)
		loose := f.Natives().Commit("p", base)
		f.Natives().SetHead("p", "work", tip)
		for _, c := range []struct {
			what string
			id   sourcedriver.OID
			want sourcedriver.Commit
		}{
			{"a head's tip", tip, sourcedriver.Commit{Present: true, Reachable: true}},
			{"a commit a head reaches", base, sourcedriver.Commit{Present: true, Reachable: true}},
			{"a commit no head reaches", loose, sourcedriver.Commit{Present: true}},
			{"a missing commit", missingCommit, sourcedriver.Commit{}},
			{"another project's commit", f.Natives().Commit("q"), sourcedriver.Commit{}},
			{"a tree", f.Natives().Tree("p"), sourcedriver.Commit{}},
		} {
			if got := inspectCommit(t, f, "p", c.id); got != c.want {
				t.Errorf("InspectCommit of %s = %+v, want %+v", c.what, got, c.want)
			}
		}
	}},
	{"losing commits are those only the removed heads reach, with the observation used", func(t *testing.T, f Fixture) {
		repository(t, f, "p")
		base := f.Natives().Commit("p")
		a := chain(f, "p", 2, base)
		b := f.Natives().Commit("p", base)
		f.Natives().SetHead("p", "a", a[1])
		f.Natives().SetHead("p", "b", b)
		f.Natives().SetHead("p", "same", b)
		heads := []sourcedriver.Head{{Name: "a", Tip: a[1]}, {Name: "b", Tip: b}, {Name: "same", Tip: b}}
		for _, c := range []struct {
			removed []string
			want    []sourcedriver.OID
		}{
			{[]string{"a"}, a},
			{[]string{"b"}, nil},
			{[]string{"b", "same"}, []sourcedriver.OID{b}},
			{[]string{"a", "b", "same"}, append([]sourcedriver.OID{base, b}, a...)},
			{nil, append([]sourcedriver.OID{base, b}, a...)},
			{[]string{"ghost"}, nil},
			{[]string{}, nil},
		} {
			loss := losing(t, f, "p", c.removed)
			if !sameCommits(loss.Commits, c.want) {
				t.Errorf("LosingCommits(%q) = %v, want %v", c.removed, loss.Commits, c.want)
			}
			if !reflect.DeepEqual(loss.Heads, heads) {
				t.Errorf("LosingCommits(%q) observed %v, want %v", c.removed, loss.Heads, heads)
			}
		}
	}},
	{"more than 256 losing commits is ErrLimit", func(t *testing.T, f Fixture) {
		repository(t, f, "p")
		commits := chain(f, "p", sourcedriver.MaxLosingCommits+1)
		f.Natives().SetHead("p", "long", commits[len(commits)-2])
		if loss := losing(t, f, "p", []string{"long"}); len(loss.Commits) != sourcedriver.MaxLosingCommits {
			t.Fatalf("LosingCommits of %d commits returned %d", sourcedriver.MaxLosingCommits, len(loss.Commits))
		}
		f.Natives().SetHead("p", "long", commits[len(commits)-1])
		if loss, err := f.Driver().LosingCommits(t.Context(), "p", []string{"long"}); !errors.Is(err, sourcedriver.ErrLimit) {
			t.Fatalf("LosingCommits of %d commits = %d commits, %v; want ErrLimit", sourcedriver.MaxLosingCommits+1, len(loss.Commits), err)
		}
	}},
	{"a head moved during a loss read is an error, not an observation", func(t *testing.T, f Fixture) {
		tip := withHead(t, f, "p", "work")
		f.Natives().SetHead("p", "other", tip)
		f.Repositories().MoveDuringRead("p", "other", "")
		if loss, err := f.Driver().LosingCommits(t.Context(), "p", []string{"work"}); err == nil {
			t.Fatalf("LosingCommits across a moving head = %+v, want an error", loss)
		}
	}},
	{"an export is a bundle of exactly the commit with its history", func(t *testing.T, f Fixture) {
		repository(t, f, "p")
		history := chain(f, "p", 3)
		later := f.Natives().Commit("p", history[2])
		f.Natives().SetHead("p", "main", later)
		f.Natives().Commit("p", history[1])
		before := f.Repositories().Snapshot("p")
		dir := stagingDirectory(t)
		e, err := f.Driver().Export(t.Context(), "p", history[2], dir)
		if err != nil {
			t.Fatalf("Export: %v", err)
		}
		if e.Commit != history[2] || e.Ref == "" || filepath.Dir(e.Bundle) != dir {
			t.Fatalf("Export = %+v, want a bundle in %s of %s", e, dir, history[2])
		}
		if got := directoryNames(t, dir); !slices.Equal(got, []string{filepath.Base(e.Bundle)}) {
			t.Fatalf("the directory holds %v, want only the bundle", got)
		}
		got := f.Repositories().ExportedHistory(e)
		if len(got) == 0 || got[0] != history[2] || !sameCommits(got, history) {
			t.Fatalf("the bundle holds %v, want %s first and exactly %v", got, history[2], history)
		}
		if f.Repositories().Snapshot("p") != before {
			t.Fatal("Export wrote P's repository")
		}
	}},
	{"an export refuses what is not a commit a head reaches and leaves nothing", func(t *testing.T, f Fixture) {
		repository(t, f, "p")
		repository(t, f, "q")
		base := f.Natives().Commit("p")
		f.Natives().SetHead("p", "main", base)
		refused := map[string]sourcedriver.OID{
			"a missing commit":         missingCommit,
			"a commit no head reaches": f.Natives().Commit("p", base),
			"a tree":                   f.Natives().Tree("p"),
			"another project's commit": f.Natives().Commit("q"),
		}
		before := f.Repositories().Snapshot("p")
		for what, id := range refused {
			dir := stagingDirectory(t)
			keptFile(t, dir)
			if e, err := f.Driver().Export(t.Context(), "p", id, dir); err == nil || errors.Is(err, sourcedriver.ErrInvalid) {
				t.Errorf("Export of %s = %+v, %v; want a refusal that is not ErrInvalid", what, e, err)
			}
			if got := directoryNames(t, dir); !slices.Equal(got, []string{"kept"}) {
				t.Errorf("the refused export of %s left %v", what, got)
			}
		}
		if f.Repositories().Snapshot("p") != before {
			t.Fatal("a refused export wrote P's repository")
		}
	}},
	{"an export requires a private directory and never replaces a file", func(t *testing.T, f Fixture) {
		tip := withHead(t, f, "p", "main")
		groupReadable := stagingDirectory(t)
		if err := os.Chmod(groupReadable, 0o750); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(stagingDirectory(t), link); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		for what, dir := range map[string]string{
			"a relative directory": "staging",
			"an unclean path":      stagingDirectory(t) + "/../staging",
			"a missing directory":  filepath.Join(t.TempDir(), "missing"),
			"a group-readable one": groupReadable,
			"a symbolic link":      link,
			"a file":               file,
		} {
			if e, err := f.Driver().Export(t.Context(), "p", tip, dir); err == nil {
				t.Errorf("Export into %s = %+v, want a refusal", what, e)
			}
		}
		if got := directoryNames(t, groupReadable); len(got) != 0 {
			t.Fatalf("the refused export left %v", got)
		}
		dir := stagingDirectory(t)
		first, err := f.Driver().Export(t.Context(), "p", tip, dir)
		if err != nil {
			t.Fatalf("Export: %v", err)
		}
		written, err := os.ReadFile(first.Bundle)
		if err != nil {
			t.Fatal(err)
		}
		if e, err := f.Driver().Export(t.Context(), "p", tip, dir); err == nil {
			t.Fatalf("a second Export into the same directory = %+v, want a refusal", e)
		}
		if again, err := os.ReadFile(first.Bundle); err != nil || string(again) != string(written) {
			t.Fatalf("the second export replaced the first bundle: %v", err)
		}
	}},
	{"an export cut off by its deadline or its context leaves nothing", func(t *testing.T, f Fixture) {
		tip := withHead(t, f, "p", "main")
		before := f.Repositories().Snapshot("p")
		dir := stagingDirectory(t)
		f.Repositories().CutExport()
		if e, err := f.Driver().Export(t.Context(), "p", tip, dir); err == nil {
			t.Fatalf("Export cut off by its deadline = %+v, want an error", e)
		}
		if got := directoryNames(t, dir); len(got) != 0 {
			t.Fatalf("the cut-off export left %v", got)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if e, err := f.Driver().Export(ctx, "p", tip, dir); err == nil {
			t.Fatalf("Export with a cancelled context = %+v, want an error", e)
		}
		if got := directoryNames(t, dir); len(got) != 0 {
			t.Fatalf("the cancelled export left %v", got)
		}
		if f.Repositories().Snapshot("p") != before {
			t.Fatal("a cut-off export wrote P's repository")
		}
	}},
}
