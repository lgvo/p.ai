package control

import (
	"context"
	"errors"

	"github.com/lgvo/p.ai/internal/api"
)

type ServiceAPI interface {
	SessionServices(context.Context, string, string, string) (api.ServiceResult, error)
}

// addServiceMethods serves the project services methods. A failure other than
// a lifecycle refusal answers unavailable with what the methods require.
func addServiceMethods(t *MethodTable, service ServiceAPI) {
	call := func(ctx context.Context, uuid, unit, action string) (api.ServiceResult, error) {
		result, err := service.SessionServices(ctx, uuid, unit, action)
		if err != nil && !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrNotFound) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return api.ServiceResult{}, errorRPC(-32004, "unavailable", "project services unavailable; require a ready owned runtime, session user manager, and compatible selected runtime package")
		}
		return result, err
	}
	Register(t, api.SessionServices, func(ctx context.Context, r api.SessionServicesRequest) (api.ServiceResult, error) {
		return call(ctx, r.UUID, "", "list")
	})
	Register(t, api.SessionServiceJournal, func(ctx context.Context, r api.SessionServiceJournalRequest) (api.ServiceResult, error) {
		return call(ctx, r.UUID, r.Unit, "journal")
	})
	Register(t, api.SessionServiceAction, func(ctx context.Context, r api.SessionServiceActionRequest) (api.ServiceResult, error) {
		return call(ctx, r.UUID, r.Unit, r.Action)
	})
}
