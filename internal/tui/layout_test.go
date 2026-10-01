package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/lgvo/p.ai/internal/control"
)

func TestBrowserLayoutBoundsInformationAndPaging(t *testing.T) {
	for _, size := range [][2]int{{120, 35}, {80, 24}, {48, 16}, {148, 40}, {120, 16}, {148, 16}} {
		m := fixture()
		m.width, m.height = size[0], size[1]
		for i := 0; i < 120; i++ {
			m.data.sessions = append(m.data.sessions, control.SessionView{UUID: fmt.Sprint(i), Project: "project-with-a-long-name", Branch: strings.Repeat("long-branch-", 9), Condition: "stopped"})
		}
		m.selectedServices = selectedServiceObservation{uuid: m.selected, state: "observed", units: []control.ProjectService{{Unit: "p-project-api.service", ActiveState: "active", SubState: "running"}}}
		frame := ansi.Strip(m.View().Content)
		if lines := strings.Split(frame, "\n"); len(lines) > m.height {
			t.Fatalf("%v overflowed height", size)
		} else {
			for _, line := range lines {
				if ansi.StringWidth(line) > m.width {
					t.Fatalf("%v overflowed width: %q", size, line)
				}
			}
		}
		for _, want := range []string{"P · Sessions", "selected 1", "Agents [A]", "Project services [S]", "D details", "PgUp/PgDn", "q/Esc back", "p policy", "b branches", "R rename", "d discard", "X delete"} {
			if !strings.Contains(frame, want) {
				t.Fatalf("%v missing %q", size, want)
			}
		}
		if size[0] >= 72 && size[1] >= 22 {
			for _, want := range []string{"UUID", "p-project-api.service", "active (running)", "Terminals:"} {
				if !strings.Contains(frame, want) {
					t.Fatalf("%v missing detail %q", size, want)
				}
			}
		}
		before := m.cursor
		capacity := m.capacity()
		m.move("pgdown")
		if m.cursor != before+capacity {
			t.Fatal("page movement disagrees with visible capacity")
		}
		if !strings.Contains(ansi.Strip(m.View().Content), fmt.Sprintf("selected %d", m.cursor+1)) {
			t.Fatal("selected page position missing")
		}
	}
}

func TestDetailsDisclosureActionsAndBack(t *testing.T) {
	m := fixture()
	m.width, m.height = 48, 16
	selected := m.selected
	m, _ = press(m, "D")
	if m.page != "details" || !strings.Contains(ansi.Strip(m.View().Content), "UUID: "+selected) {
		t.Fatal("full identity inaccessible")
	}
	for _, key := range []string{"A", "p", "S"} {
		m, _ = press(m, key)
		if m.page == "details" {
			t.Fatalf("%s disclosure action ignored", key)
		}
		m, _ = press(m, "q")
		if m.page != "details" || m.selected != selected {
			t.Fatal("nested Back lost detail context")
		}
	}
	m, _ = press(m, "q")
	if m.page != "sessions" || m.selected != selected {
		t.Fatal("detail Back lost selection")
	}
}

func TestBrowserResizeKeepsIdentityAndSelectionVisible(t *testing.T) {
	m := fixture()
	m.selected = "running"
	m.restoreSelection()
	for _, size := range [][2]int{{120, 35}, {80, 24}, {48, 16}, {120, 35}} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = next.(Model)
		if m.selected != "running" || !strings.Contains(ansi.Strip(m.View().Content), "feature") {
			t.Fatal("resize lost selected stream")
		}
	}
}

func TestSessionDetailFactsUseInjectedSemanticStyles(t *testing.T) {
	m := fixture()
	m.data.sessions[2].LatestUnattendedCondition.Source = "Codex"
	m.data.sessions[2].PolicyCondition = "current"
	m.theme.Dark.Positive = "#112233"
	m.theme.Dark.Warning = "#445566"
	m.styles = m.theme.styles(true)
	m.selectedServices = selectedServiceObservation{state: "observed", units: []control.ProjectService{{Unit: "p-project-api.service", ActiveState: "active", SubState: "running"}}}
	for _, size := range [][2]int{{120, 35}, {80, 24}, {48, 16}} {
		m.width, m.height = size[0], size[1]
		frame := m.View().Content
		for _, color := range []string{"38;2;17;34;51", "38;2;68;85;102"} {
			if !strings.Contains(frame, color) {
				t.Fatalf("%v discarded injected semantic color %s", size, color)
			}
		}
	}
	m, _ = press(m, "D")
	if !strings.Contains(m.View().Content, "38;2;17;34;51") {
		t.Fatal("detail runtime lost semantic style")
	}
	report := m.factLine("waiting-source · waiting · approval", 48)
	if !strings.HasPrefix(ansi.Strip(report), "waiting-source") || !strings.Contains(report, m.styles.warning.Render("waiting")) {
		t.Fatal("report styling changed source or condition")
	}
}
