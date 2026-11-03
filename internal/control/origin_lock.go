package control

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// The bounds of the source driver's calls to origins, which a stalled
// origin would otherwise hold without end.
const (
	// maxOriginCalls is how many calls to origins run at once, across
	// projects.
	maxOriginCalls = 2
	// originCallTimeout bounds one call to an origin, its wait for a slot
	// included.
	originCallTimeout = 35 * time.Second
)

// LockOrigin takes project's origin lock, waiting until ctx ends, and
// returns its release. The lock serializes a project's origin work, so each
// step acts on the observation it made: an origin's observation and the
// records it leads to, a creation from an origin ref, a publication's
// comparison and push, an origin project's creation, and the start of the
// project's deletion. A future collection of P's unreferenced objects must
// take it too: a pending creation relies on an origin commit no head holds.
func (s *Store) LockOrigin(ctx context.Context, project string) (release func(), err error) {
	held, _ := s.originLocks.LoadOrStore(project, make(chan struct{}, 1))
	slot := held.(chan struct{})
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// CallOrigin runs call, one call of the source driver that contacts an
// origin, with a context that ends originCallTimeout from now, once one of
// the instance's origin slots is free: at most maxOriginCalls such calls run
// at once, across projects, and the wait for a slot counts toward the
// deadline. So a stalled origin holds a project's origin lock, or the Git
// authority a publication holds, no longer than that. The caller records the
// outcome with its own context, never the call's. A call its deadline cut
// off answers that the origin did not answer in time, unless its effect may
// have happened (sourcedriver.ErrUnknownOutcome), which it keeps.
func (s *Store) CallOrigin(ctx context.Context, call func(context.Context) error) error {
	bounded, cancel := context.WithTimeout(ctx, s.originCallTimeout)
	defer cancel()
	timedOut := fmt.Errorf("the origin did not answer within %s", s.originCallTimeout)
	select {
	case s.originSlots <- struct{}{}:
		defer func() { <-s.originSlots }()
	case <-bounded.Done():
		if err := ctx.Err(); err != nil {
			return err
		}
		return timedOut
	}
	err := call(bounded)
	if err != nil && ctx.Err() == nil && bounded.Err() != nil && !errors.Is(err, sourcedriver.ErrUnknownOutcome) {
		// The call's own deadline, not the caller's, ended it: an answer
		// of the origin, never a context error the caller would take for
		// its own.
		return timedOut
	}
	return err
}
