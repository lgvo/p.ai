package incus

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// sessionRuntimeName is the Incus instance name of a session's runtime.
func sessionRuntimeName(session string) string { return "p-" + session }

// deleteSession deletes the runtime of a session reference at incarnation
// at with its storage, force-stopping it first when it runs. It refuses
// before its gate any other runtime, a runtime neither running nor stopped,
// and one an Incus operation still runs on after operationIdleWait. It sends
// one `incus delete` and never sends it again: once the gate passed, a
// failure is an unknown outcome for Settle.
func (b *Backend) deleteSession(ctx context.Context, session string, at runtimedriver.Incarnation, g runtimedriver.Gate) error {
	// The operation waited for may change the runtime: inspect it after.
	if err := b.awaitWorkspaceIdle(ctx, sessionRuntimeName(session)); err != nil {
		return err
	}
	in, exists, err := b.sessionRuntime(ctx, session)
	if err != nil || !exists {
		return err
	}
	if at == "" || incarnation(volatileIdentity(in)) != at {
		return mismatch("the runtime is not the incarnation to delete")
	}
	if in.Status != "Running" && in.Status != "Stopped" {
		return fmt.Errorf("runtime is %s, neither running nor stopped", in.Status)
	}
	if err := b.deleteChecks(ctx, in.Name); err != nil {
		return err
	}
	if err := g(); err != nil {
		return err
	}
	if in.Status == "Running" {
		// The deletion was decided: nothing in the runtime is worth a
		// graceful shutdown.
		_, stopErr := b.command(ctx, "stop", in.Name, "--force")
		stopped, exists, err := b.sessionRuntime(ctx, session)
		if stopErr != nil || err != nil || !exists || stopped.Status != "Stopped" || incarnation(volatileIdentity(stopped)) != at {
			return errors.Join(stopErr, err, errors.New("the runtime did not stop for its deletion"), runtimedriver.ErrUnknownOutcome)
		}
		if err := b.deleteChecks(ctx, in.Name); err != nil {
			return errors.Join(err, runtimedriver.ErrUnknownOutcome)
		}
	}
	if _, err := b.command(ctx, "delete", in.Name); err != nil {
		return errors.Join(err, runtimedriver.ErrUnknownOutcome)
	}
	if _, exists, err := b.sessionRuntime(ctx, session); err != nil || exists {
		return errors.Join(err, errors.New("the runtime remains after its deletion"), runtimedriver.ErrUnknownOutcome)
	}
	return nil
}

// deleteChecks are the checks right before an effect of a deletion: the
// project's confinement, and no Incus operation running on the instance,
// which could still change it.
func (b *Backend) deleteChecks(ctx context.Context, name string) error {
	if err := b.CheckConfinement(ctx); err != nil {
		return err
	}
	busy, err := b.workspaceResourceBusy(ctx, name)
	if err != nil {
		return err
	}
	if busy {
		return errors.New("an Incus operation runs on the runtime")
	}
	return nil
}

// sessionRuntime reads the whole Incus project for the runtime of a session
// reference: the instance at its name, which must carry the reference and
// this P instance's label. An instance under another name carrying the
// reference is a renamed runtime: a mismatch, so it can never pass for
// absent.
func (b *Backend) sessionRuntime(ctx context.Context, session string) (instanceJSON, bool, error) {
	if !uuidPattern.MatchString(b.config.PInstanceID) {
		return instanceJSON{}, false, errors.New("trusted P instance identity required for sessions")
	}
	instances, err := b.instanceInventory(ctx)
	if err != nil {
		return instanceJSON{}, false, err
	}
	name := sessionRuntimeName(session)
	var runtime instanceJSON
	exists := false
	for _, in := range instances {
		if in.Name == name {
			runtime, exists = in, true
		} else if in.Config[sessionRefKey] == session {
			return instanceJSON{}, false, mismatch(in.Name + " carries the session's reference under another name")
		}
	}
	if exists && (runtime.Config[sessionRefKey] != session || runtime.Config["user.p.instance_uuid"] != b.config.PInstanceID) {
		return runtime, true, mismatch(name + " is not this P instance's runtime of the session")
	}
	return runtime, exists, nil
}

// operationIdleWait bounds how long a deletion waits for the Incus operations
// on its instance to end. An exec whose incus client was killed before it
// connected, as when P cancels an observation, stays running for up to 10 s,
// whatever the instance's state.
const operationIdleWait = 15 * time.Second

// awaitWorkspaceIdle polls, for at most operationIdleWait, until no Incus
// operation on instance is pending or running, and refuses when one still
// is.
func (b *Backend) awaitWorkspaceIdle(ctx context.Context, instance string) error {
	deadline := time.Now().Add(operationIdleWait)
	for {
		busy, err := b.workspaceResourceBusy(ctx, instance)
		if err != nil || !busy {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("an Incus operation runs on the runtime")
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
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
