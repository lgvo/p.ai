package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type workspaceRPCProbe struct {
	LifecycleAPI
	called  int
	request WorkspaceInspectRequest
}

type renameRPCProbe struct {
	LifecycleAPI
	called  int
	request RenameRequest
}

type repairRPCProbe struct {
	LifecycleAPI
	previewCalls         int
	preparedPreviewCalls int
	prepareCalls         int
	preparationID        string
	confirmCalls         int
	request              RepairConfirmRequest
}

func (p *repairRPCProbe) PreviewRepair(_ context.Context, uuid string) (RepairPreview, error) {
	p.previewCalls++
	return RepairPreview{Kind: "missing_runtime", SessionUUID: uuid, Eligible: true, ConfirmationToken: strings.Repeat("a", 32)}, nil
}
func (p *repairRPCProbe) ConfirmRepair(_ context.Context, req RepairConfirmRequest) (Operation, error) {
	p.confirmCalls++
	p.request = req
	return Operation{Kind: "session.repair", SessionUUID: req.UUID, Status: "running"}, nil
}
func (p *repairRPCProbe) PreviewPreparedRepair(_ context.Context, uuid, preparationID string) (RepairPreview, error) {
	p.preparedPreviewCalls++
	p.preparationID = preparationID
	return RepairPreview{Kind: "missing_runtime", SessionUUID: uuid, PreparationOperationID: preparationID, Eligible: true}, nil
}
func (p *repairRPCProbe) PrepareRepair(_ context.Context, key, uuid string) (Operation, error) {
	p.prepareCalls++
	return Operation{Kind: "session.repair.prepare", Key: key, SessionUUID: uuid, Status: "running"}, nil
}

func TestRepairRPCClosedHostRequest(t *testing.T) {
	p := &repairRPCProbe{}
	h := StateHandlerWithLifecycle(nil, nil, nil, p)
	uuid := "550e8400-e29b-41d4-a716-446655440000"
	for _, raw := range []string{`{"v":2,"uuid":"` + uuid + `"}`, `{"v":1,"uuid":"` + uuid + `","path":"/workspace"}`, `{"v":1,"uuid":"` + uuid + `","uuid":"` + uuid + `"}`} {
		if _, e := h(context.Background(), "session.repair.preview", json.RawMessage(raw)); e == nil || e.Kind != "invalid_params" {
			t.Fatalf("unsafe preview reached authority: %s %+v", raw, e)
		}
	}
	if p.previewCalls != 0 {
		t.Fatal("invalid preview reached authority")
	}
	result, e := h(context.Background(), "session.repair.preview", json.RawMessage(`{"v":1,"uuid":"`+uuid+`"}`))
	if e != nil || p.previewCalls != 1 || result.(map[string]any)["preview"].(RepairPreview).SessionUUID != uuid {
		t.Fatalf("preview route: %+v %+v", result, e)
	}
	preparationID := "11111111-1111-4111-8111-111111111111"
	result, e = h(context.Background(), "session.repair.prepare", json.RawMessage(`{"v":1,"key":"resolve-once","uuid":"`+uuid+`"}`))
	if e != nil || p.prepareCalls != 1 || result.(map[string]any)["operation"].(Operation).Kind != "session.repair.prepare" {
		t.Fatalf("prepare route: %+v %+v", result, e)
	}
	result, e = h(context.Background(), "session.repair.preview", json.RawMessage(`{"v":1,"uuid":"`+uuid+`","preparation_operation_id":"`+preparationID+`"}`))
	if e != nil || p.preparedPreviewCalls != 1 || p.preparationID != preparationID || result.(map[string]any)["preview"].(RepairPreview).PreparationOperationID != preparationID {
		t.Fatalf("prepared preview route: %+v %+v", result, e)
	}
	for _, raw := range []string{`{"v":2,"key":"resolve","uuid":"` + uuid + `"}`, `{"v":1,"key":"resolve","uuid":"` + uuid + `","command":"sh"}`} {
		if _, e := h(context.Background(), "session.repair.prepare", json.RawMessage(raw)); e == nil || e.Kind != "invalid_params" {
			t.Fatalf("unsafe preparation reached authority: %s %+v", raw, e)
		}
	}
	for _, raw := range []string{`{"v":1,"key":"repair","uuid":"` + uuid + `","confirmation_token":"short"}`, `{"v":1,"key":"repair","uuid":"` + uuid + `","confirmation_token":"` + strings.Repeat("a", 32) + `","shell":"sh"}`} {
		if _, e := h(context.Background(), "session.repair.confirm", json.RawMessage(raw)); e == nil || e.Kind != "invalid_params" {
			t.Fatalf("unsafe confirm reached authority: %s %+v", raw, e)
		}
	}
	result, e = h(context.Background(), "session.repair.confirm", json.RawMessage(`{"v":1,"key":"repair","uuid":"`+uuid+`","confirmation_token":"`+strings.Repeat("a", 32)+`"}`))
	if e != nil || p.confirmCalls != 1 || p.request.Key != "repair" || result.(map[string]any)["operation"].(Operation).Kind != "session.repair" {
		t.Fatalf("confirm route: %+v %+v", result, e)
	}
}

