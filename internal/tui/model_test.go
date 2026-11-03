package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/lgvo/p.ai/internal/api"
)

// fakeClient answers each call with fn's value, encoded and decoded into the
// method's result type as the daemon's reply would be. p is the typed request.
type fakeClient struct {
	calls []string
	fn    func(method string, p any) (any, error)
}

func (f *fakeClient) Call(_ context.Context, method string, p, result any) error {
	f.calls = append(f.calls, method)
	v, e := f.fn(method, p)
	if e != nil {
		return e
	}
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return json.Unmarshal(raw, result)
}

// reply is the reply to a call of method whose result is raw, decoded into the
// method's result type as callOf stores it.
func reply(t *testing.T, epoch int, method, raw string) actionDone {
	t.Helper()
	for _, m := range api.Methods() {
		if m.Name == method {
			result := reflect.New(m.Result)
			if err := json.Unmarshal([]byte(raw), result.Interface()); err != nil {
				t.Fatal(err)
			}
			return actionDone{epoch: epoch, method: method, result: result.Elem().Interface()}
		}
	}
	t.Fatalf("no method %s", method)
	return actionDone{}
}

// hello is the daemon's hello reporting capabilities.
func hello(capabilities ...api.Capability) api.HelloResult {
	return api.HelloResult{Protocol: api.Protocol, Capabilities: append([]api.Capability{}, capabilities...)}
}
func press(m Model, key string) (Model, tea.Cmd) {
	k := tea.KeyPressMsg{Text: key}
	switch key {
	case "enter":
		k.Code = tea.KeyEnter
		k.Text = ""
	case "esc":
		k.Code = tea.KeyEscape
		k.Text = ""
	default:
		if len([]rune(key)) == 1 {
			k.Code = []rune(key)[0]
		}
	}
	updated, cmd := m.Update(k)
	return updated.(Model), cmd
}

// The fixture's session UUIDs; requests check that a UUID has its form.
const (
	stoppedID = "00000000-0000-4000-8000-000000000001"
	runningID = "00000000-0000-4000-8000-000000000002"
	waitingID = "00000000-0000-4000-8000-000000000003"
	targetID  = "00000000-0000-4000-8000-000000000004"
	// reviewTag is a removal plan's change tag.
	reviewTag = "20261102T120200Z-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func fixture() Model {
	m := New(nil, "/tmp/control.sock")
	m.data.sessions = []api.SessionView{{UUID: stoppedID, Project: "a", Branch: "main", Condition: "stopped"}, {UUID: runningID, Project: "a", Branch: "feature", Condition: "ready"}, {UUID: waitingID, Project: "z", Branch: "urgent", Condition: "ready", LatestUnattendedCondition: &api.UnattendedCondition{Condition: "attention", Reason: "approval required"}}}
	m.clamp()
	return m
}

func TestBrowserPriorityIdentityFilterAndEmptySelection(t *testing.T) {
	m := fixture()
	rows := m.rows()
	if rows[0].id != waitingID || rows[1].id != runningID {
		t.Fatalf("wrong global priority: %+v", rows)
	}
	m.selected = runningID
	m.restoreSelection()
	m.data.sessions[1].Branch = "zzz"
	m.restoreSelection()
	if m.selected != runningID {
		t.Fatal("refresh lost selection")
	}
	m.scope = "a"
	m.query = "ftr"
	m.clamp()
	if len(m.rows()) != 0 || m.selected != "" {
		t.Fatal("empty filter retained an actionable hidden session")
	}
	_, cmd := press(m, "enter")
	if cmd != nil {
		t.Fatal("empty result dispatched attachment")
	}
	m.scope = "z"
	m.query = "apr"
	m.clamp()
	if len(m.rows()) != 1 {
		t.Fatal("fuzzy report search failed")
	}
}

func TestBackLayersAndLiteralInput(t *testing.T) {
	m := fixture()
	m.scope = "z"
	m.query = "urgent"
	m.page = "agents"
	m, _ = press(m, "q")
	if m.page != "sessions" || m.query == "" || m.scope == "" {
		t.Fatal("page back erased enclosing filters")
	}
	m, _ = press(m, "q")
	if m.query != "" || m.scope == "" {
		t.Fatal("search must clear first")
	}
	m, _ = press(m, "q")
	if m.scope != "" {
		t.Fatal("project scope not cleared")
	}
	m.page = "form"
	m.form = "branch-name"
	for _, key := range []string{"q", "g", "G"} {
		m, _ = press(m, key)
	}
	if m.input != "qgG" || m.page != "form" {
		t.Fatalf("text input intercepted navigation: %q", m.input)
	}
}

// requestOf runs call against a client that records its request instead of
// answering, and returns the request.
func requestOf[Req any](t *testing.T, call daemonCall) Req {
	t.Helper()
	var got any
	c := &fakeClient{fn: func(_ string, p any) (any, error) {
		got = p
		return nil, errors.New("recorded")
	}}
	call.run(context.Background(), c)
	r, ok := got.(Req)
	if !ok {
		t.Fatalf("%s sent %T", call.method, got)
	}
	return r
}

func stopOf(t *testing.T, call daemonCall) api.SessionStopRequest {
	t.Helper()
	if call.method != api.SessionStop.Name {
		t.Fatalf("confirmation calls %s", call.method)
	}
	return requestOf[api.SessionStopRequest](t, call)
}

func TestCapturedStopIdentityAndDefaultNo(t *testing.T) {
	m := fixture()
	m.selected = runningID
	m.restoreSelection()
	m, _ = press(m, "s")
	if stopOf(t, m.confirm).UUID != runningID || m.page != "confirm" {
		t.Fatal("confirmation did not bind selected UUID")
	}
	m.data.sessions[1].Branch = "changed"
	m.selected = waitingID
	if stopOf(t, m.confirm).UUID != runningID {
		t.Fatal("refresh retargeted pending confirmation")
	}
	m, cmd := press(m, "enter")
	if m.page != "sessions" || cmd != nil {
		t.Fatal("Enter must decline Stop")
	}
}

func TestLateResultCannotReopenOrAttach(t *testing.T) {
	m := fixture()
	m.page = "progress"
	m.epoch = 5
	m.attachOnComplete = true
	m.contextSession.UUID = runningID
	m, _ = press(m, "q")
	old := reply(t, 5, "session.start", `{"session":{"uuid":"`+runningID+`","session_condition":"ready"}}`)
	updated, cmd := m.Update(old)
	m = updated.(Model)
	if m.page != "sessions" || cmd != nil || m.attachOnComplete {
		t.Fatal("late completion reopened abandoned startup")
	}
}

func TestResponsiveFramesAndVisiblePageMovement(t *testing.T) {
	for _, size := range [][2]int{{48, 16}, {60, 20}, {80, 24}, {120, 35}, {160, 50}} {
		m := fixture()
		m.width, m.height = size[0], size[1]
		for i := 0; i < 100; i++ {
			m.data.sessions = append(m.data.sessions, api.SessionView{UUID: fmt.Sprint(i), Project: "long-project", Branch: strings.Repeat("branch", 30), Condition: "stopped"})
		}
		m.cursor = 80
		m.clamp()
		for _, page := range []string{"sessions", "projects", "create", "services", "agents", "policy", "operations", "progress", "review", "form", "journal", "help"} {
			m.page = page
			m.review = strings.Repeat("line\n", 100)
			frame := m.View().Content
			lines := strings.Split(frame, "\n")
			if len(lines) > size[1] {
				t.Fatalf("%s exceeds height %v", page, size)
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > size[0] {
					t.Fatalf("%s exceeds width %v: %q", page, size, line)
				}
			}
		}
		m.page = "sessions"
		m.cursor = 0
		m.clamp()
		m.move("pgdown")
		if m.cursor != m.capacity() {
			t.Fatal("paging differs from visible capacity")
		}
	}
}

