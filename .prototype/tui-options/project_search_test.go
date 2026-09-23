package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"testing"
)

func TestProjectFilterFuzzySearch(t *testing.T) {
	m, _ := newAppForDataset("portfolio")
	m.browser, m.variant = true, variantTopology
	m = pressRune(t, m, 'P')
	bottom := strings.Count(strings.SplitN(m.View(), "╰", 2)[0], "\n")
	m = pressRune(t, m, '/')
	for _, r := range "fge" {
		m = pressRune(t, m, r)
	}
	if !strings.Contains(m.View(), "fuzzy search> fge") || len(m.projectMatches()) != 1 || strings.Count(strings.SplitN(m.View(), "╰", 2)[0], "\n") != bottom {
		t.Fatal("project fuzzy search failed or resized panel")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenProjectFilter || m.projectSearching {
		t.Fatal("Enter should first apply query")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenOverview || m.topologyProject != "forge" {
		t.Fatal("filtered index selected wrong project")
	}
	m = pressRune(t, m, 'P')
	m = pressRune(t, m, '/')
	for _, r := range "zzzzzz" {
		m = pressRune(t, m, r)
	}
	if len(m.projectMatches()) != 0 || !strings.Contains(m.View(), "No matching projects.") {
		t.Fatal("missing empty state")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenProjectFilter {
		t.Fatal("empty result applied a project")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.projectSearch.Value() != "" || m.screen != screenProjectFilter {
		t.Fatal("Esc must clear search first")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyHome})
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.topologyProject != "" {
		t.Fatal("All projects did not clear scope")
	}
}

func TestProjectSelectionStateCounts(t *testing.T) {
	m, _ := newAppForDataset("portfolio")
	m.browser, m.variant = true, variantTopology
	m = pressRune(t, m, 'P')
	for _, want := range []string{"total 120", "!waiting 2", "running 6", "stopped 112"} {
		if !strings.Contains(strings.Join(strings.Fields(m.View()), " "), want) {
			t.Fatalf("missing total %s", want)
		}
	}
	m = pressRune(t, m, '/')
	for _, r := range "forge" {
		m = pressRune(t, m, r)
	}
	for _, want := range []string{"total 5", "!waiting 1", "running 3", "stopped 1"} {
		if !strings.Contains(strings.Join(strings.Fields(m.View()), " "), want) {
			t.Fatalf("missing project count %s", want)
		}
	}
	m.sessions[0].Lifecycle = "stopping"
	if m.projectStateCounts("forge")["stopping"] != 1 || m.projectStateCounts("forge")["total"] != 5 {
		t.Fatal("transitional session omitted from total")
	}
}

func TestProjectWorkloadOrderAndRows(t *testing.T) {
	m, _ := newAppForDataset("portfolio")
	projects := m.orderedFilterProjects()
	for i, want := range []string{"All projects", "forge", "orbit", "p.ai"} {
		if projects[i] != want {
			t.Fatalf("wrong workload order: %v", projects)
		}
	}
	m.sessions = []session{
		{Project: "a", Lifecycle: "stopped"}, {Project: "b", Lifecycle: "stopped"},
		{Project: "b", Lifecycle: "stopped"}, {Project: "c", Lifecycle: "ready"},
	}
	m.branches = nil
	projects = m.orderedFilterProjects()
	if strings.Join(projects, ",") != "All projects,c,b,a" {
		t.Fatalf("running/total tie breaks: %v", projects)
	}
	items := m.projectFilterItems(110)
	for _, item := range items {
		if !strings.Contains(item.text, "* total") || strings.Contains(item.text, "\n") {
			t.Fatal("counts must share the project row")
		}
	}
}
