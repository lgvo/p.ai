package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// LoosenStorage makes the storage directory group-writable.
func (f *fixture) LoosenStorage() {
	f.t.Helper()
	if err := os.MkdirAll(f.storage, 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chmod(f.storage, 0o770); err != nil {
		f.t.Fatal(err)
	}
}

// DefaultBranch reads the branch HEAD names.
func (f *fixture) DefaultBranch(project string) string {
	ref := f.git(project, "", "symbolic-ref", "HEAD")
	name, ok := strings.CutPrefix(ref, "refs/heads/")
	if !ok {
		f.t.Fatalf("HEAD names %q, not a head", ref)
	}
	return name
}

// SetDefaultBranch points HEAD at refs/heads/<name>.
func (f *fixture) SetDefaultBranch(project, name string) {
	f.git(project, "", "symbolic-ref", "HEAD", "refs/heads/"+name)
}

// LeavePartial leaves a directory with objects and refs and no HEAD at the
// project's address.
func (f *fixture) LeavePartial(project string) {
	f.t.Helper()
	repo, err := f.driver.repository(project)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, dir := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(repo, dir), 0o700); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := os.Chmod(f.storage, 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(repo), 0o700); err != nil {
		f.t.Fatal(err)
	}
}

func TestCheckMakesThePrivateStorageAndRefusesAnUnsafeOne(t *testing.T) {
	f := newFixture(t).(*fixture)
	if err := f.driver.Check(t.Context()); err != nil {
		t.Fatalf("Check of a new storage: %v", err)
	}
	for _, dir := range []string{f.storage, filepath.Join(f.storage, "repositories")} {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("Check left %s as %v, %v; want a 0700 directory", dir, info, err)
		}
	}
	if err := os.Chmod(filepath.Join(f.storage, "repositories"), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := f.driver.Check(t.Context()); err == nil {
		t.Fatal("Check of a group-writable repositories directory succeeded")
	}
	orphan, err := New(filepath.Join(t.TempDir(), "missing", "source"), localConfig(), testBinary())
	if err != nil {
		t.Fatal(err)
	}
	if err := orphan.Check(t.Context()); err == nil {
		t.Fatal("Check of a storage whose parent is missing succeeded")
	}
}

// A repository Git cannot read is an error, never an absent repository,
// head or commit.
func TestAFailedGitReadIsAnErrorNotAnAbsence(t *testing.T) {
	f := newFixture(t).(*fixture)
	if _, err := f.driver.CreateRepository(t.Context(), "p"); err != nil {
		t.Fatal(err)
	}
	tip := f.Commit("p")
	f.SetHead("p", "work", tip)
	repo, _ := f.driver.repository("p")
	if err := os.WriteFile(filepath.Join(repo, "config"), []byte("[core\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := f.driver.InspectRepository(t.Context(), "p"); err == nil {
		t.Errorf("InspectRepository = %+v, want an error", r)
	}
	if got, ok, err := f.driver.InspectBranch(t.Context(), "p", "work"); err == nil {
		t.Errorf("InspectBranch = %q, %v; want an error", got, ok)
	}
	if c, err := f.driver.InspectCommit(t.Context(), "p", tip); err == nil {
		t.Errorf("InspectCommit = %+v, want an error", c)
	}
	if heads, err := f.driver.Branches(t.Context(), "p"); err == nil {
		t.Errorf("Branches = %v, want an error", heads)
	}
}

// Deletion renames the repository away inside the storage before removing
// it, and leaves nothing of it behind.
func TestDeleteRepositoryLeavesNothingInTheStorage(t *testing.T) {
	f := newFixture(t).(*fixture)
	r, err := f.driver.CreateRepository(t.Context(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.driver.DeleteRepository(t.Context(), r, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(f.storage, "repositories"))
	if err != nil || len(entries) != 1 || entries[0].Name() != repositoriesLock {
		t.Fatalf("the repositories directory holds %v, %v; want only the lock", entries, err)
	}
}
