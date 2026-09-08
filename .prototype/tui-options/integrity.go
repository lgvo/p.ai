package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m app) integrityView(width, _ int) string {
	current, partial, stale := 0, 0, 0
	for _, s := range m.sessions {
		switch s.Observation {
		case "partial":
			partial++
		case "stale":
			stale++
		default:
			current++
		}
	}
	summary := fmt.Sprintf("CURRENT %d  ·  PARTIAL %d  ·  STALE %d", current, partial, stale)
	heading := titleStyle.Render("Observation integrity") + "\n" +
		mutedStyle.Render("Completeness and freshness are first-class; derived urgency never hides evidence quality.")
	if width >= 110 {
		leftWidth := width * 54 / 100
		queue := heading + "\n\n" + summary + "\n\n" + m.integrityRows(7)
		return lipgloss.JoinHorizontal(lipgloss.Top, renderPanel(leftWidth, queue), " ", renderPanel(width-leftWidth-1, m.integritySelection()))
	}
	content := heading + "\n\n" + summary + "\n\n" + m.integrityRows(4) + "\n\n" + m.integritySelection()
	return renderPanel(width, content)
}

func (m app) integrityRows(limit int) string {
	visible := m.visibleIndices()
	if len(visible) == 0 {
		return mutedStyle.Render("No observations are available.")
	}
	position := make(map[int]int, len(visible))
	for pos, idx := range visible {
		position[idx] = pos
	}
	ordered := make([]int, 0, len(visible))
	for _, quality := range []string{"partial", "stale", "current"} {
		for _, idx := range visible {
			if observationQuality(m.sessions[idx]) == strings.ToUpper(quality) {
				ordered = append(ordered, idx)
			}
		}
	}
	rows := make([]string, 0, limit+1)
	start, end := m.sessionWindow(ordered, limit)
	if len(ordered) > limit {
		rows = append(rows, mutedStyle.Render(fmt.Sprintf("  %d–%d of %d", start+1, end, len(ordered))))
	}
	for row := start; row < end; row++ {
		idx := ordered[row]
		s := m.sessions[idx]
		presence, _ := sessionSignals(s)
		line := fmt.Sprintf("  %s %s %s %s", padRight(observationQuality(s), 10), padRight(s.ID, 16), padRight(s.Lifecycle, 11), presence)
		rows = append(rows, m.selectableLine(position[idx], line, s))
	}
	return strings.Join(rows, "\n")
}

func (m app) integritySelection() string {
	idx, ok := m.selectedSessionIndex()
	if !ok {
		return titleStyle.Render("Selected evidence") + "\n" + mutedStyle.Render("No observation is selected.")
	}
	s := m.sessions[idx]
	presence, agent := sessionSignals(s)
	lines := []string{
		titleStyle.Render("Selected evidence · " + observationQuality(s)),
		truncate(s.Project+" / "+s.Branch, 50),
		"UUID " + s.ID,
		strings.Join([]string{s.Lifecycle, presence, agent, s.Policy}, " · "),
		"actions  " + strings.Join(availableActions(s), " · "),
	}
	if s.Operation != "" {
		lines = append(lines, "progress "+s.Operation)
	}
	return strings.Join(lines, "\n")
}

func observationQuality(s session) string {
	if warning := observationWarning(s); warning != "" {
		if strings.HasPrefix(warning, "STALE") {
			return "STALE"
		}
		return warning
	}
	return "CURRENT"
}
