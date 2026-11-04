package control

import (
	"context"

	"github.com/lgvo/p.ai/internal/api"
)

// RemovalAPI discards and deletes sessions. Each is a risky change: a dry run
// plans it, and a run with the plan's change tag records its operation.
type RemovalAPI interface {
	DiscardSession(context.Context, string, api.ChangeRequest) (api.ChangeResult[api.RemovalDetails], error)
	DeleteSession(context.Context, string, api.ChangeRequest) (api.ChangeResult[api.RemovalDetails], error)
}
