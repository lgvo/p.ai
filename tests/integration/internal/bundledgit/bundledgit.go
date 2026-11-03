// Package bundledgit is what P's VM fixtures know of the bundled source
// driver: how the daemon starts it over a state directory, and where it keeps
// a project's repository. Core never sees a repository path; these fixtures
// test the bundled driver, so they may know its layout to plant and inspect
// Git objects natively, as their scenarios do.
package bundledgit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	sourcegit "github.com/lgvo/p.ai/internal/sourcedriver/git"
)

// Open starts the bundled source driver over the state directory, with no
// settings, and checks it, as the daemon does at start.
func Open(ctx context.Context, state string) (sourcedriver.Driver, error) {
	driver, err := sourcegit.New(state, json.RawMessage(`{}`))
	if err != nil {
		return nil, err
	}
	if err := driver.Check(ctx); err != nil {
		return nil, err
	}
	return driver, nil
}

// RepositoryPath is where the bundled driver over state keeps the project's
// bare repository: repositories/<hex SHA-256 of the project>.git.
func RepositoryPath(state, project string) string {
	name := sha256.Sum256([]byte(project))
	return filepath.Join(state, "repositories", hex.EncodeToString(name[:])+".git")
}
