package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
)

type renameRPCProbe struct {
	LifecycleAPI
	called  int
	request RenameRequest
}

type retainedRenameRPCProbe struct {
	LifecycleAPI
	called  int
	request RetainedRenameRequest
}

type retainedDeleteRPCProbe struct {
	LifecycleAPI
	previews, confirms          int
	project, branch, key, token string
}

func (p *retainedDeleteRPCProbe) PreviewRetainedDelete(_ context.Context, project, branch string) (api.RetainedDeletePreview, error) {
	p.previews++
	p.project, p.branch = project, branch
	return api.RetainedDeletePreview{Project: project, Branch: branch, ConfirmationToken: strings.Repeat("a", 32)}, nil
}
func (p *retainedDeleteRPCProbe) ConfirmRetainedDelete(_ context.Context, key, project, branch, token string) (api.Operation, error) {
	p.confirms++
	p.key, p.project, p.branch, p.token = key, project, branch, token
	return api.Operation{Kind: "project.retained.delete", Project: project}, nil
}
func TestRetainedDeleteRPCClosedRequests(t *testing.T) {
	p := &retainedDeleteRPCProbe{}
	h := NewHandler(HandlerConfig{Lifecycle: p})
	for _, raw := range []string{`{"v":1,"project":"app","branch":"saved"}`, `{"project":"app","branch":"saved","workspace":"/tmp"}`, `{"project":"app","branch":"saved","branch":"saved"}`} {
		if _, e := h(context.Background(), "project.retained.delete.preview", json.RawMessage(raw)); e == nil || e.Kind() != "invalid" {
			t.Fatalf("preview accepted %s: %+v", raw, e)
		}
	}
	token := strings.Repeat("a", 32)
	for _, raw := range []string{`{"v":1,"key":"delete","project":"app","branch":"saved","confirmation_token":"` + token + `"}`, `{"key":"delete","project":"app","branch":"saved","confirmation_token":"short"}`, `{"key":"delete","project":"app","branch":"saved","confirmation_token":"` + token + `","force":true}`} {
		if _, e := h(context.Background(), "project.retained.delete.confirm", json.RawMessage(raw)); e == nil || e.Kind() != "invalid" {
			t.Fatalf("confirm accepted %s: %+v", raw, e)
		}
	}
	if p.previews != 0 || p.confirms != 0 {
		t.Fatal("invalid request reached authority")
	}
	preview, e := h(context.Background(), "project.retained.delete.preview", json.RawMessage(`{"project":"app","branch":"saved"}`))
	if e != nil || preview.(api.RetainedDeletePreviewResult).Preview.Branch != "saved" || p.previews != 1 {
		t.Fatalf("preview: %+v %+v", preview, e)
	}
	confirmed, e := h(context.Background(), "project.retained.delete.confirm", json.RawMessage(`{"key":"delete","project":"app","branch":"saved","confirmation_token":"`+token+`"}`))
	if e != nil || confirmed.(api.OperationResult).Operation.Kind != "project.retained.delete" || p.confirms != 1 || p.token != token {
		t.Fatalf("confirm: %+v %+v", confirmed, e)
	}
}

func (p *retainedRenameRPCProbe) RenameRetainedBranch(_ context.Context, req RetainedRenameRequest) (api.Operation, error) {
	p.called++
	p.request = req
	return api.Operation{Kind: "project.retained.rename", Project: req.Project, Phase: "reserved"}, nil
}