func TestUntrustedTerminalTextIsInert(t *testing.T) {
	text := safe("normal\x1b]52;c;secret\a\x1b[2J\r\n\x00tail")
	if strings.ContainsAny(text, "\x1b\a\r\n\x00") {
		t.Fatalf("terminal control escaped: %q", text)
	}
}

func TestStaleServiceObservationDisablesActions(t *testing.T) {
	m := fixture()
	m.page = "services"
	m.services = []api.ProjectService{{Name: "p-project-api.service", State: api.ServiceRunning}}
	m.servicesFresh = true
	updated, _ := m.Update(actionDone{epoch: m.epoch, method: "session.services", err: errors.New("unavailable")})
	m = updated.(Model)
	_, cmd := press(m, "s")
	if cmd != nil || m.servicesFresh {
		t.Fatal("stale services still actionable")
	}
}

// TestDaemonRefusalShowsTheClientSentence checks that a refusal from the
// daemon, which carries codes and no text, shows the client's sentence for
// its reason, and the kind's sentence for a reason the client does not know.
func TestDaemonRefusalShowsTheClientSentence(t *testing.T) {
	for _, test := range []struct {
		err  *api.Error
		want string
	}{
		{api.NewError(api.KindConflict, api.ReasonAttachmentActive), "Detach every terminal from this session first."},
		{api.NewError(api.KindRetry, "a_new_reason"), "P is busy with this right now; try again in a moment."},
	} {
		m := fixture()
		updated, _ := m.Update(actionDone{epoch: m.epoch, method: "session.stop", err: test.err})
		if got := updated.(Model).notice; got != test.want {
			t.Errorf("%v showed %q, want %q", test.err, got, test.want)
		}
	}
	m := fixture()
	updated, _ := m.Update(loaded{err: api.NewError(api.KindInternal, api.ReasonStore)})
	if got := updated.(Model).notice; got != "P failed unexpectedly; the daemon's log has the cause." {
		t.Errorf("an inventory failure showed %q", got)
	}
}

func TestInventoryPaginationAndRPCError(t *testing.T) {
	f := &fakeClient{}
	f.fn = func(method string, p any) (any, error) {
		switch method {
		case "system.hello":
			return hello(api.CapabilityRuntime), nil
		case "operation.list":
			return api.OperationListResult{}, nil
		case "session.list":
			if p.(api.SessionListRequest).Limit != 8 {
				t.Fatal("wrong session page limit")
			}
			if p.(api.SessionListRequest).After == "" {
				return map[string]any{"sessions": []api.SessionView{{UUID: "a"}}, "next": "a"}, nil
			}
			return map[string]any{"sessions": []api.SessionView{{UUID: "b"}}, "next": ""}, nil
		}
		return nil, fmt.Errorf("unexpected %s", method)
	}
	v, err := loadInventory(context.Background(), f)
	if err != nil || len(v.sessions) != 2 {
		t.Fatalf("pagination: %+v %v", v, err)
	}
	f.fn = func(method string, p any) (any, error) {
		if method == "system.hello" {
			return hello(api.CapabilityRuntime), nil
		}
		return map[string]any{"next": "a"}, nil
	}
	if _, err = loadInventory(context.Background(), f); err == nil {
		t.Fatal("repeated cursor accepted")
	}
}

func TestSnapshotUUIDDoesNotSilentlySelectAnother(t *testing.T) {
	f := &fakeClient{fn: func(method string, p any) (any, error) {
		if method == "system.hello" {
			return hello(api.CapabilityRuntime), nil
		}
		return map[string]any{"sessions": []api.SessionView{{UUID: "other"}}, "next": ""}, nil
	}}
	if _, err := Snapshot(context.Background(), f, "/tmp/sock", "services", "missing", 80, 24); err == nil {
		t.Fatal("snapshot silently selected another session")
	}
}

func TestRemovalRequiresAPlanAndExplicitYes(t *testing.T) {
	m := fixture()
	m.contextSession = m.data.sessions[0]
	m.removal = &removalIntent{UUID: stoppedID, Kind: "discard"}
	m.page = "progress"
	m.client = &fakeClient{fn: func(method string, p any) (any, error) {
		t.Fatalf("unexpected command execution %s", method)
		return nil, nil
	}}
	updated, cmd := m.Update(reply(t, m.epoch, "session.discard", removalPlan("discard", stoppedID, `{"runtime":{"condition":"present"}}`)))
	m = updated.(Model)
	if run := requestOf[api.SessionDiscardRequest](t, m.confirm); m.page != "review" || cmd != nil || run.UUID != stoppedID || run.IfMatch != reviewTag {
		t.Fatal("the plan skipped review or retargeted")
	}
	m, cmd = press(m, "enter")
	if cmd != nil || m.page != "review" {
		t.Fatal("Enter authorized destructive removal")
	}
	m, cmd = press(m, "n")
	if cmd != nil || m.page != "sessions" {
		t.Fatal("cancel dispatched removal")
	}
}

func TestCanceledRemovalAndOperationInspectionStayReadOnly(t *testing.T) {
	m := fixture()
	m.page = "progress"
	m.removal = &removalIntent{UUID: stoppedID, Kind: "delete"}
	m, _ = press(m, "q")
	if m.removal != nil {
		t.Fatal("Back retained removal intent")
	}
	m.page = "progress"
	m.name = "delete" // A Rename target must never become removal intent.
	m.client = &fakeClient{fn: func(method string, p any) (any, error) {
		if method != "system.hello" {
			t.Fatalf("operation inspection dispatched %s", method)
		}
		return hello(), nil
	}}
	updated, cmd := m.Update(reply(t, m.epoch, "operation.inspect", `{"operation":{"id":"removal","kind":"session.discard","session_uuid":"`+stoppedID+`","status":"completed"}}`))
	m = updated.(Model)
	if m.page != "progress" || m.confirm.method != "" {
		t.Fatal("inspection reopened removal review")
	}
	if cmd != nil {
		if _, ok := cmd().(loaded); !ok {
			t.Fatal("operation inspection was not read-only refresh")
		}
	}
	m.removal = &removalIntent{UUID: "other", Kind: "discard"}
	_, cmd = m.Update(reply(t, m.epoch, "operation.inspect", `{"operation":{"id":"removal","kind":"session.discard","session_uuid":"`+stoppedID+`","status":"completed"}}`))
	if cmd != nil {
		if _, ok := cmd().(loaded); !ok {
			t.Fatal("wrong UUID triggered a removal plan")
		}
	}
}

func TestWrappedReviewsExposeCompleteLongFields(t *testing.T) {
	m := fixture()
	m.width = 48
	m.height = 16
	m.page = "review"
	m.review = "Path: " + strings.Repeat("long-component/", 30) + "UNIQUE_END"
	for _, line := range m.reviewLines() {
		if ansi.StringWidth(line) > 44 {
			t.Fatal("review wrapping exceeds available width")
		}
	}
	m.scroll("G", &m.reviewOffset)
	if !strings.Contains(ansi.Strip(m.View().Content), "UNIQUE_END") {
		t.Fatal("long loss field suffix is inaccessible")
	}
	m.page = "confirm"
	m.reviewOffset = 0
	m.scroll("G", &m.reviewOffset)
	if !strings.Contains(ansi.Strip(m.View().Content), "UNIQUE_END") {
		t.Fatal("long confirmation field inaccessible")
	}
}

