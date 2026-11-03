package git

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

func TestPushAnswer(t *testing.T) {
	const dest = "refs/heads/main"
	line := func(flag, summary string) string {
		return "To git@fixture:o1.git\n" + flag + "\t" + oidA + ":" + dest + "\t" + summary + "\nDone\n"
	}
	for _, c := range []struct {
		name      string
		out       string
		relation  sourcedriver.Relation
		succeeded bool
		want      sourcedriver.PublishStatus // empty: ErrUnknownOutcome
	}{
		{"an up-to-date destination", line("=", "[up to date]"), sourcedriver.RelationFastForward, true, sourcedriver.PublishSatisfied},
		{"a new destination", line("*", "[new branch]"), sourcedriver.RelationAbsent, true, sourcedriver.PublishCreated},
		{"a fast-forward", line(" ", oidB[:7]+".."+oidA[:7]), sourcedriver.RelationFastForward, true, sourcedriver.PublishAdvanced},
		{"a rejection", line("!", "[rejected] (non-fast-forward)"), sourcedriver.RelationFastForward, false, sourcedriver.PublishRefused},
		{"a remote rejection", line("!", "[remote rejected] (pre-receive hook declined)"), sourcedriver.RelationAbsent, false, sourcedriver.PublishRefused},
		{"a creation where a fast-forward was compared", line("*", "[new branch]"), sourcedriver.RelationFastForward, true, ""},
		{"a fast-forward where an absent destination was compared", line(" ", oidB[:7]+".."+oidA[:7]), sourcedriver.RelationAbsent, true, ""},
		{"a remote failure", line("!", "[remote failure] (remote failed to report status)"), sourcedriver.RelationAbsent, false, ""},
		{"a success line from a failed push", line("*", "[new branch]"), sourcedriver.RelationAbsent, false, ""},
		{"another ref's answer", "*\t" + oidA + ":refs/heads/other\t[new branch]\n", sourcedriver.RelationAbsent, true, ""},
		{"no answer", "", sourcedriver.RelationAbsent, false, ""},
	} {
		got, err := pushAnswer(c.out, dest, c.relation, c.succeeded)
		if c.want == "" && (!errors.Is(err, sourcedriver.ErrUnknownOutcome) || got != "") {
			t.Errorf("%s: pushAnswer = %q, %v; want ErrUnknownOutcome", c.name, got, err)
		}
		if c.want != "" && (err != nil || got != c.want) {
			t.Errorf("%s: pushAnswer = %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}

// TestPushThatCannotStartIsAPlainError checks that a push whose git could
// not start after the gate is a plain error, not an unknown outcome: no
// process ran, so nothing reached the origin.
func TestPushThatCannotStartIsAPlainError(t *testing.T) {
	f := newFixture(t).(*fixture)
	url := f.NewOrigin()
	if _, err := f.Driver().CreateRepository(t.Context(), "p"); err != nil {
		t.Fatal(err)
	}
	commit := f.Commit("p")
	f.SetHead("p", "work", commit)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+shellQuote(git)+" \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	d := &Driver{storage: f.storage, gitPath: wrapper}
	gateCalls := 0
	removeGit := func() error { gateCalls++; return os.Remove(wrapper) }
	publication := sourcedriver.Publication{URL: url, Branch: "work", Commit: commit, Destination: "main", Relation: sourcedriver.RelationAbsent}
	status, err := d.Publish(t.Context(), "p", publication, removeGit)
	if err == nil || errors.Is(err, sourcedriver.ErrUnknownOutcome) || status != "" || gateCalls != 1 {
		t.Fatalf("Publish whose git could not start = %q, %v after %d gate calls; want a plain error after 1", status, err, gateCalls)
	}
	if got := f.OriginRef(url, "refs/heads/main"); got != "" {
		t.Fatalf("the origin's main names %q, want nothing", got)
	}
}
