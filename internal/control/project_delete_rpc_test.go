package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
)

type projectDeleteRPCProbe struct {
	LifecycleAPI
	calls int
}

func (p *projectDeleteRPCProbe) PreviewProjectDelete(_ context.Context, req ProjectDeletePreviewRequest) (api.ProjectDeletionPreview, error) {
	p.calls++
	return api.ProjectDeletionPreview{Project: req.Project, Outcome: "delete_project_and_all_p_data"}, nil
}
func (p *projectDeleteRPCProbe) ConfirmProjectDelete(_ context.Context, req ProjectDeleteConfirmRequest) (api.Operation, error) {
	p.calls++
	return api.Operation{Kind: "project.delete", Project: req.Project, Key: req.Key}, nil
}
func TestProjectDeleteRPCClosedSchema(t *testing.T) {
	p := &projectDeleteRPCProbe{}
	h := NewHandler(HandlerConfig{Lifecycle: p})
	ctx := context.Background()
	for _, raw := range []string{`{"v":1,"project":"bulk"}`, `{"project":"../bulk"}`, `{"project":"bulk","force":true}`, `{"project":"bulk","project":"bulk"}`, `{"project":"bulk","loss_operations":{}}`, `{"project":"bulk","acknowledge_missing":["bad"]}`} {
		if _, e := h(ctx, "project.delete.preview", json.RawMessage(raw)); e == nil || e.Kind != "invalid_params" {
			t.Fatal("unsafe preview request", raw)
		}
	}
	for _, raw := range []string{`{"project":"bulk","key":"delete","confirmation_token":"short"}`, `{"project":"bulk","key":"delete","confirmation_token":"` + strings.Repeat("a", 32) + `","force":true}`} {
		if _, e := h(ctx, "project.delete.confirm", json.RawMessage(raw)); e == nil || e.Kind != "invalid_params" {
			t.Fatal("unsafe confirmation")
		}
	}
	if p.calls != 0 {
		t.Fatal("bad inputs reached deletion")
	}
	if _, e := h(ctx, "project.delete.preview", json.RawMessage(`{"project":"bulk"}`)); e != nil {
		t.Fatal(e)
	}
	if result, e := h(ctx, "project.delete.confirm", json.RawMessage(`{"project":"bulk","key":"delete","confirmation_token":"`+strings.Repeat("a", 32)+`"}`)); e != nil || result.(api.OperationResult).Operation.Kind != "project.delete" {
		t.Fatal("confirmation route", e)
	}
}
