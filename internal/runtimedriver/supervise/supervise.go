// Package supervise starts a runtime driver executable and keeps it
// running: it applies the launch checks before every start, starts exactly
// the file it checked over a socket pair, agrees the protocol in the hello,
// and restarts the driver with a bounded backoff when it dies. The
// Supervisor is the runtimedriver.Driver core holds; calls in flight when
// the driver dies end as the protocol says.
package supervise

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"reflect"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimedriver/protocol"
)

// helloWait bounds a start: from the exec to the driver's answer to the
// hello.
const helloWait = 30 * time.Second

// stopWait bounds how long a driver whose connection core closed may take
// to exit before it is killed.
const stopWait = 5 * time.Second

// Config is how to start a driver: its executable, pinned by path and
// SHA-256 in trusted host configuration, and the hello it is given.
type Config struct {
	Path   string // absolute and clean
	SHA256 string // lowercase hex
	Hello  protocol.Hello
}

// backoff is how long the supervisor waits before each restart: min after a
// driver that ran for stable, then twice the last wait, up to max.
type backoff struct {
	min, max, stable time.Duration
}

var defaultBackoff = backoff{min: 100 * time.Millisecond, max: 30 * time.Second, stable: time.Minute}

// next is the wait after last.
func (b backoff) next(last time.Duration) time.Duration {
	if last < b.min {
		return b.min
	}
	return min(2*last, b.max)
}

// Supervisor runs one driver executable and is the runtimedriver.Driver
// that reaches it. While the driver is down, every call returns an error of
// kind ErrUnavailable: nothing happened.
type Supervisor struct {
	cfg          Config
	account      uint32
	backoff      backoff
	capabilities runtimedriver.Capabilities

	ctx     context.Context // ends at Close
	cancel  context.CancelFunc
	stopped chan struct{} // closes once supervision ended

	mu      sync.Mutex
	current *process // nil while the driver is down
}

var _ runtimedriver.Driver = (*Supervisor)(nil)

// Start starts the driver cfg names and supervises it until Close. The
// first start's failure, of the launch checks, the hash, the exec or the
// hello, is returned, so that the daemon refuses to start.
func Start(ctx context.Context, cfg Config) (*Supervisor, error) {
	return start(ctx, cfg, uint32(os.Geteuid()), defaultBackoff)
}

func start(ctx context.Context, cfg Config, account uint32, b backoff) (*Supervisor, error) {
	s := &Supervisor{cfg: cfg, account: account, backoff: b, stopped: make(chan struct{})}
	p, err := s.launch(ctx)
	if err != nil {
		return nil, err
	}
	s.capabilities = p.client.Describe()
	s.current = p
	s.ctx, s.cancel = context.WithCancel(context.Background())
	go s.supervise(p)
	return s, nil
}

// Close stops the driver and its supervision.
func (s *Supervisor) Close() error {
	s.cancel()
	<-s.stopped
	return nil
}

// process is one started driver.
type process struct {
	proc    *os.Process
	client  *protocol.Client
	exited  chan struct{}    // closes once the process was reaped
	state   *os.ProcessState // how it exited, set before exited closes
	started time.Time
}

// launch applies the launch checks and starts the file checked.
func (s *Supervisor) launch(ctx context.Context) (*process, error) {
	exe, err := checkExecutable(s.cfg.Path, s.cfg.SHA256, s.account)
	if err != nil {
		return nil, err
	}
	defer exe.Close()
	return startChecked(ctx, s.cfg.Path, exe, s.cfg.Hello)
}

