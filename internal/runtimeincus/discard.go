package runtimeincus

import (
	"context"
	"errors"
	"net/url"
	"time"
)

// StopForDiscardExact force-stops only the previewed generation after the
// durable removal commit. It accepts Running and Stopped sources. Any
// attempted uncertain command must remain blocked by the caller's persisted
// stop-issued marker.
func (b *Backend) StopForDiscardExact(ctx context.Context, source Session, incusUUID, generation string, beforeStop func() error) error {
	if !uuidPattern.MatchString(incusUUID) || !uuidPattern.MatchString(generation) || beforeStop == nil {
		return errors.New("invalid exact discard stop request")
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return err
	}
	if busy, err := b.awaitWorkspaceIdle(ctx, b.name(source)); err != nil || busy {
		return errors.Join(err, errors.New("discard source has active Incus operation"))
	}
	before, err := b.Inspect(ctx, source)
	if err != nil || !before.Exists || before.IncusUUID != incusUUID || before.Generation != generation || before.Status != "Running" && before.Status != "Stopped" {
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
	if !uuidPattern.MatchString(incusUUID) || !uuidPattern.MatchString(generation) || beforeDelete == nil {
		return errors.New("invalid exact discard delete request")
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return err
	}
	if busy, err := b.awaitWorkspaceIdle(ctx, b.name(source)); err != nil || busy {
		return errors.Join(err, errors.New("discard source has active Incus operation"))
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

// awaitWorkspaceIdle polls, for at most 15 s, until no Incus operation on
// instance is pending or running, and reports whether one still is. An exec
// whose incus client was killed before it connected, as when P cancels an
// observation, stays running for up to 10 s, whatever the instance's state.
// The caller inspects the instance after the wait: the operation it waited
// for may have changed it.
func (b *Backend) awaitWorkspaceIdle(ctx context.Context, instance string) (bool, error) {
	deadline := time.Now().Add(15 * time.Second)
	for {
		busy, err := b.workspaceResourceBusy(ctx, instance)
		if err != nil || !busy {
			return busy, err
		}
		select {
		case <-ctx.Done():
			return busy, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return busy, nil
		}
	}
}

// workspaceResourceBusy reports whether an Incus operation on instance is
// still pending or running.
func (b *Backend) workspaceResourceBusy(ctx context.Context, instance string) (bool, error) {
	raw, err := b.command(ctx, "operation", "list", "--format", "json")
	if err != nil {
		return false, err
	}
	var operations []struct {
		StatusCode int                 `json:"status_code"`
		Resources  map[string][]string `json:"resources"`
	}
	if err := decode(raw, &operations); err != nil {
		return false, err
	}
	if len(operations) > 256 {
		return false, errors.New("Incus operation inventory exceeds bound")
	}
	want := "/1.0/instances/" + instance
	for _, op := range operations {
		if op.StatusCode < 100 || op.StatusCode > 401 {
			return false, errors.New("Incus operation status invalid")
		}
		for _, resource := range op.Resources["instances"] {
			parsed, err := url.Parse(resource)
			if err != nil || parsed.Path == "" || parsed.RawQuery != "" && parsed.Query().Get("project") != b.config.Project {
				return false, errors.New("Incus operation resource malformed")
			}
			if parsed.Path == want && op.StatusCode < 200 {
				return true, nil
			}
		}
	}
	return false, nil
}
