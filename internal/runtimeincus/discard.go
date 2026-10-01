package runtimeincus

import (
	"context"
	"errors"
)

// StopForDiscardExact force-stops only the previewed generation. It accepts
// Frozen and Stopped sources; a force stop of Frozen avoids resuming guest
// execution after the durable removal commit. Any attempted uncertain command
// must remain blocked by the caller's persisted stop-issued marker.
func (b *Backend) StopForDiscardExact(ctx context.Context, source Session, incusUUID, generation string, beforeStop func() error) error {
	if !uuidPattern.MatchString(incusUUID) || !uuidPattern.MatchString(generation) || beforeStop == nil || source.WorkspaceOwner != "" {
		return errors.New("invalid exact discard stop request")
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return err
	}
	before, err := b.Inspect(ctx, source)
	if err != nil || !before.Exists || before.IncusUUID != incusUUID || before.Generation != generation || before.Status != "Frozen" && before.Status != "Stopped" {
		return errors.Join(err, errors.New("discard source generation or state changed before stop"))
	}
	if before.Status == "Stopped" {
		return nil
	}
	busy, err := b.workspaceResourceBusy(ctx, b.name(source))
	if err != nil || busy {
		return errors.Join(err, errors.New("discard source has active Incus operation"))
	}
	if err := beforeStop(); err != nil {
		return err
	}
	_, effectErr := b.command(ctx, "stop", b.name(source), "--force")
	if effectErr != nil {
		return errors.Join(effectErr, errors.New("exact discard stop outcome unresolved"))
	}
	after, err := b.Inspect(ctx, source)
	if err != nil || !after.Exists || after.Status != "Stopped" || after.IncusUUID != incusUUID || after.Generation != generation {
		return errors.Join(err, errors.New("exact discard stop postcondition unavailable"))
	}
	return nil
}

// DeleteForDiscardExact sends one name-targeted Incus DELETE only after exact
// stopped-generation preflight. The callback durably marks delete-issued
// immediately before admission. A failed/uncertain request is never retried
// by name; Incus may admit it after an empty operation-list observation.
func (b *Backend) DeleteForDiscardExact(ctx context.Context, source Session, incusUUID, generation string, beforeDelete func() error) error {
	if !uuidPattern.MatchString(incusUUID) || !uuidPattern.MatchString(generation) || beforeDelete == nil || source.WorkspaceOwner != "" {
		return errors.New("invalid exact discard delete request")
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return err
	}
	before, err := b.Inspect(ctx, source)
	if err != nil || !before.Exists || before.Status != "Stopped" || before.IncusUUID != incusUUID || before.Generation != generation {
		return errors.Join(err, errors.New("discard source generation or state changed before delete"))
	}
	busy, err := b.workspaceResourceBusy(ctx, b.name(source))
	if err != nil || busy {
		return errors.Join(err, errors.New("discard source has active Incus operation"))
	}
	if err := beforeDelete(); err != nil {
		return err
	}
	_, effectErr := b.command(ctx, "delete", b.name(source))
	if effectErr != nil {
		return errors.Join(effectErr, errors.New("exact discard delete outcome unresolved"))
	}
	after, err := b.Inspect(ctx, source)
	if err != nil || after.Exists {
		return errors.Join(err, errors.New("exact discard delete postcondition unavailable"))
	}
	return nil
}
