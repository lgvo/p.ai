package tui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/lgvo/p.ai/internal/control"
)

func TestLongSessionRowsKeepRuntimeAndAttentionVisible(t *testing.T) {
	for _, width := range []int{48, 80, 120} {
		m := fixture()
		m.width, m.height = width, 24
		m.data.sessions[0].Project = strings.Repeat("long-project/", 10)
		m.data.sessions[0].Branch = strings.Repeat("branch界", 30)
		m.data.sessions[0].Condition = "ready"
		m.data.sessions[0].LatestUnattendedCondition = &control.UnattendedCondition{Condition: "attention"}
		rows := m.sessionRows()
		for _, r := range rows {
			if r.id != m.data.sessions[0].UUID {
				continue
			}
			label := m.rowLabel(r, width-6)
			if !strings.Contains(ansi.Strip(label), "running !waiting") || ansi.StringWidth(label) > width-6 {
				t.Fatalf("runtime hidden at width %d: %q", width, label)
			}
			if !strings.Contains(label, "38;2;86;211;100") || !strings.Contains(label, "38;2;227;179;65") {
				t.Fatalf("runtime and attention lost distinct colors: %q", label)
			}
		}
	}
}

func TestProjectCountsAlignAndLongNamesRemainSearchable(t *testing.T) {
	m := fixture()
	m.navigate("projects")
	long := strings.Repeat("long-project/", 8)
	m.data.projects = []control.ProjectSummary{{Path: "a"}, {Path: long}}
	m.data.sessions = []control.SessionView{{Project: "a", Condition: "ready"}, {Project: long, Condition: "stopped"}}
	for _, width := range []int{42, 74, 90} {
		column := -1
		for _, r := range m.rows() {
			label := ansi.Strip(m.rowLabel(r, width))
			if len(label) == 0 || ansi.StringWidth(label) > width {
				t.Fatalf("project counts overflow width %d: %q", width, label)
			}
			i := ansi.StringWidth(label[:strings.Index(label, "!w")])
			if column < 0 {
				column = i
			} else if i != column {
				t.Fatalf("project count columns shifted: %q", label)
			}
		}
	}
	m.choiceQuery = long
	if rows := m.rows(); len(rows) != 1 || rows[0].id != long {
		t.Fatal("bounded display changed full-name search")
	}
}

func TestCompactBrowserControlsRemainReadableAndStable(t *testing.T) {
	m := fixture()
	m.width, m.height = 48, 16
	frame := ansi.Strip(m.View().Content)
	for _, action := range []string{"/ search", "? help", "O operations", "R rename", "d discard", "X delete", "q/Esc back"} {
		if !strings.Contains(frame, action) {
			t.Fatalf("compact frame hid %s:\n%s", action, frame)
		}
	}
	before := strings.Count(frame[:strings.Index(frame, "Enter open")], "\n")
	m, _ = press(m, "j")
	m.notice = "The selected session is unavailable."
	afterFrame := ansi.Strip(m.View().Content)
	if after := strings.Count(afterFrame[:strings.Index(afterFrame, "Enter open")], "\n"); after != before {
		t.Fatal("selection/notice moved list controls")
	}
}

func TestProgressExplainsPendingSourcePreparation(t *testing.T) {
	m := fixture()
	m.navigate("progress")
	m.operationID = "operation"
	m.operation = control.Operation{Kind: "session.create", Status: "running", Phase: "branch-assigned"}
	_, lines, _ := m.inspectionContent()
	if !strings.Contains(strings.Join(lines, "\n"), "Preparing captured source and environment") {
		t.Fatal("pending creation does not explain native preparation")
	}
	m.operation.Status = "failed"
	_, lines, _ = m.inspectionContent()
	if strings.Contains(strings.Join(lines, "\n"), "pending phase") {
		t.Fatal("failed operation was presented as pending")
	}
}

