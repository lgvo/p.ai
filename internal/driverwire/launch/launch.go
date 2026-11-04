// Package launch starts a driver executable and keeps it running: it applies
// the launch checks before every start, starts exactly the file it checked
// over a socket pair with only the environment it is given, has the
// protocol agreed in the hello, and restarts the driver with a bounded
// backoff when it dies. A driver that dies or stops takes its process group
// along. What the connection carries, and what its hello says, are the
// driver's protocol's.
package launch

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/lgvo/p.ai/internal/driverwire"
)

// helloWait bounds a start: from the exec to the driver's answer to the
// hello.
const helloWait = 30 * time.Second

// stopWait bounds how long a driver whose connection core closed may take
// to exit before it is killed.
const stopWait = 5 * time.Second

// Conn is the connection to a running driver, which its hello made.
type Conn interface {
	// Close ends the connection, which tells the driver to exit.
	Close() error
	// Done closes once the connection is lost.
	Done() <-chan struct{}
}

// Config is how to start a driver and keep it running: its executable,
// pinned by path and SHA-256 in trusted host configuration, its
// environment, and its hello.
type Config[C Conn] struct {
	// Name says which driver it is, in messages, such as "runtime driver".
	Name   string
	Path   string // absolute and clean
	SHA256 string // lowercase hex
	// Env is the driver's whole environment, as KEY=value entries; it
	// inherits nothing from the daemon's.
	Env []string
	// Hello agrees the protocol with the driver just started at the other
	// end of uc and returns the connection to it. It owns uc, which it
	// closes on an error.
	Hello func(ctx context.Context, uc *net.UnixConn) (C, error)
	// Accept is applied to the connection of every driver started, before
	// it serves: a driver it refuses is killed, and its start fails with
	// the refusal. Nil accepts every driver.
	Accept func(C) error
	// Backoff is how long to wait before each restart; zero is the
	// default.
	Backoff Backoff
	// Open, when set, applies the launch checks for account, the account P
	// runs as, and returns the open file to start, which it checked and
	// hashed, named by its path; Path then only names the driver in
	// messages. When nil, the launch checks are applied to the executable
	// at Path against SHA256.
	Open func(account uint32) (*os.File, error)
	// StopAfterFailedCheck leaves the driver stopped once a restart fails
	// its launch checks, until Close; otherwise a restart is tried again.
	StopAfterFailedCheck bool
}

// Backoff is how long the supervisor waits before each restart: Min after a
// driver that ran for Stable, then twice the last wait, up to Max.
type Backoff struct {
	Min, Max, Stable time.Duration
}

var defaultBackoff = Backoff{Min: 100 * time.Millisecond, Max: 30 * time.Second, Stable: time.Minute}

// next is the wait after last.
func (b Backoff) next(last time.Duration) time.Duration {
	if last < b.Min {
		return b.Min
	}
	return min(2*last, b.Max)
}

// Supervisor runs one driver executable and holds the connection to it.
type Supervisor[C Conn] struct {
	cfg     Config[C]
	account uint32 // P's account, which the launch checks trust beside root

	ctx     context.Context // ends at Close
	cancel  context.CancelFunc
	stopped chan struct{} // closes once supervision ended

	mu      sync.Mutex
	current *process[C] // nil while the driver is down
}

// Start starts the driver cfg names and supervises it until Close. The
// first start's failure, of the launch checks, the hash, the exec, the
// hello or Accept, is returned, so that the daemon refuses to start.
func Start[C Conn](ctx context.Context, cfg Config[C]) (*Supervisor[C], error) {
	if cfg.Backoff == (Backoff{}) {
		cfg.Backoff = defaultBackoff
	}
	s := &Supervisor[C]{cfg: cfg, account: uint32(os.Geteuid()), stopped: make(chan struct{})}
	p, err := s.launch(ctx)
	if err != nil {
		return nil, err
	}
	s.current = p
	s.ctx, s.cancel = context.WithCancel(context.Background())
	go s.supervise(p)
	return s, nil
}

// Current is the connection to the running driver, and false while the
// driver is down.
func (s *Supervisor[C]) Current() (C, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		var none C
		return none, false
	}
	return s.current.client, true
}

// Close stops the driver and its supervision.
func (s *Supervisor[C]) Close() error {
	s.cancel()
	<-s.stopped
	return nil
}

// process is one started driver.
type process[C Conn] struct {
	proc   *os.Process
	client C
	// exited closes once the driver exited, its process group was killed
	// and the driver was reaped.
	exited  chan struct{}
	state   *os.ProcessState // how it exited, set before exited closes
	started time.Time
}

// launch applies the launch checks, starts the file checked and applies
// Accept.
func (s *Supervisor[C]) launch(ctx context.Context) (*process[C], error) {
	open := s.cfg.Open
	if open == nil {
		open = func(account uint32) (*os.File, error) { return checkExecutable(s.cfg.Path, s.cfg.SHA256, account) }
	}
	exe, err := open(s.account)
	if err != nil {
		// The refusal names the driver: "the runtime driver failed its
		// launch checks: ...".
		return nil, fmt.Errorf("the %s %w", s.cfg.Name, err)
	}
	defer exe.Close()
	p, err := startChecked(ctx, s.cfg, exe)
	if err != nil {
		return nil, err
	}
	if s.cfg.Accept != nil {
		if err := s.cfg.Accept(p.client); err != nil {
			p.kill()
			return nil, err
		}
	}
	return p, nil
}

