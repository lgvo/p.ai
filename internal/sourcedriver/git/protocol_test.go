package git

import (
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/conformance"
	"github.com/lgvo/p.ai/internal/sourcedriver/protocol"
	"github.com/lgvo/p.ai/internal/sourcedriver/protocol/protocoltest"
)

// remoteFixture runs the conformance suite over the fixture's storage with
// each driver served over the protocol: behind a server on one end of a
// socket pair, with the client in front of it on the other. The fixture's
// controls act on the driver being served. Restart loses the connection
// and closes the driver, which ends every service it runs, as its process's
// death would.
type remoteFixture struct {
	*fixture
	remote *protocoltest.Remote
}

func (f *remoteFixture) Driver() sourcedriver.Driver { return f.remote.Driver() }
func (f *remoteFixture) Restart()                    { f.remote.Restart() }

func TestConformanceOverTheProtocol(t *testing.T) {
	conformance.Run(t, func(t *testing.T) conformance.Fixture {
		f := &remoteFixture{fixture: newFixture(t).(*fixture)}
		f.driver.Close()
		f.remote = protocoltest.Serve(t, func() (sourcedriver.Driver, func()) {
			d := f.newDriver()
			f.driver = d
			return d, func() { d.Close() }
		}, protocol.Hello{Config: driverConfig(f.gitPath, f.origins.ssh()), Core: protocol.Core{Storage: f.storage}})
		return f
	})
}
