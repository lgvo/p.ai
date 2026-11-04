// Package supervise starts the source driver executable and keeps it
// running, with launch: it applies the launch checks before every start,
// starts exactly the file it checked over a socket pair with only the
// daemon's HOME and SSH_AUTH_SOCK, agrees the protocol in the hello, has
// the driver Check itself before it serves, and restarts it with a bounded
// backoff when it dies, after killing its process group. The Supervisor is
// the sourcedriver.Driver core holds; calls in flight when the driver dies
// end as the protocol says, calls while it is down answer
// sourcedriver.ErrUnavailable, and a restarted driver must describe what
// the first one did.
package supervise

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"slices"
	"time"

	"github.com/lgvo/p.ai/internal/driverwire/launch"
	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/protocol"
)

// ErrLaunchCheck is the error of a driver executable that failed its launch
// checks: the daemon does not start it.
var ErrLaunchCheck = launch.ErrLaunchCheck

// checkWait bounds the Check a driver passes before it serves: a restarted
// bundled driver ends there the Git services its dead predecessor left.
const checkWait = time.Minute

// inherited are the variables of the daemon's environment the driver gets,
// each when the daemon has it: the account's home, for its OpenSSH
// configuration and known hosts, and its SSH agent, with which the driver
// reaches origins. It gets nothing else; its own configuration names the
// programs it runs.
var inherited = []string{"HOME", "SSH_AUTH_SOCK"}

// Config is how to start the driver: its executable, pinned by path and
// SHA-256 in trusted host configuration, and the hello it is given, whose
// Core.Executable the supervisor sets to Path.
type Config struct {
	Path   string // absolute and clean
	SHA256 string // lowercase hex
	Hello  protocol.Hello
}

// Supervisor runs one source driver executable and is the
// sourcedriver.Driver that reaches it. While the driver is down, every call
// returns an error of kind ErrUnavailable: nothing happened.
type Supervisor struct {
	run *launch.Supervisor[*protocol.Client]
	// description is what the driver described at its first start, which
	// described says came.
	description sourcedriver.Description
	described   bool
}

var _ sourcedriver.Driver = (*Supervisor)(nil)

// Start starts the driver cfg names and supervises it until Close. The
// first start's failure, of the launch checks, the hash, the exec, the
// hello or the driver's Check, is returned, so that the daemon refuses to
// start.
func Start(ctx context.Context, cfg Config) (*Supervisor, error) {
	return start(ctx, cfg, launch.Backoff{})
}

// start is Start with backoff b, or the default for a zero b.
func start(ctx context.Context, cfg Config, b launch.Backoff) (*Supervisor, error) {
	s := &Supervisor{}
	hello := cfg.Hello
	hello.Core.Executable = cfg.Path
	run, err := launch.Start(ctx, launch.Config[*protocol.Client]{
		Name: "source driver", Path: cfg.Path, SHA256: cfg.SHA256, Env: environment(),
		Hello: func(ctx context.Context, uc *net.UnixConn) (*protocol.Client, error) {
			return protocol.Connect(ctx, uc, hello)
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

// environment is the driver's whole environment: the inherited variables
// the daemon has.
func environment() []string {
	var env []string
	for _, name := range inherited {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

// accept keeps what the driver describes at its first start, fails the
// start of a restarted driver that describes otherwise, and has every
// driver started Check itself before it serves.
func (s *Supervisor) accept(c *protocol.Client) error {
	if !s.described {
		s.description, s.described = c.Describe(), true
	} else if !reflect.DeepEqual(c.Describe(), s.description) {
		return errors.New("the restarted source driver describes itself otherwise than at its first start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkWait)
	defer cancel()
	if err := c.Check(ctx); err != nil {
		return fmt.Errorf("the source driver failed its check: %w", err)
	}
	return nil
}

// Close stops the driver and its supervision.
func (s *Supervisor) Close() error { return s.run.Close() }

var errDown = fmt.Errorf("the source driver is not running: %w", sourcedriver.ErrUnavailable)

// driver is the running driver's client.
func (s *Supervisor) driver() (*protocol.Client, error) {
	c, ok := s.run.Current()
	if !ok {
		return nil, errDown
	}
	return c, nil
}

// Describe is what the driver described at its first start; a restarted
// driver must describe the same.
func (s *Supervisor) Describe() sourcedriver.Description {
	d := s.description
	d.Commands = slices.Clone(d.Commands)
	return d
}
