package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/gitnames"
)

// outputDelay bounds how long a service that has ended waits for its client
// to take Git's last output, as today's Git service does.
const outputDelay = 2 * time.Second

// ServeGit serves one command of P's Git service: git upload-pack or git
// receive-pack on the project's repository, with today's restrictions, in a
// process group of its own that is killed when ctx ends, the service passes
// its deadline or the driver closes. It returns only once that process has
// exited and its hook's approval, if any, has answered: from then on
// nothing it started can change a head. A write of Git's output to a client
// that stopped reading may still be pending then; it fails once core closes
// the channel.
func (d *Driver) ServeGit(ctx context.Context, command string, stream sourcedriver.Stream, open sourcedriver.Open) error {
	kind, project, ok := parseCommand(command)
	if !ok {
		return fmt.Errorf("the SSH command is not a Git service's: %w", sourcedriver.ErrInvalid)
	}
	if d.services.isClosed() {
		return errClosed
	}
	service, approve, err := open(kind, project)
	if err != nil {
		return err
	}
	if err := checkService(service, kind, project); err != nil {
		return err
	}
	ctx, running, err := d.services.start(ctx, service)
	if err != nil {
		return err
	}
	defer d.services.end(running)
	return d.serve(ctx, service, approve, stream)
}

// parseCommand reads the SSH command as today's Git service does: the
// service's word, then the project in single quotes, a leading slash
// allowed.
func parseCommand(raw string) (sourcedriver.ServiceKind, string, bool) {
	var kind sourcedriver.ServiceKind
	if rest, ok := strings.CutPrefix(raw, "git-upload-pack '"); ok {
		kind, raw = sourcedriver.ServiceUpload, rest
	} else if rest, ok := strings.CutPrefix(raw, "git-receive-pack '"); ok {
		kind, raw = sourcedriver.ServiceReceive, rest
	} else {
		return "", "", false
	}
	project, ok := strings.CutSuffix(raw, "'")
	project = strings.TrimPrefix(project, "/")
	if !ok || strings.ContainsAny(project, "'\\\n\r\t") || !validProject(project) {
		return "", "", false
	}
	return kind, project, true
}

// checkService refuses a service open authorized for another command, or
// without the ceilings and head the service needs.
func checkService(s sourcedriver.Service, kind sourcedriver.ServiceKind, project string) error {
	switch {
	case s.Kind != kind || s.Project != project:
		return fmt.Errorf("core opened a %s service of %q for a %s command of %q", s.Kind, s.Project, kind, project)
	case s.ID == "" || s.MaxInput <= 0 || s.Deadline <= 0:
		return fmt.Errorf("the service lacks an ID or a ceiling: %w", sourcedriver.ErrInvalid)
	case kind == sourcedriver.ServiceReceive && !gitnames.Branch(s.Branch):
		return fmt.Errorf("the receive service's branch: %w", sourcedriver.ErrInvalid)
	}
	return nil
}

// serve checks the repository, opens the hook's approval for a receive
// service, and runs Git.
func (d *Driver) serve(ctx context.Context, service sourcedriver.Service, approve sourcedriver.Approve, stream sourcedriver.Stream) error {
	repo, err := d.repository(service.Project)
	if err != nil {
		return err
	}
	if present, err := d.storagePresent(); err != nil {
		return err
	} else if !present {
		return sourcedriver.ErrNotFound
	}
	if _, err := os.Lstat(repo); errors.Is(err, os.ErrNotExist) {
		return sourcedriver.ErrNotFound
	} else if err != nil {
		return err
	}
	if _, err := d.checkRepository(ctx, repo); err != nil {
		return err
	}
	args := transportGitConfigArgs(service.Kind, d.hooksDirectory(), service.MaxInput)
	if service.Kind == sourcedriver.ServiceUpload {
		return d.transport(ctx, service, append(args, "upload-pack", repo), d.gitEnv(), stream)
	}
	if err := d.installHook(); err != nil {
		return err
	}
	a, err := d.startApproval(ctx, service.Branch, approve)
	if err != nil {
		return err
	}
	defer a.stop()
	return d.transport(ctx, service, append(args, "receive-pack", repo), d.gitEnv(d.hookEnv(service.Branch, a)...), stream)
}

