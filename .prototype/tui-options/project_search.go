package main

import (
	"fmt"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"sort"
	"strings"
)

func (m app) projectMatches() []int {
	indices := newSelector(stringItems(m.orderedFilterProjects()), m.projectSearch.Value(), 0, 1, 1).indices()
	sort.Ints(indices) // Retain workload ordering among fuzzy matches.
	return indices
}

func (m app) orderedFilterProjects() []string {
	projects := m.projects()
	counts := map[string]map[string]int{}
	for _, project := range projects {
		counts[project] = m.projectStateCounts(project)
	}
	sort.SliceStable(projects, func(i, j int) bool {
		a, b := counts[projects[i]], counts[projects[j]]
		for _, state := range []string{"!waiting", "running", "total"} {
			if a[state] != b[state] {
				return a[state] > b[state]
			}
		}
		return projects[i] < projects[j]
	})
	return append([]string{"All projects"}, projects...)
}

func (m app) projectFilterItems(width int) []selectorItem {
	projects := m.orderedFilterProjects()
	items := []selectorItem{}
	totals := m.projectStateCounts("")
	for _, idx := range m.projectMatches() {
		name, project := projects[idx], projects[idx]
		if idx == 0 {
			project = ""
		}
		counts := m.projectStateCounts(project)
		summary := projectRowCounts(counts, totals, false)
		if lipgloss.Width(summary)+16 > width {
			summary = projectRowCounts(counts, totals, true)
		}
		nameWidth := max(1, min(projectNameMaxWidth, width-2-lipgloss.Width(summary)-1))
		row := "  " + padRight(truncate(name, nameWidth), nameWidth) + " " + summary
		items = append(items, selectorItem{index: idx, text: row, search: name})
	}
	return items
}

func projectRowCounts(counts, totals map[string]int, compact bool) string {
	labels := []string{"!waiting", "running", "stopped", "total"}
	if compact {
		labels = []string{"!w", "r", "s", "t"}
	}
	colors := []lipgloss.TerminalColor{warning, positive, muted, ink}
	parts := []string{}
	for i, state := range []string{"!waiting", "running", "stopped", "total"} {
		parts = append(parts, lipgloss.NewStyle().Foreground(colors[i]).Render(fmt.Sprintf("%s %*d", labels[i], len(fmt.Sprint(totals[state])), counts[state])))
	}
	return strings.Join(parts[:3], " · ") + " * " + parts[3]
}

func (m app) updateProjectFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	matches := m.projectMatches()
	if m.projectSearching {
		switch key {
		case "enter":
			m.projectSearching = false
			m.projectSearch.Blur()
			return m, nil
		case "up", "down":
			m.projectChoice, _ = selectorNavigation(key, m.projectChoice, len(matches), m.projectCapacity())
			return m, nil
		default:
			var cmd tea.Cmd
			m.projectSearch, cmd = m.projectSearch.Update(msg)
			m.projectChoice = 0
			return m, cmd
		}
	}
	if key == "/" {
		m.projectSearching = true
		m.projectSearch.Focus()
		return m, textinput.Blink
	}
	if selected, handled := selectorNavigation(key, m.projectChoice, len(matches), m.projectCapacity()); handled {
		m.projectChoice = selected
		return m, nil
	}
	if key == "enter" && len(matches) > 0 {
		selectedID := ""
		if idx, ok := m.selectedSessionIndex(); ok {
			selectedID = m.sessions[idx].ID
		}
		projects := m.orderedFilterProjects()
		m.topologyProject = projects[matches[min(m.projectChoice, len(matches)-1)]]
		if m.topologyProject == "All projects" {
			m.topologyProject = ""
		}
		m.selected = 0
		m.selectSession(selectedID)
		m.screen = screenOverview
	}
	return m, nil
}

// Waiting is separated from other running sessions, so counts sum to the total.
func (m app) projectStateCounts(project string) map[string]int {
	counts := map[string]int{}
	total := 0
	for _, s := range m.sessions {
		if project != "" && s.Project != project {
			continue
		}
		total++
		state := s.Lifecycle
		if state == "ready" {
			state = "running"
			if sessionFlag(s) == "waiting" {
				state = "!waiting"
			}
		}
		counts[state]++
	}
	counts["total"] = total
	return counts
}