func TestRetainedRenameRPCClosedRequest(t *testing.T) {
	p := &retainedRenameRPCProbe{}
	h := NewHandler(HandlerConfig{Lifecycle: p})
	oid := strings.Repeat("a", 40)
	for _, raw := range []string{
		`{"v":1,"key":"move","project":"app","old_branch":"saved","new_branch":"archive","expected_old_tip":"` + oid + `"}`,
		`{"key":"move","project":"app","old_branch":"saved","new_branch":"archive","expected_old_tip":"` + oid + `","force":true}`,
		`{"key":"move","project":"app","old_branch":"saved","new_branch":"saved","expected_old_tip":"` + oid + `"}`,
		`{"key":"move","project":"app","old_branch":"saved","new_branch":"archive","expected_old_tip":"` + oid + `","project":"app"}`,
	} {
		if _, e := h(context.Background(), "project.retained.rename", json.RawMessage(raw)); e == nil || e.Kind() != "invalid" {
			t.Fatalf("unsafe request %s: %+v", raw, e)
		}
	}
	if p.called != 0 {
		t.Fatal("invalid request reached authority")
	}
	result, e := h(context.Background(), "project.retained.rename", json.RawMessage(`{"key":"move","project":"app","old_branch":"saved","new_branch":"archive","expected_old_tip":"`+oid+`"}`))
	if e != nil || p.called != 1 || p.request.Project != "app" || p.request.ExpectedOldTip != oid || result.(api.OperationResult).Operation.Kind != "project.retained.rename" {
		t.Fatalf("route: %+v %+v", result, e)
	}
}

func (p *renameRPCProbe) RenameSession(_ context.Context, req RenameRequest) (api.Operation, error) {
	p.called++
	p.request = req
	return api.Operation{Kind: "session.rename", Status: "running", SessionUUID: req.UUID}, nil
}

func TestRenameRPCUsesClosedRequest(t *testing.T) {
	p := &renameRPCProbe{}
	handler := NewHandler(HandlerConfig{Lifecycle: p})
	uuid := "550e8400-e29b-41d4-a716-446655440000"
	tip := strings.Repeat("a", 40)
	for _, raw := range []string{
		`{"v":1,"key":"rename","uuid":"` + uuid + `","new_branch":"next","expected_old_tip":"` + tip + `"}`,
		`{"key":"rename","uuid":"` + uuid + `","new_branch":"../escape","expected_old_tip":"` + tip + `"}`,
		`{"key":"rename","uuid":"` + uuid + `","new_branch":"next","expected_old_tip":""}`,
		`{"key":"rename","uuid":"` + uuid + `","new_branch":"next","expected_old_tip":"` + tip + `","path":"/workspace"}`,
		`{"key":"rename","key":"rename","uuid":"` + uuid + `","new_branch":"next","expected_old_tip":"` + tip + `"}`,
	} {
		if _, rpcErr := handler(context.Background(), "session.rename", json.RawMessage(raw)); rpcErr == nil || rpcErr.Kind() != "invalid" {
			t.Fatalf("unsafe rename RPC accepted: %s: %+v", raw, rpcErr)
		}
	}
	if p.called != 0 {
		t.Fatal("invalid request reached rename authority")
	}
	result, rpcErr := handler(context.Background(), "session.rename", json.RawMessage(`{"key":"rename","uuid":"`+uuid+`","new_branch":"next","expected_old_tip":"`+tip+`"}`))
	if rpcErr != nil || p.called != 1 || p.request.NewBranch != "next" || p.request.ExpectedOldTip != tip ||
		result.(api.OperationResult).Operation.Kind != "session.rename" {
		t.Fatalf("rename route: %+v %+v", result, rpcErr)
	}
}

type removalRPCProbe struct {
	LifecycleAPI
	called  int
	request RemovalPreviewRequest
}

func (p *removalRPCProbe) PreviewRemoval(_ context.Context, req RemovalPreviewRequest) (api.RemovalPreview, error) {
	p.called++
	p.request = req
	return api.RemovalPreview{Kind: req.Kind, SessionUUID: req.UUID, ConfirmationToken: strings.Repeat("a", 32)}, nil
}

