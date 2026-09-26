package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type createReplaceRPCProbe struct {
	LifecycleAPI
	previews, confirms int
	uuid, key, token   string
	request            ReserveSessionRequest
}

func (p *createReplaceRPCProbe) PreviewCreateReplace(_ context.Context, uuid string, req ReserveSessionRequest) (CreateReplacePreview, error) {
	p.previews++
	p.uuid, p.request = uuid, req
	return CreateReplacePreview{OldUUID: uuid, NewRequest: req, Eligible: true, ConfirmationToken: strings.Repeat("a", 32)}, nil
}
func (p *createReplaceRPCProbe) ConfirmCreateReplace(_ context.Context, uuid, key, token string) (Operation, error) {
	p.confirms++
	p.uuid, p.key, p.token = uuid, key, token
	return Operation{Kind: "session.create", SessionUUID: uuid}, nil
}
func TestCreateReplaceRPCClosedRequests(t *testing.T) {
	p := &createReplaceRPCProbe{}
	h := StateHandlerWithLifecycle(nil, nil, nil, p)
	uuid := "550e8400-e29b-41d4-a716-446655440000"
	token := strings.Repeat("a", 32)
	for _, raw := range []string{
		`{"v":2,"old_uuid":"` + uuid + `","key":"new","project":"app","branch":"work","choice":"existing"}`,
		`{"v":1,"old_uuid":"bad","key":"new","project":"app","branch":"work","choice":"existing"}`,
		`{"v":1,"old_uuid":"` + uuid + `","key":"new","project":"app","branch":"work","choice":"existing","force":true}`,
		`{"v":1,"old_uuid":"` + uuid + `","key":"new","project":"app","branch":"work","choice":"existing","source":"refs/heads/main"}`,
		`{"v":1,"old_uuid":"` + uuid + `","key":"new","project":"app","branch":"work","choice":"existing","key":"again"}`,
	} {
		if _, e := h(context.Background(), "session.create.replace.preview", json.RawMessage(raw)); e == nil || e.Kind != "invalid_params" {
			t.Fatalf("preview accepted %s: %+v", raw, e)
		}
	}
	for _, raw := range []string{
		`{"v":2,"old_uuid":"` + uuid + `","key":"new","confirmation_token":"` + token + `"}`,
		`{"v":1,"old_uuid":"` + uuid + `","key":"new","confirmation_token":"short"}`,
		`{"v":1,"old_uuid":"` + uuid + `","key":"new","confirmation_token":"` + token + `","force":true}`,
		`{"v":1,"old_uuid":"` + uuid + `","key":"new","confirmation_token":"` + strings.Repeat("A", 32) + `"}`,
	} {
		if _, e := h(context.Background(), "session.create.replace.confirm", json.RawMessage(raw)); e == nil || e.Kind != "invalid_params" {
			t.Fatalf("confirm accepted %s: %+v", raw, e)
		}
	}
	if p.previews != 0 || p.confirms != 0 {
		t.Fatal("invalid request reached lifecycle")
	}
	result, e := h(context.Background(), "session.create.replace.preview", json.RawMessage(`{"v":1,"old_uuid":"`+uuid+`","key":"new","project":"app","branch":"work","choice":"existing"}`))
	if e != nil || p.previews != 1 || p.uuid != uuid || p.request.Key != "new" || !result.(map[string]any)["preview"].(CreateReplacePreview).Eligible {
		t.Fatalf("preview routing: %+v %+v", result, e)
	}
	newPreview, e := h(context.Background(), "session.create.replace.preview", json.RawMessage(`{"v":1,"old_uuid":"`+uuid+`","key":"fresh","project":"app","branch":"different","choice":"new","source":"refs/heads/main"}`))
	if e != nil || p.request.Choice != "new" || p.request.Source != "refs/heads/main" || newPreview.(map[string]any)["preview"].(CreateReplacePreview).NewRequest.Branch != "different" {
		t.Fatalf("local new choice routing: %+v %+v", newPreview, e)
	}
	result, e = h(context.Background(), "session.create.replace.confirm", json.RawMessage(`{"v":1,"old_uuid":"`+uuid+`","key":"new","confirmation_token":"`+token+`"}`))
	if e != nil || p.confirms != 1 || p.key != "new" || p.token != token || result.(map[string]any)["operation"].(Operation).Kind != "session.create" {
		t.Fatalf("confirm routing: %+v %+v", result, e)
	}
}
