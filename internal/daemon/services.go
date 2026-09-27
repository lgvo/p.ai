package daemon

import (
	"context"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

type serviceBroker struct {
	runtimeincus.Scoped
	unit, action string
	result       control.ServiceResult
}

func (b *serviceBroker) Services(ctx context.Context) (plugin.RuntimeState, error) {
	result, err := b.Backend.Services(ctx, b.Session, b.unit, b.action)
	if err != nil {
		return plugin.RuntimeState{}, err
	}
	b.result = result
	return plugin.RuntimeState{Exists: true, Status: "Running"}, nil
}

func (l *lifecycle) SessionServices(ctx context.Context, id, unit, action string) (control.ServiceResult, error) {
	var zero control.ServiceResult
	if action != "list" && !runtimeincus.ValidProjectService(unit) {
		return zero, control.ErrInvalid
	}
	release, err := l.lockSession(ctx, id)
	if err != nil {
		return zero, err
	}
	defer release()
	if err = l.checkNoWorkspaceInspect(ctx, id); err != nil {
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
	if _, err = plugin.RunRuntime(ctx, l.runtimePlugin, "runtime.services", b); err != nil {
		return zero, err
	}
	return b.result, nil
}
