package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/plugin"
)

// awaitHostReady observes systemd through the scoped runtime broker. When
// startup fails while the owned instance remains running it stops that
// instance, preserving its writable files for an ordinary retry.
func awaitHostReady(parent context.Context, timeout time.Duration, observe func(context.Context) (plugin.RuntimeState, error), stop func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var reason error
	mustStop := false
	for {
		state, err := observe(ctx)
		if err != nil {
			reason = err
			mustStop = true
		} else {
			if state.HostReady {
				return nil
			}
			if state.DiagnosticAvailable && state.Diagnostic != "" {
				reason = errors.New(state.Diagnostic)
			}
			if !state.Exists {
				if reason == nil {
					reason = errors.New("runtime missing during host startup")
				}
				break
			}
			if state.Status == "Stopped" {
				if reason == nil {
					reason = errors.New("interactive host stopped before ready")
				}
				break
			}
			if state.Status != "Running" {
				if reason == nil {
					reason = errors.New("runtime status unavailable during host startup")
				}
				mustStop = true
				break
			}
			if strings.HasPrefix(state.HostUnit, "failed/") {
				if reason == nil {
					reason = errors.New("interactive host failed")
				}
				mustStop = true
				break
			}
		}
		select {
		case <-ctx.Done():
			if reason == nil {
				reason = errors.New("interactive host readiness timed out")
			} else {
				reason = fmt.Errorf("interactive host readiness timed out: %w", reason)
			}
			mustStop = parent.Err() == nil
			goto done
		case <-time.After(250 * time.Millisecond):
		}
	}
done:
	if mustStop && parent.Err() == nil {
		stopCtx, stopCancel := context.WithTimeout(parent, 45*time.Second)
		defer stopCancel()
		if err := stop(stopCtx); err != nil && !seenStopped(stopCtx, observe) {
			reason = fmt.Errorf("%v; stopping failed: %w", reason, err)
		}
	}
	if reason == nil {
		reason = errors.New("interactive host unavailable")
	}
	return reason
}

// seenStopped observes the instance until it shows stopped, and reports
// whether it did before ctx ended or the instance went missing. A stop can
// fail while the instance stops anyway, as when the guest powers itself off at
// the same time, and a startup failure is reported with the instance stopped.
func seenStopped(ctx context.Context, observe func(context.Context) (plugin.RuntimeState, error)) bool {
	for {
		state, err := observe(ctx)
		if err == nil && (!state.Exists || state.Status == "Stopped") {
			return state.Exists
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(250 * time.Millisecond):
		}
	}
}
