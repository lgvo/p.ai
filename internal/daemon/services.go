package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// maxServicesResult bounds the encoded result of a services method.
const maxServicesResult = 48000

// ServicesOffered is what the driver describes: without project services,
// the services capability is not configured.
func (l *lifecycle) ServicesOffered() bool { return l.driver.Describe().Services }

func (l *lifecycle) SessionServices(ctx context.Context, id string) (api.SessionServicesResult, error) {
	var services []runtimedriver.Service
	err := l.onServices(ctx, id, false, func(call context.Context, t runtimedriver.Target) (err error) {
		services, err = l.driver.Services(call, t)
		return err
	})
	if err != nil {
		return api.SessionServicesResult{}, err
	}
	result := api.SessionServicesResult{UUID: id, Services: make([]api.ProjectService, 0, len(services)), ObservedAt: observedNow()}
	for _, s := range services {
		result.Services = append(result.Services, api.ProjectService{Name: s.Name, Description: s.Description, State: api.ServiceState(s.State), Detail: s.Detail})
	}
	return result, nil
}

// SessionServiceAction returns once the runtime queued the action; the
// inventory shows its progress.
func (l *lifecycle) SessionServiceAction(ctx context.Context, id, name, action string) (api.SessionServiceActionResult, error) {
	if !api.ValidServiceName(name) {
		return api.SessionServiceActionResult{}, control.Invalid("name")
	}
	err := l.onServices(ctx, id, true, func(call context.Context, t runtimedriver.Target) error {
		return l.driver.ServiceAction(call, t, name, runtimedriver.Action(action))
	})
	if err != nil {
		return api.SessionServiceActionResult{}, err
	}
	return api.SessionServiceActionResult{UUID: id, Name: name, ObservedAt: observedNow()}, nil
}

// SessionServiceLogs returns the service's log tail, cut further at its
// oldest end while the encoded result exceeds its bound.
func (l *lifecycle) SessionServiceLogs(ctx context.Context, id, name string) (api.SessionServiceLogsResult, error) {
	if !api.ValidServiceName(name) {
		return api.SessionServiceLogsResult{}, control.Invalid("name")
	}
	var logs runtimedriver.Logs
	err := l.onServices(ctx, id, false, func(call context.Context, t runtimedriver.Target) (err error) {
		logs, err = l.driver.ServiceLogs(call, t, name)
		return err
	})
	if err != nil {
		return api.SessionServiceLogsResult{}, err
	}
	result := api.SessionServiceLogsResult{UUID: id, Name: name, Logs: logs.Text, LogsTruncated: logs.Truncated, ObservedAt: observedNow()}
	for {
		encoded, err := json.Marshal(result)
		if err != nil {
			return api.SessionServiceLogsResult{}, err
		}
		if len(encoded) <= maxServicesResult {
			return result, nil
		}
		result.LogsTruncated = true
		result.Logs = strings.ToValidUTF8(result.Logs[len(result.Logs)/4:], "�")
	}
}

func observedNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// onServices runs one services call on the ready session id, at its
// runtime's current incarnation; a runtime that is not running complete with
// a ready host is refused before the driver is called, and one whose host
// cannot be read or that is incomplete failed. The inventory and the
// logs are reads: a change that arrives while one runs waits for it instead
// of being refused. An action is a change. A service the session does not
// have is not found.
func (l *lifecycle) onServices(ctx context.Context, id string, change bool, call func(context.Context, runtimedriver.Target) error) error {
	var release func()
	var err error
	if change {
		release, err = l.lockSession(ctx, id)
	} else {
		release, err = l.readSession(id)
		// A read runs its runtime commands to their end even when its client
		// goes, so a change that waits for the read never meets them:
		// cancelling a command kills its incus client, and an exec Incus has
		// already created stays on the instance for up to 10 s.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
	}
	if err != nil {
		return err
	}
	defer release()
	if err = l.admitSessionWork(ctx, id, ""); err != nil {
		return err
	}
	s, err := startEligible(ctx, id, l.store.GetSession, l.policyCondition)
	if err != nil {
		return err
	}
	spec, err := l.sessionSpec(ctx, s)
	if err != nil {
		return err
	}
	notReady := &control.Refusal{Kind: api.KindConflict, Reason: api.ReasonSessionNotRunning, Target: api.TargetSession(id),
		Cause: fmt.Errorf("%w: project services require a ready session", control.ErrConflict)}
	observed, err := l.driver.Inspect(ctx, spec)
	if err != nil {
		return control.Failed(api.ReasonRuntime, err)
	}
	switch {
	case observed.State != runtimedriver.StateAbsent && !observed.Complete:
		return control.Failed(api.ReasonRuntime, errors.New("session runtime is incomplete"))
	case observed.Host.Unknown:
		return control.Failed(api.ReasonRuntime, errors.New("the session host cannot be read"))
	case observed.State != runtimedriver.StateRunning || !observed.Host.Ready || observed.Incarnation == "":
		return notReady
	}
	return servicesRefusal(id, notReady, call(ctx, runtimedriver.Target{Session: spec, At: observed.Incarnation}))
}

// servicesRefusal is the answer to a services call of session id that the
// driver refused with err: a service the session does not have is not found,
// a session host no longer ready is notReady, a driver without project
// services has them not configured, and a service manager that refused or
// could not be reached failed with reason services. Anything else failed in
// the runtime.
func servicesRefusal(id string, notReady, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, runtimedriver.ErrNoService):
		return &control.Refusal{Kind: api.KindNotFound, Reason: api.ReasonService, Target: api.TargetSession(id), Cause: fmt.Errorf("%w: %w", control.ErrNotFound, err)}
	case errors.Is(err, runtimedriver.ErrNotReady):
		return notReady
	case errors.Is(err, runtimedriver.ErrUnsupported):
		return control.NotConfigured(api.ReasonServices)
	case errors.Is(err, runtimedriver.ErrUnavailable):
		return control.Failed(api.ReasonServices, err)
	}
	return control.Failed(api.ReasonRuntime, err)
}
