package memory

import (
	"fmt"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/gitnames"
)

// settleHead answers from the head's state alone: the World runs every
// effect at once under its lock, so nothing the driver admitted can still
// change the head once its call ended.
func (d *Driver) settleHead(e sourcedriver.Effect) (sourcedriver.Settlement, error) {
	if err := validHeadEffect(e); err != nil {
		return sourcedriver.Unknown, err
	}
	d.w.mu.Lock()
	defer d.w.mu.Unlock()
	r, err := d.w.open(e.Project)
	if err != nil {
		return sourcedriver.Unknown, err
	}
	s := r.state(e.Branch)
	switch {
	case s.symbolic:
		return sourcedriver.Unknown, nil
	case s.tip == e.New:
		return sourcedriver.Applied, nil
	case s.tip == e.Old:
		return sourcedriver.NotApplied, nil
	}
	return sourcedriver.Unknown, nil
}

// validHeadEffect refuses a head effect that does not name its states.
func validHeadEffect(e sourcedriver.Effect) error {
	if !gitnames.HeadEffect(e) {
		return fmt.Errorf("%s effect on %q: %w", e.Kind, e.Branch, sourcedriver.ErrInvalid)
	}
	return nil
}
