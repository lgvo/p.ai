package git

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// hostileRepository makes the project's repository try to run code on every
// Git command: hooks in its own hooks directory and in a hooks path its
// config names, an fsmonitor command, and a promisor remote whose
// upload-pack command a lazy fetch of a missing object would run. Each
// writes marker.
func hostileRepository(t *testing.T, f *fixture, project, marker string) {
	t.Helper()
	repo, err := f.driver.repository(project)
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$0\" >> " + marker + "\ncat >/dev/null 2>&1\nexit 0\n"
	elsewhere := filepath.Join(t.TempDir(), "hooks")
	for _, dir := range []string{filepath.Join(repo, "hooks"), elsewhere} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, hook := range []string{"reference-transaction", "post-update", "pre-commit", "post-checkout", "pre-auto-gc", "post-index-change"} {
			if err := os.WriteFile(filepath.Join(dir, hook), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	command := filepath.Join(elsewhere, "pre-commit")
	f.git(project, "", "config", "core.hooksPath", elsewhere)
	f.git(project, "", "config", "core.fsmonitor", command)
	f.git(project, "", "config", "core.repositoryformatversion", "1")
	f.git(project, "", "config", "extensions.partialClone", "origin")
	f.git(project, "", "config", "remote.origin.url", t.TempDir())
	f.git(project, "", "config", "remote.origin.promisor", "true")
	f.git(project, "", "config", "remote.origin.uploadpack", command)
}

// The driver runs no code a repository can change, whatever its hooks and
// config ask for.
func TestOperationsRunNoCodeTheRepositoryNames(t *testing.T) {
	f := newFixture(t).(*fixture)
	ctx := t.Context()
	if _, err := f.driver.CreateRepository(ctx, "p"); err != nil {
		t.Fatal(err)
	}
	base := f.Commit("p")
	f.SetHead("p", "main", base)
	marker := filepath.Join(t.TempDir(), "ran")
	hostileRepository(t, f, "p", marker)

	if err := f.driver.CreateBranch(ctx, "p", "work", base, func() error { return nil }); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := f.driver.DeleteBranch(ctx, "p", "work", base, func() error { return nil }); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	if _, err := f.driver.InspectCommit(ctx, "p", sourcedriver.OID("ffffffffffffffffffffffffffffffffffffffff")); err != nil {
		t.Fatalf("InspectCommit of a missing commit: %v", err)
	}
	if _, err := f.driver.InspectCommit(ctx, "p", base); err != nil {
		t.Fatalf("InspectCommit: %v", err)
	}
	if _, err := f.driver.Branches(ctx, "p"); err != nil {
		t.Fatalf("Branches: %v", err)
	}
	if _, _, err := f.driver.InspectBranch(ctx, "p", "main"); err != nil {
		t.Fatalf("InspectBranch: %v", err)
	}
	if _, err := f.driver.LosingCommits(ctx, "p", nil); err != nil {
		t.Fatalf("LosingCommits: %v", err)
	}
	if _, err := f.driver.CreateRepository(ctx, "p"); err != nil {
		t.Fatalf("CreateRepository of the existing repository: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "staging")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := f.driver.Export(ctx, "p", base, dir); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if ran, err := os.ReadFile(marker); err == nil {
		t.Fatalf("the repository's code ran:\n%s", ran)
	}
}
