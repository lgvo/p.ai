package git

import (
	"context"
	"crypto/sha256"
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
