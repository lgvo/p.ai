package gitnames

import (
	"errors"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// noRenameRule adds nothing to RenameBranch, as the in-memory driver does.
func noRenameRule(string) (bool, error) { return true, nil }

const (
	sha1ID   = "0123456789abcdef0123456789abcdef01234567"
	sha256ID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// The accepted and refused values of each kind, from today's rules:
// validBranch (api, gitservice), ValidateRenameBranch, ValidGitOriginRef,
// ValidateOriginURL (origin_test.go), the creation source selector and
// validOID.
var accepted = map[sourcedriver.NameKind][]string{
	sourcedriver.NameBranch: {"main", "feature/x", "a.b", "A_b-1", "x/y/z", "-lead", "HEAD", strings.Repeat("b", 200)},
	sourcedriver.NameRenameBranch: {"main", "renamed", "archive", "team/topic-1",
		strings.Repeat("r", 100)},
	sourcedriver.NameOriginRef: {"refs/heads/main", "refs/tags/v1.0", "refs/heads/feature/雪", "refs/heads/a@b",
		"refs/heads/-x", "refs/heads/" + strings.Repeat("x", 512)},
	sourcedriver.NameOriginURL: {"ssh://git@example.com/org/repo.git", "ssh://alias.example:2222/a.git",
		"git@host-alias:team/repo.git", "git@host:~/repo.git"},
	sourcedriver.NameCommit: {sha1ID, sha256ID},
}

var refused = map[sourcedriver.NameKind][]string{
	sourcedriver.NameBranch: {"", strings.Repeat("b", 201), "a.lock", "a/b.lock", "a..b", "a//b", "/a", "a/", ".a",
		"a.", "a/.b", "a b", "a~1", "a^", "a:b", "a?b", "a*b", "a[b", `a\b`, "a@{b", "雪", "a\x00b"},
	sourcedriver.NameRenameBranch: {"", strings.Repeat("r", 101), "-x", "HEAD", "../escape", "a//b", ".private",
		"a.lock", "a..b"},
	sourcedriver.NameOriginRef: {"", "main", "refs/heads/", "refs/tags/", "refs/remotes/origin/main", "refs/heads/a..b",
		"refs/heads/a@{1}", "refs/heads/a.", "refs/heads/.a", "refs/heads/a.lock", "refs/heads/a//b",
		"refs/heads/a b", "refs/heads/a~1", "refs/heads/a^", "refs/heads/a:b", "refs/heads/a?", "refs/heads/a*",
		"refs/heads/a[", `refs/heads/a\b`, "refs/heads/a\x7f", "refs/heads/\xff", "refs/heads/" + strings.Repeat("x", 513)},
	sourcedriver.NameOriginURL: {"", "/tmp/a.git", "file:///tmp/a.git", "https://example.com/x",
		"ext::ssh evil", "git@host", "host:repo", "ssh://git:password@host/repo",
		"ssh://host/repo?x=y", "git@host:repo;touch", "git@host:-option",
		"git@host:../repo", "git@host:repo\nX", "ssh://host:0/repo"},
	sourcedriver.NameCommit: {"", strings.Repeat("0", 40), strings.Repeat("0", 64), strings.ToUpper(sha1ID),
		sha1ID[:39], sha1ID + "0", strings.Repeat("g", 40)},
	sourcedriver.NameCreationSource: {"", "main", "refs/heads/", "refs/heads/../x", "refs/heads/雪", "refs/tags/v1",
		strings.Repeat("0", 40), strings.ToUpper(sha1ID)},
	sourcedriver.NameDestination: {"", "main", "refs/heads/", "refs/tags/v1", "refs/heads/a..b", "refs/heads/a.lock"},
	"unknown-kind":               {"main"},
}

func TestCheckAcceptsTodaysValuesOfEachKind(t *testing.T) {
	for kind, values := range accepted {
		for _, v := range values {
			in := sourcedriver.Name{Kind: kind, Value: v}
			out, err := Check([]sourcedriver.Name{in}, noRenameRule)
			if err != nil || len(out) != 1 || out[0] != in {
				t.Errorf("Check(%s %q) = %v, %v; want it back unchanged", kind, v, out, err)
			}
		}
	}
}

func TestCheckRefusesTodaysRefusedValuesOfEachKind(t *testing.T) {
	for kind, values := range refused {
		for _, v := range values {
			in := sourcedriver.Name{Kind: kind, Value: v}
			_, err := Check([]sourcedriver.Name{in}, noRenameRule)
			var invalid *sourcedriver.InvalidName
			if !errors.As(err, &invalid) || invalid.Index != 0 || invalid.Name != in || !errors.Is(err, sourcedriver.ErrInvalid) {
				t.Errorf("Check(%s %q) = %v, want an InvalidName at 0 matching ErrInvalid", kind, v, err)
			}
		}
	}
}

func TestCheckClassifiesCreationSourcesAndDestinations(t *testing.T) {
	in := []sourcedriver.Name{
		{Kind: sourcedriver.NameCreationSource, Value: "refs/heads/feature/x"},
		{Kind: sourcedriver.NameCreationSource, Value: sha1ID},
		{Kind: sourcedriver.NameCreationSource, Value: sha256ID},
		{Kind: sourcedriver.NameDestination, Value: "refs/heads/release/雪"},
	}
	want := []sourcedriver.Name{
		{Kind: sourcedriver.NameBranch, Value: "feature/x"},
		{Kind: sourcedriver.NameCommit, Value: sha1ID},
		{Kind: sourcedriver.NameCommit, Value: sha256ID},
		{Kind: sourcedriver.NameDestination, Value: "release/雪"},
	}
	out, err := Check(in, noRenameRule)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("Check(%v) = %v, want %v", in[i], out[i], want[i])
		}
	}
}