// startChecked starts exe, the open executable the launch checks passed,
// by its descriptor: the child execs /proc/self/fd/N for its own copy N of
// exe, so a file put at the path after the check is never started. The
// child gets the connection on driverwire.ConnFD, exe on
// driverwire.ExecutableFD, /dev/null as input, the daemon's standard error
// for its output, exe's path as its only argument and cfg.Env as its whole
// environment, in a process group of its own, which it leads. It then
// agrees the hello.
func startChecked[C Conn](ctx context.Context, cfg Config[C], exe *os.File) (*process[C], error) {
	parent, child, err := driverwire.SocketPair(cfg.Name + " connection")
	if err != nil {
		return nil, fmt.Errorf("a connection for the %s: %w", cfg.Name, err)
	}
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		parent.Close()
		child.Close()
		return nil, err
	}
	files := make([]*os.File, driverwire.ExecutableFD+1)
	files[0], files[1], files[2] = devnull, os.Stderr, os.Stderr
	files[driverwire.ConnFD], files[driverwire.ExecutableFD] = child, exe
	proc, err := os.StartProcess("/proc/self/fd/"+strconv.Itoa(driverwire.ExecutableFD), []string{exe.Name()}, &os.ProcAttr{
		Dir: "/", Files: files,
		// Never nil, which would give the driver the daemon's environment.
		Env: append([]string{}, cfg.Env...),
		Sys: &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL},
	})
	child.Close()
	devnull.Close()
	if err != nil {
		parent.Close()
		return nil, fmt.Errorf("start %s %s: %w", cfg.Name, cfg.Path, err)
	}
	p := &process[C]{proc: proc, exited: make(chan struct{}), started: time.Now()}
	go p.wait()
	hctx, cancel := context.WithTimeout(ctx, helloWait)
	defer cancel()
	client, err := cfg.Hello(hctx, parent)
	if err != nil {
		p.end()
		return nil, fmt.Errorf("start %s %s: %w", cfg.Name, cfg.Path, err)
	}
	p.client = client
	return p, nil
}

// wait waits for the driver to exit, however it ends, then kills its
// process group: the driver's ID, which the group bears, stays reserved
// while the driver is dead and not yet reaped, so the kill reaches nothing
// but the group, and everything the driver started that did not leave it,
// such as a Git command still changing a head, ends before a new driver
// serves. Only then does it reap the driver. A driver whose exit cannot be
// awaited without reaping it is reaped with its group left as it is.
func (p *process[C]) wait() {
	if err := awaitExit(p.proc.Pid); err == nil {
		syscall.Kill(-p.proc.Pid, syscall.SIGKILL)
	} else {
		log.Printf("the driver %d's exit could not be awaited, so its process group was not killed: %v", p.proc.Pid, err)
	}
	p.state, _ = p.proc.Wait()
	close(p.exited)
}

// pPID is waitid's idtype for one process by its ID, which the syscall
// package does not define.
const pPID = 1

// awaitExit waits until the child pid exited, without reaping it, so that
// it stays a zombie that holds its ID.
func awaitExit(pid int) error {
	var info [128]byte // a siginfo_t, which waitid fills and nothing reads
	for {
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, pPID, uintptr(pid), uintptr(unsafe.Pointer(&info[0])), syscall.WEXITED|syscall.WNOWAIT, 0, 0)
		switch errno {
		case 0:
			return nil
		case syscall.EINTR:
			continue
		}
		return errno
	}
}

// kill ends the driver at once: its connection and its process, then waits
// until its group was killed and it was reaped.
func (p *process[C]) kill() {
	p.client.Close()
	p.end()
}

// end kills the driver's process and waits until its group was killed and
// it was reaped.
func (p *process[C]) end() {
	p.proc.Kill()
	<-p.exited
}

// stop closes the connection, which tells the driver to exit, and kills it
// if it has not after stopWait. Either way its group is killed before it is
// reaped.
func (p *process[C]) stop() {
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
func (s *Supervisor[C]) supervise(p *process[C]) {
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
		if time.Since(p.started) >= s.cfg.Backoff.Stable {
			wait = 0
		}
		if lost {
			log.Printf("%s %s lost its connection and was killed (%v); restarting it", s.cfg.Name, s.cfg.Path, p.state)
		} else {
			log.Printf("%s %s ended (%v); restarting it", s.cfg.Name, s.cfg.Path, p.state)
		}
		if p, wait = s.restart(wait); p == nil {
			return
		}
		s.setCurrent(p)
		log.Printf("%s %s restarted", s.cfg.Name, s.cfg.Path)
	}
}

// restart starts the driver again, waiting before each attempt as the
// backoff says after the last wait, until a start succeeds or Close, or a
// start fails its launch checks and the driver stays stopped.
func (s *Supervisor[C]) restart(wait time.Duration) (*process[C], time.Duration) {
	for {
		wait = s.cfg.Backoff.next(wait)
		if !s.sleep(wait) {
			return nil, wait
		}
		p, err := s.launch(s.ctx)
		if err == nil {
			return p, wait
		}
		if s.cfg.StopAfterFailedCheck && errors.Is(err, ErrLaunchCheck) {
			log.Printf("%s %s did not restart and stays stopped: %v", s.cfg.Name, s.cfg.Path, err)
			return nil, wait
		}
		log.Printf("%s %s did not restart: %v", s.cfg.Name, s.cfg.Path, err)
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
func (s *Supervisor[C]) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-s.ctx.Done():
		return false
	}
}

func (s *Supervisor[C]) setCurrent(p *process[C]) {
	s.mu.Lock()
	s.current = p
	s.mu.Unlock()
}
