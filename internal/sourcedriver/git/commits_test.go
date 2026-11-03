package git

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// Snapshot lists every path under the repository directory with its type
// and mode, a regular file's digest and a link's target, so a change to
// any file, the config, hooks, incarnation and objects included, shows.
func (f *fixture) Snapshot(project string) string {
	f.t.Helper()
	repo, err := f.driver.repository(project)
	if err != nil {
		f.t.Fatal(err)
	}
	var paths []string
	err = filepath.WalkDir(repo, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repo, path)
		line := fmt.Sprintf("%s %v", rel, info.Mode())
		switch {
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			line += fmt.Sprintf(" %x", sha256.Sum256(data))
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			line += " -> " + target
		}
		paths = append(paths, line)
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.Join(paths, "\n")
}

// ExportedHistory clones the bundle, as a session's clone would fetch it,
// and lists the commit its ref names, then every commit the clone holds.
func (f *fixture) ExportedHistory(e sourcedriver.Export) []sourcedriver.OID {
	f.t.Helper()
	clone := filepath.Join(f.t.TempDir(), "clone.git")
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Env = append(cmd.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			f.t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("clone", "--quiet", "--mirror", e.Bundle, clone)
	tip := run("--git-dir="+clone, "rev-parse", "--verify", e.Ref+"^{commit}")
	history := []sourcedriver.OID{sourcedriver.OID(tip)}
	for _, id := range strings.Fields(run("--git-dir="+clone, "rev-list", "--all")) {
		if id != tip {
			history = append(history, sourcedriver.OID(id))
		}
	}
	return history
}

// CutExport ends the next export's deadline once its work is staged,
// through the driver's export-staged seam.
func (f *fixture) CutExport() {
	d := f.driver
	d.seams.mu.Lock()
	defer d.seams.mu.Unlock()
	d.seams.exportStaged = func(cut context.CancelFunc) { cut() }
}

// The snapshot export and name cases compare shows every write to the
// repository: one to its config and one to the bytes of an object.
func TestSnapshotShowsAChangedConfigAndAChangedObject(t *testing.T) {
	f := newFixture(t).(*fixture)
	if _, err := f.driver.CreateRepository(t.Context(), "p"); err != nil {
		t.Fatal(err)
	}
	commit := f.Commit("p")
	before := f.Snapshot("p")
	f.git("p", "", "config", "core.logAllRefUpdates", "true")
	afterConfig := f.Snapshot("p")
	if afterConfig == before {
		t.Fatal("the snapshot did not change with the config")
	}
	repo, _ := f.driver.repository("p")
	object := filepath.Join(repo, "objects", string(commit[:2]), string(commit[2:]))
	if err := os.Chmod(object, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(object, 0o444); err != nil {
		t.Fatal(err)
	}
	if f.Snapshot("p") != afterConfig {
		t.Fatal("the snapshot changed with nothing written")
	}
	data, err := os.ReadFile(object)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(object, 0o600); err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 0xff
	if err := os.WriteFile(object, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(object, 0o444); err != nil {
		t.Fatal(err)
	}
	if f.Snapshot("p") == afterConfig {
		t.Fatal("the snapshot did not change with an object's bytes")
	}
}

// exportDirectory is a private directory such as core makes for an export.
func exportDirectory(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "staging")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A commit whose object Git cannot read is a failed read, never a missing
// commit, though Git answers that it is missing.
func TestACorruptCommitIsAnErrorNotAMissingCommit(t *testing.T) {
	f := newFixture(t).(*fixture)
	if _, err := f.driver.CreateRepository(t.Context(), "p"); err != nil {
		t.Fatal(err)
	}
	tip := f.Commit("p")
	f.SetHead("p", "main", tip)
	corrupt(t, f, "p", tip)
	if c, err := f.driver.InspectCommit(t.Context(), "p", tip); err == nil {
		t.Errorf("InspectCommit of a corrupt commit = %+v, want an error", c)
	}
	dir := exportDirectory(t)
	if e, err := f.driver.Export(t.Context(), "p", tip, dir); err == nil || errors.Is(err, sourcedriver.ErrConflict) {
		t.Errorf("Export of a corrupt commit = %+v, %v; want an error that is not ErrConflict", e, err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Errorf("the failed export left %v, %v", entries, err)
	}
}

// missingCommit is a commit ID no test repository holds.
const missingCommit = sourcedriver.OID("ffffffffffffffffffffffffffffffffffffffff")

// corrupt overwrites the loose object of id with bytes Git cannot read.
func corrupt(t *testing.T, f *fixture, project string, id sourcedriver.OID) {
	t.Helper()
	repo, _ := f.driver.repository(project)
	object := filepath.Join(repo, "objects", string(id[:2]), string(id[2:]))
	if err := os.Chmod(object, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(object, []byte("not a Git object"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// An intact commit that only a head whose tip Git cannot read reaches is a
// failed read of what reaches it, never a commit no head reaches, though
// Git lists no head and exits 0.
func TestACorruptHeadIsAFailedReachabilityRead(t *testing.T) {
	f := newFixture(t).(*fixture)
	if _, err := f.driver.CreateRepository(t.Context(), "p"); err != nil {
		t.Fatal(err)
	}
	base := f.Commit("p")
	tip := f.Commit("p", base)
	f.SetHead("p", "main", tip)
	corrupt(t, f, "p", tip)
	if c, err := f.driver.InspectCommit(t.Context(), "p", base); err == nil {
		t.Errorf("InspectCommit of a commit only a corrupt head reaches = %+v, want an error", c)
	}
	if e, err := f.driver.Export(t.Context(), "p", base, exportDirectory(t)); err == nil || errors.Is(err, sourcedriver.ErrConflict) {
		t.Errorf("Export of a commit only a corrupt head reaches = %+v, %v; want an error that is not ErrConflict", e, err)
	}
}

// A warning Git writes beside a complete answer, such as a deprecated
// setting in the repository's config, fails no read: present and missing
// commits answer as they would without it.
func TestAGitWarningIsNotAFailedRead(t *testing.T) {
	f := newFixture(t).(*fixture)
	if _, err := f.driver.CreateRepository(t.Context(), "p"); err != nil {
		t.Fatal(err)
	}
	tip := f.Commit("p")
	f.SetHead("p", "main", tip)
	f.git("p", "", "config", "core.fsyncObjectFiles", "true")
	ctx := t.Context()
	if c, err := f.driver.InspectCommit(ctx, "p", tip); err != nil || c != (sourcedriver.Commit{Present: true, Reachable: true}) {
		t.Errorf("InspectCommit of a head's tip = %+v, %v; want present and reachable", c, err)
	}
	if c, err := f.driver.InspectCommit(ctx, "p", missingCommit); err != nil || c != (sourcedriver.Commit{}) {
		t.Errorf("InspectCommit of a missing commit = %+v, %v; want not present", c, err)
	}
	if heads, err := f.driver.Branches(ctx, "p"); err != nil || len(heads) != 1 {
		t.Errorf("Branches = %v, %v; want main", heads, err)
	}
	if loss, err := f.driver.LosingCommits(ctx, "p", []string{"main"}); err != nil || len(loss.Commits) != 1 {
		t.Errorf("LosingCommits(main) = %+v, %v; want its one commit", loss, err)
	}
	if err := f.driver.CreateBranch(ctx, "p", "work", tip, func() error { return nil }); err != nil {
		t.Errorf("CreateBranch at a present commit: %v", err)
	}
	if err := f.driver.CreateBranch(ctx, "p", "ghost", missingCommit, func() error { return nil }); !errors.Is(err, sourcedriver.ErrConflict) {
		t.Errorf("CreateBranch at a missing commit: %v, want ErrConflict", err)
	}
	if _, err := f.driver.Export(ctx, "p", tip, exportDirectory(t)); err != nil {
		t.Errorf("Export: %v", err)
	}
}

// An export Git fails after its work is staged leaves nothing in its
// directory and writes nothing to P's repository: a commit whose tree P
// does not hold passes the checks, and Git fails to pack it.
func TestAnExportGitFailsToWriteLeavesNothing(t *testing.T) {
	f := newFixture(t).(*fixture)
	if _, err := f.driver.CreateRepository(t.Context(), "p"); err != nil {
		t.Fatal(err)
	}
	broken := sourcedriver.OID(f.git("p", "tree "+strings.Repeat("1", 40)+"\nauthor P <p@example.test> 0 +0000\ncommitter P <p@example.test> 0 +0000\n\nbroken\n",
		"hash-object", "-t", "commit", "-w", "--stdin"))
	f.SetHead("p", "broken", broken)
	before := f.Snapshot("p")
	dir := exportDirectory(t)
	if e, err := f.driver.Export(t.Context(), "p", broken, dir); err == nil {
		t.Fatalf("Export of a commit without its tree = %+v, want an error", e)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("the failed export left %v, %v", entries, err)
	}
	if f.Snapshot("p") != before {
		t.Fatal("the failed export wrote P's repository")
	}
}
