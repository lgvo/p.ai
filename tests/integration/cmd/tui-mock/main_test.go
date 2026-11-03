package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/strictjson"
)

// The tests call one connection's handler as ServeConn does once the
// connection agreed the protocol, with params as a client sends them.

// send calls method with raw JSON params.
func send(ctx context.Context, h control.Handler, method, params string) (json.RawMessage, *api.Error) {
	result, err := h(ctx, method, json.RawMessage(params))
	if err != nil {
		return nil, err
	}
	return result.(json.RawMessage), nil
}

// do calls m with req and decodes its result as strictly as a client does.
func do[Req api.Request, Res any](t *testing.T, h control.Handler, m api.Method[Req, Res], req Req) Res {
	t.Helper()
	params, _ := json.Marshal(req)
	raw, err := send(context.Background(), h, m.Name, string(params))
	if err != nil {
		t.Fatalf("%s: %+v", m.Name, err)
	}
	var result Res
	if err := strictjson.Decode(raw, &result); err != nil {
		t.Fatalf("%s result %s: %v", m.Name, raw, err)
	}
	return result
}

// refusal calls m with req and returns the error it answers, nil for a
// result.
func refusal[Req api.Request, Res any](h control.Handler, m api.Method[Req, Res], req Req) *api.Error {
	params, _ := json.Marshal(req)
	_, err := send(context.Background(), h, m.Name, string(params))
	return err
}

func connect(f *fixture) control.Handler { return f.handler(&connection{}) }

func sessionID(f *fixture, project, branch string) string {
	for id, s := range f.sessions {
		if s.Project == project && s.Branch == branch {
			return id
		}
	}
	return ""
}

