package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestVimKeysAndContextIsolation(t *testing.T) {
	m, _ := newAppForDataset("portfolio")
	m.browser, m.variant = true, variantTopology
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyHome})
	capacity := m.browserSessionCapacity()
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlF})
	if m.selected != capacity {
		t.Fatal("Ctrl+F did not page")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
	if m.selected != capacity-max(1, capacity/2) {
		t.Fatal("Ctrl+U did not half-page")
	}
	m = pressRune(t, m, 'G')
	if m.selected != 119 {
		t.Fatal("G missed last session")
	}
	m = pressRune(t, m, 'g')
	if m.selected != 119 {
		t.Fatal("single g moved selection")
	}
	m = pressRune(t, m, 'g')
	if m.selected != 0 {
		t.Fatal("gg missed first session")
	}
	m = pressRune(t, m, 'g')
	m = pressRune(t, m, 'j')
	m = pressRune(t, m, 'g')
	if m.selected != 1 {
		t.Fatal("intervening key did not cancel gg sequence")
	}
	m = pressRune(t, m, '/')
	m = pressRune(t, m, 'g')
	m = pressRune(t, m, 'G')
	if m.filter.Value() != "gG" {
		t.Fatal("Vim keys intercepted search text")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m = pressRune(t, m, 'c')
	m = chooseCreationOption(t, m, "forge")
	m = chooseCreationOption(t, m, newBranchOption)
	m = chooseCreationOption(t, m, "main")
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("feature/ggG")})
	if m.createName.Value() != "feature/ggG" {
		t.Fatal("Vim keys intercepted name input")
	}
	m = newApp()
	m.browser, m.variant = true, variantTopology
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlB})
	if m.screen != screenTerminal || !m.terminalPrefix {
		t.Fatal("Ctrl+B no longer opens terminal popup")
	}
}

func TestCreationAndProjectVimNavigation(t *testing.T) {
	m, _ := newAppForDataset("portfolio")
	m.browser = true
	m.startCreate()
	m = pressRune(t, m, 'G')
	if m.createChoice != len(m.projects())-1 {
		t.Fatal("G missed last project")
	}
	m = pressRune(t, m, 'g')
	m = pressRune(t, m, 'g')
	if m.createChoice != 0 {
		t.Fatal("gg missed first project")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlD})
	if m.createChoice != max(1, m.creationCapacity()/2) {
		t.Fatal("creation half-page failed")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m.variant = variantTopology
	m = pressRune(t, m, 'P')
	m = pressRune(t, m, 'G')
	if m.projectChoice != len(m.projects()) {
		t.Fatal("project filter G failed")
	}
}

func TestReducedPortfolioAndGroupedOrders(t *testing.T) {
	m, _ := newAppForDataset("portfolio")
	m.browser, m.variant = true, variantTopology
	running, stopped, waiting := 0, 0, 0
	for _, s := range m.sessions {
		if s.Lifecycle == "ready" {
			running++
		}
		if s.Lifecycle == "stopped" {
			stopped++
		}
		if sessionFlag(s) == "waiting" {
			waiting++
		}
	}
	if running != 8 || stopped != 112 || waiting != 2 {
		t.Fatalf("unexpected mix %d/%d/%d", running, stopped, waiting)
	}
	indices := m.visibleIndices()
	previousRank := -1
	project := ""
	seen := map[string]bool{}
	for _, idx := range indices {
		current := m.sessions[idx]
		rank := sessionPriority(current)
		if rank < previousRank {
			t.Fatal("lower priority session appeared before waiting/running")
		}
		if rank != previousRank {
			seen = map[string]bool{}
			project = ""
		}
		if current.Project != project {
			if seen[current.Project] {
				t.Fatal("project group split within priority band")
			}
			seen[current.Project] = true
			project = current.Project
		}
		previousRank = rank
	}
	if sessionPriority(m.sessions[indices[0]]) != 0 || sessionPriority(m.sessions[indices[2]]) != 1 || sessionPriority(m.sessions[indices[8]]) != 2 {
		t.Fatal("incorrect priority boundaries")
	}

	m.filter.SetValue("orbit")
	m.selected = 0
	before := m.visibleIndices()
	allowed := map[int]bool{}
	for _, idx := range before {
		allowed[idx] = true
	}
	m.topologyProject = ""
	after := m.visibleIndices()
	if len(after) != len(before) {
		t.Fatal("sorting changed fuzzy result count")
	}
	for _, idx := range after {
		if !allowed[idx] {
			t.Fatal("sorting changed fuzzy result membership")
		}
	}
	id := m.sessions[m.visibleIndices()[0]].ID
	m.recordInteraction(id)
	if m.sessions[m.visibleIndices()[m.selected]].ID != id {
		t.Fatal("recent interaction lost selection")
	}
}

func TestProjectNeverOutranksWaitingFromAnotherProject(t *testing.T) {
	m := newApp()
	m.browser = true
	waiting := m.sessions[0]
	waiting.Project = "zeta"
	running := waiting
	running.ID = "running"
	running.Project = "alpha"
	running.Agents = nil
	running.Agent = ""
	stopped := running
	stopped.ID = "stopped"
	stopped.Lifecycle = "stopped"
	m.sessions = []session{stopped, running, waiting}
	indices := m.visibleIndices()
	if indices[0] != 2 || indices[1] != 1 || indices[2] != 0 {
		t.Fatalf("project grouping overrode global priority: %v", indices)
	}
	m.topologyProject = "alpha"
	m.variant = variantTopology
	indices = m.visibleIndices()
	if len(indices) != 2 || indices[0] != 1 || indices[1] != 0 {
		t.Fatal("priority failed under project filter")
	}
}
