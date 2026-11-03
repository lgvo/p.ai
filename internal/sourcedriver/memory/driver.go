package memory

import (
	"context"
	"fmt"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// Driver is an in-memory source driver over a World.
type Driver struct {
	w    *World
	dead chan struct{}
}

var _ sourcedriver.Driver = (*Driver)(nil)

// NewDriver returns a driver over w. Several may share a World, as a
// restarted driver shares its backend with the one that died.
func NewDriver(w *World) *Driver { return &Driver{w: w, dead: make(chan struct{})} }

// Kill makes the driver die, as its process would: every later call fails
// with ErrUnavailable.
func (d *Driver) Kill() {
	d.w.mu.Lock()
	defer d.w.mu.Unlock()
	select {
	case <-d.dead:
	default:
		close(d.dead)
	}
}

// alive refuses every call to a driver that was killed.
func (d *Driver) alive() error {
	select {
	case <-d.dead:
		return fmt.Errorf("the driver is not running: %w", sourcedriver.ErrUnavailable)
	default:
		return nil
	}
}

// Backend is the backend the in-memory driver describes.
const Backend = "memory"

func (d *Driver) Describe() sourcedriver.Description {
	return sourcedriver.Description{Backend: Backend, Version: "1", Commands: []string{"git-upload-pack", "git-receive-pack"}}
}

// Settle settles a head effect or a service.
func (d *Driver) Settle(_ context.Context, e sourcedriver.Effect) (sourcedriver.Settlement, error) {
	if err := d.alive(); err != nil {
		return sourcedriver.Unknown, err
	}
	switch e.Kind {
	case sourcedriver.EffectCreateBranch, sourcedriver.EffectDeleteBranch:
		return d.settleHead(e)
	case sourcedriver.EffectService:
		return d.settleService(e)
	}
	return sourcedriver.Unknown, fmt.Errorf("effect kind %q: %w", e.Kind, sourcedriver.ErrInvalid)
}
