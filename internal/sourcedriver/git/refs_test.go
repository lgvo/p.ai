package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// Refs outside refs/heads/ are not P's heads, a sibling namespace whose name
// starts like it included: Branches does not list them, and a commit only
// they hold is not reachable, so not exported.
func TestOnlyHeadsAreBranchesAndReachCommits(t *testing.T) {
	f := newFixture(t).(*fixture)
	if _, err := f.driver.CreateRepository(t.Context(), "p"); err != nil {
		t.Fatal(err)
	}
	tip := f.Commit("p")
	hidden := f.Commit("p")
	f.SetHead("p", "main", tip)
	f.SetHead("p", "work", tip)
	for _, ref := range []string{"refs/p/secret", "refs/heads-private/secret", "refs/tags/v1"} {
		f.SetOtherRef("p", ref, hidden)
	}
	want := []sourcedriver.Head{{Name: "main", Tip: tip}, {Name: "work", Tip: tip}}
	if heads, err := f.driver.Branches(t.Context(), "p"); err != nil || !reflect.DeepEqual(heads, want) {
		t.Fatalf("Branches = %v, %v; want %v", heads, err, want)
	}
	if commit, err := f.driver.InspectCommit(t.Context(), "p", hidden); err != nil || commit != (sourcedriver.Commit{Present: true}) {
		t.Fatalf("InspectCommit of a commit only other refs hold = %+v, %v; want present and not reachable", commit, err)
	}
	if e, err := f.driver.Export(t.Context(), "p", hidden, exportDirectory(t)); !errors.Is(err, sourcedriver.ErrConflict) {
		t.Fatalf("Export of a commit only other refs hold = %+v, %v; want ErrConflict", e, err)
	}
}

// Git keeps a head as a path, so refs/heads/a and refs/heads/a/b cannot both
// exist: creating one where the other is refuses and leaves the other.
func TestCreationRefusesANameThatCollidesWithAHeadsPath(t *testing.T) {
	f := newFixture(t).(*fixture)
	if _, err := f.driver.CreateRepository(t.Context(), "p"); err != nil {
		t.Fatal(err)
	}
	tip := f.Commit("p")
	f.SetHead("p", "feature/new", tip)
	for _, name := range []string{"feature/new/child", "feature"} {
		if err := f.driver.CreateBranch(t.Context(), "p", name, tip, func() error { return nil }); err == nil {
			t.Errorf("CreateBranch(%s) beside the head feature/new succeeded", name)
		}
	}
	want := []sourcedriver.Head{{Name: "feature/new", Tip: tip}}
	if heads, err := f.driver.Branches(t.Context(), "p"); err != nil || !reflect.DeepEqual(heads, want) {
		t.Fatalf("after the refused creations Branches = %v, %v; want %v", heads, err, want)
	}
}

// A creation interrupted before it renamed its repository into place leaves
// a temporary directory beside the repositories; a later creation still makes
// the repository, and converges.
func TestCreationAfterAnInterruptedOneMakesTheRepository(t *testing.T) {
	f := newFixture(t).(*fixture)
	if err := f.driver.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(f.storage, "repositories", ".p-git-init-interrupted"), 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := f.driver.CreateRepository(t.Context(), "p")
	if err != nil || !first.Exists {
		t.Fatalf("CreateRepository = %+v, %v; want the repository", first, err)
	}
	if again, err := f.driver.CreateRepository(t.Context(), "p"); err != nil || again != first {
		t.Fatalf("a repeated CreateRepository = %+v, %v; want %+v", again, err, first)
	}
}

// A read cut off by its context answers the context's error at once, never
// an absence.
func TestAReadCutOffByItsContextAnswersItsErrorPromptly(t *testing.T) {
	f := newFixture(t).(*fixture)
	if _, err := f.driver.CreateRepository(t.Context(), "p"); err != nil {
		t.Fatal(err)
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep unavailable")
	}
	slow := filepath.Join(t.TempDir(), "slow-git")
	script := "#!/bin/sh\nfor arg; do if [ \"$arg\" = show-ref ]; then exec '" + sleep + "' 30; fi; done\nexec '" + f.driver.gitPath + "' \"$@\"\n"
	if err := os.WriteFile(slow, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	f.driver.gitPath = slow
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if tip, ok, err := f.driver.InspectBranch(ctx, "p", "main"); !errors.Is(err, context.DeadlineExceeded) || ok {
		t.Fatalf("InspectBranch cut off = %q, %v, %v; want the deadline's error", tip, ok, err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("InspectBranch answered %v after its deadline", elapsed)
	}
}

// A head Git cannot read, which its listings skip, is a failed read, never
// an absent head: Branches fails, and so does every answer that a commit
// no head reaches, though Git lists no head and exits 0. Only a commit an
// intact head reaches is still read.
func TestABrokenHeadIsAFailedReadNotAnAbsentHead(t *testing.T) {
	for _, c := range []struct {
		what string
		// file and content are the loose ref written under refs/heads/;
		// name is the branch InspectBranch reads, when one can name it.
		file, content, name string
	}{
		{"a head whose ref Git cannot parse", "work", "zzzz\n", "work"},
		{"a ref whose name Git refuses", "work..old", "", ""},
		{"a head at an object the repository does not hold", "work", string(missingCommit) + "\n", "work"},
	} {
		t.Run(c.what, func(t *testing.T) {
			f := newFixture(t).(*fixture)
			if _, err := f.driver.CreateRepository(t.Context(), "p"); err != nil {
				t.Fatal(err)
			}
			kept := f.Commit("p")
			f.SetHead("p", "main", kept)
			lost := f.Commit("p")
			content := c.content
			if content == "" {
				content = string(lost) + "\n"
			}
			repo, _ := f.driver.repository("p")
			if err := os.WriteFile(filepath.Join(repo, "refs", "heads", c.file), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if heads, err := f.driver.Branches(ctx, "p"); err == nil {
				t.Errorf("Branches = %v, want an error", heads)
			}
			if c.name != "" {
				if tip, ok, err := f.driver.InspectBranch(ctx, "p", c.name); err == nil {
					t.Errorf("InspectBranch(%s) = %q, %v; want an error", c.name, tip, ok)
				}
			}
			if commit, err := f.driver.InspectCommit(ctx, "p", lost); err == nil {
				t.Errorf("InspectCommit of a commit no intact head reaches = %+v, want an error", commit)
			}
			if e, err := f.driver.Export(ctx, "p", lost, exportDirectory(t)); err == nil || errors.Is(err, sourcedriver.ErrConflict) {
				t.Errorf("Export of a commit no intact head reaches = %+v, %v; want an error that is not ErrConflict", e, err)
			}
			if commit, err := f.driver.InspectCommit(ctx, "p", kept); err != nil || commit != (sourcedriver.Commit{Present: true, Reachable: true}) {
				t.Errorf("InspectCommit of an intact head's tip = %+v, %v; want present and reachable", commit, err)
			}
		})
	}
}
