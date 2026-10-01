package control

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
)

var projectServicePattern = regexp.MustCompile(`^p-project-[A-Za-z0-9][A-Za-z0-9_.-]{0,100}\.service$`)

func ValidProjectServiceUnit(unit string) bool { return projectServicePattern.MatchString(unit) }

// Project services belong to the session user manager, never host/system units.
type ProjectService struct {
	Unit        string `json:"unit"`
	Description string `json:"description"`
	ActiveState string `json:"active_state"`
	SubState    string `json:"sub_state"`
}

type ServiceResult struct {
	V                int              `json:"v"`
	UUID             string           `json:"uuid"`
	Services         []ProjectService `json:"services,omitempty"`
	Unit             string           `json:"unit,omitempty"`
	Journal          string           `json:"journal,omitempty"`
	JournalTruncated bool             `json:"journal_truncated,omitempty"`
	ObservedAt       string           `json:"observed_at"`
}

type ServiceAPI interface {
	SessionServices(context.Context, string, string, string) (ServiceResult, error)
}

func serviceHandler(ctx context.Context, method string, raw json.RawMessage, service ServiceAPI) (any, *RPCError) {
	var p struct {
		V      int    `json:"v"`
		UUID   string `json:"uuid"`
		Unit   string `json:"unit,omitempty"`
		Action string `json:"action,omitempty"`
	}
	if strictDecode(raw, &p) != nil || p.V != 1 || !validUUID(p.UUID) {
		return nil, errorRPC(-32602, "invalid_params", "services require v=1 and session UUID")
	}
	switch method {
	case "session.services":
		if p.Unit != "" || p.Action != "" {
			return nil, errorRPC(-32602, "invalid_params", "service inventory takes no unit/action")
		}
		p.Action = "list"
	case "session.service.journal":
		if !ValidProjectServiceUnit(p.Unit) || p.Action != "" {
			return nil, errorRPC(-32602, "invalid_params", "journal requires unit and no action")
		}
		p.Action = "journal"
	case "session.service.action":
		if !ValidProjectServiceUnit(p.Unit) || (p.Action != "start" && p.Action != "stop" && p.Action != "restart") {
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
