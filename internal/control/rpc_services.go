package control

import (
	"context"
	"errors"

	"github.com/lgvo/p.ai/internal/api"
)

// ServiceAPI observes and acts on a session's project services.
type ServiceAPI interface {
	SessionServices(ctx context.Context, uuid string) (api.SessionServicesResult, error)
	SessionServiceAction(ctx context.Context, uuid, name, action string) (api.SessionServiceActionResult, error)
	SessionServiceLogs(ctx context.Context, uuid, name string) (api.SessionServiceLogsResult, error)
}

// addServiceMethods serves the project services methods. A failure other than
// a lifecycle refusal answers unavailable with what the methods require.
func addServiceMethods(t *MethodTable, service ServiceAPI) {
	Register(t, api.SessionServices, func(ctx context.Context, r api.SessionServicesRequest) (api.SessionServicesResult, error) {
		result, err := service.SessionServices(ctx, r.UUID)
		return result, servicesError(err)
	})
	Register(t, api.SessionServiceLogs, func(ctx context.Context, r api.SessionServiceLogsRequest) (api.SessionServiceLogsResult, error) {
		result, err := service.SessionServiceLogs(ctx, r.UUID, r.Name)
		return result, servicesError(err)
	})
	Register(t, api.SessionServiceAction, func(ctx context.Context, r api.SessionServiceActionRequest) (api.SessionServiceActionResult, error) {
		result, err := service.SessionServiceAction(ctx, r.UUID, r.Name, r.Action)
		return result, servicesError(err)
	})
}

// servicesError keeps a lifecycle refusal and answers any other failure as
// unavailable, naming what the services methods require.
func servicesError(err error) error {
	if err == nil || errors.Is(err, ErrInvalid) || errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errorRPC(-32004, "unavailable", "project services unavailable; require a ready owned runtime and session user manager")
}
