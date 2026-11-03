package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/plugin"
	sourcegit "github.com/lgvo/p.ai/internal/sourcedriver/git"
)

// gitSource is the bundled source driver over store's state directory, with
// project app's repository at path repo and tip, a commit no head holds yet.
// A test changes the repository natively, with dispatchGit, as Git or a
// session's push would.
func gitSource(t *testing.T, store *control.Store) (driver *sourcegit.Driver, repo, tip string) {
	t.Helper()
	state := store.StateDir()
	driver, err := sourcegit.New(state, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = driver.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = driver.CreateRepository(ctx, "app"); err != nil {
		t.Fatal(err)
	}
	// The bundled driver's layout: a bare repository named by the hash of
	// its project.
	sum := sha256.Sum256([]byte("app"))
	repo = filepath.Join(state, "repositories", hex.EncodeToString(sum[:])+".git")
	tree := dispatchGit(t, repo, "mktree")
	tip = dispatchGit(t, repo, "-c", "user.name=P fixture", "-c", "user.email=p@example.test", "commit-tree", tree, "-m", "fixture")
	return driver, repo, tip
}

// gitserviceFixture is gitservice over store's state directory, serving the
// source driver's repositories for what has not moved to the driver yet:
// the image builder's bundle.
func gitserviceFixture(t *testing.T, store *control.Store) *gitservice.Backend {
	t.Helper()
	state := store.StateDir()
	dir := filepath.Join(state, "source-package")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plugin.json", "p-git-ssh"} {
		data, err := os.ReadFile(filepath.Join("../../plugins/bundled/source-git", name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	build := exec.Command("go", "build", "-o", filepath.Join(dir, "git.wasm"), "../../plugins/bundled/source-git")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOTOOLCHAIN=local")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Git module: %v %s", err, out)
	}
	pkg, err := plugin.Conformance(dir)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := gitservice.New(store, state, []plugin.Active{{Package: pkg, Grants: []string{"git.project"}}})
	if err != nil {
		t.Fatal(err)
	}
	return backend
}
