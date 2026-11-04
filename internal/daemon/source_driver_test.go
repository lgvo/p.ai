package daemon

import (
	"testing"

	sourcememory "github.com/lgvo/p.ai/internal/sourcedriver/memory"
)

// memorySource is an in-memory source driver for a daemon test, with the
// native state its controls act on.
func memorySource(t *testing.T) (*sourcememory.World, *sourcememory.Driver) {
	t.Helper()
	w := sourcememory.NewWorld()
	return w, sourcememory.NewDriver(w)
}
