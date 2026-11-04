package control

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
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
		`{"key":"rename","uuid":"` + uuid + `","new_branch":"next\nx","expected_old_tip":"` + tip + `"}`,
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

func TestLifecyclePagesFitClientFrame(t *testing.T) {
	// A full page of operations whose diagnostics are at the limit fits one
	// frame; diagnostics that JSON escapes into six bytes each shorten it.
	ops := make([]api.OperationSummary, 20)
	for i := range ops {
		ops[i] = SummarizeOperation(api.Operation{ID: fmt.Sprintf("%036d", i), Key: strings.Repeat("k", 128), Kind: "session.create", Project: strings.Repeat("p", 255), SessionUUID: strings.Repeat("b", 36), Status: "blocked", Phase: "source-ready", Reason: "runtime", Diagnostic: strings.Repeat("d", api.MaxDiagnosticBytes)})
	}
	raw, err := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: api.OperationListResult{Operations: ops, Next: strings.Repeat("a", 36)}})
	if err != nil || len(raw)+1 > lifecyclePageResultCeiling {
		t.Fatalf("a page of operations at the diagnostic limit exceeds a frame: %d %v", len(raw), err)
	}
	escaped := make([]api.OperationSummary, 20)
	for i := range escaped {
		escaped[i] = ops[i]
		escaped[i].Key, escaped[i].Diagnostic = strings.Repeat("\x00", 128), strings.Repeat("\x00", api.MaxDiagnosticBytes)
	}
	escapedPage, err := boundedPage(escaped, "", func(v api.OperationSummary) string { return v.ID }, func(v []api.OperationSummary, next string) api.OperationListResult {
		return api.OperationListResult{Operations: v, Next: next}
	})
	if err != nil || len(escapedPage.Operations) == 0 || len(escapedPage.Operations) >= len(escaped) || escapedPage.Next != escapedPage.Operations[len(escapedPage.Operations)-1].ID {
		t.Fatalf("an escaped page was not shortened with a continuation: %d %v", len(escapedPage.Operations), err)
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
	diagnostic := fmt.Sprintf("workspace upstream mismatch: expected %q, actual %q", expected, actual)
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

func TestCreationImageViewShowsTheCommitImage(t *testing.T) {
	base, image, commit := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 40)
	builder := func(cycle uint64, state string) *CreationBuilderState {
		return &CreationBuilderState{Cycle: cycle, State: state}
	}
	devshell := &CreationImage{Ref: image, Built: true, Selection: "devshell", MaterialSHA256: strings.Repeat("d", 64)}
	cases := []struct {
		name    string
		kind    string
		builder *CreationBuilderState
		image   *CreationImage
		want    *api.ImageView
	}{
		{"pending", "session.create", builder(0, "not-attempted"), nil,
			&api.ImageView{CommitOID: commit, System: "aarch64-linux", Selection: "pending", State: "pending", BaseImageFingerprint: base, ImageFingerprint: base}},
		{"building", "session.create", builder(1, "attempted"), nil,
			&api.ImageView{CommitOID: commit, System: "aarch64-linux", Selection: "pending", State: "building", BaseImageFingerprint: base, ImageFingerprint: base}},
		{"built devShell", "session.create", builder(1, "absent"), devshell,
			&api.ImageView{CommitOID: commit, System: "aarch64-linux", Selection: "devShells.aarch64-linux.default", State: "built", BaseImageFingerprint: base, ImageFingerprint: image}},
		{"reused without flake", "session.create", builder(0, "not-attempted"), &CreationImage{Ref: image, Selection: "no-flake"},
			&api.ImageView{CommitOID: commit, System: "aarch64-linux", Selection: "base", State: "reused", BaseImageFingerprint: base, ImageFingerprint: image}},
		{"reused without default devShell", "session.create", builder(0, "not-attempted"), &CreationImage{Ref: image, Selection: "no-default"},
			&api.ImageView{CommitOID: commit, System: "aarch64-linux", Selection: "base", State: "reused", BaseImageFingerprint: base, ImageFingerprint: image}},
		{"settled as not accepted", "session.create", builder(1, "abandoned"), nil,
			&api.ImageView{CommitOID: commit, System: "aarch64-linux", Selection: "pending", State: "pending", BaseImageFingerprint: base, ImageFingerprint: base}},
		{"rebuilding a vanished image", "session.create", builder(1, "attempted"), &CreationImage{Ref: image, Selection: "no-flake"},
			&api.ImageView{CommitOID: commit, System: "aarch64-linux", Selection: "base", State: "building", BaseImageFingerprint: base, ImageFingerprint: image}},
		{"not a creation", "session.rename", builder(0, "not-attempted"), nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := CreationEvidence{
				CapturedOID: commit, ImageFingerprint: base, EnvironmentBuilder: tc.builder, Image: tc.image,
				Environment: &EnvironmentIntent{System: "aarch64-linux", BaseFingerprint: base, BuilderStoragePool: "builders", BuilderPolicySHA256: strings.Repeat("f", 64), ImageBuilder: "incus-commit-image/1"},
			}
			if tc.image != nil {
				ev.ImageFingerprint = tc.image.Ref
			}
			raw, _ := json.Marshal(ev)
			if got := CreationImageView(api.Operation{Kind: tc.kind, Evidence: raw}); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
	raw, _ := json.Marshal(CreationEvidence{CapturedOID: commit, ImageFingerprint: base})
	if got := CreationImageView(api.Operation{Kind: "session.create", Evidence: raw}); got != nil {
		t.Fatalf("creation without builders shows an image: %+v", got)
	}
}
