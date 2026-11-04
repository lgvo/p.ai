// Package supervise starts the runtime driver executable and keeps it
// running, with launch: it applies the launch checks before every start,
// starts exactly the file it checked over a socket pair with an empty
// environment, agrees the protocol in the hello, and restarts the driver
// with a bounded backoff when it dies. The Supervisor is the
// runtimedriver.Driver core holds; calls in flight when the driver dies end
// as the protocol says, and a restarted driver must describe what the first
// one did.
package supervise

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"

	"github.com/lgvo/p.ai/internal/driverwire/launch"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimedriver/protocol"
)

// ErrLaunchCheck is the error of a driver executable that failed its launch
// checks: the daemon does not start it.
var ErrLaunchCheck = launch.ErrLaunchCheck

// Config is how to start a driver: its executable, pinned by path and
// SHA-256 in trusted host configuration, and the hello it is given.
type Config struct {
	Path   string // absolute and clean
	SHA256 string // lowercase hex
	Hello  protocol.Hello
}

// Supervisor runs one driver executable and is the runtimedriver.Driver
// that reaches it. While the driver is down, every call returns an error of
// kind ErrUnavailable: nothing happened.
type Supervisor struct {
	run *launch.Supervisor[*protocol.Client]
	// capabilities is what the driver described at its first start, which
	// described says came.
	capabilities runtimedriver.Capabilities
	described    bool
}

var _ runtimedriver.Driver = (*Supervisor)(nil)

// Start starts the driver cfg names and supervises it until Close. The
// first start's failure, of the launch checks, the hash, the exec or the
// hello, is returned, so that the daemon refuses to start.
func Start(ctx context.Context, cfg Config) (*Supervisor, error) {
	return start(ctx, cfg, launch.Backoff{})
}

// start is Start with backoff b, or the default for a zero b.
func start(ctx context.Context, cfg Config, b launch.Backoff) (*Supervisor, error) {
	s := &Supervisor{}
	run, err := launch.Start(ctx, launch.Config[*protocol.Client]{
		Name: "runtime driver", Path: cfg.Path, SHA256: cfg.SHA256,
		Hello: func(ctx context.Context, uc *net.UnixConn) (*protocol.Client, error) {
			return protocol.Connect(ctx, uc, cfg.Hello)
		},
		Accept:  s.accept,
		Backoff: b,
	})
	if err != nil {
		return nil, err
	}
	s.run = run
	return s, nil
}

// accept keeps what the driver describes at its first start, and fails the
// start of a restarted driver that describes other capabilities.
func (s *Supervisor) accept(c *protocol.Client) error {
	if !s.described {
		s.capabilities, s.described = c.Describe(), true
		return nil
	}
	if !reflect.DeepEqual(c.Describe(), s.capabilities) {
		return errors.New("the restarted driver describes other capabilities than at its first start")
	}
	return nil
}

// Close stops the driver and its supervision.
func (s *Supervisor) Close() error { return s.run.Close() }

var errDown = fmt.Errorf("the runtime driver is not running: %w", runtimedriver.ErrUnavailable)

// driver is the running driver's client.
func (s *Supervisor) driver() (*protocol.Client, error) {
	c, ok := s.run.Current()
	if !ok {
		return nil, errDown
	}
	return c, nil
}