func TestRemovalPreviewRPCUsesClosedRequest(t *testing.T) {
	p := &removalRPCProbe{}
	handler := NewHandler(HandlerConfig{Lifecycle: p})
	uuid := "550e8400-e29b-41d4-a716-446655440000"
	loss := "11111111-1111-1111-1111-111111111111"
	for _, raw := range []string{
		`{"v":1,"uuid":"` + uuid + `","kind":"discard"}`,
		`{"uuid":"` + uuid + `","kind":"delete","path":"/workspace"}`,
		`{"uuid":"` + uuid + `","kind":"discard","loss_operation_id":"` + loss + `"}`,
		`{"uuid":"` + uuid + `","kind":"rename","acknowledge_missing_runtime":true}`,
	} {
		if _, rpcErr := handler(context.Background(), "session.removal.preview", json.RawMessage(raw)); rpcErr == nil || rpcErr.Kind() != "invalid" {
			t.Fatalf("unsafe removal request accepted: %s: %+v", raw, rpcErr)
		}
	}
	if p.called != 0 {
		t.Fatal("invalid removal request reached authority")
	}
	result, rpcErr := handler(context.Background(), "session.removal.preview", json.RawMessage(`{"uuid":"`+uuid+`","kind":"delete"}`))
	if rpcErr != nil || p.called != 1 || p.request.UUID != uuid || p.request.Kind != "delete" || p.request.AcknowledgeMissingRuntime ||
		result.(api.RemovalPreviewResult).Preview.ConfirmationToken != strings.Repeat("a", 32) {
		t.Fatalf("removal preview route: %+v %+v", result, rpcErr)
	}
	_, rpcErr = handler(context.Background(), "session.removal.preview", json.RawMessage(`{"uuid":"`+uuid+`","kind":"discard","acknowledge_missing_runtime":true}`))
	if rpcErr != nil || p.called != 2 || !p.request.AcknowledgeMissingRuntime {
		t.Fatalf("missing runtime acknowledgement route: %+v", rpcErr)
	}
}

