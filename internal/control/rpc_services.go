package control

import (
	"context"

	"github.com/lgvo/p.ai/internal/api"
)

// ServiceAPI observes and acts on a session's project services.
type ServiceAPI interface {
	SessionServices(ctx context.Context, uuid string) (api.SessionServicesResult, error)
	SessionServiceAction(ctx context.Context, uuid, name, action string) (api.SessionServiceActionResult, error)
	SessionServiceLogs(ctx context.Context, uuid, name string) (api.SessionServiceLogsResult, error)
}

// addServiceMethods serves the project services methods.
func addServiceMethods(t *MethodTable, service ServiceAPI) {
	Register(t, api.SessionServices, func(ctx context.Context, r api.SessionServicesRequest) (api.SessionServicesResult, error) {
		return service.SessionServices(ctx, r.UUID)
	})
	Register(t, api.SessionServiceLogs, func(ctx context.Context, r api.SessionServiceLogsRequest) (api.SessionServiceLogsResult, error) {
		return service.SessionServiceLogs(ctx, r.UUID, r.Name)
	})
	Register(t, api.SessionServiceAction, func(ctx context.Context, r api.SessionServiceActionRequest) (api.SessionServiceActionResult, error) {
		return service.SessionServiceAction(ctx, r.UUID, r.Name, r.Action)
	})
}
