package memory

import (
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/conformance"
)

// fixture runs the conformance suite over a World.
type fixture struct {
	w      *World
	driver *Driver
}

func newFixture(*testing.T) conformance.Fixture {
	w := NewWorld()
	return &fixture{w: w, driver: NewDriver(w)}
}

func (f *fixture) Driver() sourcedriver.Driver { return f.driver }

func (f *fixture) Restart() {
	f.driver.Kill()
	f.driver = NewDriver(f.w)
}

func (f *fixture) Natives() conformance.Natives                 { return f.w }
func (f *fixture) Repositories() conformance.RepositoryControls { return f.w }
func (f *fixture) Settling() conformance.SettlingControls       { return f.w }
func (f *fixture) Service() conformance.ServiceControls         { return f.w }
func (f *fixture) Origin() conformance.OriginControls           { return f.w }

func TestConformance(t *testing.T) {
	conformance.Run(t, newFixture)
}