func TestFixtureInventoryUsesUniqueBoundedPagesAndClosedParams(t *testing.T) {
	f := newFixture(t.TempDir(), "portfolio")
	h := connect(f)
	seen := map[string]bool{}
	after := ""
	pages := 0
	for {
		page := do(t, h, api.SessionList, api.SessionListRequest{Limit: 8, After: after})
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
	for _, raw := range []string{`{"limit":8,"aftr":""}`, `{"limit":"8"}`, `{"limit":8,"limit":8}`, `{"v":1,"limit":8}`, `{"limit":9}`} {
		if _, err := send(context.Background(), h, "session.list", raw); err == nil || err.Kind() != "invalid" {
			t.Fatalf("invalid request %s: %+v", raw, err)
		}
	}
}

func TestFixtureRetainedBranchesPageByFullRef(t *testing.T) {
	f := newFixture(t.TempDir(), "portfolio")
	h := connect(f)
	branches := []string{}
	after := ""
	for pages := 0; ; pages++ {
		if pages == 8 {
			t.Fatal("retained branch pages do not end")
		}
		page := do(t, h, api.ProjectRetainedBranches, api.ProjectRetainedBranchesRequest{Project: "forge", Limit: 8, After: after})
		for _, b := range page.Branches {
			if b.Ref != "refs/heads/"+b.Branch || b.OID != tip {
				t.Fatalf("retained branch %+v", b)
			}
			branches = append(branches, b.Branch)
		}
		if page.Next == "" {
			break
		}
		// The cursor is a full ref, which the request's Validate requires.
		after = page.Next
	}
	if len(branches) != 32 || branches[0] != "retained/00" || branches[31] != "retained/31" {
		t.Fatalf("retained branches %v", branches)
	}
	if _, err := send(context.Background(), h, "project.retained_branches", `{"project":"forge","limit":8,"after":"retained/07"}`); err == nil || err.Kind() != "invalid" {
		t.Fatalf("branch name cursor: %+v", err)
	}
}

func TestFixtureCreationIdempotencyAndSourceFreshness(t *testing.T) {
	f := newFixture(t.TempDir(), "small")
	h := connect(f)
	create := api.SessionCreateRequest{Key: "create", Project: "forge", Branch: "feat/new", Choice: "new", Source: tip}
	first := do(t, h, api.SessionCreate, create)
	second := do(t, h, api.SessionCreate, create)
	if !reflect.DeepEqual(first, second) || len(f.sessions) != 5 {
		t.Fatal("creation replay duplicated intent")
	}
	create.Branch = "feat/other"
	if err := refusal(h, api.SessionCreate, create); err == nil || err.Kind() != api.KindInvalid || err.Reason() != api.ReasonKeyReused || len(f.sessions) != 5 {
		t.Fatalf("key reused for another intent: %+v", err)
	}
	for _, stale := range []struct {
		request api.SessionCreateRequest
		kind    api.ErrorKind
		reason  string
	}{
		{api.SessionCreateRequest{Key: "stale-origin", Project: "forge", Branch: "feat/stale", Choice: "new", OriginRef: "refs/heads/main", ExpectedCommitOID: tip, ExpectedOriginURL: "https://wrong.invalid"}, api.KindPreconditionFailed, api.ReasonChanged},
		{api.SessionCreateRequest{Key: "stale-source", Project: "forge", Branch: "feat/stale", Choice: "new", Source: strings.Repeat("1", 40)}, api.KindNotFound, api.ReasonSource},
	} {
		if err := refusal(h, api.SessionCreate, stale.request); err == nil || err.Kind() != stale.kind || err.Reason() != stale.reason || len(f.sessions) != 5 {
			t.Fatalf("stale source %s accepted: %+v", stale.request.Key, err)
		}
	}
}

// The fixture's removal runs only with the tag of its own plan: an expired
// tag, a changed state, and the tag of another kind or session are refused.
func TestFixtureRemovalRunsOnlyItsOwnPlan(t *testing.T) {
	f := newFixture(t.TempDir(), "portfolio")
	f.native = &nativeServer{tmux: filepath.Join(t.TempDir(), "tmux.sock")}
	h := connect(f)
	id := sessionID(f, "atlas", "feat/ui")
	other := sessionID(f, "atlas", "fix/cache")
	discard := do(t, h, api.SessionDiscard, api.SessionDiscardRequest{UUID: id})
	if discard.ChangeTag == "" || discard.Plan == nil || !discard.Plan.Allowed {
		t.Fatalf("plan: %+v", discard)
	}
	expiry, _ := api.ParseChangeTag(discard.ChangeTag)
	expired := time.Now().Add(-time.Minute).UTC().Format(api.ChangeTagTimeLayout) + discard.ChangeTag[len(api.ChangeTagTimeLayout):]
	for _, run := range []struct {
		name string
		call func() *api.Error
	}{
		{"an expired tag", func() *api.Error {
			return refusal(h, api.SessionDiscard, api.SessionDiscardRequest{UUID: id, ChangeRequest: api.ChangeRequest{Key: "expired", IfMatch: expired}})
		}},
		{"another expiry", func() *api.Error {
			edited := expiry.Add(time.Hour).Format(api.ChangeTagTimeLayout) + discard.ChangeTag[len(api.ChangeTagTimeLayout):]
			return refusal(h, api.SessionDiscard, api.SessionDiscardRequest{UUID: id, ChangeRequest: api.ChangeRequest{Key: "edited", IfMatch: edited}})
		}},
		{"a delete", func() *api.Error {
			return refusal(h, api.SessionDelete, api.SessionDeleteRequest{UUID: id, ChangeRequest: api.ChangeRequest{Key: "delete", IfMatch: discard.ChangeTag}})
		}},
		{"another session", func() *api.Error {
			return refusal(h, api.SessionDiscard, api.SessionDiscardRequest{UUID: other, ChangeRequest: api.ChangeRequest{Key: "other", IfMatch: discard.ChangeTag}})
		}},
	} {
		if err := run.call(); err == nil || err.Kind() != api.KindPreconditionFailed {
			t.Fatalf("%s: %+v", run.name, err)
		}
		if _, ok := f.sessions[id]; !ok {
			t.Fatalf("%s removed the session", run.name)
		}
	}
	f.refs["atlas"]["feat/ui"] = strings.Repeat("9", 40)
	if err := refusal(h, api.SessionDiscard, api.SessionDiscardRequest{UUID: id, ChangeRequest: api.ChangeRequest{Key: "changed", IfMatch: discard.ChangeTag}}); err == nil || err.Kind() != api.KindPreconditionFailed || err.Reason() != api.ReasonChanged {
		t.Fatalf("a changed branch: %+v", err)
	}
	// As P's, whether the container runs is information only: a Start
	// between the calls keeps the tag.
	discard = do(t, h, api.SessionDiscard, api.SessionDiscardRequest{UUID: id})
	s := f.sessions[id]
	s.Condition = "ready"
	f.sessions[id] = s
	if run := do(t, h, api.SessionDiscard, api.SessionDiscardRequest{UUID: id, ChangeRequest: api.ChangeRequest{Key: "started", IfMatch: discard.ChangeTag}}); run.Operation == nil {
		t.Fatalf("a started session's run: %+v", run)
	}
}

func TestFixtureRemovalPlanMatchesPublicShapeAndSeparatesBranchLoss(t *testing.T) {
	f := newFixture(t.TempDir(), "portfolio")
	h := connect(f)
	id := sessionID(f, "atlas", "feat/ui")
	for _, kind := range []string{"discard", "delete"} {
		var plan api.ChangeResult[api.RemovalDetails]
		if kind == "delete" {
			plan = do(t, h, api.SessionDelete, api.SessionDeleteRequest{UUID: id})
		} else {
			plan = do(t, h, api.SessionDiscard, api.SessionDiscardRequest{UUID: id})
		}
		p := plan.Plan.Details
		if plan.Plan.Action != "session."+kind || plan.Plan.Target != "session/"+id || p.Runtime.Condition != "present" || p.Runtime.InstanceName != "p-"+id ||
			p.Runtime.OriginalStatus != "Stopped" || p.AssignedTip != tip || p.AssignedRefMissing || !reflect.DeepEqual(plan.Plan.Loss[0], api.Loss{Kind: api.LossRuntimeData, Target: "session/" + id, Certain: true}) {
			t.Fatalf("runtime plan shape differs from public contract: %+v", plan)
		}
		if (p.BranchLoss != nil) != (kind == "delete") {
			t.Fatal("branch loss applies only to Delete")
		}
		if p.BranchLoss != nil && !slices.Contains(p.BranchLoss.PRefs, api.PRef{Name: "refs/heads/feat/ui", OID: tip}) {
			t.Fatalf("session Delete plan misses its ref: %+v", p.BranchLoss)
		}
	}
}

func TestFixtureRemovesARunningUnattachedSessionAndRefusesAnAttachedOne(t *testing.T) {
	f := newFixture(t.TempDir(), "small")
	f.native = &nativeServer{tmux: filepath.Join(t.TempDir(), "tmux.sock")}
	h := connect(f)
	running := sessionID(f, "forge", "fix/token-refresh")
	plan := do(t, h, api.SessionDiscard, api.SessionDiscardRequest{UUID: running})
	if plan.Plan.Details.Runtime.OriginalStatus != "Running" {
		t.Fatalf("running session planned as %q", plan.Plan.Details.Runtime.OriginalStatus)
	}
	run := api.SessionDiscardRequest{UUID: running, ChangeRequest: api.ChangeRequest{Key: "discard", IfMatch: plan.ChangeTag}}
	done := do(t, h, api.SessionDiscard, run)
	if done.Operation == nil || done.Operation.Kind != "session.discard" {
		t.Fatalf("run: %+v", done)
	}
	if _, ok := f.sessions[running]; ok {
		t.Fatal("running session survived its Discard")
	}
	if f.refs["forge"]["fix/token-refresh"] == "" {
		t.Fatal("Discard removed the assigned branch")
	}
	if replay := do(t, h, api.SessionDiscard, run); replay.Operation == nil || replay.Operation.ID != done.Operation.ID {
		t.Fatalf("replay: %+v", replay)
	}
	attached := sessionID(f, "orbit", "feat/job-retries")
	s := f.sessions[attached]
	s.AttachedCount = 1
	f.sessions[attached] = s
	refused := do(t, h, api.SessionDelete, api.SessionDeleteRequest{UUID: attached})
	if refused.Plan.Allowed || refused.ChangeTag != "" || !reflect.DeepEqual(refused.Plan.Refusals, []api.Refusal{{Code: api.ReasonAttachmentActive}}) {
		t.Fatalf("attached session plan: %+v", refused)
	}
}

func TestFixtureRenameRefusesARunningSessionAsBusy(t *testing.T) {
	f := newFixture(t.TempDir(), "small")
	id := sessionID(f, "forge", "fix/token-refresh")
	err := refusal(connect(f), api.SessionRename, api.SessionRenameRequest{Key: "rename", UUID: id, NewBranch: "feat/renamed", ExpectedOldTip: tip})
	if err == nil || err.Kind() != api.KindConflict || err.Reason() != api.ReasonSessionNotStopped || f.sessions[id].Branch != "fix/token-refresh" {
		t.Fatalf("running session rename: %+v", err)
	}
}

func TestFixtureHasNoLossInspection(t *testing.T) {
	f := newFixture(t.TempDir(), "small")
	h := connect(f)
	id := sessionID(f, "forge", "fix/token-refresh")
	if _, err := send(context.Background(), h, "workspace.loss.inspect", `{"key":"inspect","uuid":"`+id+`"}`); err == nil || err.Kind() != "method_not_found" {
		t.Fatalf("loss inspection: %+v", err)
	}
	if _, err := send(context.Background(), h, "session.discard", `{"uuid":"`+id+`","loss_operation_id":"inspect"}`); err == nil || err.Kind() != "invalid" {
		t.Fatalf("a plan with a loss operation: %+v", err)
	}
	hello, rpcErr := send(context.Background(), h, "system.hello", `{"protocol":"`+api.Protocol+`"}`)
	if rpcErr != nil || strings.Contains(string(hello), "workspace.loss.inspect") {
		t.Fatalf("hello lists loss inspection: %s %+v", hello, rpcErr)
	}
	for _, m := range api.Methods() {
		if m.Name == "workspace.loss.inspect" {
			t.Fatal("the protocol has loss inspection")
		}
	}
}

func TestFixtureHelloReportsEveryCapability(t *testing.T) {
	hello := do(t, connect(newFixture(t.TempDir(), "small")), api.SystemHello, api.HelloRequest{Protocol: api.Protocol})
	want := []api.Capability{api.CapabilityGit, api.CapabilityRuntime, api.CapabilityServices}
	if hello.Protocol != api.Protocol || !api.ValidUUID(hello.InstanceID) || !reflect.DeepEqual(hello.Capabilities, want) {
		t.Fatalf("hello %+v", hello)
	}
}

// The fixture serves the protocol methods its clients call and its own two
// methods; every other name answers method_not_found.
func TestFixtureServesOnlyProtocolAndFixtureMethods(t *testing.T) {
	served := map[string]bool{mockInspect.Name: true, mockConfigure.Name: true}
	for _, m := range []string{"system.hello", "system.health", "project.list", "session.list", "operation.list",
		"project.branches", "project.retained_branches", "origin.refresh", "origin.sources", "project.create",
		"session.create", "session.inspect", "session.start", "session.stop", "session.rename",
		"session.discard", "session.delete", "operation.inspect", "operation.wait", "operation.retry",
		"session.services", "session.service.action", "session.service.logs", "session.attach",
		"attachment.claim", "attachment.confirm", "attachment.ping"} {
		served[m] = true
	}
	known := map[string]bool{mockInspect.Name: true, mockConfigure.Name: true}
	for _, m := range api.Methods() {
		known[m.Name] = true
	}
	f := newFixture(t.TempDir(), "small")
	table := control.NewMethodTable()
	f.register(table, &attachmentLease{})
	for name := range known {
		if table.Registered(name) != served[name] {
			t.Errorf("%s registered %v, want %v", name, table.Registered(name), served[name])
		}
	}
	for name := range served {
		if !known[name] {
			t.Errorf("%s is outside the protocol", name)
		}
	}
	h := connect(f)
	for _, name := range []string{"system.capabilities", "system.inspect", "session.identity", "status.report", "mock.reset", "workspace.loss.inspect"} {
		if _, err := send(context.Background(), h, name, `{}`); err == nil || err.Kind() != "method_not_found" {
			t.Errorf("%s: %+v", name, err)
		}
	}
}

func TestFixturePendingAttachmentIsSingleUseAndExpires(t *testing.T) {
	f := newFixture(t.TempDir(), "small")
	id := sessionID(f, "forge", "feat/device-login")
	expired := strings.Repeat("a", 64)
	f.tokens[expired] = pending{UUID: id, Expires: time.Now().Add(-time.Second)}
	if err := refusal(connect(f), api.AttachmentClaim, api.AttachmentClaimRequest{Token: expired}); err == nil {
		t.Fatal("expired token accepted")
	}
	attachment := do(t, connect(f), api.SessionAttach, api.SessionAttachRequest{UUID: id})
	c := &connection{}
	do(t, f.handler(c), api.AttachmentClaim, api.AttachmentClaimRequest{Token: attachment.Token})
	if c.lease.UUID != id {
		t.Fatal("wrong lease")
	}
	if err := refusal(connect(f), api.AttachmentClaim, api.AttachmentClaimRequest{Token: attachment.Token}); err == nil {
		t.Fatal("token replay accepted")
	}
}

func TestFixtureCancelledDelayDoesNotMutate(t *testing.T) {
	f := newFixture(t.TempDir(), "small")
	f.faults["project.create"] = fault{Method: "project.create", DelayMS: 5000}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := send(ctx, connect(f), "project.create", `{"key":"cancel","project":"new"}`)
	if err == nil || err.Kind() != "cancelled" || f.projects["new"].Path != "" {
		t.Fatalf("cancelled request mutated fixture: %+v", err)
	}
}

func TestServiceEmptyFaultHasClosedRPCSchemaAndIsOneObservation(t *testing.T) {
	f := newFixture(t.TempDir(), "small")
	h := connect(f)
	id := sessionID(f, "forge", "feat/device-login")
	do(t, h, mockConfigure, fault{Method: "session.services", ServicesEmpty: true})
	result := do(t, h, api.SessionServices, api.SessionServicesRequest{UUID: id})
	if result.UUID != id || len(result.Services) != 0 {
		t.Fatalf("empty observation: %+v", result)
	}
	if result = do(t, h, api.SessionServices, api.SessionServicesRequest{UUID: id}); len(result.Services) != 3 {
		t.Fatal("fault changed underlying inventory")
	}
	for _, body := range []string{`{"method":"session.services","services_empty":"true"}`, `{"method":"session.services","services_empt":true}`,
		`{"method":"session.services","delay_ms":5001}`, `{"method":""}`, `{"v":1,"method":"session.services"}`,
		`{"method":"session.services","error":"busy"}`} {
		if _, err := send(context.Background(), h, "mock.configure", body); err == nil || err.Kind() != "invalid" {
			t.Fatalf("accepted malformed fixture setting %s: %+v", body, err)
		}
	}
}

// A connection's hello is recorded with its calls, unless the connection only
// inspects or configures the fixture.
func TestFixtureRecordsClientCallsButNotItsOwnInspection(t *testing.T) {
	f := newFixture(t.TempDir(), "small")
	hello := `{"protocol":"` + api.Protocol + `"}`
	for _, method := range []string{"mock.inspect", "session.list", ""} {
		c := &connection{}
		h := f.handler(c)
		if _, err := send(context.Background(), h, "system.hello", hello); err != nil {
			t.Fatal(err)
		}
		if method == "mock.inspect" {
			send(context.Background(), h, method, `{}`)
		} else if method == "session.list" {
			send(context.Background(), h, method, `{"limit":8}`)
		}
		f.release(c)
	}
	want := []call{{"system.hello", json.RawMessage(hello)}, {"session.list", json.RawMessage(`{"limit":8}`)}, {"system.hello", json.RawMessage(hello)}}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls %+v", f.calls)
	}
}