func TestFinalizedInspectionHasFreshRequestGuidanceAndInertRetryKey(t *testing.T) {
	for _, kind := range []string{"workspace.inspect", "workspace.loss.inspect"} {
		t.Run(kind, func(t *testing.T) {
			client := &fakeClient{fn: func(string, params) (any, error) { t.Fatal("finalized Retry dispatched an RPC"); return nil, nil }}
			m := fixture()
			m.client = client
			m.navigate("progress")
			m.operationID = "finalized"
			m.operation = control.Operation{ID: "finalized", Kind: kind, Status: "failed", Phase: "cleaned"}
			_, lines, commands := m.inspectionContent()
			if !strings.Contains(strings.Join(lines, "\n"), "Return and request a fresh inspection") || strings.Contains(commands, "retry") {
				t.Fatalf("finalized inspection advertised Retry: %q %q", lines, commands)
			}
			var cmd tea.Cmd
			m, cmd = press(m, "r")
			if cmd != nil || m.working || len(client.calls) != 0 {
				t.Fatal("actual r key accepted finalized inspection Retry")
			}
			m, cmd = press(m, "q")
			if m.page != "sessions" || cmd != nil {
				t.Fatal("Back did not leave finalized inspection")
			}
		})
	}
	client := &fakeClient{fn: func(method string, p params) (any, error) {
		if method != "operation.retry" || p["id"] != "blocked" {
			t.Fatalf("wrong resumable request: %s %+v", method, p)
		}
		return map[string]any{"operation": control.Operation{ID: "blocked", Kind: "session.create", Status: "running"}}, nil
	}}
	m := fixture()
	m.client = client
	m.navigate("progress")
	m.operationID = "blocked"
	m.operation = control.Operation{ID: "blocked", Kind: "session.create", Status: "blocked", Phase: "branch-assigned"}
	m, cmd := press(m, "r")
	if cmd == nil || !m.working {
		t.Fatal("generic resumable blocked operation lost Retry")
	}
	cmd()
	if len(client.calls) != 1 || client.calls[0] != "operation.retry" {
		t.Fatalf("actual Retry key did not reach RPC: %v", client.calls)
	}
}

func TestInventoryRefreshClampsOpenLists(t *testing.T) {
	for _, item := range []struct{ page, query string }{{"projects", ""}, {"projects", "vnsh"}, {"operations", ""}} {
		t.Run(item.page+"/"+item.query, func(t *testing.T) {
			m := fixture()
			m.data.projects = []control.ProjectSummary{{Path: "a"}, {Path: "b"}, {Path: "vanished"}}
			m.data.operations = []control.OperationSummary{{ID: "a"}, {ID: "b"}, {ID: "c"}}
			m.navigate(item.page)
			m.choiceQuery = item.query
			m.cursor = len(m.rows()) - 1
			updated, _ := m.Update(loaded{data: inventory{projects: []control.ProjectSummary{{Path: "a"}}}})
			m = updated.(Model)
			if m.cursor != max(0, len(m.rows())-1) {
				t.Fatalf("refresh left cursor %d outside %d rows", m.cursor, len(m.rows()))
			}
			m, cmd := press(m, "enter")
			if cmd != nil {
				t.Fatal("refresh/Enter dispatched an unexpected request")
			}
			if item.page == "projects" && item.query == "" && (m.page != "sessions" || m.scope != "a") {
				t.Fatalf("Enter did not select the remaining project: page=%s scope=%s", m.page, m.scope)
			}
			if item.query != "" && (m.page != "projects" || m.scope != "") {
				t.Fatal("empty filtered inventory selected a hidden project")
			}
		})
	}
}

func TestBranchObservationClampsShrinkingChoices(t *testing.T) {
	for _, page := range []string{"create", "retained"} {
		t.Run(page, func(t *testing.T) {
			m := fixture()
			m.navigate(page)
			m.form = "branch"
			m.choices = []row{{id: "a"}, {id: "b"}, {id: "c"}, {id: "d"}}
			m.cursor = 3
			updated, _ := m.Update(actionDone{epoch: m.epoch, method: "creation.branches", raw: json.RawMessage(`{"Retained":[{"branch":"remaining","oid":"tip"}]}`)})
			m = updated.(Model)
			if m.cursor < 0 || m.cursor >= len(m.rows()) || m.rows()[m.cursor].id != "remaining" {
				t.Fatalf("branch result left invalid selection: %d %+v", m.cursor, m.rows())
			}
		})
	}
}

