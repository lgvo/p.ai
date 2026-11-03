package memory

import (
	"context"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/gitnames"
)

// CheckNames applies Git's syntax for P's names. The rename-branch rule
// adds nothing to gitnames.RenameBranch, which models what
// `git check-ref-format --branch` refuses of the names a P branch allows.
func (d *Driver) CheckNames(_ context.Context, names []sourcedriver.Name) ([]sourcedriver.Name, error) {
	if err := d.alive(); err != nil {
		return nil, err
	}
	return gitnames.Check(names, func(string) (bool, error) { return true, nil })
}
