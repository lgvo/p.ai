// Package bundledgit is what P's VM fixtures know of the bundled source
// driver: how p-source-git starts it over a state directory, and where it
// keeps a project's repository. Core never sees a repository path; these
// fixtures test the bundled driver, so they may know its layout to plant and
// inspect Git objects natively, as their scenarios do.
package bundledgit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	sourcegit "github.com/lgvo/p.ai/internal/sourcedriver/git"
)

// Open starts the bundled source driver in process over the state
// directory, with the git and ssh found on PATH, and checks it, as
// p-source-git does at each start. Its Git service's pre-receive hook runs
// the calling fixture's own program, as `<program> git-hook`.
func Open(ctx context.Context, state string) (sourcedriver.Driver, error) {
	config, err := config()
	if err != nil {
		return nil, err
	}
	program, err := os.Executable()
	if err != nil {
		return nil, err
	}
	driver, err := sourcegit.New(state, config, program)
	if err != nil {
		return nil, err
	}
	if err := driver.Check(ctx); err != nil {
		return nil, err
	}
	return driver, nil
}

// config is the bundled driver's config with the git and ssh on PATH,
// resolved as the VM helper source-driver-config.sh resolves them.
func config() (json.RawMessage, error) {
	var c sourcegit.Config
	for _, program := range []struct {
		name string
		path *string
	}{{"git", &c.Git}, {"ssh", &c.SSH}} {
		found, err := exec.LookPath(program.name)
		if err != nil {
			return nil, err
		}
		if *program.path, err = filepath.EvalSymlinks(found); err != nil {
			return nil, err
		}
	}
	return json.Marshal(c)
}

// RepositoryPath is where the bundled driver over state keeps the project's
// bare repository: repositories/<hex SHA-256 of the project>.git.
func RepositoryPath(state, project string) string {
	name := sha256.Sum256([]byte(project))
	return filepath.Join(state, "repositories", hex.EncodeToString(name[:])+".git")
}
