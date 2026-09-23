package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"testing"
)

func TestPortfolioInventory(t *testing.T) {
	m, err := newAppForDataset("portfolio")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.projects()) != 24 || len(m.sessions) != 120 || len(m.branches) != 124 {
		t.Fatal("unexpected portfolio size")
	}
	seen := map[string]bool{}
	for _, s := range m.sessions {
		if seen[s.ID] || len(s.ID) != 36 {
			t.Fatal("invalid/duplicate fixture UUID")
		}
		seen[s.ID] = true
	}
}

func TestPortfolioWorkloadContext(t *testing.T) {
	active := map[string]int{}
	assigned := map[string]bool{}
	branchNames := map[string]bool{}
	for _, s := range portfolioSessions() {
		key := s.Project + "/" + s.Branch
		if assigned[key] || branchNames[s.Branch] {
			t.Fatalf("repeated session branch: %s", key)
		}
		assigned[key], branchNames[s.Branch] = true, true
		if s.Lifecycle == "ready" {
			active[s.Project]++
			if len(s.Agents) == 0 {
				t.Fatalf("active work missing agents: %s", key)
			}
		} else if len(s.Agents) != 0 {
			t.Fatalf("stopped session has active agents: %s", key)
		}
	}
	if len(active) != 3 || active["forge"] != 4 || active["orbit"] != 2 || active["p.ai"] != 2 {
		t.Fatalf("active work should share current projects: %v", active)
	}
	for _, b := range portfolioBranches() {
		key := b.Project + "/" + b.Name
		if assigned[key] {
			t.Fatalf("duplicate or assigned retained branch: %s", key)
		}
		assigned[key] = true
	}
}

func TestPagingUsesRenderedCapacity(t *testing.T) {
	m, _ := newAppForDataset("portfolio")
	m.browser, m.variant = true, variantTopology
	capacity := m.browserSessionCapacity()
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	if m.selected != capacity {
		t.Fatal("session page movement differs from capacity")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnd})
	if m.selected != len(m.sessions)-1 {
		t.Fatal("End did not reach last session")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	if m.selected != len(m.sessions)-1 {
		t.Fatal("page movement wrapped unexpectedly")
	}
	m = pressRune(t, m, 'c')
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyHome})
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	if m.createChoice != m.creationCapacity() {
		t.Fatal("project page movement differs from capacity")
	}
	m = chooseCreationOption(t, m, "forge")
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnd})
	if m.creationOptions()[m.createChoice] != newBranchOption {
		t.Fatal("branch End missed creation action")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	if m.createChoice != m.creationCapacity() {
		t.Fatal("source page movement differs from capacity")
	}
}

func TestPortfolioResponsiveSelectors(t *testing.T) {
	for _, size := range [][2]int{{48, 16}, {60, 20}, {80, 24}, {120, 35}, {160, 50}} {
		for _, place := range []string{"sessions", "project", "branch", "source", "project-filter"} {
			m, _ := newAppForDataset("portfolio")
			m.browser, m.variant = true, variantTopology
			if place == "project-filter" {
				m = pressRune(t, m, 'P')
			} else if place != "sessions" {
				m.startCreate()
				if place != "project" {
					m = chooseCreationOption(t, m, "forge")
				}
				if place == "source" {
					m = chooseCreationOption(t, m, newBranchOption)
				}
			}
			m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnd})
			selected, choice := m.selected, m.createChoice
			updated, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m = updated.(app)
			if selected != m.selected || choice != m.createChoice {
				t.Fatal("resize changed selection")
			}
			view := m.View()
			if strings.Contains(view, "frame clipped") {
				t.Fatalf("%s at %dx%d clipped:\n%s", place, size[0], size[1], view)
			}
			if !strings.Contains(view, "Ctrl-C") {
				t.Fatalf("%s lost commands at %v", place, size)
			}
			if place == "project" || place == "branch" || place == "source" {
				if !strings.Contains(view, "› ") {
					t.Fatalf("%s lost selection at %v", place, size)
				}
				bottom := panelBottomRow(t, m)
				m = searchCreation(t, m, "zzzzzz")
				if panelBottomRow(t, m) != bottom {
					t.Fatal("search resized responsive selector")
				}
			}
		}
	}
}
