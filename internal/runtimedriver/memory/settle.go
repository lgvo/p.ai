package memory

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// Settle answers from native state only. An effect is applied when its
// intended outcome is present exactly and nothing admitted, or still
// admissible, is under way on its target. It is not applied when the
// backend never admitted it and holds no delayed admission of it: unlike a
// real backend, the World knows every request it received. Otherwise the
// answer is unknown.
func (d *Driver) Settle(_ context.Context, e runtimedriver.Effect) (runtimedriver.Settlement, error) {
	if err := d.alive(); err != nil {
		return runtimedriver.Unknown, err
	}
	w := d.w
	switch e.Kind {
	case runtimedriver.EffectCreate, runtimedriver.EffectStart, runtimedriver.EffectStop, runtimedriver.EffectDelete,
		runtimedriver.EffectWrite, runtimedriver.EffectRemove, runtimedriver.EffectTerminal:
		if err := w.checkSession(e.Target.Session); err != nil {
			return runtimedriver.Unknown, err
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	switch e.Kind {
	case runtimedriver.EffectCreate:
		return w.settleCreate(e.Target.Session)
	case runtimedriver.EffectStart:
		return w.settleState(e.Target, runtimedriver.StateRunning)
	case runtimedriver.EffectStop:
		return w.settleState(e.Target, runtimedriver.StateStopped)
	case runtimedriver.EffectDelete:
		return w.settleDelete(e.Target), nil
	case runtimedriver.EffectWrite, runtimedriver.EffectRemove:
		return w.settleWorkspace(e)
	case runtimedriver.EffectBuild:
		return w.settleBuild(e.Build), nil
	case runtimedriver.EffectDeleteImage:
		return w.settleDeleteImage(e.Image), nil
	case runtimedriver.EffectTerminal:
		return w.settleTerminal(e.Terminal), nil
	}
	return runtimedriver.Unknown, fmt.Errorf("unknown effect kind %q", e.Kind)
}

// settled answers for an effect whose outcome is not present, once its
// caller found nothing in flight on the target, a delayed admission of the
// effect included: not applied only when the backend never admitted it.
// Called with w.mu held.
func (w *World) settled(key string) runtimedriver.Settlement {
	if w.admitted[key] {
		return runtimedriver.Unknown
	}
	return runtimedriver.NotApplied
}

func (w *World) settleCreate(s runtimedriver.Session) (runtimedriver.Settlement, error) {
	key := effectKey(runtimedriver.EffectCreate, s.Ref.ID)
	if w.inFlight(address(s.Ref)) {
		return runtimedriver.Unknown, nil
	}
	o, err := w.observe(s)
	switch {
	case err != nil:
		return runtimedriver.Unknown, err
	case o == nil:
		return w.settled(key), nil
	}
	return runtimedriver.Applied, nil
}

// settleState settles a start or a stop, which core may also send again.
func (w *World) settleState(t runtimedriver.Target, want runtimedriver.State) (runtimedriver.Settlement, error) {
	o, err := w.observe(t.Session)
	if err != nil || o == nil || w.inFlight(o.name) || t.At != "" && o.incarnation != t.At || o.observedState() != want {
		return runtimedriver.Unknown, err
	}
	if want == runtimedriver.StateRunning && !o.host.ready {
		return runtimedriver.Unknown, nil
	}
	return runtimedriver.Applied, nil
}

// settleDelete: applied once nothing in scope carries the reference.
func (w *World) settleDelete(t runtimedriver.Target) runtimedriver.Settlement {
	r := t.Session.Ref
	if w.inFlight(address(r)) {
		return runtimedriver.Unknown
	}
	if len(w.carrying(r)) == 0 {
		return runtimedriver.Applied
	}
	return w.settled(effectKey(runtimedriver.EffectDelete, r.ID))
}

func (w *World) settleWorkspace(e runtimedriver.Effect) (runtimedriver.Settlement, error) {
	if err := checkWorkspacePath(e.Path); err != nil {
		return runtimedriver.Unknown, err
	}
	o, err := w.observe(e.Target.Session)
	if err != nil || o == nil || o.incarnation != e.Target.At {
		return runtimedriver.Unknown, errors.Join(err, errors.New("the runtime of the workspace effect is gone"))
	}
	if w.inFlight(o.name) {
		return runtimedriver.Unknown, nil
	}
	n, err := o.resolve(e.Path)
	if err != nil {
		return runtimedriver.Unknown, err
	}
	if e.Kind == runtimedriver.EffectWrite && n != nil && n.equals(e.Content) || e.Kind == runtimedriver.EffectRemove && n == nil {
		return runtimedriver.Applied, nil
	}
	return w.settled(effectKey(e.Kind, o.ref.ID, e.Path)), nil
}

// settleBuild: applied when one image of this instance carries the build,
// labeled exactly, and its builder is gone.
func (w *World) settleBuild(b runtimedriver.Build) runtimedriver.Settlement {
	if w.inFlight(address(b.Ref)) || len(w.carrying(b.Ref)) > 0 {
		return runtimedriver.Unknown
	}
	var carrying []*image
	for _, im := range w.images {
		if im.instance == thisInstance && im.labels[runtimedriver.LabelBuild] == b.Ref.ID {
			carrying = append(carrying, im)
		}
	}
	switch {
	case len(carrying) == 1 && maps.Equal(carrying[0].labels, b.Labels):
		return runtimedriver.Applied
	case len(carrying) == 0:
		return w.settled(effectKey(runtimedriver.EffectBuild, b.Ref.ID))
	}
	return runtimedriver.Unknown
}

func (w *World) settleDeleteImage(img runtimedriver.Image) runtimedriver.Settlement {
	if w.inFlight(string(img.Ref)) {
		return runtimedriver.Unknown
	}
	if w.images[img.Ref] == nil {
		return runtimedriver.Applied
	}
	return w.settled(effectKey(runtimedriver.EffectDeleteImage, string(img.Ref)))
}

// settleTerminal: applied once the attachment's guest process ended.
func (w *World) settleTerminal(id string) runtimedriver.Settlement {
	a := w.attachments[id]
	switch {
	case a == nil:
		return runtimedriver.NotApplied
	case a.processEnded:
		return runtimedriver.Applied
	}
	return runtimedriver.Unknown
}