func TestCreationBackRejectsDelayedBranchResults(t *testing.T) {
	for _, lateError := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "error"}[lateError], func(t *testing.T) {
			f := &fakeClient{fn: func(method string, p params) (any, error) {
				project := p["project"].(string)
				switch method {
				case "project.branches":
					return map[string]any{"refs": []ref{{Ref: "refs/heads/main", OID: "tip"}}}, nil
				case "project.retained_branches":
					return map[string]any{"branches": []ref{{Branch: project + "-branch", OID: "tip"}}}, nil
				case "origin.refresh":
					return map[string]any{"origin": map[string]string{"status": "unknown"}}, nil
				default:
					return nil, errors.New("unexpected call " + method)
				}
			}}
			m := fixture()
			m.client = f
			m.data.projects = []control.ProjectSummary{{Path: "a", Registry: "active"}, {Path: "b", Registry: "active"}}
			m, _ = press(m, "c")
			m, _ = press(m, "j")
			m, first := press(m, "enter")
			if first == nil || m.form != "branch" || !m.working {
				t.Fatal("project selection did not start branch observation")
			}
			old := first().(actionDone)
			if lateError {
				old.err = errors.New("old project unavailable")
			}
			m, _ = press(m, "esc")
			if m.form != "project" || m.working || m.pendingMethod != "" || m.cursor != 0 {
				t.Fatalf("Back did not leave canceled branch request: form=%s working=%v pending=%s cursor=%d", m.form, m.working, m.pendingMethod, m.cursor)
			}
			updated, cmd := m.Update(old)
			m = updated.(Model)
			if cmd != nil || m.form != "project" || m.working || m.notice != "" || len(m.choices) != 3 || m.choices[1].id != "a" || m.choices[2].id != "b" {
				t.Fatalf("late branch result overwrote project choices: %+v", m.choices)
			}
			// Deliver after another project request starts: the stale response
			// must not overwrite its choices or clear its busy/error state.
			m, _ = press(m, "j")
			m, _ = press(m, "j")
			m, second := press(m, "enter")
			if second == nil || m.creation["project"] != "b" {
				t.Fatal("Back left a branch masquerading as a project")
			}
			updated, cmd = m.Update(old)
			m = updated.(Model)
			if cmd != nil || !m.working || m.pendingMethod != "creation.branches" || len(m.choices) != 0 || m.notice != "" {
				t.Fatalf("late result changed the new request: choices=%+v working=%v pending=%s notice=%s", m.choices, m.working, m.pendingMethod, m.notice)
			}
			updated, _ = m.Update(second())
			m = updated.(Model)
			m, _ = press(m, "j")
			m, _ = press(m, "enter")
			if m.page != "confirm" || m.confirmParams["project"] != "b" || m.confirmParams["branch"] != "b-branch" {
				t.Fatalf("new project was not kept bound: %+v", m.confirmParams)
			}
		})
	}
}

