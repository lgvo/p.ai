package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// A missing ID of either length Git uses, SHA-1's or SHA-256's, is a
// commit the repository does not hold, not a failed read.
func TestInspectCommitOfAMissingIDOfEitherLengthIsNotPresent(t *testing.T) {
	f := newFixture(t).(*fixture)
	if _, err := f.driver.CreateRepository(t.Context(), "p"); err != nil {
		t.Fatal(err)
	}
	f.SetHead("p", "main", f.Commit("p"))
	for _, id := range []sourcedriver.OID{sourcedriver.OID(strings.Repeat("e", 40)), sourcedriver.OID(strings.Repeat("e", 64))} {
		if c, err := f.driver.InspectCommit(t.Context(), "p", id); err != nil || c != (sourcedriver.Commit{}) {
			t.Errorf("InspectCommit of the missing %d-character ID = %+v, %v; want not present", len(id), c, err)
		}
	}
}

// Git's answer past the driver's byte bound for it is an error, never an
// answer read from part of it: a head whose name is longer than any P
// branch makes the heads that reach a commit too long to read.
func TestAGitAnswerPastItsByteBoundIsAnError(t *testing.T) {
	f := newFixture(t).(*fixture)
	if _, err := f.driver.CreateRepository(t.Context(), "p"); err != nil {
		t.Fatal(err)
	}
	tip := f.Commit("p")
	parts := make([]string, 25)
	for i := range parts {
		parts[i] = strings.Repeat("n", 200)
	}
	long := "refs/heads/" + strings.Join(parts, "/")
	repo, _ := f.driver.repository("p")
	packed := "# pack-refs with: peeled fully-peeled sorted \n" + string(tip) + " " + long + "\n"
	if err := os.WriteFile(filepath.Join(repo, "packed-refs"), []byte(packed), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := f.git("p", "", "for-each-ref", "--format=%(refname)"); got != long {
		t.Fatalf("Git lists %d bytes of refs, want the %d-byte head", len(got), len(long))
	}
	if c, err := f.driver.InspectCommit(t.Context(), "p", tip); err == nil {
		t.Fatalf("InspectCommit with the heads' answer past its bound = %+v, want an error", c)
	}
}
