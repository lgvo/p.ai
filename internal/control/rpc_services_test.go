package control

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
)

type serviceProbe struct {
	LifecycleAPI
	calls int
}

func (p *serviceProbe) SessionServices(_ context.Context, id, unit, action string) (api.ServiceResult, error) {
	p.calls++
	return api.ServiceResult{UUID: id, Unit: unit}, nil
}
func TestServicesRPCClosedAuthority(t *testing.T) {
	p := &serviceProbe{}
	h := NewHandler(HandlerConfig{Lifecycle: p})
	id := "550e8400-e29b-41d4-a716-446655440000"
	for _, test := range []struct{ method, raw string }{
		{"session.services", fmt.Sprintf(`{"uuid":%q,"action":"stop"}`, id)},
		{"session.service.action", fmt.Sprintf(`{"uuid":%q,"unit":"p-interactive.service","action":"stop"}`, id)},
		{"session.service.action", fmt.Sprintf(`{"uuid":%q,"unit":"p-project-api.service","action":"kill"}`, id)},
		{"session.service.journal", fmt.Sprintf(`{"uuid":%q,"unit":"p-project-api.service","root":"/"}`, id)},
		{"session.services", fmt.Sprintf(`{"uuid":%q,"uuid":%q}`, id, id)},
	} {
		if _, e := h(context.Background(), test.method, json.RawMessage(test.raw)); e == nil || e.Kind != "invalid_params" {
			t.Fatalf("accepted %s: %+v", test.raw, e)
		}
	}
	if p.calls != 0 {
		t.Fatal("invalid request reached authority")
	}
	if _, e := h(context.Background(), "session.service.action", json.RawMessage(fmt.Sprintf(`{"uuid":%q,"unit":"p-project-api.service","action":"restart"}`, id))); e != nil || p.calls != 1 {
		t.Fatalf("valid action refused: %+v", e)
	}
}
