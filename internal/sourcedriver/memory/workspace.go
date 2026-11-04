package memory

import (
	"context"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/gitclone"
)

// RenameWorkspace computes the rename's Git edits as the bundled driver
// does: a session's clone does not depend on where P's repositories live.
func (d *Driver) RenameWorkspace(_ context.Context, r sourcedriver.WorkspaceRename, files []sourcedriver.WorkspaceFile) (sourcedriver.WorkspaceEdits, error) {
	if err := d.alive(); err != nil {
		return sourcedriver.WorkspaceEdits{}, err
	}
	return gitclone.Rename(r, files)
}