func (p *renameRPCProbe) RenameSession(_ context.Context, req RenameRequest) (Operation, error) {
	p.called++
	p.request = req
	return Operation{Kind: "session.rename", Status: "running", SessionUUID: req.UUID}, nil
}

func TestRenameRPCUsesClosedVersionedRequest(t *testing.T) {
	p := &renameRPCProbe{}
	handler := StateHandlerWithLifecycle(nil, nil, nil, p)
	uuid := "550e8400-e29b-41d4-a716-446655440000"
	tip := strings.Repeat("a", 40)
	for _, raw := range []string{
		`{"v":2,"key":"rename","uuid":"` + uuid + `","new_branch":"next","expected_old_tip":"` + tip + `"}`,
		`{"v":1,"key":"rename","uuid":"` + uuid + `","new_branch":"../escape","expected_old_tip":"` + tip + `"}`,
		`{"v":1,"key":"rename","uuid":"` + uuid + `","new_branch":"next","expected_old_tip":""}`,
		`{"v":1,"key":"rename","uuid":"` + uuid + `","new_branch":"next","expected_old_tip":"` + tip + `","path":"/workspace"}`,
		`{"v":1,"key":"rename","key":"rename","uuid":"` + uuid + `","new_branch":"next","expected_old_tip":"` + tip + `"}`,
	} {
		if _, rpcErr := handler(context.Background(), "session.rename", json.RawMessage(raw)); rpcErr == nil || rpcErr.Kind != "invalid_params" {
			t.Fatalf("unsafe rename RPC accepted: %s: %+v", raw, rpcErr)
		}
	}
	if p.called != 0 {
		t.Fatal("invalid request reached rename authority")
	}
	result, rpcErr := handler(context.Background(), "session.rename", json.RawMessage(`{"v":1,"key":"rename","uuid":"`+uuid+`","new_branch":"next","expected_old_tip":"`+tip+`"}`))
	if rpcErr != nil || p.called != 1 || p.request.NewBranch != "next" || p.request.ExpectedOldTip != tip ||
		result.(map[string]any)["operation"].(Operation).Kind != "session.rename" {
		t.Fatalf("rename route: %+v %+v", result, rpcErr)
	}
}

type removalRPCProbe struct {
	LifecycleAPI
	called  int
	request RemovalPreviewRequest
}

func (p *removalRPCProbe) PreviewRemoval(_ context.Context, req RemovalPreviewRequest) (RemovalPreview, error) {
	p.called++
	p.request = req
	return RemovalPreview{Kind: req.Kind, SessionUUID: req.UUID, ConfirmationToken: strings.Repeat("a", 32)}, nil
}

