package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

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
	// reviewToken is a removal review's confirmation token.
	reviewToken = "0123456789abcdef0123456789abcdef"
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

func TestRemovalRequiresPreviewAndExplicitYes(t *testing.T) {
	m := fixture()
	m.contextSession = m.data.sessions[0]
	m.removal = &removalIntent{UUID: stoppedID, Kind: "discard"}
	m.page = "progress"
	m.client = &fakeClient{fn: func(method string, p any) (any, error) {
		t.Fatalf("unexpected command execution %s", method)
		return nil, nil
	}}
	updated, cmd := m.Update(reply(t, m.epoch, "session.removal.preview", `{"preview":{"session_uuid":"`+stoppedID+`","kind":"discard","confirmation_token":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","runtime":{"condition":"present","runtime_data_lost":true}}}`))
	m = updated.(Model)
	if m.page != "review" || cmd != nil || requestOf[api.SessionDiscardRequest](t, m.confirm).UUID != stoppedID {
		t.Fatal("preview skipped review or retargeted")
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
			t.Fatal("wrong UUID triggered removal preview")
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
	m.contextSession = api.SessionView{UUID: runningID, Project: "app", Branch: "main", Diagnostic: strings.Repeat("detail/", 100) + "DIAGNOSTIC_END", LatestUnattendedCondition: &api.UnattendedCondition{Reason: strings.Repeat("reason/", 100) + "REPORT_END"}, Environment: &api.EnvironmentView{ImageFingerprint: strings.Repeat("image/", 100) + "IMAGE_END"}}
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