// startChecked starts exe, the open executable the launch checks passed,
// by its descriptor: the child execs /proc/self/fd/N for its own copy N of
// exe, so a file put at path after the check is never started. The child
// gets the connection on protocol.ConnFD, exe on protocol.ExecutableFD,
// /dev/null as input, the daemon's standard error for its output, path as
// its only argument and an empty environment. It then agrees the hello.
func startChecked(ctx context.Context, path string, exe *os.File, hello protocol.Hello) (*process, error) {
	parent, child, err := socketPair()
	if err != nil {
		return nil, fmt.Errorf("a connection for the runtime driver: %w", err)
	}
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		parent.Close()
		child.Close()
		return nil, err
	}
	files := make([]*os.File, protocol.ExecutableFD+1)
	files[0], files[1], files[2] = devnull, os.Stderr, os.Stderr
	files[protocol.ConnFD], files[protocol.ExecutableFD] = child, exe
	proc, err := os.StartProcess("/proc/self/fd/"+strconv.Itoa(protocol.ExecutableFD), []string{path}, &os.ProcAttr{
		Dir: "/", Env: []string{}, Files: files,
		Sys: &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL},
	})
	child.Close()
	devnull.Close()
	if err != nil {
		parent.Close()
		return nil, fmt.Errorf("start runtime driver %s: %w", path, err)
	}
	p := &process{proc: proc, exited: make(chan struct{}), started: time.Now()}
	go func() {
		p.state, _ = proc.Wait()
		close(p.exited)
	}()
	hctx, cancel := context.WithTimeout(ctx, helloWait)
	defer cancel()
	client, err := protocol.Connect(hctx, parent, hello)
	if err != nil {
		p.kill()
		return nil, fmt.Errorf("start runtime driver %s: %w", path, err)
	}
	p.client = client
	return p, nil
}

// socketPair is a connected pair of Unix stream sockets: core's end as a
// connection and the driver's as a file.
func socketPair() (*net.UnixConn, *os.File, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	local := os.NewFile(uintptr(fds[0]), "runtime driver connection")
	remote := os.NewFile(uintptr(fds[1]), "runtime driver connection")
	c, err := net.FileConn(local)
	local.Close()
	if err != nil {
		remote.Close()
		return nil, nil, err
	}
	return c.(*net.UnixConn), remote, nil
}

// kill ends the process at once and reaps it.
func (p *process) kill() {
	if p.client != nil {
		p.client.Close()
	}
	p.proc.Kill()
	<-p.exited
}

// stop closes the connection, which tells the driver to exit, and kills it
// if it has not after stopWait.
func (p *process) stop() {
	p.client.Close()
	wait := time.NewTimer(stopWait)
	defer wait.Stop()
	select {
	case <-p.exited:
	case <-wait.C:
		p.kill()
	}
}

// supervise watches the running driver p and restarts it when its
// connection is lost or its process exits, until Close.
func (s *Supervisor) supervise(p *process) {
	defer close(s.stopped)
	var wait time.Duration
	for {
		select {
		case <-p.client.Done():
		case <-p.exited:
		case <-s.ctx.Done():
			s.setCurrent(nil)
			p.stop()
			return
		}
		s.setCurrent(nil)
		lost := !isClosed(p.exited)
		p.kill()
		if time.Since(p.started) >= s.backoff.stable {
			wait = 0
		}
		if lost {
			log.Printf("runtime driver %s lost its connection and was killed (%v); restarting it", s.cfg.Path, p.state)
		} else {
			log.Printf("runtime driver %s ended (%v); restarting it", s.cfg.Path, p.state)
		}
		if p, wait = s.restart(wait); p == nil {
			return
		}
		s.setCurrent(p)
		log.Printf("runtime driver %s restarted", s.cfg.Path)
	}
}

// restart starts the driver again, waiting before each attempt as the
// backoff says after the last wait, until a start succeeds or Close. A
// driver that describes other capabilities than at the first start fails
// its start.
func (s *Supervisor) restart(wait time.Duration) (*process, time.Duration) {
	for {
		wait = s.backoff.next(wait)
		if !s.sleep(wait) {
			return nil, wait
		}
		p, err := s.launch(s.ctx)
		if err == nil && !reflect.DeepEqual(p.client.Describe(), s.capabilities) {
			p.kill()
			err = errors.New("the restarted driver describes other capabilities than at its first start")
		}
		if err == nil {
			return p, wait
		}
		log.Printf("runtime driver %s did not restart: %v", s.cfg.Path, err)
	}
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// sleep waits for d and reports false if Close came first.
func (s *Supervisor) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-s.ctx.Done():
		return false
	}
}

func (s *Supervisor) setCurrent(p *process) {
	s.mu.Lock()
	s.current = p
	s.mu.Unlock()
}

var errDown = fmt.Errorf("the runtime driver is not running: %w", runtimedriver.ErrUnavailable)

// driver is the running driver's client.
func (s *Supervisor) driver() (*protocol.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return nil, errDown
	}
	return s.current.client, nil
}