func TestRemovalPreviewRPCUsesClosedVersionedRequest(t *testing.T) {
	p := &removalRPCProbe{}
	handler := StateHandlerWithLifecycle(nil, nil, nil, p)
	uuid := "550e8400-e29b-41d4-a716-446655440000"
	loss := "11111111-1111-1111-1111-111111111111"
	for _, raw := range []string{
		`{"v":2,"uuid":"` + uuid + `","kind":"discard","loss_operation_id":"` + loss + `"}`,
		`{"v":1,"uuid":"` + uuid + `","kind":"delete","loss_operation_id":"` + loss + `","path":"/workspace"}`,
		`{"v":1,"uuid":"` + uuid + `","kind":"discard","loss_operation_id":"` + loss + `","acknowledge_missing_runtime":true}`,
		`{"v":1,"uuid":"` + uuid + `","kind":"discard"}`,
		`{"v":1,"uuid":"` + uuid + `","kind":"rename","acknowledge_missing_runtime":true}`,
	} {
		if _, rpcErr := handler(context.Background(), "session.removal.preview", json.RawMessage(raw)); rpcErr == nil || rpcErr.Kind != "invalid_params" {
			t.Fatalf("unsafe removal request accepted: %s: %+v", raw, rpcErr)
		}
	}
	if p.called != 0 {
		t.Fatal("invalid removal request reached authority")
	}
	result, rpcErr := handler(context.Background(), "session.removal.preview", json.RawMessage(`{"v":1,"uuid":"`+uuid+`","kind":"delete","loss_operation_id":"`+loss+`"}`))
	if rpcErr != nil || p.called != 1 || p.request.UUID != uuid || p.request.LossOperationID != loss ||
		result.(map[string]any)["preview"].(RemovalPreview).ConfirmationToken != strings.Repeat("a", 32) {
		t.Fatalf("removal preview route: %+v %+v", result, rpcErr)
	}
	_, rpcErr = handler(context.Background(), "session.removal.preview", json.RawMessage(`{"v":1,"uuid":"`+uuid+`","kind":"discard","acknowledge_missing_runtime":true}`))
	if rpcErr != nil || p.called != 2 || !p.request.AcknowledgeMissingRuntime || p.request.LossOperationID != "" {
		t.Fatalf("missing runtime acknowledgement route: %+v", rpcErr)
	}
}

func (p *workspaceRPCProbe) InspectWorkspace(_ context.Context, req WorkspaceInspectRequest) (Operation, error) {
	p.called++
	p.request = req
	return Operation{Kind: "workspace.inspect", Status: "running", SessionUUID: req.SessionUUID}, nil
}

func (p *workspaceRPCProbe) InspectWorkspaceLoss(_ context.Context, req WorkspaceInspectRequest) (Operation, error) {
	p.called++
	p.request = req
	return Operation{Kind: "workspace.loss.inspect", Status: "running", SessionUUID: req.SessionUUID}, nil
}

func TestWorkspaceRPCUsesClosedVersionedRequest(t *testing.T) {
	p := &workspaceRPCProbe{}
	handler := StateHandlerWithLifecycle(nil, nil, nil, p)
	uuid := "550e8400-e29b-41d4-a716-446655440000"
	for _, raw := range []string{
		`{"v":2,"key":"inspect-1","uuid":"` + uuid + `"}`,
		`{"v":1,"key":"inspect-1","uuid":"` + uuid + `","path":"/workspace"}`,
		`{"v":1,"key":"inspect-1","key":"inspect-1","uuid":"` + uuid + `"}`,
		`{"v":1,"key":"inspect-1","uuid":"not-a-uuid"}`,
	} {
		if _, rpcErr := handler(context.Background(), "workspace.inspect", json.RawMessage(raw)); rpcErr == nil || rpcErr.Kind != "invalid_params" {
			t.Fatalf("unsafe workspace request accepted: %s: %+v", raw, rpcErr)
		}
	}
	if p.called != 0 {
		t.Fatal("invalid requests reached the workspace authority")
	}
	result, rpcErr := handler(context.Background(), "workspace.inspect", json.RawMessage(`{"v":1,"key":"inspect-1","uuid":"`+uuid+`"}`))
	if rpcErr != nil || p.called != 1 || p.request.Key != "inspect-1" || p.request.SessionUUID != uuid {
		t.Fatalf("closed workspace request not routed: %+v %+v", result, rpcErr)
	}
}

