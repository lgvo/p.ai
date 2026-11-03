package daemon

import (
	"context"

	"github.com/lgvo/p.ai/internal/api"
)

// The daemon's risky changes each answer through a control.Change. These
// are what several of them share: holding a session while a change is
// planned and admitted, and the loss items, written once per source of loss.

// holdingSession holds session uuid's lock while fn runs.
func (l *lifecycle) holdingSession(uuid string) func(context.Context, func(context.Context) error) error {
	return func(ctx context.Context, fn func(context.Context) error) error {
		release, err := l.lockSession(ctx, uuid)
		if err != nil {
			return err
		}
		defer release()
		return fn(ctx)
	}
}

// containerLoss is the loss of session uuid's container, which exists: the
// container and everything in it. P does not look inside.
func containerLoss(uuid string) api.Loss {
	return api.Loss{Kind: api.LossRuntimeData, Target: api.TargetSession(uuid), Certain: true}
}

// missingRuntimeGone is what is already gone with session uuid's missing
// runtime: whatever it held. A plan lists it in Gone, never in Loss.
func missingRuntimeGone(uuid string) api.Loss {
	return api.Loss{Kind: api.LossRuntimeData, Target: api.TargetSession(uuid), Certain: true}
}

// branchLoss is the loss of the commits that leave P's repository with b, at
// target: a count and a sample of at most api.MaxLossSample of them, in b's
// order. It is empty when no commit leaves.
func branchLoss(target string, b *api.RemovalBranchLoss) []api.Loss {
	if b == nil || len(b.CommitsLosingPReachability) == 0 {
		return nil
	}
	commits := b.CommitsLosingPReachability
	sample := append([]string(nil), commits[:min(len(commits), api.MaxLossSample)]...)
	return []api.Loss{{Kind: api.LossPCommits, Target: target, Count: len(commits), Sample: sample, Certain: true}}
}

// unreachableObjectsLoss is the loss of the Git objects in project's
// repository that no branch reaches. P does not count them, so it cannot rule
// the loss out.
func unreachableObjectsLoss(project string) api.Loss {
	return api.Loss{Kind: api.LossUnreachableObjects, Target: api.TargetProject(project), Reason: api.LossNotEnumerated}
}
