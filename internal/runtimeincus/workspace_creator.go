package runtimeincus

import (
	"context"
	"errors"
)

// CheckAssembledCreationSource accepts exactly one stopped ordinary runtime
// under its deterministic name. A second session identity or any creator
// builder blocks inspection; neither is adopted or removed.
func (b *Backend) CheckAssembledCreationSource(ctx context.Context, source Session, creationID, incusUUID, generation string) error {
	if !uuidPattern.MatchString(creationID) || !uuidPattern.MatchString(incusUUID) || !uuidPattern.MatchString(generation) || source.WorkspaceOwner != "" {
		return errors.New("failed creation native identity unavailable")
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return err
	}
	observed, err := b.Inspect(ctx, source)
	if err != nil || !observed.Exists || observed.Status != "Stopped" || observed.IncusUUID != incusUUID || observed.Generation != generation {
		return errors.Join(err, errors.New("failed creation stopped native identity changed"))
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
	matched := false
	for _, in := range instances {
		if in.Name == "" || seen[in.Name] || in.Type != "container" || in.Status != "Running" && in.Status != "Stopped" && in.Status != "Frozen" || in.Config == nil || in.ExpandedConfig == nil {
			return errors.New("failed creation native inventory ambiguous")
		}
		seen[in.Name] = true
		if in.Name == "p-builder-"+creationID || in.Config["user.p.builder_request_uuid"] == creationID || in.ExpandedConfig["user.p.builder_request_uuid"] == creationID {
			return errors.New("failed creation has an unexpected native builder")
		}
		if in.Name == b.name(source) {
			if !validIdentity(in.Config, source) || !validIdentity(in.ExpandedConfig, source) ||
				in.Status != "Stopped" || in.Config["volatile.uuid"] != incusUUID || in.Config["volatile.uuid.generation"] != generation ||
				in.ExpandedConfig["volatile.uuid"] != incusUUID || in.ExpandedConfig["volatile.uuid.generation"] != generation {
				return errors.New("failed creation native inventory source changed")
			}
			matched = true
		} else if in.Config["user.p.session_uuid"] == source.SessionUUID || in.ExpandedConfig["user.p.session_uuid"] == source.SessionUUID {
			return errors.New("failed creation has a competing native identity")
		}
	}
	if !matched {
		return errors.New("failed creation source missing from native inventory")
	}
	return nil
}
