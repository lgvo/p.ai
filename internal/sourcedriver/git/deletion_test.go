package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// dieAfterMove makes the driver's next deletion die right after it moved a
// repository away from its address.
func dieAfterMove(d *Driver) {
	d.seams.mu.Lock()
	defer d.seams.mu.Unlock()
	d.seams.repositoryMoved = func() error { return errors.New("the driver died") }
}

// occupant is what may be at a project's address: an observation a test
// takes before and after a call, to show the call left it as it was.
func occupant(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		return "nothing: " + err.Error()
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			t.Fatal(err)
		}
		return "symlink to " + target
	case info.Mode().IsRegular():
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return "file " + string(data)
	}
	incarnation, err := readIncarnation(path)
	if err != nil {
		return "directory without an incarnation"
	}
	return "repository " + incarnation
}

// addressFixture is a fixture with the repositories of p and q, and p's
// address.
func addressFixture(t *testing.T) (*fixture, sourcedriver.Repository, sourcedriver.Repository, string) {
	t.Helper()
	f := newFixture(t).(*fixture)
	p, err := f.driver.CreateRepository(t.Context(), "p")
	if err != nil {
		t.Fatal(err)
	}
	q, err := f.driver.CreateRepository(t.Context(), "q")
	if err != nil {
		t.Fatal(err)
	}
	address, _ := f.driver.repository("p")
	return f, p, q, address
}

// occupy puts at p's address a file, a symbolic link to q's repository, or
// another incarnation of p's repository, as a creation or an administrator
// could while a deletion's gate runs.
var occupy = map[string]func(t *testing.T, f *fixture, address string){
	"another incarnation": func(t *testing.T, f *fixture, address string) {
		if _, err := f.driver.CreateRepository(t.Context(), "p"); err != nil {
			t.Fatal(err)
		}
	},
	"a file": func(t *testing.T, f *fixture, address string) {
		if err := os.WriteFile(address, []byte("kept"), 0o600); err != nil {
			t.Fatal(err)
		}
	},
	"a symbolic link to another project's repository": func(t *testing.T, f *fixture, address string) {
		q, _ := f.driver.repository("q")
		if err := os.Symlink(q, address); err != nil {
			t.Fatal(err)
		}
	},
}

// A deletion moves only the incarnation it checked: whatever took the
// address while its gate ran stays there, even when the driver dies right
// after a move.
func TestDeletionMovesNothingButItsIncarnation(t *testing.T) {
	for what, put := range occupy {
		t.Run(what, func(t *testing.T) {
			f, p, q, address := addressFixture(t)
			var want string
			gate := func() error {
				if err := f.driver.DeleteRepository(t.Context(), p, func() error { return nil }); err != nil {
					t.Fatalf("DeleteRepository inside the gate: %v", err)
				}
				put(t, f, address)
				want = occupant(t, address)
				dieAfterMove(f.driver)
				return nil
			}
			if err := f.driver.DeleteRepository(t.Context(), p, gate); err == nil {
				t.Fatalf("DeleteRepository with %s at the address succeeded", what)
			}
			if got := occupant(t, address); got != want {
				t.Fatalf("the address holds %s, want %s as it was", got, want)
			}
			if r, err := f.driver.InspectRepository(t.Context(), "q"); err != nil || r != q {
				t.Fatalf("q's repository is %+v, %v; want %+v", r, err, q)
			}
		})
	}
}

// A file or a symbolic link at the address is not a repository: no call
// reports it absent, replaces it or moves it.
func TestSomethingElseAtTheAddressIsNeverMovedOrReplaced(t *testing.T) {
	for what, put := range occupy {
		if what == "another incarnation" {
			continue
		}
		t.Run(what, func(t *testing.T) {
			f, p, q, address := addressFixture(t)
			if err := f.driver.DeleteRepository(t.Context(), p, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			put(t, f, address)
			want := occupant(t, address)
			if r, err := f.driver.InspectRepository(t.Context(), "p"); err == nil {
				t.Errorf("InspectRepository = %+v, want an error", r)
			}
			if r, err := f.driver.CreateRepository(t.Context(), "p"); err == nil {
				t.Errorf("CreateRepository = %+v, want an error", r)
			}
			for _, r := range []sourcedriver.Repository{p, {Project: "p", Exists: true, Incarnation: q.Incarnation}} {
				if err := f.driver.DeleteRepository(t.Context(), r, unexpected(t)); err == nil {
					t.Errorf("DeleteRepository(%+v) succeeded", r)
				}
			}
			if got := occupant(t, address); got != want {
				t.Fatalf("the address holds %s, want %s as it was", got, want)
			}
			if r, err := f.driver.InspectRepository(t.Context(), "q"); err != nil || r != q {
				t.Fatalf("q's repository is %+v, %v; want %+v", r, err, q)
			}
		})
	}
}

// A deletion killed right after its move leaves the address empty and its
// own incarnation, and only it, set aside.
func TestDeletionKilledAfterItsMoveLeavesTheAddressEmpty(t *testing.T) {
	f, p, _, _ := addressFixture(t)
	dieAfterMove(f.driver)
	if err := f.driver.DeleteRepository(t.Context(), p, func() error { return nil }); err == nil {
		t.Fatal("DeleteRepository that died after its move succeeded")
	}
	if r, err := f.driver.InspectRepository(t.Context(), "p"); err != nil || r.Exists {
		t.Fatalf("after the death InspectRepository = %+v, %v; want absent", r, err)
	}
	set, err := filepath.Glob(filepath.Join(f.storage, "repositories", ".p-git-delete-*", "repository"))
	if err != nil || len(set) != 1 {
		t.Fatalf("set aside: %v, %v; want one repository", set, err)
	}
	if incarnation, err := readIncarnation(set[0]); err != nil || incarnation != p.Incarnation {
		t.Fatalf("the repository set aside is %q, %v; want %q", incarnation, err, p.Incarnation)
	}
	if r, err := f.driver.CreateRepository(t.Context(), "p"); err != nil || r.Incarnation == p.Incarnation {
		t.Fatalf("CreateRepository after the death = %+v, %v; want a new incarnation", r, err)
	}
}

func unexpected(t *testing.T) sourcedriver.Gate {
	return func() error {
		t.Error("the gate was called for a call that had to be refused")
		return nil
	}
}

// Two drivers over one storage, as two processes would be, take turns at
// creating and deleting repositories.
func TestDriversOverOneStorageTakeTurnsAtRepositories(t *testing.T) {
	f := newFixture(t).(*fixture)
	if err := f.driver.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	other := f.newDriver()
	release, err := other.lockRepositories(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if r, err := f.driver.CreateRepository(waiting, "p"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CreateRepository while another driver holds the lock = %+v, %v; want it to wait", r, err)
	}
	release()
	if _, err := f.driver.CreateRepository(t.Context(), "p"); err != nil {
		t.Fatalf("CreateRepository once the lock is free: %v", err)
	}
}
