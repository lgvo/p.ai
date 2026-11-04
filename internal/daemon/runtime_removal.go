package daemon

import (
	"context"
	"errors"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// Removal deletes a reviewed runtime through the driver's Delete, which acts
// only at the incarnation the review recorded. The driver calls the delete's
// gate right before its first effect, and the removal records its delete
// intent there. A delete whose outcome is unknown is settled and never sent
// again: only a delete the driver settles as applied lets the removal go on
// to prove the runtime absent; any other answer keeps it blocked.

// errDeleteUnresolved is why a removal waits on a delete the driver has not
// settled.
var errDeleteUnresolved = errors.New("runtime delete outcome unresolved")

// deleteRuntime deletes the runtime of s at incarnation at. issued records
// the delete's intent; a failure after it is settled.
func (l *lifecycle) deleteRuntime(ctx context.Context, s runtimedriver.Session, at runtimedriver.Incarnation, issued runtimedriver.Gate) error {
	sent := false
	err := l.driver.Delete(ctx, s.Ref, at, tracked(issued, &sent))
	if err == nil || !sent {
		return err
	}
	return l.settleRuntimeDelete(ctx, s, at, errors.Join(err, errDeleteUnresolved))
}

// settleRuntimeDelete succeeds only when the driver settles the issued delete
// of the runtime of s at incarnation at as applied. Otherwise the delete's
// outcome is unknown: it returns unresolved, with what the driver said.
func (l *lifecycle) settleRuntimeDelete(ctx context.Context, s runtimedriver.Session, at runtimedriver.Incarnation, unresolved error) error {
	settled, err := l.driver.Settle(ctx, runtimedriver.Effect{Kind: runtimedriver.EffectDelete, Target: runtimedriver.Target{Session: s, At: at}})
	if err == nil && settled == runtimedriver.Applied {
		return nil
	}
	return control.OutcomeUnknown(errors.Join(unresolved, err))
}

// stopReviewedRuntime stops the running runtime of s at incarnation at,
// recording the stop through issued first. Stop is idempotent by reference,
// so a removal that finds its stop recorded sends it again.
func (l *lifecycle) stopReviewedRuntime(ctx context.Context, s runtimedriver.Session, at runtimedriver.Incarnation, issued func() error) error {
	if err := issued(); err != nil {
		return err
	}
	stopped, err := l.driver.Stop(ctx, s)
	if err != nil || stopped.State != runtimedriver.StateStopped || stopped.Incarnation != at {
		return errors.Join(err, errors.New("exact stop postcondition unavailable"))
	}
	return nil
}

// tracked is g, also telling through passed whether it passed: after that,
// a failed call's outcome is unknown.
func tracked(g runtimedriver.Gate, passed *bool) runtimedriver.Gate {
	return func() error {
		if err := g(); err != nil {
			return err
		}
		*passed = true
		return nil
	}
}
