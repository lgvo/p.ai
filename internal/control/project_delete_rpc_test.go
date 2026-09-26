package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type projectDeleteRPCProbe struct {
	LifecycleAPI
	calls int
}

func (p *projectDeleteRPCProbe) PreviewProjectDelete(_ context.Context, req ProjectDeletePreviewRequest) (ProjectDeletePreview, error) {
	p.calls++
	return ProjectDeletePreview{Project: req.Project, Outcome: "delete_project_and_all_p_data"}, nil
}
func (p *projectDeleteRPCProbe) ConfirmProjectDelete(_ context.Context, req ProjectDeleteConfirmRequest) (Operation, error) {
	p.calls++
	return Operation{Kind: "project.delete", Project: req.Project, Key: req.Key}, nil
}
func TestProjectDeleteRPCClosedSchema(t *testing.T) {
	p := &projectDeleteRPCProbe{}
	h := StateHandlerWithLifecycle(nil, nil, nil, p)
	ctx := context.Background()
	for _, raw := range []string{`{"v":2,"project":"bulk"}`, `{"v":1,"project":"../bulk"}`, `{"v":1,"project":"bulk","force":true}`, `{"v":1,"project":"bulk","project":"bulk"}`, `{"v":1,"project":"bulk","loss_operations":{"bad":"bad"}}`, `{"v":1,"project":"bulk","acknowledge_missing":["bad"]}`} {
		if _, e := h(ctx, "project.delete.preview", json.RawMessage(raw)); e == nil || e.Kind != "invalid_params" {
			t.Fatal("unsafe preview request", raw)
		}
	}
	for _, raw := range []string{`{"v":1,"project":"bulk","key":"delete","confirmation_token":"short"}`, `{"v":1,"project":"bulk","key":"delete","confirmation_token":"` + strings.Repeat("a", 32) + `","force":true}`} {
		if _, e := h(ctx, "project.delete.confirm", json.RawMessage(raw)); e == nil || e.Kind != "invalid_params" {
			t.Fatal("unsafe confirmation")
		}
	}
	if p.calls != 0 {
		t.Fatal("bad inputs reached deletion")
	}
	if _, e := h(ctx, "project.delete.preview", json.RawMessage(`{"v":1,"project":"bulk","loss_operations":{}}`)); e != nil {
		t.Fatal(e)
	}
	if result, e := h(ctx, "project.delete.confirm", json.RawMessage(`{"v":1,"project":"bulk","key":"delete","confirmation_token":"`+strings.Repeat("a", 32)+`"}`)); e != nil || result.(map[string]any)["operation"].(Operation).Kind != "project.delete" {
		t.Fatal("confirmation route", e)
	}
}
