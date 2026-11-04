package git

import (
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver/gitnames"
)

// gitnames.RenameBranch claims that, of the names a P branch allows, `git
// check-ref-format --branch` refuses only a leading '-' and HEAD. The
// bundled driver runs the command as well; this holds the claim to Git.
func TestCheckRefFormatRefusesOnlyWhatRenameBranchRefuses(t *testing.T) {
	d := newFixture(t).(*fixture).driver
	for _, name := range []string{"main", "-x", "-", "HEAD", "a/HEAD", "HEAD/a", "x.y", "a-b/c_d", "A.B-c_d/e", "a.b.c", "_", "x-"} {
		if !gitnames.Branch(name) {
			t.Fatalf("%q is not a P branch; pick names a P branch allows", name)
		}
		ok, err := d.checkRefFormat(t.Context(), name)
		if err != nil {
			t.Fatalf("check-ref-format %q: %v", name, err)
		}
		if want := gitnames.RenameBranch(name); ok != want {
			t.Errorf("git check-ref-format --branch %q accepts: %v; gitnames.RenameBranch says %v", name, ok, want)
		}
	}
}
