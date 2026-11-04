package git

import (
	"context"
	"fmt"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/gitnames"
)

// settleHead answers from the head as Git holds it. A head effect is one
// update-ref run inside its call, and a call that ended left no Git process
// behind, so nothing the driver admitted can still change the head.
func (d *Driver) settleHead(ctx context.Context, e sourcedriver.Effect) (sourcedriver.Settlement, error) {
	if !gitnames.HeadEffect(e) {
		return sourcedriver.Unknown, fmt.Errorf("%s effect on %q: %w", e.Kind, e.Branch, sourcedriver.ErrInvalid)
	}
	repo, err := d.open(ctx, e.Project)
	if err != nil {
		return sourcedriver.Unknown, err
	}
	s, err := d.readHead(ctx, repo, e.Branch)
	switch {
	case err != nil:
		return sourcedriver.Unknown, err
	case s.symbolic:
		return sourcedriver.Unknown, nil
	case s.tip == e.New:
		return sourcedriver.Applied, nil
	case s.tip == e.Old:
		return sourcedriver.NotApplied, nil
	}
	return sourcedriver.Unknown, nil
}
