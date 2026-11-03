package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
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
