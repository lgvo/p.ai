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
	"github.com/lgvo/p.ai/internal/sourcedriver"
	sourcegit "github.com/lgvo/p.ai/internal/sourcedriver/git"
)

// bundledSource is the bundled source driver over state, the state
// directory, in process and checked, with the git and ssh on PATH, and the
// function that ends its Git services. The daemon runs the same driver as
// p-source-git; tests that need real Git reach it here, as a
// sourcedriver.Driver.
func bundledSource(t *testing.T, state string) (sourcedriver.Driver, func() error) {
	t.Helper()
	var config sourcegit.Config
	for _, program := range []struct {
		name string
		path *string
	}{{"git", &config.Git}, {"ssh", &config.SSH}} {
		found, err := exec.LookPath(program.name)
		if err != nil {
			t.Fatal(err)
		}
		if *program.path, err = filepath.EvalSymlinks(found); err != nil {
			t.Fatal(err)
		}
	}
	settings, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	// The daemon's tests push nothing, so the Git service's hook, the test
	// binary, never runs.
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	driver, err := sourcegit.New(state, settings, program)
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	return driver, driver.Close
}

// gitSource is the bundled source driver over store's state directory, in
// process, with project app's repository at path repo and tip, a
// commit no head holds yet. A test changes the repository natively, with
// dispatchGit, as Git or a session's push would.
func gitSource(t *testing.T, store *control.Store) (driver sourcedriver.Driver, repo, tip string) {
	t.Helper()
	state := store.StateDir()
	ctx := context.Background()
	driver, _ = bundledSource(t, state)
	if _, err := driver.CreateRepository(ctx, "app"); err != nil {
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