func TestWorkspaceLossRPCUsesClosedVersionedRequest(t *testing.T) {
	p := &workspaceRPCProbe{}
	handler := StateHandlerWithLifecycle(nil, nil, nil, p)
	uuid := "550e8400-e29b-41d4-a716-446655440000"
	for _, raw := range []string{
		`{"v":2,"key":"loss-1","uuid":"` + uuid + `"}`,
		`{"v":1,"key":"loss-1","uuid":"` + uuid + `","path":"/home/p"}`,
		`{"v":1,"key":"loss-1","key":"loss-1","uuid":"` + uuid + `"}`,
		`{"v":1,"key":"loss-1","uuid":"not-a-uuid"}`,
	} {
		if _, rpcErr := handler(context.Background(), "workspace.loss.inspect", json.RawMessage(raw)); rpcErr == nil || rpcErr.Kind != "invalid_params" {
			t.Fatalf("unsafe loss request accepted: %s: %+v", raw, rpcErr)
		}
	}
	if p.called != 0 {
		t.Fatal("invalid loss request reached authority")
	}
	result, rpcErr := handler(context.Background(), "workspace.loss.inspect", json.RawMessage(`{"v":1,"key":"loss-1","uuid":"`+uuid+`"}`))
	if rpcErr != nil || p.called != 1 || p.request.Key != "loss-1" || p.request.SessionUUID != uuid || result.(map[string]any)["operation"].(Operation).Kind != "workspace.loss.inspect" {
		t.Fatalf("closed loss request not routed: %+v %+v", result, rpcErr)
	}
}

func TestLifecyclePagesFitClientFrame(t *testing.T) {
	ops := make([]OperationSummary, 20)
	for i := range ops {
		ops[i] = SummarizeOperation(Operation{ID: fmt.Sprintf("%036d", i), Key: strings.Repeat("\x00", 128), Kind: "session.create", Project: strings.Repeat("p", 255), SessionUUID: strings.Repeat("b", 36), Status: "blocked", Phase: "source-ready", Diagnostic: strings.Repeat("\x00", 1024)})
	}
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"v": 1, "operations": ops, "next": strings.Repeat("a", 36)}})
	if err != nil || len(raw)+1 > MaxFrameBytes {
		t.Fatalf("operation page exceeds frame: %d %v", len(raw), err)
	}
	sessions := make([]SessionView, 8)
	for i := range sessions {
		sessions[i] = SessionView{UUID: strings.Repeat("a", 35) + string(rune('a'+i)), Project: strings.Repeat("p", 255), Branch: strings.Repeat("b", 200), Registry: "established", PolicySHA256: strings.Repeat("f", 64), Condition: "stopped", PolicyCondition: "outdated", Diagnostic: strings.Repeat("\x00", 1024), LatestUnattendedCondition: &UnattendedCondition{Condition: "attention", Source: strings.Repeat("s", 128), Reason: strings.Repeat("<", 256), Adapter: strings.Repeat("a", 128), AdapterVersion: strings.Repeat("v", 64), ReceivedAt: "2026-09-23T00:00:00.123456789Z", ReceiveSequence: 9223372036854775807}}
	}
	page, rpcErr := boundedLifecyclePage("sessions", sessions, "", func(v SessionView) string { return v.UUID })
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	returned := page["sessions"].([]SessionView)
	if len(returned) == 0 || len(returned) >= len(sessions) || page["next"] != returned[len(returned)-1].UUID || page["next"].(string) >= sessions[len(returned)].UUID {
		t.Fatalf("session page did not trim with a continuation: %d %+v", len(returned), page["next"])
	}
	maxID := json.RawMessage(`"` + strings.Repeat(`\u0000`, 128) + `"`)
	raw, err = json.Marshal(rpcResponse{JSONRPC: "2.0", ID: maxID, Result: page})
	if err != nil || len(raw)+1 > MaxFrameBytes {
		t.Fatalf("session page exceeds frame: %d %v", len(raw), err)
	}
	opPage, rpcErr := boundedLifecyclePage("operations", ops, "", func(v OperationSummary) string { return v.ID })
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	returnedOps := opPage["operations"].([]OperationSummary)
	if len(returnedOps) != len(ops) || opPage["next"] != "" {
		t.Fatalf("legal operation summaries unexpectedly trimmed: %d", len(returnedOps))
	}
	raw, err = json.Marshal(rpcResponse{JSONRPC: "2.0", ID: maxID, Result: opPage})
	if err != nil || len(raw)+1 > MaxFrameBytes {
		t.Fatalf("operation page exceeds frame: %d %v", len(raw), err)
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
	got := CreationEnvironmentView(Operation{Kind: "session.create", Evidence: raw})
	if got == nil || got.BaseImageFingerprint != base || got.ImageFingerprint != selected || got.Cache != "hit" || got.Key != ev.EnvironmentState.Key {
		t.Fatalf("lost bounded environment provenance: %+v", got)
	}
}
