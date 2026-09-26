package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type createCleanupRPCProbe struct {
	LifecycleAPI
	calls int
}

func (p *createCleanupRPCProbe) PreviewCreateCleanup(_ context.Context, uuid string) (CreateCleanupPreview, error) {
	p.calls++
	return CreateCleanupPreview{UUID: uuid, Eligible: true}, nil
}
func (p *createCleanupRPCProbe) ConfirmCreateCleanup(_ context.Context, uuid, key, token string) (Operation, error) {
	p.calls++
	return Operation{Kind: "session.create.cleanup", SessionUUID: uuid, Key: key}, nil
}
func TestCreateCleanupRPCStrictClosedSchema(t *testing.T) {
	p := &createCleanupRPCProbe{}
	h := StateHandlerWithLifecycle(nil, nil, nil, p)
	ctx := context.Background()
	uuid := "550e8400-e29b-41d4-a716-446655440000"
	token := strings.Repeat("a", 32)
	for _, raw := range []string{`{"v":2,"uuid":"` + uuid + `"}`, `{"v":1,"uuid":"bad"}`, `{"v":1,"uuid":"` + uuid + `","force":true}`, `{"v":1,"uuid":"` + uuid + `","uuid":"` + uuid + `"}`} {
		if _, e := h(ctx, "session.create.cleanup.preview", json.RawMessage(raw)); e == nil || e.Kind != "invalid_params" {
			t.Fatal("unsafe preview admitted")
		}
	}
	for _, raw := range []string{`{"v":1,"uuid":"` + uuid + `","key":"cleanup","confirmation_token":"short"}`, `{"v":1,"uuid":"` + uuid + `","key":"cleanup","confirmation_token":"` + token + `","force":true}`} {
		if _, e := h(ctx, "session.create.cleanup.confirm", json.RawMessage(raw)); e == nil || e.Kind != "invalid_params" {
			t.Fatal("unsafe confirm admitted")
		}
	}
	if p.calls != 0 {
		t.Fatal("invalid request reached lifecycle")
	}
	if result, e := h(ctx, "session.create.cleanup.preview", json.RawMessage(`{"v":1,"uuid":"`+uuid+`"}`)); e != nil || !result.(map[string]any)["preview"].(CreateCleanupPreview).Eligible {
		t.Fatal("preview route")
	}
	if result, e := h(ctx, "session.create.cleanup.confirm", json.RawMessage(`{"v":1,"uuid":"`+uuid+`","key":"cleanup","confirmation_token":"`+token+`"}`)); e != nil || result.(map[string]any)["operation"].(Operation).Kind != "session.create.cleanup" {
		t.Fatal("confirmation route")
	}
}
