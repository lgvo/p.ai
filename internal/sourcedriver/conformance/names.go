package conformance

import (
	"errors"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

const (
	sha1Commit   = "0123456789abcdef0123456789abcdef01234567"
	sha256Commit = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// acceptedNames are values of each kind today's rules accept, each with the
// value CheckNames returns for core.
var acceptedNames = []struct{ in, out sourcedriver.Name }{
	{named(sourcedriver.NameBranch, "main"), named(sourcedriver.NameBranch, "main")},
	{named(sourcedriver.NameBranch, "team/feature-1.x_y"), named(sourcedriver.NameBranch, "team/feature-1.x_y")},
	{named(sourcedriver.NameBranch, strings.Repeat("b", 200)), named(sourcedriver.NameBranch, strings.Repeat("b", 200))},
	{named(sourcedriver.NameRenameBranch, "renamed"), named(sourcedriver.NameRenameBranch, "renamed")},
	{named(sourcedriver.NameRenameBranch, strings.Repeat("r", 100)), named(sourcedriver.NameRenameBranch, strings.Repeat("r", 100))},
	{named(sourcedriver.NameOriginRef, "refs/heads/main"), named(sourcedriver.NameOriginRef, "refs/heads/main")},
	{named(sourcedriver.NameOriginRef, "refs/tags/v1.0"), named(sourcedriver.NameOriginRef, "refs/tags/v1.0")},
	{named(sourcedriver.NameOriginRef, "refs/heads/feature/雪"), named(sourcedriver.NameOriginRef, "refs/heads/feature/雪")},
	{named(sourcedriver.NameOriginURL, "ssh://git@example.com/org/repo.git"), named(sourcedriver.NameOriginURL, "ssh://git@example.com/org/repo.git")},
	{named(sourcedriver.NameOriginURL, "git@host-alias:team/repo.git"), named(sourcedriver.NameOriginURL, "git@host-alias:team/repo.git")},
	{named(sourcedriver.NameCommit, sha1Commit), named(sourcedriver.NameCommit, sha1Commit)},
	{named(sourcedriver.NameCommit, sha256Commit), named(sourcedriver.NameCommit, sha256Commit)},
	{named(sourcedriver.NameCreationSource, "refs/heads/team/main"), named(sourcedriver.NameBranch, "team/main")},
	{named(sourcedriver.NameCreationSource, sha1Commit), named(sourcedriver.NameCommit, sha1Commit)},
	{named(sourcedriver.NameDestination, "refs/heads/release/雪"), named(sourcedriver.NameDestination, "release/雪")},
}

// refusedNames are values of each kind today's rules refuse.
var refusedNames = []sourcedriver.Name{
	named(sourcedriver.NameBranch, ""),
	named(sourcedriver.NameBranch, strings.Repeat("b", 201)),
	named(sourcedriver.NameBranch, "a..b"),
	named(sourcedriver.NameBranch, "a.lock"),
	named(sourcedriver.NameBranch, "a//b"),
	named(sourcedriver.NameBranch, ".hidden"),
	named(sourcedriver.NameBranch, "a b"),
	named(sourcedriver.NameBranch, "a~1"),
	named(sourcedriver.NameBranch, "雪"),
	named(sourcedriver.NameRenameBranch, strings.Repeat("r", 101)),
	named(sourcedriver.NameRenameBranch, "-x"),
	named(sourcedriver.NameRenameBranch, "HEAD"),
	named(sourcedriver.NameRenameBranch, "../escape"),
	named(sourcedriver.NameOriginRef, "main"),
	named(sourcedriver.NameOriginRef, "refs/remotes/origin/main"),
	named(sourcedriver.NameOriginRef, "refs/heads/a..b"),
	named(sourcedriver.NameOriginRef, "refs/heads/a@{1}"),
	named(sourcedriver.NameOriginRef, "refs/heads/a b"),
	named(sourcedriver.NameOriginURL, "/tmp/a.git"),
	named(sourcedriver.NameOriginURL, "https://example.com/x"),
	named(sourcedriver.NameOriginURL, "ext::ssh evil"),
	named(sourcedriver.NameOriginURL, "ssh://git:password@host/repo"),
	named(sourcedriver.NameOriginURL, "git@host:-option"),
	named(sourcedriver.NameCommit, strings.Repeat("0", 40)),
	named(sourcedriver.NameCommit, strings.ToUpper(sha1Commit)),
	named(sourcedriver.NameCommit, sha1Commit[:12]),
	named(sourcedriver.NameCreationSource, "main"),
	named(sourcedriver.NameCreationSource, "refs/tags/v1"),
	named(sourcedriver.NameCreationSource, "refs/heads/a..b"),
	named(sourcedriver.NameDestination, "refs/tags/v1"),
	named(sourcedriver.NameDestination, "main"),
	named(sourcedriver.NameDestination, "refs/heads/"),
	named("unknown-kind", "main"),
}

// invalidBranches and invalidCommits are values CheckNames refuses as a
// branch and as a commit, for the operations that take them.
var (
	invalidBranches = []string{"", "a..b", "a b", "../escape", "a.lock", strings.Repeat("b", 201)}
	invalidCommits  = []sourcedriver.OID{"", sourcedriver.OID(strings.Repeat("0", 40)), sourcedriver.OID(strings.ToUpper(sha1Commit)), sourcedriver.OID(sha1Commit[:12]), "refs/heads/main"}
)

func named(kind sourcedriver.NameKind, value string) sourcedriver.Name {
	return sourcedriver.Name{Kind: kind, Value: value}
}

// refusedAsInvalid reports a call that did not refuse with ErrInvalid.
func refusedAsInvalid(t *testing.T, call string, err error) {
	t.Helper()
	if !errors.Is(err, sourcedriver.ErrInvalid) {
		t.Errorf("%s = %v, want ErrInvalid", call, err)
	}
}

var nameCases = []testCase{
	{"today's accepted values of each kind come back as core uses them", func(t *testing.T, f Fixture) {
		in := make([]sourcedriver.Name, len(acceptedNames))
		for i, c := range acceptedNames {
			in[i] = c.in
		}
		out, err := f.Driver().CheckNames(t.Context(), in)
		if err != nil {
			t.Fatalf("CheckNames of accepted values: %v", err)
		}
		if len(out) != len(in) {
			t.Fatalf("CheckNames returned %d values for %d", len(out), len(in))
		}
		for i, c := range acceptedNames {
			if out[i] != c.out {
				t.Errorf("CheckNames(%v) = %v, want %v", c.in, out[i], c.out)
			}
		}
	}},
	{"today's refused values of each kind are refused", func(t *testing.T, f Fixture) {
		for _, n := range refusedNames {
			_, err := f.Driver().CheckNames(t.Context(), []sourcedriver.Name{n})
			var invalid *sourcedriver.InvalidName
			if !errors.As(err, &invalid) || invalid.Index != 0 || invalid.Name != n || !errors.Is(err, sourcedriver.ErrInvalid) {
				t.Errorf("CheckNames(%v) = %v, want an InvalidName at 0 matching ErrInvalid", n, err)
			}
		}
	}},
	{"the first value at fault is named", func(t *testing.T, f Fixture) {
		in := []sourcedriver.Name{acceptedNames[0].in, acceptedNames[5].in, refusedNames[2], refusedNames[0]}
		_, err := f.Driver().CheckNames(t.Context(), in)
		var invalid *sourcedriver.InvalidName
		if !errors.As(err, &invalid) || invalid.Index != 2 || invalid.Name != in[2] {
			t.Fatalf("CheckNames = %v, want an InvalidName at 2 for %v", err, in[2])
		}
	}},
	{"every operation refuses what CheckNames refuses", func(t *testing.T, f Fixture) {
		tip := withHead(t, f, "p", "main")
		before := f.Repositories().Snapshot("p")
		ctx, d := t.Context(), f.Driver()
		for _, bad := range invalidBranches {
			_, _, err := d.InspectBranch(ctx, "p", bad)
			refusedAsInvalid(t, "InspectBranch("+bad+")", err)
			refusedAsInvalid(t, "CreateBranch("+bad+")", d.CreateBranch(ctx, "p", bad, tip, unexpectedGate(t)))
			refusedAsInvalid(t, "DeleteBranch("+bad+")", d.DeleteBranch(ctx, "p", bad, tip, unexpectedGate(t)))
			_, err = d.LosingCommits(ctx, "p", []string{"main", bad})
			refusedAsInvalid(t, "LosingCommits("+bad+")", err)
			_, err = d.Settle(ctx, sourcedriver.Effect{Kind: sourcedriver.EffectCreateBranch, Project: "p", Branch: bad, New: tip})
			refusedAsInvalid(t, "Settle of a creation of "+bad, err)
		}
		for _, bad := range invalidCommits {
			call := func(op string) string { return op + "(" + string(bad) + ")" }
			refusedAsInvalid(t, call("CreateBranch"), d.CreateBranch(ctx, "p", "work", bad, unexpectedGate(t)))
			refusedAsInvalid(t, call("DeleteBranch"), d.DeleteBranch(ctx, "p", "main", bad, unexpectedGate(t)))
			_, err := d.InspectCommit(ctx, "p", bad)
			refusedAsInvalid(t, call("InspectCommit"), err)
			_, err = d.Export(ctx, "p", bad, stagingDirectory(t))
			refusedAsInvalid(t, call("Export"), err)
			_, err = d.Settle(ctx, sourcedriver.Effect{Kind: sourcedriver.EffectDeleteBranch, Project: "p", Branch: "main", Old: bad})
			refusedAsInvalid(t, call("Settle of a deletion"), err)
		}
		if f.Repositories().Snapshot("p") != before {
			t.Fatal("a refused operation wrote P's repository")
		}
	}},
}
