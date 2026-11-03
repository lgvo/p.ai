package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
)

type createCleanupRPCProbe struct {
	LifecycleAPI
	calls int
}

func (p *createCleanupRPCProbe) PreviewCreateCleanup(_ context.Context, uuid string) (api.CreateCleanupPreview, error) {
	p.calls++
	return api.CreateCleanupPreview{UUID: uuid, Eligible: true}, nil
}
func (p *createCleanupRPCProbe) ConfirmCreateCleanup(_ context.Context, uuid, key, token string) (api.Operation, error) {
	p.calls++
	return api.Operation{Kind: "session.create.cleanup", SessionUUID: uuid, Key: key}, nil
}
func TestCreateCleanupRPCStrictClosedSchema(t *testing.T) {
	p := &createCleanupRPCProbe{}
	h := NewHandler(HandlerConfig{Lifecycle: p})
	ctx := context.Background()
	uuid := "550e8400-e29b-41d4-a716-446655440000"
	token := strings.Repeat("a", 32)
	for _, raw := range []string{`{"v":1,"uuid":"` + uuid + `"}`, `{"uuid":"bad"}`, `{"uuid":"` + uuid + `","force":true}`, `{"uuid":"` + uuid + `","uuid":"` + uuid + `"}`, `{"uuid":"` + uuid + `","loss_operation_id":"` + uuid + `"}`} {
		if _, e := h(ctx, "session.create.cleanup.preview", json.RawMessage(raw)); e == nil || e.Kind != "invalid_params" {
			t.Fatal("unsafe preview admitted")
		}
	}
	for _, raw := range []string{`{"uuid":"` + uuid + `","key":"cleanup","confirmation_token":"short"}`, `{"uuid":"` + uuid + `","key":"cleanup","confirmation_token":"` + token + `","force":true}`} {
		if _, e := h(ctx, "session.create.cleanup.confirm", json.RawMessage(raw)); e == nil || e.Kind != "invalid_params" {
			t.Fatal("unsafe confirm admitted")
		}
	}
	if p.calls != 0 {
		t.Fatal("invalid request reached lifecycle")
	}
	if result, e := h(ctx, "session.create.cleanup.preview", json.RawMessage(`{"uuid":"`+uuid+`"}`)); e != nil || !result.(api.CreateCleanupPreviewResult).Preview.Eligible {
		t.Fatal("preview route")
	}
	if result, e := h(ctx, "session.create.cleanup.confirm", json.RawMessage(`{"uuid":"`+uuid+`","key":"cleanup","confirmation_token":"`+token+`"}`)); e != nil || result.(api.OperationResult).Operation.Kind != "session.create.cleanup" {
		t.Fatal("confirmation route")
	}
}
