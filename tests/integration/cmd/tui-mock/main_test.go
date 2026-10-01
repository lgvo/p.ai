package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/control"
)

func callFixture(t *testing.T, f *fixture, method string, p object) (json.RawMessage, *attachmentLease) {
	t.Helper()
	p["v"] = 1
	raw, _ := json.Marshal(p)
	lease := &attachmentLease{}
	result, err := f.handle(context.Background(), method, raw, lease)
	if err != nil {
		t.Fatalf("%s: %+v", method, err)
	}
	return result.(json.RawMessage), lease
}
func TestFixtureInventoryUsesUniqueBoundedPagesAndClosedParams(t *testing.T) {
	f := newFixture(t.TempDir(), "portfolio")
	seen := map[string]bool{}
	after := ""
	pages := 0
	for {
		raw, _ := callFixture(t, f, "session.list", object{"limit": 8, "after": after})
		var page struct {
			Sessions []struct {
				UUID string `json:"uuid"`
			} `json:"sessions"`
			Next string `json:"next"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Sessions) > 8 {
			t.Fatal("unbounded page")
		}
		for _, s := range page.Sessions {
			if seen[s.UUID] {
				t.Fatal("duplicate session")
			}
			seen[s.UUID] = true
		}
		pages++
		if page.Next == "" {
			break
		}
		if page.Next == after {
			t.Fatal("stuck cursor")
		}
		after = page.Next
	}
	if len(seen) != 120 || pages != 15 {
		t.Fatalf("inventory=%d pages=%d", len(seen), pages)
	}
	for _, raw := range []string{`{"v":1,"limit":8,"aftr":""}`, `{"v":1,"limit":"8"}`, `{"v":1,"v":1}`, `{"v":2}`} {
		_, err := f.handle(context.Background(), "session.list", json.RawMessage(raw), &attachmentLease{})
		if err == nil {
			t.Fatalf("accepted invalid request %s", raw)
		}
	}
}
func TestFixtureCreationIdempotencyAndSourceFreshness(t *testing.T) {
	f := newFixture(t.TempDir(), "small")
	p := object{"key": "create", "project": "forge", "branch": "feat/new", "choice": "new", "source": tip}
	first, _ := callFixture(t, f, "session.create", p)
	second, _ := callFixture(t, f, "session.create", p)
	if string(first) != string(second) || len(f.sessions) != 5 {
		t.Fatal("creation replay duplicated intent")
	}
	p = object{"v": 1, "key": "stale", "project": "forge", "branch": "feat/stale", "choice": "new", "origin_ref": "refs/heads/main", "expected_commit_oid": tip, "expected_origin_url": "https://wrong.invalid"}
	raw, _ := json.Marshal(p)
	_, err := f.handle(context.Background(), "session.create", raw, &attachmentLease{})
	if err == nil || len(f.sessions) != 5 {
		t.Fatal("stale origin accepted")
	}
}
func TestFixtureRemovalPreviewBindsKindSessionAndExpiry(t *testing.T) {
	f := newFixture(t.TempDir(), "portfolio")
	var id string
	for _, s := range f.sessions {
		if s.Project == "atlas" && s.Branch == "feat/ui" {
			id = s.UUID
		}
	}
	for _, review := range []preview{{UUID: id, Kind: "discard", Expires: time.Now().Add(-time.Second)}, {UUID: id, Kind: "delete", Expires: time.Now().Add(time.Minute)}, {UUID: "other", Kind: "discard", Expires: time.Now().Add(time.Minute)}} {
		f.previews["token"] = review
		_, err := f.handle(context.Background(), "session.discard", json.RawMessage(`{"v":1,"key":"remove","uuid":"`+id+`","confirmation_token":"token"}`), &attachmentLease{})
		if err == nil {
			t.Fatal("invalid preview accepted")
		}
		if _, ok := f.sessions[id]; !ok {
			t.Fatal("invalid review removed session")
		}
	}
}
func TestFixtureRemovalPreviewMatchesPublicShapeAndSeparatesBranchLoss(t *testing.T) {
	f := newFixture(t.TempDir(), "portfolio")
	var id string
	for _, s := range f.sessions {
		if s.Project == "atlas" && s.Branch == "feat/ui" {
			id = s.UUID
		}
	}
	raw, _ := callFixture(t, f, "workspace.loss.inspect", object{"key": "inspect", "uuid": id})
	var accepted struct {
		Operation control.Operation `json:"operation"`
	}
	_ = json.Unmarshal(raw, &accepted)
	for _, kind := range []string{"discard", "delete"} {
		data, _ := callFixture(t, f, "session.removal.preview", object{"uuid": id, "kind": kind, "loss_operation_id": accepted.Operation.ID})
		var response struct {
			V       int                    `json:"v"`
			Preview control.RemovalPreview `json:"preview"`
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		p := response.Preview
		if p.Runtime.Condition != "present" || p.Runtime.InstanceName != "p-"+id || p.Runtime.LossOperationID != accepted.Operation.ID || len(p.Runtime.Fingerprint) != 64 {
			t.Fatal("runtime review shape differs from public contract")
		}
		if (p.BranchLoss != nil) != (kind == "delete") {
			t.Fatal("branch loss applies only to Delete")
		}
		var loss map[string]json.RawMessage
		if err := json.Unmarshal(p.Runtime.Loss, &loss); err != nil {
			t.Fatal(err)
		}
		if string(loss["schema"]) != `"p.workspace-loss/v1"` || string(loss["runtime_data_will_be_removed"]) != "true" || len(loss["worktrees"]) == 0 || len(loss["p_refs"]) == 0 {
			t.Fatal("workspace-loss contract is absent")
		}
		for _, invented := range []string{"tracked_changes", "untracked_files", "home_files"} {
			if _, ok := loss[invented]; ok {
				t.Fatal("non-contract loss field:", invented)
			}
		}
	}
}
func TestFixturePendingAttachmentIsSingleUseAndExpires(t *testing.T) {
	f := newFixture(t.TempDir(), "small")
	var id string
	for _, s := range f.sessions {
		id = s.UUID
		break
	}
	f.tokens[strings.Repeat("a", 64)] = pending{UUID: id, Expires: time.Now().Add(-time.Second)}
	_, err := f.handle(context.Background(), "attachment.claim", json.RawMessage(`{"v":1,"token":"`+strings.Repeat("a", 64)+`"}`), &attachmentLease{})
	if err == nil {
		t.Fatal("expired token accepted")
	}
	data, _ := callFixture(t, f, "session.attach", object{"uuid": id})
	var response struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(data, &response)
	_, lease := callFixture(t, f, "attachment.claim", object{"token": response.Token})
	if lease.UUID != id {
		t.Fatal("wrong lease")
	}
	_, err = f.handle(context.Background(), "attachment.claim", json.RawMessage(`{"v":1,"token":"`+response.Token+`"}`), &attachmentLease{})
	if err == nil {
		t.Fatal("token replay accepted")
	}
}
func TestFixtureCancelledDelayDoesNotMutate(t *testing.T) {
	f := newFixture(t.TempDir(), "small")
	f.faults["project.create"] = fault{DelayMS: 5000}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := f.handle(ctx, "project.create", json.RawMessage(`{"v":1,"key":"cancel","project":"new"}`), &attachmentLease{})
	if err == nil || f.projects["new"].Path != "" {
		t.Fatal("cancelled request mutated fixture")
	}
}

func TestServiceEmptyFaultHasClosedRPCSchemaAndIsOneObservation(t *testing.T) {
	f := newFixture(t.TempDir(), "small")
	var id string
	for uuid, s := range f.sessions {
		if s.Condition == "ready" {
			id = uuid
			break
		}
	}
	callFixture(t, f, "mock.configure", object{"method": "session.services", "services_empty": true})
	raw, _ := callFixture(t, f, "session.services", object{"uuid": id})
	var result control.ServiceResult
	if err := json.Unmarshal(raw, &result); err != nil || result.UUID != id || len(result.Services) != 0 {
		t.Fatalf("empty observation: %s %v", raw, err)
	}
	raw, _ = callFixture(t, f, "session.services", object{"uuid": id})
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Services) != 3 {
		t.Fatal("fault changed underlying inventory")
	}
	for _, body := range []string{`{"v":1,"method":"session.services","services_empty":"true"}`, `{"v":1,"method":"session.services","services_empt":true}`} {
		if _, err := f.handle(context.Background(), "mock.configure", json.RawMessage(body), &attachmentLease{}); err == nil {
			t.Fatal("accepted malformed fixture setting")
		}
	}
}