// transportGitConfigArgs are today's restrictions of the Git service: only
// heads are advertised, a want must name an advertised tip, and a receive
// runs P's hook only, refuses deletions and non-fast-forwards and bounds
// its pack. The protective options come last: Git's command-line -c
// overrides the trusted environment, so earlier transport settings cannot
// enable pruning.
func transportGitConfigArgs(kind sourcedriver.ServiceKind, hooksDir string, maxInput int64) []string {
	args := []string{"-c", "transfer.hideRefs=refs", "-c", "transfer.hideRefs=!refs/heads", "-c", "uploadpack.allowTipSHA1InWant=false", "-c", "uploadpack.allowReachableSHA1InWant=false", "-c", "uploadpack.allowAnySHA1InWant=false", "-c", "uploadpack.allowRefInWant=false"}
	if kind == sourcedriver.ServiceReceive {
		args = append(args, "-c", "core.hooksPath="+hooksDir, "-c", "receive.denyDeletes=true", "-c", "receive.denyNonFastForwards=true", "-c", "receive.maxInputSize="+strconv.FormatInt(maxInput, 10))
	}
	return append(args, noImplicitMaintenanceArgs()...)
}

// transport runs Git with args on the stream until it exits, or until ctx
// ends, when it kills Git's process group. The driver feeds Git's input and
// copies its output itself, so that a client that stops sending or reading
// cannot keep the service past its deadline. The service's record lasts
// from before Git reads any input until Git has exited.
func (d *Driver) transport(ctx context.Context, service sourcedriver.Service, args, env []string, stream sourcedriver.Stream) error {
	p, err := startTransport(d.gitPath, args, env)
	if err != nil {
		return err
	}
	removeRecord, err := d.recordService(service.ID, p.cmd.Process.Pid)
	if err != nil {
		p.abandon()
		return err
	}
	input := &boundedInput{source: stream.In, remaining: service.MaxInput}
	go p.feed(input)
	copied := make(chan error, 2)
	go copyOutput(stream.Out, p.stdout, copied)
	go copyOutput(stream.Err, p.stderr, copied)
	exit := p.wait(ctx)
	removeRecord()
	outputErr := p.awaitOutput(ctx, copied)
	switch {
	case input.overflow.Load():
		return fmt.Errorf("the client's input passed the service's ceiling: %w", sourcedriver.ErrLimit)
	case ctx.Err() != nil:
		return serviceEnd(ctx)
	case exit != nil:
		return fmt.Errorf("git %s failed: %w", args[len(args)-2], exit)
	}
	return outputErr
}

// serviceEnd says why a service's context ended: its deadline, the
// driver's Close, or the caller.
func serviceEnd(ctx context.Context) error {
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errClosed):
		return cause
	case errors.Is(cause, errDeadline):
		return fmt.Errorf("the service ran past its deadline: %w", sourcedriver.ErrLimit)
	default:
		return ctx.Err()
	}
}

// transportProcess is one running upload-pack or receive-pack, with the
// driver's ends of its pipes.
type transportProcess struct {
	cmd            *exec.Cmd
	stdin          *os.File // the driver writes Git's input here
	stdout, stderr *os.File // and reads its output here
}

func startTransport(gitPath string, args, env []string) (*transportProcess, error) {
	var parent, child [3]*os.File
	closeAll := func() {
		for _, f := range append(parent[:], child[:]...) {
			if f != nil {
				f.Close()
			}
		}
	}
	var err error
	if child[0], parent[0], err = os.Pipe(); err != nil {
		closeAll()
		return nil, err
	}
	for i := 1; i < 3; i++ {
		if parent[i], child[i], err = os.Pipe(); err != nil {
			closeAll()
			return nil, err
		}
	}
	cmd := exec.Command(gitPath, args...)
	cmd.Env = env
	// Git names the repository by its path; no working directory of the
	// daemon's should matter to it.
	cmd.Dir = "/"
	cmd.Stdin, cmd.Stdout, cmd.Stderr = child[0], child[1], child[2]
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err = cmd.Start()
	for _, f := range child {
		f.Close()
	}
	if err != nil {
		for _, f := range parent {
			f.Close()
		}
		return nil, err
	}
	return &transportProcess{cmd: cmd, stdin: parent[0], stdout: parent[1], stderr: parent[2]}, nil
}

// feed copies the client's input to Git until it ends, passes its ceiling
// or Git stops reading. A read the client never answers keeps it blocked
// until core closes the channel; it writes nothing more to Git then.
func (p *transportProcess) feed(input io.Reader) {
	_, _ = io.Copy(p.stdin, input)
	p.stdin.Close()
}

// copyOutput copies one of Git's outputs to the client's stream, or
// discards it when the stream has none.
func copyOutput(w io.Writer, from *os.File, copied chan<- error) {
	if w == nil {
		w = io.Discard
	}
	_, err := io.Copy(w, from)
	copied <- err
}

// wait waits for Git to exit, killing its process group first if ctx ends.
func (p *transportProcess) wait(ctx context.Context) error {
	exited := make(chan error, 1)
	go func() { exited <- p.cmd.Wait() }()
	select {
	case err := <-exited:
		p.stdin.Close()
		return err
	case <-ctx.Done():
		killGroup(p.cmd.Process)
		err := <-exited
		p.stdin.Close()
		return err
	}
}