func TestSmallInspectionPagesExposeLongFieldsAndHelp(t *testing.T) {
	m := fixture()
	m.width, m.height = 48, 16
	m.contextSession = api.SessionView{UUID: runningID, Project: "app", Branch: "main", Diagnostic: strings.Repeat("detail/", 100) + "DIAGNOSTIC_END", LatestUnattendedCondition: &api.UnattendedCondition{Reason: strings.Repeat("reason/", 100) + "REPORT_END"}, Image: &api.ImageView{ImageFingerprint: strings.Repeat("image/", 100) + "IMAGE_END"}}
	for _, item := range []struct{ page, suffix string }{{"agents", "REPORT_END"}, {"policy", "IMAGE_END"}, {"progress", "DIAGNOSTIC_END"}, {"help", "manual test."}} {
		m.navigate(item.page)
		m, _ = press(m, "G")
		if !strings.Contains(strings.ReplaceAll(ansi.Strip(m.View().Content), "\n", ""), item.suffix) {
			t.Fatalf("%s long content inaccessible: %s", item.page, m.View().Content)
		}
		m, _ = press(m, "g")
		m, _ = press(m, "g")
		if m.reviewOffset != 0 {
			t.Fatalf("%s gg did not return to top", item.page)
		}
	}
}

func TestCreationConfirmationShowsExactSourceAndBindsOrigin(t *testing.T) {
	m := fixture()
	m.creation = creationDraft{key: "create", project: "app"}
	m.form = "source"
	m.sourceOriginURL = "ssh://reviewed.example/repo"
	m.sourceParams = map[string]sourceSelection{"origin:refs/heads/main": {originRef: "refs/heads/main", expectedCommitOID: strings.Repeat("a", 40), expectedOriginURL: m.sourceOriginURL}}
	m.choose(row{id: "origin:refs/heads/main", label: "External origin · refs/heads/main", detail: strings.Repeat("a", 40)})
	m.input = "work"
	m.submitForm()
	if !strings.Contains(m.review, "refs/heads/main") || !strings.Contains(m.review, strings.Repeat("a", 40)) || !strings.Contains(m.review, m.sourceOriginURL) || requestOf[api.SessionCreateRequest](t, m.confirm).ExpectedOriginURL != m.sourceOriginURL {
		t.Fatal("source/origin omitted or not bound")
	}
	m, _ = press(m, "esc") // Confirmation to branch name.
	m, _ = press(m, "esc") // Branch name to source.
	m, _ = press(m, "esc") // Source to branch.
	if m.form != "branch" {
		t.Fatal("Back did not restore branch selection")
	}
	m.choose(row{id: "retained", detail: strings.Repeat("b", 40)})
	if r := requestOf[api.SessionCreateRequest](t, m.confirm); r.Source != "" || r.OriginRef != "" || r.ExpectedCommitOID != "" || r.ExpectedOriginURL != "" {
		t.Fatalf("retained branch inherited an external-source field: %+v", r)
	}
}

func TestServiceKeyDuringRefreshCapturesExactlyOneIntent(t *testing.T) {
	m := fixture()
	m.page = "services"
	m.contextSession.UUID = runningID
	m.services = []api.ProjectService{{Name: "p-project-a.service", State: api.ServiceRunning}, {Name: "p-project-b.service", State: api.ServiceRunning}}
	m.servicesFresh, m.working = true, true
	m.pendingMethod = "session.services"
	m, cmd := press(m, "s")
	if cmd != nil || m.queuedService == nil || m.queuedService.Unit != "p-project-a.service" || m.queuedService.Action != "stop" {
		t.Fatal("refresh dropped the captured Stop intent")
	}
	m, cmd = press(m, "r")
	if cmd != nil || m.queuedService.Action != "stop" {
		t.Fatal("repeated key replaced the pending intent")
	}
	canceled := m
	canceled, _ = press(canceled, "q")
	updated, abandoned := canceled.Update(reply(t, m.epoch, "session.services", `{"uuid":"`+runningID+`","services":[]}`))
	if abandoned != nil || updated.(Model).queuedService != nil {
		t.Fatal("Back allowed a late queued action")
	}
	failed, _ := m.Update(actionDone{epoch: m.epoch, method: "session.services", err: errors.New("busy")})
	if failed.(Model).queuedService != nil || failed.(Model).servicesFresh {
		t.Fatal("failed observation retained an automatic action")
	}
	missing, missingCmd := m.Update(reply(t, m.epoch, "session.services", `{"uuid":"`+runningID+`","services":[]}`))
	if missingCmd != nil || missing.(Model).queuedService != nil {
		t.Fatal("absent unit dispatched an action")
	}
	wrong, wrongCmd := m.Update(reply(t, m.epoch, "session.services", `{"uuid":"other","services":[{"name":"p-project-a.service"}]}`))
	if wrongCmd != nil || wrong.(Model).queuedService != nil || wrong.(Model).servicesFresh {
		t.Fatal("wrong-session observation authorized a queued action")
	}
	calls := 0
	m.client = &fakeClient{fn: func(method string, p any) (any, error) {
		calls++
		if method != "session.service.action" || p != (api.SessionServiceActionRequest{UUID: runningID, Name: "p-project-a.service", Action: "stop"}) {
			t.Fatalf("refresh/selection retargeted or re-toggled intent: %s %+v", method, p)
		}
		return map[string]any{}, nil
	}}
	m.cursor = 1 // Selection moves while the read is running.
	updated, cmd = m.Update(reply(t, m.epoch, "session.services", `{"uuid":"`+runningID+`","services":[{"name":"p-project-b.service","state":"running"},{"name":"p-project-a.service","state":"stopped"}]}`))
	if cmd == nil || updated.(Model).queuedService != nil {
		t.Fatal("fresh observation failed to dispatch and consume exactly one intent")
	}
	cmd()
	if calls != 1 {
		t.Fatal("intent was not issued exactly once")
	}
}

// TestStoppedOperationShowsWhatItsReasonMeans checks that a blocked or failed
// operation shows the client's sentence for its reason, then its diagnostic
// on a labelled line of its own.
func TestStoppedOperationShowsWhatItsReasonMeans(t *testing.T) {
	want := []string{"The runtime failed; check Incus. Retry it once that is resolved.", "Diagnostic: Incus did not answer"}
	if got := operationDetail("blocked", api.ReasonRuntime, "Incus did not answer"); !reflect.DeepEqual(got, want) {
		t.Fatalf("detail %q, want %q", got, want)
	}
	if got := operationDetail("running", "", "detail"); !reflect.DeepEqual(got, []string{"detail"}) {
		t.Fatalf("a running operation's detail %q", got)
	}
}

