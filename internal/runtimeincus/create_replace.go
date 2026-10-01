package runtimeincus

import (
	"context"
	"errors"
)

// ConfirmFailedCreateEffectsAbsent proves that neither the deterministic
// session name nor this creation's builder request occupies the project.
// The caller must separately prove the durable phase never issued builder
// creation; an inventory miss alone does not settle a delayed request.
func (b *Backend) ConfirmFailedCreateEffectsAbsent(ctx context.Context, s Session, creationID string) error {
	if !uuidPattern.MatchString(creationID) {
		return errors.New("invalid creation identity")
	}
	if err := b.ConfirmSessionRuntimeAbsent(ctx, s); err != nil {
		return err
	}
	raw, err := b.command(ctx, "list", "--format", "json")
	if err != nil {
		return err
	}
	var instances []instanceJSON
	if err := decode(raw, &instances); err != nil || len(instances) > 64 {
		return errors.New("failed creation native inventory unavailable")
	}
	seen := map[string]bool{}
	for _, in := range instances {
		if in.Name == "" || seen[in.Name] || in.Type != "container" || in.Status != "Running" && in.Status != "Stopped" && in.Status != "Frozen" || in.Config == nil || in.ExpandedConfig == nil {
			return errors.New("failed creation native inventory ambiguous")
		}
		seen[in.Name] = true
		if in.Name == "p-builder-"+creationID || in.Config["user.p.builder_request_uuid"] == creationID ||
			in.ExpandedConfig["user.p.builder_request_uuid"] == creationID || in.Name == "p-"+s.SessionUUID ||
			in.Config["user.p.session_uuid"] == s.SessionUUID || in.ExpandedConfig["user.p.session_uuid"] == s.SessionUUID {
			return errors.New("failed creation native resource is present")
		}
	}
	return nil
}
