package git

import (
	"context"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/gitclone"
)

// RenameWorkspace computes the Git edits that rename a session's clone from
// the files core read; it runs no Git and touches no storage.
func (d *Driver) RenameWorkspace(_ context.Context, r sourcedriver.WorkspaceRename, files []sourcedriver.WorkspaceFile) (sourcedriver.WorkspaceEdits, error) {
	return gitclone.Rename(r, files)
}