func TestHelpRestartsInterruptedBranchRead(t *testing.T) {
	for _, page := range []string{"create", "retained"} {
		for _, failed := range []bool{false, true} {
			t.Run(page+"/"+map[bool]string{false: "success", true: "error"}[failed], func(t *testing.T) {
				f := &fakeClient{fn: func(method string, p params) (any, error) {
					if p["project"] != "app" {
						t.Fatalf("read changed project: %+v", p)
					}
					switch method {
					case "project.branches":
						return map[string]any{}, nil
					case "project.retained_branches":
						return map[string]any{"branches": []ref{{Branch: "retained", OID: "tip"}}}, nil
					case "origin.refresh":
						return map[string]any{"origin": map[string]string{"status": "unknown"}}, nil
					default:
						t.Fatal("unexpected method " + method)
						return nil, nil
					}
				}}
				m := fixture()
				m.client = f
				m.data.projects = []control.ProjectSummary{{Path: "app", Registry: "active"}}
				m.data.sessions[2].Project = "app"
				// Open through actual keys with a response still outstanding.
				if page == "create" {
					m, _ = press(m, "c")
					m, _ = press(m, "j")
				}
				key := "b"
				if page == "create" {
					key = "enter"
				}
				m, cmd := press(m, key)
				if cmd == nil {
					t.Fatal("branch read did not start")
				}
				old := cmd().(actionDone)
				old.raw = json.RawMessage(`{"Retained":[{"branch":"obsolete","oid":"old"}]}`)
				m, _ = press(m, "?")
				if m.page != "help" {
					t.Fatal("Help did not open")
				}
				m, restarted := press(m, "esc")
				if m.page != page || restarted == nil || !m.working || m.pendingMethod != "creation.branches" {
					t.Fatalf("Help return did not restart branch loading: page=%s working=%v pending=%s", m.page, m.working, m.pendingMethod)
				}
				updated, next := m.Update(old)
				m = updated.(Model)
				if next != nil || !m.working || len(m.choices) != 0 {
					t.Fatal("old read interfered with restarted branch loading")
				}
				result := restarted().(actionDone)
				if failed {
					result.err = errors.New("branches unavailable")
				}
				updated, _ = m.Update(result)
				m = updated.(Model)
				if m.working || m.pendingMethod != "" {
					t.Fatal("restarted read remained pending")
				}
				if failed {
					if m.notice != "branches unavailable" || len(m.choices) != 0 {
						t.Fatal("restarted read error was not surfaced")
					}
					m, _ = press(m, "esc")
					if m.page == "help" || page == "create" && m.form != "project" {
						t.Fatal("failed branch read prevented Back")
					}
				} else {
					found := false
					for _, choice := range m.choices {
						found = found || choice.id == "retained"
					}
					if !found {
						t.Fatal("fresh branch choices were not loaded")
					}
					m, _ = press(m, "?")
					m, next = press(m, "esc")
					if next != nil || m.working {
						t.Fatal("Help restarted an already completed read")
					}
				}
			})
		}
	}
}

func TestHelpReturnDoesNotReplayMutation(t *testing.T) {
	m := fixture()
	m.navigate("progress")
	m.begin("session.create", params{"project": "app"})
	m, _ = press(m, "?")
	m, cmd := press(m, "esc")
	if cmd != nil || m.page != "progress" || m.working || m.pendingMethod != "" {
		t.Fatal("Help return replayed a mutating request")
	}
}

func TestRepeatedHelpKeepsInterruptedReadContext(t *testing.T) {
	m := fixture()
	m.creation = params{"project": "app"}
	m.navigate("create")
	m.form = "branch"
	m.begin("creation.branches", params{"project": "app"})
	m, _ = press(m, "?")
	m, _ = press(m, "?")
	m, cmd := press(m, "esc")
	if cmd == nil || m.page != "create" || m.form != "branch" || !m.working {
		t.Fatal("repeated Help forgot its caller or interrupted read")
	}
}

func TestServiceStatesKeepActionsTruthfulInCompactView(t *testing.T) {
	m := fixture()
	m.width, m.height = 48, 16
	m.navigate("services")
	m.servicesFresh = true
	m.services = []control.ProjectService{{Unit: "p-project-notes-worker.service", ActiveState: "unknown", SubState: "not-loaded"}}
	frame := ansi.Strip(m.View().Content)
	if !strings.Contains(frame, "installed") || !strings.Contains(frame, "s start") || strings.Contains(frame, "s start/stop") {
		t.Fatalf("unloaded installed service is ambiguous: %s", frame)
	}
	m.services[0].ActiveState, m.services[0].SubState = "active", "running"
	if frame = ansi.Strip(m.View().Content); !strings.Contains(frame, "s stop") {
		t.Fatal("active service does not offer Stop")
	}
	m.servicesFresh = false
	if frame = ansi.Strip(m.View().Content); strings.Contains(frame, "s stop") || !strings.Contains(frame, "Service observation unavailable/loading") {
		t.Fatal("unavailable inventory offers active controls")
	}
}

