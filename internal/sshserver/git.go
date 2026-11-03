package sshserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/sourcedriver"
)

const (
	// gitMaxInput and gitDeadline are the ceilings core passes every Git
	// service: the bytes a client may send and how long the service may
	// run.
	gitMaxInput int64 = 1 << 30
	gitDeadline       = 10 * time.Minute
	// firstSettleWait and maxSettleWait bound the wait between two Settle
	// calls for a lost service, which doubles from the first to the max.
	firstSettleWait = 100 * time.Millisecond
	maxSettleWait   = 5 * time.Second
)

// errPushRefused is Approve's refusal of an update the service may not make.
var errPushRefused = errors.New("the update is not the service's to make")

// Git routes the commands the driver declares to its ServeGit, which parses
// the rest of the command and calls core's open: open checks the key's
// principal for the project and takes the lease that keeps guards out while
// the service can write. The lease ends when ServeGit returns; a call lost
// before the service was proven over keeps it until the driver settles the
// service ended, or until lifetime ends.
func Git(lifetime context.Context, authority *control.Store, driver sourcedriver.Driver) Route {
	return Route{Words: driver.Describe().Commands, Service: &gitService{lifetime: lifetime, authority: authority, driver: driver}}
}

// gitService serves each command through the driver's ServeGit, with an
// open of core's own for that call.
type gitService struct {
	lifetime  context.Context
	authority *control.Store
	driver    sourcedriver.Driver
}

func (g *gitService) Serve(ctx context.Context, e Exec) error {
	o := &opening{git: g, ctx: ctx, fingerprint: e.Fingerprint}
	err := g.driver.ServeGit(ctx, e.Command, sourcedriver.Stream{In: e.In, Out: e.Out, Err: e.Err}, o.open)
	if lease := o.end(); lease != nil {
		if err == nil || refusal(err) {
			lease.release()
		} else {
			go g.releaseWhenSettled(lease)
		}
	}
	return err
}

// refusal reports whether ServeGit answered err itself, which it does only
// once nothing the service started can change a head. Another error may be
// a call lost with the driver.
func refusal(err error) bool {
	for _, kind := range []error{sourcedriver.ErrInvalid, sourcedriver.ErrLimit, sourcedriver.ErrConflict, sourcedriver.ErrNotFound} {
		if errors.Is(err, kind) {
			return true
		}
	}
	return false
}

// releaseWhenSettled asks the driver what became of a lost service until it
// answers that the service ended, then releases its lease. When lifetime
// ends first, the lease stays held: nothing proves the service over.
func (g *gitService) releaseWhenSettled(l *lease) {
	effect := sourcedriver.Effect{Kind: sourcedriver.EffectService, Project: l.service.Project, Service: l.service.ID}
	for wait := firstSettleWait; ; wait = min(2*wait, maxSettleWait) {
		if settled, err := g.driver.Settle(g.lifetime, effect); err == nil && settled == sourcedriver.Applied {
			l.release()
			return
		}
		select {
		case <-g.lifetime.Done():
			return
		case <-time.After(wait):
		}
	}
}

// lease is the Git authority one service holds.
type lease struct {
	service sourcedriver.Service
	release func()
}

// opening is core's side of one ServeGit call.
type opening struct {
	git         *gitService
	ctx         context.Context
	fingerprint string

	mu     sync.Mutex
	lease  *lease
	called bool // open was called, or the call ended
}

// open authorizes the service once: it takes the lease for the key's
// principal on project and returns the service with core's ceilings, and
// the Approve of its one update.
func (o *opening) open(kind sourcedriver.ServiceKind, project string) (sourcedriver.Service, sourcedriver.Approve, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.called {
		return sourcedriver.Service{}, nil, errors.New("open is called once, during ServeGit")
	}
	o.called = true
	id, err := serviceID()
	if err != nil {
		return sourcedriver.Service{}, nil, err
	}
	branch, release, err := o.git.authority.AcquireGitLease(o.ctx, o.fingerprint, project, string(kind))
	if err != nil {
		return sourcedriver.Service{}, nil, err
	}
	s := sourcedriver.Service{ID: id, Project: project, Kind: kind, MaxInput: gitMaxInput, Deadline: gitDeadline}
	if kind == sourcedriver.ServiceReceive {
		s.Branch = branch
	}
	o.lease = &lease{service: s, release: release}
	return s, o.approve(s), nil
}

// end ends the call: a later open is refused. It returns the lease open
// took, if any.
func (o *opening) end() *lease {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.called = true
	return o.lease
}

// approve is the Approve of service s: it allows only an update of a
// receive service's own head and, when the update creates the head, only
// by consuming the session's unborn main grant, which stays consumed even
// if the push then fails.
func (o *opening) approve(s sourcedriver.Service) sourcedriver.Approve {
	return func(branch string, old, new sourcedriver.OID) error {
		if s.Kind != sourcedriver.ServiceReceive || branch != s.Branch || new == "" {
			return errPushRefused
		}
		if err := o.ctx.Err(); err != nil {
			return err
		}
		if old != "" {
			return nil
		}
		consumed, err := o.git.authority.ConsumeGitUnbornGrant(o.ctx, o.fingerprint, s.Project)
		if err != nil {
			return err
		}
		if !consumed {
			return errPushRefused
		}
		return nil
	}
}

// serviceID names a service for Settle.
func serviceID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(random[:]), nil
}
