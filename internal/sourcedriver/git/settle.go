package git

import (
	"context"
	"fmt"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// Settle settles a head effect or a service.
func (d *Driver) Settle(ctx context.Context, e sourcedriver.Effect) (sourcedriver.Settlement, error) {
	switch e.Kind {
	case sourcedriver.EffectCreateBranch, sourcedriver.EffectDeleteBranch:
		return d.settleHead(ctx, e)
	case sourcedriver.EffectService:
		return d.settleService(e)
	}
	return sourcedriver.Unknown, fmt.Errorf("effect kind %q: %w", e.Kind, sourcedriver.ErrInvalid)
}
