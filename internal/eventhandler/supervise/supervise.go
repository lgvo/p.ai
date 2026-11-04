// Package supervise starts the event handler that trusted host
// configuration selects and keeps it running, with launch: before every
// start it applies the launch checks to the handler's package and hashes
// it, starts exactly the entry it checked over a socket pair with no
// environment, agrees the protocol in the hello and requires the handler to
// answer as its package names it. A handler that exits, or misses an
// event's deadline, is killed with its process group and started again
// with a bounded backoff; one whose package then fails the launch checks
// stays stopped.
package supervise

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/lgvo/p.ai/internal/driverwire/launch"
	"github.com/lgvo/p.ai/internal/eventhandler"
	"github.com/lgvo/p.ai/internal/plugin"
)

// Config is the handler host configuration selects: its package, pinned by
// directory and package digest, and the handler's own config, which only
// the handler reads.
type Config struct {
	Path   string // the package directory, absolute and clean
	SHA256 string // the package digest, lowercase hex
	Config json.RawMessage
}

// Supervisor runs one event handler.
type Supervisor struct {
	run *launch.Supervisor[*eventhandler.Client]
}

// Start starts the handler cfg selects and supervises it until Close. The
// first start's failure, of the launch checks, the digest, the exec, the
// hello or the handler's identity, is returned, so that the daemon refuses
// to start.
func Start(ctx context.Context, cfg Config) (*Supervisor, error) {
	// manifest is the package of the last launch, which the hello's answer
	// must name.
	var manifest plugin.Manifest
	run, err := launch.Start(ctx, launch.Config[*eventhandler.Client]{
		Name: "event handler", Path: cfg.Path,
		Open: func(account uint32) (*os.File, error) {
			pkg, entry, err := plugin.OpenExecutable(cfg.Path, cfg.SHA256, account)
			if err != nil {
				return nil, err
			}
			if pkg.Manifest.Capability != plugin.CapabilityEventHandler {
				entry.Close()
				return nil, launch.Refusal(cfg.Path, "provides "+pkg.Manifest.Capability+", not "+plugin.CapabilityEventHandler)
			}
			manifest = pkg.Manifest
			return entry, nil
		},
		Hello: func(ctx context.Context, uc *net.UnixConn) (*eventhandler.Client, error) {
			return eventhandler.Connect(ctx, uc, cfg.Config)
		},
		Accept: func(c *eventhandler.Client) error {
			if got := c.Identity(); got.ID != manifest.ID || got.Version != manifest.Version {
				return fmt.Errorf("the event handler answered as %s %s, but its package is %s %s", got.ID, got.Version, manifest.ID, manifest.Version)
			}
			return nil
		},
		StopAfterFailedCheck: true,
	})
	if err != nil {
		return nil, err
	}
	return &Supervisor{run: run}, nil
}

// Close stops the handler and its supervision.
func (s *Supervisor) Close() error { return s.run.Close() }

// errDown is the error of an event sent while the handler is not running.
var errDown = fmt.Errorf("the event handler is not running: %w", eventhandler.ErrUnavailable)

// Handle sends e to the running handler and returns what it did with it.
// While the handler is down, it returns an error of kind
// eventhandler.ErrUnavailable at once. When ctx's deadline passes before
// the handler answers, the handler may still be busy with e, or may hold
// up P's writes to it: its connection is cut at once, so it is killed and
// started again, and Handle returns the deadline's error.
func (s *Supervisor) Handle(ctx context.Context, e eventhandler.Event) (eventhandler.Status, error) {
	c, ok := s.run.Current()
	if !ok {
		return "", errDown
	}
	cut := context.AfterFunc(ctx, func() {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			c.Close()
		}
	})
	status, err := c.Handle(ctx, e)
	cut()
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) && !errors.Is(err, context.DeadlineExceeded) {
		// The cut ended a write in progress, which reports only the lost
		// connection.
		err = fmt.Errorf("%w: %w", err, ctx.Err())
	}
	return status, err
}