func TestCheckNamesTheFirstValueAtFault(t *testing.T) {
	in := []sourcedriver.Name{
		{Kind: sourcedriver.NameBranch, Value: "main"},
		{Kind: sourcedriver.NameCommit, Value: "nope"},
		{Kind: sourcedriver.NameBranch, Value: "a..b"},
	}
	_, err := Check(in, noRenameRule)
	var invalid *sourcedriver.InvalidName
	if !errors.As(err, &invalid) || invalid.Index != 1 || invalid.Name != in[1] {
		t.Fatalf("Check = %v, want an InvalidName at index 1", err)
	}
}

func TestCheckAppliesTheDriversRenameRuleAfterItsOwn(t *testing.T) {
	var asked []string
	rule := func(name string) (bool, error) {
		asked = append(asked, name)
		return name != "refused-by-git", nil
	}
	names := []sourcedriver.Name{{Kind: sourcedriver.NameRenameBranch, Value: "ok"}, {Kind: sourcedriver.NameRenameBranch, Value: "refused-by-git"}}
	_, err := Check(names, rule)
	var invalid *sourcedriver.InvalidName
	if !errors.As(err, &invalid) || invalid.Index != 1 {
		t.Fatalf("Check = %v, want the driver's refusal at index 1", err)
	}
	if _, err := Check([]sourcedriver.Name{{Kind: sourcedriver.NameRenameBranch, Value: "-x"}}, rule); err == nil {
		t.Fatal("Check accepted a name its own rule refuses")
	}
	if len(asked) != 2 {
		t.Fatalf("the driver's rule was asked about %q, want only the names the shared rule accepts", asked)
	}
}

func TestCheckFailsWhenTheDriversRuleCannotDecide(t *testing.T) {
	broken := errors.New("git did not run")
	_, err := Check([]sourcedriver.Name{{Kind: sourcedriver.NameRenameBranch, Value: "ok"}}, func(string) (bool, error) { return false, broken })
	if !errors.Is(err, broken) || errors.Is(err, sourcedriver.ErrInvalid) {
		t.Fatalf("Check with a failing rule = %v, want that failure and not ErrInvalid", err)
	}
}

func TestHeadEffectNamesItsStates(t *testing.T) {
	create, del := sourcedriver.EffectCreateBranch, sourcedriver.EffectDeleteBranch
	for _, c := range []struct {
		e    sourcedriver.Effect
		want bool
	}{
		{sourcedriver.Effect{Kind: create, Branch: "work", New: sha1ID}, true},
		{sourcedriver.Effect{Kind: del, Branch: "work", Old: sha1ID}, true},
		{sourcedriver.Effect{Kind: create, Branch: "work", Old: sha1ID, New: sha1ID}, false},
		{sourcedriver.Effect{Kind: create, Branch: "work"}, false},
		{sourcedriver.Effect{Kind: del, Branch: "work", Old: sha1ID, New: sha1ID}, false},
		{sourcedriver.Effect{Kind: del, Branch: "work"}, false},
		{sourcedriver.Effect{Kind: del, Branch: "a..b", Old: sha1ID}, false},
		{sourcedriver.Effect{Kind: create, Branch: "work", New: sourcedriver.OID(strings.Repeat("0", 40))}, false},
		{sourcedriver.Effect{Kind: sourcedriver.EffectService, Branch: "work", New: sha1ID}, false},
	} {
		if got := HeadEffect(c.e); got != c.want {
			t.Errorf("HeadEffect(%+v) = %v, want %v", c.e, got, c.want)
		}
	}
}