// TestWaitThatEndsRunningWaitsAgainAndKeepsTheIntentToAttach follows a
// creation: a wait answering running is a result, so the TUI waits again and
// still enters the session once the creation completes.
func TestWaitThatEndsRunningWaitsAgainAndKeepsTheIntentToAttach(t *testing.T) {
	var waited []api.OperationWaitRequest
	client := &fakeClient{fn: func(method string, p any) (any, error) {
		if method == api.OperationWait.Name {
			waited = append(waited, p.(api.OperationWaitRequest))
		}
		return nil, nil
	}}
	m := fixture()
	m.client = client
	created := reply(t, m.epoch, "session.create", `{"operation":{"id":"creation","kind":"session.create","session_uuid":"`+waitingID+`","status":"running"}}`)
	updated, cmd := m.Update(created)
	m = updated.(Model)
	if m.page != "progress" || !m.attachOnComplete || cmd == nil {
		t.Fatalf("creation did not start waiting: page %s attach %v", m.page, m.attachOnComplete)
	}
	cmd()
	still := reply(t, m.epoch, "operation.wait", `{"operation":{"id":"creation","kind":"session.create","session_uuid":"`+waitingID+`","status":"running","phase":"principals-ready"}}`)
	updated, cmd = m.Update(still)
	m = updated.(Model)
	if !m.attachOnComplete || cmd == nil || m.pendingMethod != api.OperationWait.Name {
		t.Fatalf("a running answer ended the wait: attach %v pending %s", m.attachOnComplete, m.pendingMethod)
	}
	cmd()
	if len(waited) != 2 || waited[1].ID != "creation" || waited[1].Timeout != waitSeconds {
		t.Fatalf("waits %+v", waited)
	}
	done := reply(t, m.epoch, "operation.wait", `{"operation":{"id":"creation","kind":"session.create","session_uuid":"`+waitingID+`","status":"completed"}}`)
	updated, _ = m.Update(done)
	m = updated.(Model)
	if m.pendingMethod != api.SessionInspect.Name || m.contextSession.UUID != waitingID {
		t.Fatalf("completion did not move on to entry: pending %s", m.pendingMethod)
	}
}

func TestWaitFitsTheCallBudget(t *testing.T) {
	ctx, cancel := callContext()
	defer cancel()
	deadline, _ := ctx.Deadline()
	if budget := time.Until(deadline); budget < waitSeconds*time.Second+4*time.Second {
		t.Fatalf("a %d-second wait leaves %s of a %s budget to connect and reply", waitSeconds, budget-waitSeconds*time.Second, budget)
	}
}

// TestProgressPageFollowsTheInventory: while a wait is in flight the page
// takes the running operation's phase from the 3-second refresh; an operation
// that is not running and changed elsewhere is read again; and a wait that
// ended in an error starts again on the next tick.
func TestProgressPageFollowsTheInventory(t *testing.T) {
	m := fixture()
	m.client = &fakeClient{fn: func(string, any) (any, error) { return nil, nil }}
	m.navigate("progress")
	m.operationID = "op"
	m.operation = api.Operation{ID: "op", Kind: "session.rename", Status: "running", Phase: "reserved"}
	m.working, m.pendingMethod = true, api.OperationWait.Name
	inventory := func(status, phase string) loaded {
		data := m.data
		data.operations = []api.OperationSummary{{ID: "op", Kind: "session.rename", Status: status, Phase: phase}}
		return loaded{data: data}
	}
	updated, _ := m.Update(inventory("running", "workspace-renamed"))
	m = updated.(Model)
	if m.operation.Phase != "workspace-renamed" || m.pendingMethod != api.OperationWait.Name {
		t.Fatalf("phase %s, pending %s", m.operation.Phase, m.pendingMethod)
	}
	// The wait ends in an error; the next tick waits again.
	updated, _ = m.Update(actionDone{epoch: m.epoch, method: api.OperationWait.Name, err: errors.New("transport")})
	m = updated.(Model)
	updated, _ = m.Update(tick{})
	m = updated.(Model)
	if m.pendingMethod != api.OperationWait.Name {
		t.Fatalf("the tick did not wait again: pending %s", m.pendingMethod)
	}
	// A blocked operation another client retried is read again.
	m.working, m.pendingMethod = false, ""
	m.operation.Status = "blocked"
	updated, _ = m.Update(inventory("completed", "completed"))
	m = updated.(Model)
	if m.pendingMethod != api.OperationInspect.Name {
		t.Fatalf("a change made elsewhere was not read: pending %s", m.pendingMethod)
	}
}

// TestStartAndStopFollowTheirOperations: Enter on a stopped session follows
// the start operation and enters the session once it reads ready; a stop
// follows its operation to "Stopped". TestStopOfAStoppedSessionSaysItIsAlreadyStopped
// covers a stop that records nothing.
func TestStartAndStopFollowTheirOperations(t *testing.T) {
	m := fixture()
	m.client = &fakeClient{fn: func(string, any) (any, error) { return nil, nil }}
	m.contextSession = m.data.sessions[0]
	m.enterSession()
	started := reply(t, m.epoch, "session.start", `{"changed":true,"operation":{"id":"start","kind":"session.start","session_uuid":"`+stoppedID+`","status":"running","phase":"starting"}}`)
	updated, _ := m.Update(started)
	m = updated.(Model)
	if m.page != "progress" || m.operationID != "start" || m.pendingMethod != api.OperationWait.Name || !m.attachOnComplete {
		t.Fatalf("start was not followed: page %s op %s pending %s", m.page, m.operationID, m.pendingMethod)
	}
	updated, _ = m.Update(reply(t, m.epoch, "operation.wait", `{"operation":{"id":"start","kind":"session.start","session_uuid":"`+stoppedID+`","status":"completed","phase":"ready"}}`))
	m = updated.(Model)
	if m.pendingMethod != api.SessionInspect.Name {
		t.Fatalf("a completed start did not read the session: pending %s", m.pendingMethod)
	}

	m = fixture()
	m.client = &fakeClient{fn: func(string, any) (any, error) { return nil, nil }}
	m.contextSession = m.data.sessions[1]
	m.navigate("confirm")
	updated, _ = m.Update(reply(t, m.epoch, "session.stop", `{"changed":true,"operation":{"id":"stop","kind":"session.stop","session_uuid":"`+runningID+`","status":"running","phase":"stopping"}}`))
	m = updated.(Model)
	if m.page != "progress" || m.pendingMethod != api.OperationWait.Name || m.attachOnComplete {
		t.Fatalf("stop was not followed: page %s pending %s", m.page, m.pendingMethod)
	}
	updated, _ = m.Update(reply(t, m.epoch, "operation.wait", `{"operation":{"id":"stop","kind":"session.stop","session_uuid":"`+runningID+`","status":"completed","phase":"stopped"}}`))
	m = updated.(Model)
	if m.notice != "Stopped. Files and branch retained." {
		t.Fatalf("notice %q", m.notice)
	}
	// The page reads as the session's readiness, as it did before Stop
	// recorded an operation.
	if _, lines, _ := m.inspectionContent(); len(lines) < 3 || lines[0] != "a / feature" || lines[2] != "Condition: stopped" {
		t.Fatalf("stop page %q", lines)
	}
	// A completed stop opened from O is only observed: no Stopped notice,
	// and the operation's own layout whatever session was last acted on.
	m.data.operations = []api.OperationSummary{{ID: "stop", Kind: "session.stop", SessionUUID: runningID, Status: "completed"}}
	m.navigate("operations")
	m, _ = press(m, "enter")
	if m.contextSession.UUID != "" {
		t.Fatal("an opened operation kept the session last acted on")
	}
	m.notice = ""
	updated, _ = m.Update(reply(t, m.epoch, "operation.inspect", `{"operation":{"id":"stop","kind":"session.stop","session_uuid":"`+runningID+`","status":"completed","phase":"stopped"}}`))
	m = updated.(Model)
	if m.notice == "Stopped. Files and branch retained." {
		t.Fatal("an observed stop was reported as the page's own")
	}
}

