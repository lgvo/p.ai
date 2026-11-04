package git

import (
	"context"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/gitnames"
)

// CheckNames applies Git's syntax for P's names. A branch a rename creates,
// or a retained branch a change names, must also pass
// `git check-ref-format --branch`.
func (d *Driver) CheckNames(ctx context.Context, names []sourcedriver.Name) ([]sourcedriver.Name, error) {
	return gitnames.Check(names, func(name string) (bool, error) { return d.checkRefFormat(ctx, name) })
}

// checkRefFormat runs `git check-ref-format --branch` on name, outside any
// repository; an error only when Git did not run.
func (d *Driver) checkRefFormat(ctx context.Context, name string) (bool, error) {
	checked, err := d.run(ctx, "/", nil, 4096, "check-ref-format", "--branch", name)
	if err != nil {
		return false, err
	}
	return checked.Exit == 0, nil
}