// abandon kills a Git that must not serve, and closes its pipes.
func (p *transportProcess) abandon() {
	killGroup(p.cmd.Process)
	_ = p.cmd.Wait()
	p.stdin.Close()
	p.stdout.Close()
	p.stderr.Close()
}

// killGroup kills the process group whose leader is process, unless the
// leader has been waited for: its ID may then name another group.
func killGroup(process *os.Process) {
	if process.Signal(syscall.Signal(0)) != nil {
		return
	}
	_ = syscall.Kill(-process.Pid, syscall.SIGKILL)
}

// awaitOutput lets the client take Git's last output for at most
// outputDelay, or until ctx ends, then cuts it off.
func (p *transportProcess) awaitOutput(ctx context.Context, copied <-chan error) error {
	timer := time.NewTimer(outputDelay)
	defer timer.Stop()
	defer p.stdout.Close()
	defer p.stderr.Close()
	var first error
	for range 2 {
		select {
		case err := <-copied:
			if first == nil {
				first = err
			}
		case <-timer.C:
			return errors.New("the client did not take Git's output")
		case <-ctx.Done():
			return serviceEnd(ctx)
		}
	}
	return first
}

// boundedInput passes at most remaining bytes of the client's input; a
// byte more fails the read and marks the overflow.
type boundedInput struct {
	source    io.Reader
	remaining int64
	overflow  atomic.Bool
}

func (r *boundedInput) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		var one [1]byte
		n, err := r.source.Read(one[:])
		if n > 0 {
			r.overflow.Store(true)
			return 0, errors.New("the client's input passed the service's ceiling")
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.source.Read(p)
	r.remaining -= int64(n)
	return n, err
}

var (
	// errClosed ends the services of a closed driver.
	errClosed = fmt.Errorf("the driver is closed: %w", sourcedriver.ErrUnavailable)
	// errDeadline ends a service at its deadline.
	errDeadline = errors.New("the service's deadline passed")
)

// services are the Git services a driver runs, by core's ID. The zero value
// runs none.
type services struct {
	mu      sync.Mutex
	closed  bool
	running map[*runningService]struct{}
	owner   string // names this driver in its services' records
}

// runningService is one ServeGit call between open and its return.
type runningService struct {
	id     string
	cancel context.CancelCauseFunc
	ended  chan struct{}
}

func (s *services) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// start records a service before any Git runs, and returns its context,
// which its deadline and Close end.
func (s *services) start(parent context.Context, service sourcedriver.Service) (context.Context, *runningService, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, errClosed
	}
	ctx, cancel := context.WithCancelCause(parent)
	timer := time.AfterFunc(service.Deadline, func() { cancel(errDeadline) })
	r := &runningService{id: service.ID, ended: make(chan struct{}), cancel: func(cause error) {
		timer.Stop()
		cancel(cause)
	}}
	if s.running == nil {
		s.running = map[*runningService]struct{}{}
	}
	s.running[r] = struct{}{}
	return ctx, r, nil
}

// end forgets a service once nothing it started can change a head.
func (s *services) end(r *runningService) {
	r.cancel(nil)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, r)
	close(r.ended)
}

// Close ends every service the driver runs, as the daemon's shutdown or a
// replaced driver must, and refuses later ones; it returns once nothing they
// started can change a head.
func (d *Driver) Close() error {
	d.services.mu.Lock()
	d.services.closed = true
	running := make([]*runningService, 0, len(d.services.running))
	for r := range d.services.running {
		running = append(running, r)
	}
	d.services.mu.Unlock()
	for _, r := range running {
		r.cancel(errClosed)
	}
	for _, r := range running {
		<-r.ended
	}
	return nil
}

// settleService answers Unknown while this driver runs a service of that
// ID, or while a record of one, by any driver over this storage, names a
// Git that is still the recorded process; Applied otherwise. A driver that
// died abruptly leaves its services' records, and the next driver's Check
// ends them.
func (d *Driver) settleService(e sourcedriver.Effect) (sourcedriver.Settlement, error) {
	if e.Service == "" {
		return sourcedriver.Unknown, fmt.Errorf("a service effect without its ID: %w", sourcedriver.ErrInvalid)
	}
	if d.services.runs(e.Service) {
		return sourcedriver.Unknown, nil
	}
	records, err := d.serviceRecords(e.Service)
	if err != nil {
		return sourcedriver.Unknown, err
	}
	for _, r := range records {
		if alive, err := r.alive(); err != nil || alive {
			return sourcedriver.Unknown, err
		}
	}
	return sourcedriver.Applied, nil
}

// runs reports whether this driver runs a service of that ID.
func (s *services) runs(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for r := range s.running {
		if r.id == id {
			return true
		}
	}
	return false
}