func TestLifecyclePagesFitClientFrame(t *testing.T) {
	ops := make([]api.OperationSummary, 20)
	for i := range ops {
		ops[i] = SummarizeOperation(api.Operation{ID: fmt.Sprintf("%036d", i), Key: strings.Repeat("\x00", 128), Kind: "session.create", Project: strings.Repeat("p", 255), SessionUUID: strings.Repeat("b", 36), Status: "blocked", Phase: "source-ready", Diagnostic: strings.Repeat("\x00", 1024)})
	}
	raw, err := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: api.OperationListResult{Operations: ops, Next: strings.Repeat("a", 36)}})
	if err != nil || len(raw)+1 > api.MaxFrameBytes {
		t.Fatalf("operation page exceeds frame: %d %v", len(raw), err)
	}
	sessions := make([]api.SessionView, 8)
	for i := range sessions {
		sessions[i] = api.SessionView{UUID: strings.Repeat("a", 35) + string(rune('a'+i)), Project: strings.Repeat("p", 255), Branch: strings.Repeat("b", 200), Registry: "established", PolicySHA256: strings.Repeat("f", 64), Condition: "stopped", PolicyCondition: "outdated", Diagnostic: strings.Repeat("\x00", 1024), LatestUnattendedCondition: &api.UnattendedCondition{Condition: "attention", Source: strings.Repeat("s", 128), Reason: strings.Repeat("<", 256), Adapter: strings.Repeat("a", 128), AdapterVersion: strings.Repeat("v", 64), ReceivedAt: "2026-09-23T00:00:00.123456789Z", ReceiveSequence: 9223372036854775807}}
	}
	page, err := boundedPage(sessions, "", func(v api.SessionView) string { return v.UUID }, func(v []api.SessionView, next string) api.SessionListResult {
		return api.SessionListResult{Sessions: v, Next: next}
	})
	if err != nil {
		t.Fatal(err)
	}
	returned := page.Sessions
	if len(returned) == 0 || len(returned) >= len(sessions) || page.Next != returned[len(returned)-1].UUID || page.Next >= sessions[len(returned)].UUID {
		t.Fatalf("session page did not trim with a continuation: %d %+v", len(returned), page.Next)
	}
	maxID := json.RawMessage(`"` + strings.Repeat(`\u0000`, 128) + `"`)
	raw, err = json.Marshal(rpcResponse{JSONRPC: "2.0", ID: maxID, Result: page})
	if err != nil || len(raw)+1 > api.MaxFrameBytes {
		t.Fatalf("session page exceeds frame: %d %v", len(raw), err)
	}
	opPage, err := boundedPage(ops, "", func(v api.OperationSummary) string { return v.ID }, func(v []api.OperationSummary, next string) api.OperationListResult {
		return api.OperationListResult{Operations: v, Next: next}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(opPage.Operations) != len(ops) || opPage.Next != "" {
		t.Fatalf("legal operation summaries unexpectedly trimmed: %d", len(opPage.Operations))
	}
	raw, err = json.Marshal(rpcResponse{JSONRPC: "2.0", ID: maxID, Result: opPage})
	if err != nil || len(raw)+1 > api.MaxFrameBytes {
		t.Fatalf("operation page exceeds frame: %d %v", len(raw), err)
	}
}

func TestRenameMismatchDiagnosticAndPagesRetainFullNamesWithinFrame(t *testing.T) {
	expected := "origin:refs/heads/" + strings.Repeat("a", 100)
	actual := strings.Repeat("b", 100) + ":refs/heads/" + strings.Repeat("c", 100)
	diagnostic := fmt.Sprintf("workspace upstream mismatch: expected %q, actual %q; correct Git manually and retry", expected, actual)
	summary := SummarizeOperation(api.Operation{Kind: "session.rename", Diagnostic: diagnostic})
	if summary.Diagnostic != diagnostic || !strings.Contains(summary.Diagnostic, actual) {
		t.Fatalf("public mismatch lost reviewed names: %q", summary.Diagnostic)
	}
	ops := make([]api.OperationSummary, 20)
	for i := range ops {
		ops[i] = SummarizeOperation(api.Operation{ID: fmt.Sprintf("%036d", i), Key: strings.Repeat("\x00", 128), Kind: "session.rename", Project: strings.Repeat("p", 255), SessionUUID: strings.Repeat("b", 36), Status: "failed", Phase: "stale", Diagnostic: strings.Repeat("\x00", 1024)})
	}
	page, err := boundedPage(ops, "", func(v api.OperationSummary) string { return v.ID }, func(v []api.OperationSummary, next string) api.OperationListResult {
		return api.OperationListResult{Operations: v, Next: next}
	})
	if err != nil {
		t.Fatal(err)
	}
	returned := page.Operations
	if len(returned) == 0 || len(returned) >= len(ops) || page.Next != returned[len(returned)-1].ID {
		t.Fatalf("rename page did not preserve bounded continuation: %d %+v", len(returned), page.Next)
	}
	raw, err := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: page})
	if err != nil || len(raw)+1 > api.MaxFrameBytes {
		t.Fatalf("rename page exceeds frame: %d %v", len(raw), err)
	}
}

func TestEnvironmentProjectionKeepsPinnedBaseAndSelectedImageDistinct(t *testing.T) {
	base, selected := strings.Repeat("a", 64), strings.Repeat("b", 64)
	ev := CreationEvidence{
		CapturedOID: strings.Repeat("c", 40), ImageFingerprint: selected,
		Environment: &EnvironmentIntent{
			ModuleID: "environment-nix", ModuleSHA256: strings.Repeat("d", 64), ConfigSHA256: strings.Repeat("e", 64),
			System: "x86_64-linux", BaseFingerprint: base, BuilderStoragePool: "builders", BuilderPolicySHA256: strings.Repeat("f", 64),
		},
		EnvironmentState: &EnvironmentState{Key: strings.Repeat("1", 64), Fingerprint: selected, CacheHit: true},
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	got := CreationEnvironmentView(api.Operation{Kind: "session.create", Evidence: raw})
	if got == nil || got.BaseImageFingerprint != base || got.ImageFingerprint != selected || got.Cache != "hit" || got.Key != ev.EnvironmentState.Key {
		t.Fatalf("lost bounded environment provenance: %+v", got)
	}
}
