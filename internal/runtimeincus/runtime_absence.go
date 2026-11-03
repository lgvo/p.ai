package runtimeincus

import (
	"context"
	"errors"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// RuntimePresentError reports that a container of the session exists where
// its absence was to be proven: under its own name, or another one carrying
// its identity.
type RuntimePresentError struct{ text string }

func (e *RuntimePresentError) Error() string { return e.text }

// Unwrap gives the driver's kind of the refusal: an object carries the
// session's reference where none was expected.
func (e *RuntimePresentError) Unwrap() error { return runtimedriver.ErrMismatch }

// ConfirmSessionRuntimeAbsent is a project-scoped, read-only proof. It checks
// the deterministic name and the complete bounded project inventory so a
// renamed or conflicting same-UUID runtime is never adopted as absence.
func (b *Backend) ConfirmSessionRuntimeAbsent(ctx context.Context, s Session) error {
	if err := validateSession(b.config, s); err != nil {
		return err
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return err
	}
	observed, err := b.Inspect(ctx, s)
	if err != nil {
		return errors.Join(err, errors.New("expected session runtime is not authoritatively absent"))
	}
	if observed.Exists {
		return &RuntimePresentError{"expected session runtime is not authoritatively absent"}
	}
	raw, err := b.command(ctx, "list", "--format", "json")
	if err != nil {
		return err
	}
	var instances []instanceJSON
	if err := decode(raw, &instances); err != nil || len(instances) > 4 {
		return errors.New("session runtime inventory unavailable")
	}
	seen := map[string]bool{}
	for _, in := range instances {
		if in.Name == "" || seen[in.Name] || in.Type != "container" || in.Status != "Running" && in.Status != "Stopped" && in.Status != "Frozen" || in.Config == nil || in.ExpandedConfig == nil {
			return errors.New("session runtime inventory ambiguous")
		}
		seen[in.Name] = true
		if in.Name == b.name(s) || in.Config["user.p.session_uuid"] == s.SessionUUID || in.ExpandedConfig["user.p.session_uuid"] == s.SessionUUID {
			return &RuntimePresentError{"competing session runtime identity at " + in.Name}
		}
	}
	return nil
}