// TestWaitingLineWrapsWithinThePage: at 48 columns the waiting line wraps
// between words instead of losing its end.
func TestWaitingLineWrapsWithinThePage(t *testing.T) {
	for _, page := range []string{"progress", "sessions"} {
		m := fixture()
		m.width, m.height = 48, 16
		m.navigate(page)
		m.working = true
		frame := ansi.Strip(m.View().Content)
		if !strings.Contains(strings.Join(strings.Fields(frame), " "), "Waiting for daemon… (Back leaves accepted work running)") {
			t.Errorf("%s clips the waiting line:\n%s", page, frame)
		}
	}
}

// TestProgressPageUsesTheRowsBelowIt: the operation page's text takes every
// row the title, controls and feedback leave, so at 48x16 a completed
// operation's page note and a blocked one's Diagnostic need no scrolling.
func TestProgressPageUsesTheRowsBelowIt(t *testing.T) {
	m := fixture()
	m.width, m.height = 48, 16
	m.navigate("progress")
	m.operationID = "2bd0d0d5-d157-58bb-bde4-011a6427a029"
	m.operation = api.Operation{ID: m.operationID, Kind: "session.rename", Key: "fixture-failed", Status: "completed", Phase: "complete"}
	frame := strings.Join(strings.Fields(ansi.Strip(m.View().Content)), " ")
	if !strings.Contains(frame, "Leaving this screen does not cancel an accepted operation.") {
		t.Errorf("completed page clips its note:\n%s", ansi.Strip(m.View().Content))
	}
	m.operation.Status, m.operation.Phase = "blocked", "rename"
	m.operation.Reason, m.operation.Diagnostic = api.ReasonRuntime, "Incus did not answer within 30 seconds"
	frame = strings.Join(strings.Fields(ansi.Strip(m.View().Content)), " ")
	if !strings.Contains(frame, "Diagnostic: Incus did not answer within 30 seconds") {
		t.Errorf("blocked page hides its Diagnostic:\n%s", ansi.Strip(m.View().Content))
	}
	if lines := strings.Split(ansi.Strip(m.View().Content), "\n"); len(lines) != m.height-2 {
		t.Errorf("blocked page draws %d rows, want all %d but the two feedback rows:\n%s", len(lines), m.height-2, strings.Join(lines, "\n"))
	}
}

// TestRetryIsOfferedForABlockedOperationOnly: Retry resumes a blocked
// operation; for any other status the page neither offers nor sends it.
func TestRetryIsOfferedForABlockedOperationOnly(t *testing.T) {
	for _, status := range []string{"running", "completed", "failed", "blocked"} {
		t.Run(status, func(t *testing.T) {
			client := &fakeClient{fn: func(string, any) (any, error) { return nil, errors.New("recorded") }}
			m := fixture()
			m.client = client
			m.navigate("progress")
			m.operationID = "op"
			m.operation = api.Operation{ID: "op", Kind: "session.create", Status: status}
			_, lines, commands := m.inspectionContent()
			offered := strings.Contains(commands, "r retry") || strings.Contains(strings.Join(lines, "\n"), "Retry resumes")
			m, cmd := press(m, "r")
			if cmd != nil {
				cmd()
			}
			sent := len(client.calls) > 0
			if want := status == "blocked"; offered != want || sent != want {
				t.Fatalf("%s operation: Retry offered %v, sent %v", status, offered, sent)
			}
		})
	}
	// The page of the session's own start or stop offers Retry only when it
	// is blocked too.
	m := fixture()
	m.contextSession = m.data.sessions[1]
	m.navigate("progress")
	m.operationID = "stop"
	m.operation = api.Operation{ID: "stop", Kind: "session.stop", SessionUUID: runningID, Status: "completed"}
	if _, _, commands := m.inspectionContent(); strings.Contains(commands, "retry") {
		t.Fatalf("completed stop offers %q", commands)
	}
	m.operationID = ""
	if _, _, commands := m.inspectionContent(); strings.Contains(commands, "retry") {
		t.Fatalf("readiness page offers %q", commands)
	}
}

