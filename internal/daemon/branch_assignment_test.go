package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
)

type fakeBranch struct {
	tip             string
	exists          bool
	createCalls     int
	originCalls     int
	originOperation string
	effectThenError bool
}

func (f *fakeBranch) InspectBranchRef(context.Context, string, string) (string, bool, error) {
	return f.tip, f.exists, nil
}
func (f *fakeBranch) CreateBranch(_ context.Context, _, _, oid string) error {
	f.createCalls++
	f.tip, f.exists = oid, true
	if f.effectThenError {
		return errors.New("injected lost CAS result")
	}
	return nil
}
func (f *fakeBranch) CreateCapturedOriginBranch(_ context.Context, operation, _, _, oid string) error {
	f.originCalls++
	f.originOperation = operation
	return f.CreateBranch(context.Background(), "", "", oid)
}

func TestCapturedBranchCASReplayAndMismatch(t *testing.T) {
	ctx := context.Background()
	req := control.ReserveSessionRequest{Project: "app", Branch: "work", Choice: "new", Source: "refs/heads/main"}
	oid := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	branch := &fakeBranch{effectThenError: true}
	if err := ensureBranchAssigned(ctx, branch, "operation", req, oid); err == nil {
		t.Fatal("lost CAS result ignored")
	}
	if err := ensureBranchAssigned(ctx, branch, "operation", req, oid); err != nil || branch.createCalls != 1 {
		t.Fatalf("exact CAS replay duplicated ref: %d %v", branch.createCalls, err)
	}
	branch.tip = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := ensureBranchAssigned(ctx, branch, "operation", req, oid); err == nil || branch.createCalls != 1 {
		t.Fatalf("mismatched ref accepted or rewritten: %d %v", branch.createCalls, err)
	}
	req.Choice = "existing"
	branch.tip = oid
	if err := ensureBranchAssigned(ctx, branch, "operation", req, oid); err != nil || branch.createCalls != 1 {
		t.Fatalf("existing ref mutated: %d %v", branch.createCalls, err)
	}
}

func TestOriginBranchAssignmentUsesCapturedOperation(t *testing.T) {
	branch := &fakeBranch{}
	req := control.ReserveSessionRequest{Project: "app", Branch: "from-origin", Choice: "new", OriginRef: "refs/heads/main"}
	oid := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := ensureBranchAssigned(context.Background(), branch, "captured-operation", req, oid); err != nil {
		t.Fatal(err)
	}
	if branch.originCalls != 1 || branch.originOperation != "captured-operation" || branch.tip != oid {
		t.Fatalf("origin assignment omitted durable operation: %+v", branch)
	}
}
