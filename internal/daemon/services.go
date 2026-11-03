package daemon

import (
	"context"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

type serviceBroker struct {
	runtimeincus.Scoped
	unit, action string
	result       runtimeincus.ServiceObservation
}

func (b *serviceBroker) Services(ctx context.Context) (plugin.RuntimeState, error) {
	result, err := b.Backend.Services(ctx, b.Session, b.unit, b.action)
	if err != nil {
		return plugin.RuntimeState{}, err
	}
	b.result = result
	return plugin.RuntimeState{Exists: true, Status: "Running"}, nil
}

func (l *lifecycle) SessionServices(ctx context.Context, id string) (api.SessionServicesResult, error) {
	o, err := l.observeServices(ctx, id, "", "list")
	return api.SessionServicesResult{UUID: id, Services: o.Services, ObservedAt: o.ObservedAt}, err
}

func (l *lifecycle) SessionServiceAction(ctx context.Context, id, name, action string) (api.SessionServiceActionResult, error) {
	o, err := l.observeServices(ctx, id, name, action)
	return api.SessionServiceActionResult{UUID: id, Name: name, ObservedAt: o.ObservedAt}, err
}

func (l *lifecycle) SessionServiceLogs(ctx context.Context, id, name string) (api.SessionServiceLogsResult, error) {
	o, err := l.observeServices(ctx, id, name, "logs")
	return api.SessionServiceLogsResult{UUID: id, Name: name, Logs: o.Logs, LogsTruncated: o.LogsTruncated, ObservedAt: o.ObservedAt}, err
}

// observeServices runs one services call in the session's runtime: list,
// logs, or an action.
func (l *lifecycle) observeServices(ctx context.Context, id, unit, action string) (runtimeincus.ServiceObservation, error) {
	var zero runtimeincus.ServiceObservation
	if action != "list" && !runtimeincus.ValidProjectService(unit) {
		return zero, control.Invalid("name")
	}
	// The inventory and the logs are reads: a change that arrives while one
	// runs waits for it instead of being refused. An action is a change.
	var release func()
	var err error
	if action == "list" || action == "logs" {
		release, err = l.readSession(id)
		// A read runs its runtime commands to their end even when its client
		// goes, so a change that waits for the read never meets them:
		// cancelling a command kills its incus client, and an exec Incus has
		// already created stays on the instance for up to 10 s.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
	} else {
		release, err = l.lockSession(ctx, id)
	}
	if err != nil {
		return zero, err
	}
	defer release()
	if err = l.admitSessionWork(ctx, id, ""); err != nil {
		return zero, err
	}
	s, err := startEligible(ctx, id, l.store.GetSession, l.policyCondition)
	if err != nil {
		return zero, err
	}
	native, err := l.runtimeSession(ctx, s)
	if err != nil {
		return zero, err
	}
	b := &serviceBroker{Scoped: runtimeincus.Scoped{Backend: l.runtime, Session: native}, unit: unit, action: action}
	if _, err = runRuntime(ctx, l.runtimePlugin, "runtime.services", b); err != nil {
		// The session's user service manager runs in the runtime; a refusal
		// the runtime typed, such as a session that is not running, keeps its
		// answer.
		return zero, control.Failed(api.ReasonRuntime, err)
	}
	return b.result, nil
}
