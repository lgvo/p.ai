package daemon

import (
	"context"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
)

// processOriginChange runs an origin change operation.
func (l *lifecycle) processOriginChange(op api.Operation) {
	if op.Status == "running" {
		l.origins.RunOriginChange(l.ctx, op)
	}
}

// processPublication runs a publication operation.
func (l *lifecycle) processPublication(op api.Operation) {
	if op.Status == "running" {
		l.origins.RunPublication(l.ctx, op)
	}
}

// newGitLifecycle is the lifecycle of a daemon with Git and no runtime: it
// runs only the journal's origin operations.
func newGitLifecycle(ctx context.Context, store *control.Store, git *gitCapability) *lifecycle {
	l := &lifecycle{store: store, git: git, ctx: ctx, gitOnly: true, working: map[string]bool{}, queueSlots: make(chan struct{}, 8),
		sessionLocks: map[string]chan struct{}{}, changedPolicies: map[string]bool{}}
	store.ObserveJournal(l)
	return l
}
