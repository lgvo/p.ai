package tui

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
)

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
