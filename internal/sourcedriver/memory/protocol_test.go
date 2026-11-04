package memory

import (
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/conformance"
	"github.com/lgvo/p.ai/internal/sourcedriver/protocol"
	"github.com/lgvo/p.ai/internal/sourcedriver/protocol/protocoltest"
)

// remoteFixture runs the conformance suite over a World whose drivers are
// served over the protocol: each behind a server on one end of a socket
// pair, with the client in front of it on the other. Restart loses the
// connection and kills the driver, as its process's death would.
type remoteFixture struct {
	*protocoltest.Remote
	w *World
}

func (f *remoteFixture) Natives() conformance.Natives                 { return f.w }
func (f *remoteFixture) Repositories() conformance.RepositoryControls { return f.w }
func (f *remoteFixture) Settling() conformance.SettlingControls       { return f.w }
func (f *remoteFixture) Service() conformance.ServiceControls         { return f.w }
func (f *remoteFixture) Origin() conformance.OriginControls           { return f.w }

func TestConformanceOverTheProtocol(t *testing.T) {
	conformance.Run(t, func(t *testing.T) conformance.Fixture {
		w := NewWorld()
		remote := protocoltest.Serve(t, func() (sourcedriver.Driver, func()) {
			d := NewDriver(w)
			return d, d.Kill
		}, protocol.Hello{Config: []byte(`{}`)})
		return &remoteFixture{Remote: remote, w: w}
	})
}