// TestOpenedOperationShowsItsListedFactsWhileItIsRead: while O's inspect is
// in flight, the page shows what the list read of the operation, not empty
// lines or another operation's facts.
func TestOpenedOperationShowsItsListedFactsWhileItIsRead(t *testing.T) {
	m := fixture()
	m.client = &fakeClient{fn: func(string, any) (any, error) { return nil, nil }}
	m.operation = api.Operation{ID: "other", Kind: "session.create", Key: "other-key", Status: "completed", Phase: "complete"}
	m.data.operations = []api.OperationSummary{{ID: "op", Key: "fixture-failed", Kind: "session.rename", Project: "a", SessionUUID: stoppedID, Status: "blocked", Phase: "rename", Reason: api.ReasonRuntime, Diagnostic: "Incus did not answer"}}
	m.navigate("operations")
	m, _ = press(m, "enter")
	if m.pendingMethod != api.OperationInspect.Name {
		t.Fatalf("pending %s", m.pendingMethod)
	}
	_, lines, _ := m.inspectionContent()
	text := strings.Join(lines, "\n")
	for _, want := range []string{"session.rename · blocked / rename", "Key: fixture-failed", "Session: " + stoppedID, "Diagnostic: Incus did not answer"} {
		if !strings.Contains(text, want) {
			t.Fatalf("opening page lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "other-key") {
		t.Fatalf("opening page shows the operation opened before:\n%s", text)
	}
}

// TestFollowReadErrorKeepsFollowingAndTheIntentToEnter: a read that follows
// an operation, or the session it created, and answers an error is read
// again on the next tick; the page keeps the intent to enter the session.
func TestFollowReadErrorKeepsFollowingAndTheIntentToEnter(t *testing.T) {
	m := fixture()
	m.client = &fakeClient{fn: func(string, any) (any, error) { return nil, nil }}
	updated, _ := m.Update(reply(t, m.epoch, "session.create", `{"operation":{"id":"creation","kind":"session.create","session_uuid":"`+waitingID+`","status":"completed"}}`))
	m = updated.(Model)
	updated, _ = m.Update(actionDone{epoch: m.epoch, method: api.OperationWait.Name, err: api.NewError(api.KindRetry, api.ReasonDeadline)})
	m = updated.(Model)
	if !m.attachOnComplete || m.notice == "" {
		t.Fatalf("a failed wait dropped the intent to enter: attach %v notice %q", m.attachOnComplete, m.notice)
	}
	updated, _ = m.Update(tick{})
	m = updated.(Model)
	if m.pendingMethod != api.OperationWait.Name {
		t.Fatalf("the tick did not wait again: pending %q", m.pendingMethod)
	}
	updated, _ = m.Update(reply(t, m.epoch, "operation.wait", `{"operation":{"id":"creation","kind":"session.create","session_uuid":"`+waitingID+`","status":"completed"}}`))
	m = updated.(Model)
	if m.pendingMethod != api.SessionInspect.Name {
		t.Fatalf("completion did not move on to entry: pending %q", m.pendingMethod)
	}
	// The session read before entry fails, then is read again.
	updated, _ = m.Update(actionDone{epoch: m.epoch, method: api.SessionInspect.Name, err: errors.New("transport")})
	m = updated.(Model)
	updated, _ = m.Update(tick{})
	m = updated.(Model)
	if !m.attachOnComplete || m.pendingMethod != api.SessionInspect.Name {
		t.Fatalf("a failed session read ended entry: attach %v pending %q", m.attachOnComplete, m.pendingMethod)
	}
	// An operation opened from O is read with inspect again, not waited for.
	m = fixture()
	m.client = &fakeClient{fn: func(string, any) (any, error) { return nil, nil }}
	m.data.operations = []api.OperationSummary{{ID: "op", Kind: "session.rename", Status: "blocked"}}
	m.navigate("operations")
	m, _ = press(m, "enter")
	updated, _ = m.Update(actionDone{epoch: m.epoch, method: api.OperationInspect.Name, err: errors.New("transport")})
	m = updated.(Model)
	updated, _ = m.Update(tick{})
	m = updated.(Model)
	if m.pendingMethod != api.OperationInspect.Name {
		t.Fatalf("a failed inspect was not read again: pending %q", m.pendingMethod)
	}
	// An operation that no longer exists is not read again.
	updated, _ = m.Update(actionDone{epoch: m.epoch, method: api.OperationInspect.Name, err: api.NewError(api.KindNotFound, api.ReasonOperation)})
	m = updated.(Model)
	updated, _ = m.Update(tick{})
	m = updated.(Model)
	if m.working {
		t.Fatalf("a missing operation was read again: pending %q", m.pendingMethod)
	}
	// Help keeps a follow read that failed, or was in flight, for its return.
	m = fixture()
	m.client = &fakeClient{fn: func(string, any) (any, error) { return nil, nil }}
	updated, _ = m.Update(reply(t, m.epoch, "session.create", `{"operation":{"id":"creation","kind":"session.create","session_uuid":"`+waitingID+`","status":"completed"}}`))
	m = updated.(Model)
	for _, failed := range []bool{true, false} {
		if failed {
			updated, _ = m.Update(actionDone{epoch: m.epoch, method: api.OperationWait.Name, err: errors.New("transport")})
			m = updated.(Model)
		}
		m, _ = press(m, "?")
		m, _ = press(m, "q")
		updated, _ = m.Update(tick{})
		m = updated.(Model)
		if m.page != "progress" || m.pendingMethod != api.OperationWait.Name || !m.attachOnComplete {
			t.Fatalf("Help (failed %v) lost the follow: page %s pending %q attach %v", failed, m.page, m.pendingMethod, m.attachOnComplete)
		}
	}
	// A running operation whose wait answers that it is gone is not waited for again.
	m.operation.Status = "running"
	updated, _ = m.Update(actionDone{epoch: m.epoch, method: api.OperationWait.Name, err: api.NewError(api.KindNotFound, api.ReasonOperation)})
	m = updated.(Model)
	updated, _ = m.Update(tick{})
	m = updated.(Model)
	if m.working {
		t.Fatalf("a missing running operation was waited for again: pending %q", m.pendingMethod)
	}
	// A failed change is not sent again.
	m = fixture()
	m.client = &fakeClient{fn: func(string, any) (any, error) { return nil, nil }}
	m.contextSession = m.data.sessions[0]
	m.enterSession()
	updated, _ = m.Update(actionDone{epoch: m.epoch, method: api.SessionStart.Name, err: errors.New("transport")})
	m = updated.(Model)
	updated, _ = m.Update(tick{})
	m = updated.(Model)
	if m.attachOnComplete || m.working {
		t.Fatalf("a failed start was followed: attach %v pending %q", m.attachOnComplete, m.pendingMethod)
	}
}

// TestStopOfAStoppedSessionSaysItIsAlreadyStopped: s on a session the
// browser reads stopped answers at once, as the prototype does; a stop the
// daemon answers changed:false says the same instead of "Stopped.".
func TestStopOfAStoppedSessionSaysItIsAlreadyStopped(t *testing.T) {
	m := fixture()
	m.selected = stoppedID
	m.restoreSelection()
	m, cmd := press(m, "s")
	if cmd != nil || m.page != "sessions" || m.notice != "Session is already stopped." {
		t.Fatalf("s on a stopped session: page %s notice %q", m.page, m.notice)
	}
	m = fixture()
	m.client = &fakeClient{fn: func(string, any) (any, error) { return nil, nil }}
	m.contextSession = m.data.sessions[1]
	m.navigate("confirm")
	updated, _ := m.Update(reply(t, m.epoch, "session.stop", `{"changed":false,"session":{"uuid":"`+runningID+`","project":"a","branch":"feature","session_condition":"stopped"}}`))
	m = updated.(Model)
	if m.notice != "Session is already stopped." || m.operationID != "" {
		t.Fatalf("a stop that changed nothing: notice %q op %q", m.notice, m.operationID)
	}
	if _, lines, _ := m.inspectionContent(); len(lines) < 3 || lines[2] != "Condition: stopped" {
		t.Fatalf("page %q", lines)
	}
}

// TestStaleLineWrapsAndNamesTheFailedRefresh: the STALE line says the
// refresh failed, whatever failed, and wraps between words at 48 columns.
func TestStaleLineWrapsAndNamesTheFailedRefresh(t *testing.T) {
	for _, size := range [][2]int{{120, 35}, {80, 24}, {48, 16}} {
		m := fixture()
		m.width, m.height = size[0], size[1]
		updated, _ := m.Update(loaded{err: api.NewError(api.KindInternal, "")})
		m = updated.(Model)
		frame := ansi.Strip(m.View().Content)
		flat := strings.Join(strings.Fields(frame), " ")
		if !strings.Contains(flat, "STALE · refresh failed; actions need a fresh observation") || strings.Contains(flat, "connection failed") {
			t.Errorf("%vx%v STALE line:\n%s", size[0], size[1], frame)
		}
		lines := strings.Split(frame, "\n")
		if len(lines) > m.height {
			t.Errorf("%vx%v frame has %d rows", size[0], size[1], len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > m.width {
				t.Errorf("%vx%v line %q is too wide", size[0], size[1], line)
			}
		}
	}
}

// TestUnissuedQueuedServiceActionIsItsOwnSentence: a failed service read
// says on its own that the queued action was not issued.
func TestUnissuedQueuedServiceActionIsItsOwnSentence(t *testing.T) {
	m := fixture()
	m.contextSession = m.data.sessions[1]
	m.navigate("services")
	m.working, m.pendingMethod = true, api.SessionServices.Name
	m.queuedService = &serviceIntent{runningID, "p-project-api.service", "stop"}
	updated, _ := m.Update(actionDone{epoch: m.epoch, method: api.SessionServices.Name, err: api.NewError(api.KindFailed, api.ReasonRuntime)})
	m = updated.(Model)
	if want := "The runtime failed; check Incus and try again. The queued service action was not issued."; m.notice != want {
		t.Fatalf("notice %q, want %q", m.notice, want)
	}
}

// TestLongTextPageSaysHowManyLinesAreBelow: a text page longer than its rows
// ends each screen but the last with how many lines are below, as the
// prototype ends a clipped list, so a fold inside a sentence never reads as
// the page's end.
func TestLongTextPageSaysHowManyLinesAreBelow(t *testing.T) {
	m := fixture()
	m.width, m.height = 48, 16
	m.navigate("progress")
	m.operationID = "2bd0d0d5-d157-58bb-bde4-011a6427a029"
	m.operation = api.Operation{ID: m.operationID, Kind: "session.rename", Key: "fixture-failed", Status: "blocked", Phase: "rename", Reason: api.ReasonRuntime, Diagnostic: "Incus did not answer within 30 seconds"}
	_, text, _ := m.inspectionContent()
	total, capacity := len(m.wrappedText(text)), m.capacity()
	if total <= capacity {
		t.Fatalf("the blocked page's %d lines fit its %d rows", total, capacity)
	}
	for offset := 0; offset <= total-capacity; offset++ {
		frame := strings.Split(ansi.Strip(m.View().Content), "\n")
		controls := 0
		for controls < len(frame) && !strings.HasPrefix(frame[controls], "j/k/Pg scroll") {
			controls++
		}
		if controls == 0 || controls == len(frame) {
			t.Fatalf("no controls row:\n%s", strings.Join(frame, "\n"))
		}
		last := strings.TrimSpace(frame[controls-1])
		if offset == total-capacity {
			if strings.Contains(strings.Join(frame, "\n"), "below") || last != "inputs need a new request." {
				t.Fatalf("the last screen ends with %q:\n%s", last, strings.Join(frame, "\n"))
			}
			break
		}
		if want := fmt.Sprintf("… %d more lines below", total-offset-(capacity-1)); last != want {
			t.Fatalf("screen at line %d ends with %q, want %q:\n%s", offset, last, want, strings.Join(frame, "\n"))
		}
		m, _ = press(m, "j")
	}
}

// TestEnteringAStoppedSessionReadsStarting: while Enter's start, or the read
// of the session before entry, is in flight, a session last read stopped
// reads starting, as the prototype's boot page does; once read, the page
// shows the condition read.
func TestEnteringAStoppedSessionReadsStarting(t *testing.T) {
	condition := func(m Model) string {
		_, lines, _ := m.inspectionContent()
		for _, line := range lines {
			if strings.HasPrefix(line, "Condition: ") {
				return line
			}
		}
		return ""
	}
	m := fixture()
	m.client = &fakeClient{fn: func(string, any) (any, error) { return nil, nil }}
	m.selected = stoppedID
	m.restoreSelection()
	m, _ = press(m, "enter")
	if m.pendingMethod != api.SessionStart.Name || condition(m) != "Condition: starting" {
		t.Fatalf("start in flight: pending %q, %q", m.pendingMethod, condition(m))
	}
	updated, _ := m.Update(reply(t, m.epoch, "session.start", `{"changed":true,"operation":{"id":"start","kind":"session.start","session_uuid":"`+stoppedID+`","status":"completed","phase":"ready"}}`))
	m = updated.(Model)
	if m.pendingMethod != api.SessionInspect.Name || condition(m) != "Condition: starting" {
		t.Fatalf("read before entry in flight: pending %q, %q", m.pendingMethod, condition(m))
	}
	updated, _ = m.Update(reply(t, m.epoch, "session.inspect", `{"session":{"uuid":"`+stoppedID+`","project":"a","branch":"main","session_condition":"stopped","diagnostic":"boot failed"}}`))
	m = updated.(Model)
	if condition(m) != "Condition: stopped" {
		t.Fatalf("after the read: %q", condition(m))
	}
}

// TestProgressPageDrawsOneBlankRowWithoutADetail: an operation's page, a
// start or stop's, or a session's readiness without a reason or diagnostic
// draws one blank row between its facts and its note, not an empty detail
// between two; a detail sits between two blank rows.
func TestProgressPageDrawsOneBlankRowWithoutADetail(t *testing.T) {
	for _, page := range []struct {
		name    string
		id      string
		op      api.Operation
		session api.SessionView
		blanks  int
	}{
		{"completed operation", "op", api.Operation{ID: "op", Kind: "session.rename", Status: "completed", Phase: "complete"}, api.SessionView{}, 1},
		{"completed stop", "stop", api.Operation{ID: "stop", Kind: "session.stop", SessionUUID: runningID, Status: "completed"}, api.SessionView{UUID: runningID, Project: "a", Branch: "feature", Condition: "ready"}, 1},
		{"readiness", "", api.Operation{}, api.SessionView{UUID: stoppedID, Project: "a", Branch: "main", Condition: "stopped"}, 1},
		{"blocked operation", "op", api.Operation{ID: "op", Kind: "session.rename", Status: "blocked", Reason: api.ReasonRuntime, Diagnostic: "Incus did not answer"}, api.SessionView{}, 2},
		{"readiness with a diagnostic", "", api.Operation{}, api.SessionView{UUID: stoppedID, Project: "a", Branch: "main", Condition: "stopped", Diagnostic: "boot failed"}, 2},
	} {
		t.Run(page.name, func(t *testing.T) {
			m := fixture()
			m.navigate("progress")
			m.operationID, m.operation, m.contextSession = page.id, page.op, page.session
			_, lines, _ := m.inspectionContent()
			blanks := 0
			for i, line := range lines {
				if line != "" {
					continue
				}
				blanks++
				if i == 0 || lines[i-1] == "" {
					t.Fatalf("blank row %d follows no fact:\n%s", i, strings.Join(lines, "\n"))
				}
			}
			if blanks != page.blanks {
				t.Fatalf("%d blank rows, want %d:\n%s", blanks, page.blanks, strings.Join(lines, "\n"))
			}
		})
	}
}

// TestRecoveredRefreshClearsItsFailureSentence: a refresh that succeeds after
// a failed one clears STALE and the failure's sentence with it; a notice
// written since stays.
func TestRecoveredRefreshClearsItsFailureSentence(t *testing.T) {
	m := fixture()
	failed := loaded{err: api.NewError(api.KindInternal, "")}
	updated, _ := m.Update(failed)
	m = updated.(Model)
	if !m.stale || m.notice == "" {
		t.Fatalf("failed refresh: stale %v notice %q", m.stale, m.notice)
	}
	updated, _ = m.Update(loaded{data: m.data})
	m = updated.(Model)
	if m.stale || m.notice != "" {
		t.Fatalf("recovered refresh: stale %v notice %q", m.stale, m.notice)
	}
	updated, _ = m.Update(failed)
	m = updated.(Model)
	m.notice = "Detach every terminal before reviewing removal."
	updated, _ = m.Update(loaded{data: m.data})
	m = updated.(Model)
	if m.stale || m.notice != "Detach every terminal before reviewing removal." {
		t.Fatalf("recovery after a later notice: stale %v notice %q", m.stale, m.notice)
	}
	// An action that fails with the same sentence keeps it after recovery.
	updated, _ = m.Update(failed)
	m = updated.(Model)
	updated, _ = m.Update(actionDone{epoch: m.epoch, method: api.SessionStop.Name, err: api.NewError(api.KindInternal, "")})
	m = updated.(Model)
	sentence := m.notice
	updated, _ = m.Update(loaded{data: m.data})
	m = updated.(Model)
	if m.stale || m.notice != sentence {
		t.Fatalf("recovery after an action's error: stale %v notice %q, want %q", m.stale, m.notice, sentence)
	}
}

// TestPagingALongTextPageShowsEveryLine: Page Down and Page Up move by the
// lines a screen shows above its cue, so paging through Help at 48x16 skips
// no line either way.
func TestPagingALongTextPageShowsEveryLine(t *testing.T) {
	m := fixture()
	m.width, m.height = 48, 16
	m.navigate("help")
	_, text, _ := m.inspectionContent()
	want := m.wrappedText(text)
	// shown is the text lines of the current screen, without its cue.
	shown := func(m Model) []string {
		frame := strings.Split(ansi.Strip(m.View().Content), "\n")
		rows := []string{}
		for _, row := range frame[1:] {
			if strings.HasPrefix(row, "j/k/Pg scroll") {
				break
			}
			if !strings.HasSuffix(row, "more lines below") {
				rows = append(rows, strings.TrimRight(row, " "))
			}
		}
		return rows
	}
	next := 0
	for {
		rows := shown(m)
		if m.reviewOffset > next {
			t.Fatalf("Page Down skipped lines %d to %d", next, m.reviewOffset-1)
		}
		next = m.reviewOffset + len(rows)
		if next >= len(want) {
			break
		}
		updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
		m = updated.(Model)
	}
	end := m.reviewOffset
	for m.reviewOffset > 0 {
		updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
		m = updated.(Model)
		if top := m.reviewOffset + len(shown(m)); top < end {
			t.Fatalf("Page Up skipped lines %d to %d", top, end-1)
		}
		end = m.reviewOffset
	}
}

// TestCreatedSessionIsNamedByItsCreation: once a Create completes, the page
// that waits for the new session's read names that session from the
// creation's intent, never from the session the browser showed before, and
// states no condition until the read answers.
func TestCreatedSessionIsNamedByItsCreation(t *testing.T) {
	for _, item := range []struct {
		method, intent, name string
	}{
		{"session.create", `{"project":"z","session":"` + targetID + `","branch":"new-work"}`, "z / new-work"},
		{"project.create", `{"project":"z","session":"` + targetID + `"}`, "z"},
	} {
		t.Run(item.method, func(t *testing.T) {
			m := fixture()
			m.client = &fakeClient{fn: func(string, any) (any, error) { return nil, nil }}
			m.contextSession = m.data.sessions[1]
			op := `{"operation":{"id":"creation","kind":"` + item.method + `","session_uuid":"` + targetID + `","intent":` + item.intent + `,"status":"%s"}}`
			updated, _ := m.Update(reply(t, m.epoch, item.method, fmt.Sprintf(op, "running")))
			m = updated.(Model)
			updated, _ = m.Update(reply(t, m.epoch, "operation.wait", fmt.Sprintf(op, "completed")))
			m = updated.(Model)
			if m.pendingMethod != api.SessionInspect.Name {
				t.Fatalf("completion did not read the session: pending %q", m.pendingMethod)
			}
			_, lines, _ := m.inspectionContent()
			text := strings.Join(lines, "\n")
			if lines[0] != item.name || !strings.Contains(text, "Session: "+targetID) {
				t.Fatalf("the created session is not named %q:\n%s", item.name, text)
			}
			if strings.Contains(text, "feature") || strings.Contains(text, "Condition:") {
				t.Fatalf("the page states the earlier session's facts:\n%s", text)
			}
			updated, _ = m.Update(reply(t, m.epoch, "session.inspect", `{"session":{"uuid":"`+targetID+`","project":"z","branch":"new-work","session_condition":"stopped","diagnostic":"boot failed"}}`))
			m = updated.(Model)
			if _, lines, _ = m.inspectionContent(); !strings.Contains(strings.Join(lines, "\n"), "Condition: stopped") {
				t.Fatalf("after the read:\n%s", strings.Join(lines, "\n"))
			}
		})
	}
}

// reviewRows is a review of n numbered rows.
func reviewRows(n int) string {
	rows := []string{}
	for i := 1; i <= n; i++ {
		rows = append(rows, fmt.Sprintf("row %02d", i))
	}
	return strings.Join(rows, "\n")
}

// frameRows is the frame's rows, without styling or trailing space.
func frameRows(m Model) []string {
	rows := strings.Split(ansi.Strip(m.View().Content), "\n")
	for i := range rows {
		rows[i] = strings.TrimRight(rows[i], " ")
	}
	return rows
}

// TestReviewPagesSayHowManyLinesAreBelow: a creation, confirmation or
// removal review longer than its rows ends each screen but the last with how
// many lines are below, as the inspection pages do.
func TestReviewPagesSayHowManyLinesAreBelow(t *testing.T) {
	for _, page := range []string{"creation-review", "confirm", "review"} {
		t.Run(page, func(t *testing.T) {
			m := fixture()
			m.width, m.height = 48, 16
			m.page = page
			m.review = reviewRows(30)
			capacity := m.capacity()
			rows := strings.Join(frameRows(m), "\n")
			cue := fmt.Sprintf("… %d more lines below", 30-(capacity-1))
			if !strings.Contains(rows, fmt.Sprintf("row %02d\n%s\n", capacity-1, cue)) || strings.Contains(rows, fmt.Sprintf("row %02d", capacity)) {
				t.Fatalf("the first screen does not end with %q:\n%s", cue, rows)
			}
			m, _ = press(m, "G")
			if rows = strings.Join(frameRows(m), "\n"); strings.Contains(rows, "below") || !strings.Contains(rows, "row 30") {
				t.Fatalf("the last screen:\n%s", rows)
			}
		})
	}
}

// TestPagingAReviewShowsEveryLine: Page Down and Page Up on a review move by
// the lines a screen shows above its cue, so paging skips no line.
func TestPagingAReviewShowsEveryLine(t *testing.T) {
	for _, page := range []string{"creation-review", "confirm", "review"} {
		t.Run(page, func(t *testing.T) {
			m := fixture()
			m.width, m.height = 48, 16
			m.page = page
			m.review = reviewRows(30)
			// shown is the first and last row the current screen shows.
			shown := func(m Model) (int, int) {
				first, last := 0, 0
				for _, row := range frameRows(m) {
					var n int
					if _, err := fmt.Sscanf(row, "row %d", &n); err == nil {
						if first == 0 {
							first = n
						}
						last = n
					}
				}
				return first, last
			}
			first, last := shown(m)
			for last < 30 {
				updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
				m = updated.(Model)
				next, end := shown(m)
				if next > last+1 || end <= last {
					t.Fatalf("Page Down from rows %d-%d showed rows %d-%d", first, last, next, end)
				}
				first, last = next, end
			}
			for first > 1 {
				updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
				m = updated.(Model)
				next, end := shown(m)
				if end < first-1 || next >= first {
					t.Fatalf("Page Up from rows %d-%d showed rows %d-%d", first, last, next, end)
				}
				first, last = next, end
			}
		})
	}
}

// TestLongFeedbackKeepsAReviewsControls: when feedback leaves a review one row
// for its text, the review still shows its controls and its default-No prompt.
func TestLongFeedbackKeepsAReviewsControls(t *testing.T) {
	m := fixture()
	m.width, m.height = 48, 16
	m.page = "confirm"
	m.review = reviewRows(30)
	m.notice = strings.Repeat("The daemon's answer is long. ", 12)
	if m.capacity() != 1 {
		t.Fatalf("the feedback leaves %d rows, want 1", m.capacity())
	}
	frame := strings.Join(frameRows(m), "\n")
	if !strings.Contains(frame, "row 01") || strings.Contains(frame, "row 02") || !strings.Contains(frame, "y confirm") || !strings.Contains(frame, "Confirm [y/N]") {
		t.Fatalf("the review lost its controls:\n%s", frame)
	}
}