func TestAttachmentReturnSelectsNewCreatedSession(t *testing.T) {
	m := fixture()
	m.page = "progress"
	m.selected = m.data.sessions[0].UUID
	m.contextSession = m.data.sessions[len(m.data.sessions)-1]
	updated, _ := m.Update(attached{})
	m = updated.(Model)
	if m.selected != m.contextSession.UUID {
		t.Fatal("detach returned to a different session")
	}
}

func TestConfirmationAndUUIDRemainReadable(t *testing.T) {
	m := fixture()
	m.navigate("confirm")
	m.review = "Proceed?"
	if frame := m.View().Content; !strings.Contains(frame, "38;2;227;179;65") || !strings.Contains(ansi.Strip(frame), "Confirm [y/N]") {
		t.Fatal("confirmation lost its caution style")
	}
	m.navigate("sessions")
	m.width, m.height = 120, 35
	m.data.sessions[0].UUID = "26d77503-7577-44a1-9315-2ce82e54a52b"
	m.selected = m.data.sessions[0].UUID
	m.restoreSelection()
	if frame := ansi.Strip(m.View().Content); !strings.Contains(frame, m.selected) {
		t.Fatal("wide pane split UUID despite available width")
	}
}

func TestJournalBackRetainsReviewedService(t *testing.T) {
	m := fixture()
	m.services = []control.ProjectService{{Unit: "p-project-notes-db.service"}, {Unit: "p-project-notes-worker.service"}}
	m.servicesFresh = true
	m.serviceUnit = m.services[1].Unit
	m.navigate("journal")
	m, cmd := press(m, "q")
	if cmd == nil || m.page != "services" || m.cursor != 1 {
		t.Fatal("journal Back selected a different service")
	}
	m, _ = press(m, "s")
	if m.queuedService == nil || m.queuedService.Unit != m.serviceUnit {
		t.Fatal("action after journal Back did not capture the reviewed unit")
	}
}

func TestServiceHelpBackRetainsUnitAfterInventoryReorder(t *testing.T) {
	m := fixture()
	m.services = []control.ProjectService{{Unit: "p-project-notes-db.service"}, {Unit: "p-project-notes-worker.service"}}
	m.servicesFresh = true
	m.navigate("services")
	m.cursor = 1
	m, _ = press(m, "?")
	m.services[0], m.services[1] = m.services[1], m.services[0]
	m, _ = press(m, "q")
	if m.page != "services" || m.services[m.cursor].Unit != "p-project-notes-worker.service" {
		t.Fatal("Help Back selected a different service")
	}
}

func TestCapacityFeedbackKeepsRemedyAndConfirmationControls(t *testing.T) {
	for _, size := range [][2]int{{120, 35}, {80, 24}, {48, 16}} {
		m := fixture()
		m.width, m.height = size[0], size[1]
		m.navigate("confirm")
		m.review = strings.Repeat("Captured project, branch, source and policy.\n", 12)
		m.notice = "busy: session capacity exhausted; P keeps one slot for loss inspection. Discard or Delete a session to free capacity; Stop retains its slot"
		frame := ansi.Strip(m.View().Content)
		flat := strings.ReplaceAll(frame, "\n", "")
		for _, text := range []string{"Discard or Delete", "Stop retains its slot", "Confirm [y/N]", "q/Esc back"} {
			if !strings.Contains(flat, text) {
				t.Fatalf("%vx%v feedback or controls clipped: %q missing in %s", size[0], size[1], text, frame)
			}
		}
		lines := strings.Split(frame, "\n")
		if len(lines) > m.height {
			t.Fatal("feedback escaped the viewport")
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > m.width {
				t.Fatal("feedback escaped terminal width")
			}
		}
	}
}
