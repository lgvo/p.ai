package runtimeincus

import (
	"context"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// Each observation of a session's effect says whether it shows the effect
// applied, and what Settle compares across its two observations: the
// runtime's incarnation, or a fixed word when the outcome has none. None is
// applied while an Incus operation runs on the runtime, which may still
// change it.

// observeCreated: the session's runtime is at its address, as specified or
// lacking only devices an interrupted Create had not added, with an
// incarnation, the one at when it is given.
func (b *Backend) observeCreated(ctx context.Context, s Session, at runtimedriver.Incarnation) (string, bool, error) {
	o, err := b.InspectCreated(ctx, s)
	if err != nil || !o.Exists {
		return "", false, err
	}
	current := incarnationOf(o)
	if current == "" || at != "" && current != at {
		return "", false, nil
	}
	return b.idle(ctx, o.Name, string(current))
}

// observeState: the runtime at incarnation at is in state want, and a
// running one's session host is ready.
func (b *Backend) observeState(ctx context.Context, s Session, at runtimedriver.Incarnation, want runtimedriver.State) (string, bool, error) {
	o, err := b.InspectCreated(ctx, s)
	if err != nil || !o.Exists {
		return "", false, err
	}
	got := b.observe(ctx, s, o)
	if got.Incarnation == "" || at != "" && got.Incarnation != at || got.State != want || want == runtimedriver.StateRunning && !got.Host.Ready {
		return "", false, nil
	}
	return b.idle(ctx, o.Name, string(got.Incarnation))
}

// observeDeleted: no instance in the project carries the session's
// reference, at its address or under another name.
func (b *Backend) observeDeleted(ctx context.Context, session string) (string, bool, error) {
	if _, exists, err := b.sessionRuntime(ctx, session); err != nil || exists {
		return "", false, err
	}
	return b.idle(ctx, sessionRuntimeName(session), "absent")
}

// observeWorkspace: in the stopped runtime at the effect's incarnation, the
// path is exactly the written content, or absent after a removal.
func (b *Backend) observeWorkspace(ctx context.Context, e runtimedriver.Effect) (string, bool, error) {
	if err := checkWorkspacePath(e.Path); err != nil {
		return "", false, err
	}
	w, err := b.stoppedWorkspace(ctx, e.Target)
	if err != nil {
		return "", false, err
	}
	if e.Kind == runtimedriver.EffectRemove {
		entry, err := w.lstat(ctx, e.Path)
		if err != nil || entry.exists {
			return "", false, err
		}
	} else if held, err := w.holds(ctx, e.Path, e.Content); err != nil || !held {
		return "", false, err
	}
	return b.idle(ctx, w.api.instance, string(e.Kind))
}

// idle completes an observation that shows the outcome: it is applied only
// while no Incus operation runs on the instance.
func (b *Backend) idle(ctx context.Context, instance, outcome string) (string, bool, error) {
	busy, err := b.workspaceResourceBusy(ctx, instance)
	if err != nil || busy {
		return "", false, err
	}
	return outcome, true, nil
}
