package runtimeincus

import (
	"context"
	"errors"
	"fmt"
)

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
	if err != nil || observed.Exists {
		return errors.Join(err, errors.New("expected session runtime is not authoritatively absent"))
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
			return fmt.Errorf("competing session runtime identity at %s", in.Name)
		}
	}
	return nil
}
