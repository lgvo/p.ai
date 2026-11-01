package control

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/strictjson"
)

type ServiceAPI interface {
	SessionServices(context.Context, string, string, string) (api.ServiceResult, error)
}

func serviceHandler(ctx context.Context, method string, raw json.RawMessage, service ServiceAPI) (any, *api.RPCError) {
	var p struct {
		V      int    `json:"v"`
		UUID   string `json:"uuid"`
		Unit   string `json:"unit,omitempty"`
		Action string `json:"action,omitempty"`
	}
	if strictjson.Decode(raw, &p) != nil || p.V != 1 || !validUUID(p.UUID) {
		return nil, errorRPC(-32602, "invalid_params", "services require v=1 and session UUID")
	}
	switch method {
	case "session.services":
		if p.Unit != "" || p.Action != "" {
			return nil, errorRPC(-32602, "invalid_params", "service inventory takes no unit/action")
		}
		p.Action = "list"
	case "session.service.journal":
		if !api.ValidProjectServiceUnit(p.Unit) || p.Action != "" {
			return nil, errorRPC(-32602, "invalid_params", "journal requires unit and no action")
		}
		p.Action = "journal"
	case "session.service.action":
		if !api.ValidProjectServiceUnit(p.Unit) || (p.Action != "start" && p.Action != "stop" && p.Action != "restart") {
			return nil, errorRPC(-32602, "invalid_params", "service action requires unit and start/stop/restart")
		}
	}
	result, err := service.SessionServices(ctx, p.UUID, p.Unit, p.Action)
	if err != nil {
		if !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrNotFound) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return nil, errorRPC(-32004, "unavailable", "project services unavailable; require a ready owned runtime, session user manager, and compatible selected runtime package")
		}
		return nil, lifecycleRPC(err)
	}
	return result, nil
}
