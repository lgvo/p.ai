package control

import (
	"context"

	"github.com/lgvo/p.ai/internal/api"
)

// ServiceAPI observes and acts on a session's project services.
type ServiceAPI interface {
	// ServicesOffered reports whether the runtime offers project services.
	// When it does not, the services capability is not configured.
	ServicesOffered() bool
	SessionServices(ctx context.Context, uuid string) (api.SessionServicesResult, error)
	SessionServiceAction(ctx context.Context, uuid, name, action string) (api.SessionServiceActionResult, error)
	SessionServiceLogs(ctx context.Context, uuid, name string) (api.SessionServiceLogsResult, error)
}

// offeredServices is life's ServiceAPI when its runtime offers project
// services.
func offeredServices(life LifecycleAPI) (ServiceAPI, bool) {
	service, ok := life.(ServiceAPI)
	return service, ok && service.ServicesOffered()
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
