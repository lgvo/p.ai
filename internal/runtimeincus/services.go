package runtimeincus

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

func ValidProjectService(unit string) bool { return api.ValidProjectServiceUnit(unit) }

// ServiceObservation is what one services call observed: the inventory for
// list, the log tail for logs, and only the time for an action.
type ServiceObservation struct {
	Services      []api.ProjectService
	Logs          string
	LogsTruncated bool
	ObservedAt    string
}

// Services is the WASI runtime plugin's services call until the daemon uses
// the driver's services operations. Unlike ServiceAction, its action waits
// for the job.
func (b *Backend) Services(ctx context.Context, s Session, unit, action string) (ServiceObservation, error) {
	var result ServiceObservation
	if action != "list" && action != "logs" && action != "start" && action != "stop" && action != "restart" {
		return result, errors.New("unsupported service action")
	}
	if action == "list" && unit != "" || action != "list" && !ValidProjectService(unit) {
		return result, errors.New("only session-user p-project-*.service units are supported")
	}
	before, err := b.ObserveHost(ctx, s)
	if err != nil {
		return result, err
	}
	if !before.Exists || before.Status != "Running" || !before.Ready {
		return result, &control.Refusal{Kind: api.KindConflict, Reason: api.ReasonSessionNotRunning, Target: api.TargetSession(s.SessionUUID),
			Cause: fmt.Errorf("%w: project services require a ready session", control.ErrConflict)}
	}
	var raw []byte
	switch action {
	case "list":
		services, err := b.serviceInventory(ctx, s)
		if err != nil {
			return result, servicesFailed(err)
		}
		result.Services = make([]api.ProjectService, 0, len(services))
		for _, service := range services {
			result.Services = append(result.Services, api.ProjectService{Name: service.Name, Description: service.Description, State: api.ServiceState(service.State), Detail: service.Detail})
		}
	case "logs":
		if raw, err = b.serviceJournal(ctx, s, unit); err != nil {
			return result, servicesFailed(err)
		}
	default:
		if _, err := b.userCommand(ctx, s, "/usr/libexec/p/systemctl", "--user", "--no-pager", action, unit); err != nil {
			return result, servicesFailed(serviceManagerUnavailable())
		}
	}
	if err := b.servicesRecheck(ctx, s, before.Observation); err != nil {
		return ServiceObservation{}, err
	}
	result.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if action == "logs" {
		// Keep the RPC bounded even for long log messages. This is a tail.
		if len(raw) > 16000 {
			result.LogsTruncated = true
			raw = raw[len(raw)-16000:]
		}
		result.Logs = strings.ToValidUTF8(string(raw), "�")
		for {
			encoded, e := json.Marshal(api.SessionServiceLogsResult{UUID: s.SessionUUID, Name: unit, Logs: result.Logs, LogsTruncated: result.LogsTruncated, ObservedAt: result.ObservedAt})
			if e != nil {
				return ServiceObservation{}, e
			}
			if len(encoded) <= 48000 {
				break
			}
			result.LogsTruncated = true
			result.Logs = strings.ToValidUTF8(result.Logs[len(result.Logs)/4:], "�")
		}
	}
	return result, nil
}

// servicesFailed answers a service manager that refused or could not be
// reached as failed services, as the daemon's services methods do until they
// use the driver's services operations.
func servicesFailed(err error) error {
	if errors.Is(err, runtimedriver.ErrUnavailable) {
		return control.Failed(api.ReasonServices, err)
	}
	return err
}
