package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m *app) previewRecoveryPlan() {
	idx, ok := m.selectedSessionIndex()
	if !ok {
		m.message = "No session is available for a recovery plan."
		return
	}
	s := m.sessions[idx]
	switch s.Lifecycle {
	case "missing", "unreachable":
		m.message = fmt.Sprintf("Bounded recovery plan for %s: inspect runtime → reconcile registry → retry only the authorized action.", s.ID)
	case "stopped":
		m.message = fmt.Sprintf("Bounded restart plan for %s: validate policy → start → inspect readiness.", s.ID)
	default:
		m.message = fmt.Sprintf("No recovery is currently eligible for %s; inspect remains available.", s.ID)
	}
}

func (m app) operationsView(width, _ int) string {
	indices := m.visibleIndices()
	active, failed, attached := 0, 0, 0
	for _, idx := range indices {
		s := m.sessions[idx]
		if s.Operation != "" {
			active++
		}
		if s.Lifecycle == "missing" || s.Lifecycle == "unreachable" || s.Agent == "failed" {
			failed++
		}
		attached += s.AttachedCount
	}
	observationWidth := 46
	if width >= 110 {
		observationWidth = 40
	}
	timeline := titleStyle.Render("Operations / recovery console") +
		fmt.Sprintf("  ·  activity %d  ·  degraded %d  ·  attached %d", active, failed, attached) + "\n" +
		mutedStyle.Render("Newest fixture observations first; recovery never implies hidden automatic mutation.") + "\n\n" +
		m.operationRows(6, observationWidth)
	if width >= 110 {
		leftWidth := width * 61 / 100
		actions := m.detailView() + "\n\n" + titleStyle.Render("Recovery contract") +
			"\nr previews a bounded plan\nretry preserves request identity\npartial deletion only converges toward absent"
		return lipgloss.JoinHorizontal(lipgloss.Top, renderPanel(leftWidth, timeline), " ", renderPanel(width-leftWidth-1, actions))
	}
	return renderPanel(width, timeline+"\n\n"+m.operationsCompactDetail())
}

func (m app) operationRows(limit, observationWidth int) string {
	indices := m.visibleIndices()
	rows := []string{mutedStyle.Render("  TIME      SESSION        OBSERVATION")}
	shown := 0
	for offset := len(indices) - 1; offset >= 0 && shown < limit; offset-- {
		s := m.sessions[indices[offset]]
		observation := s.Operation
		if observation == "" {
			observation = fmt.Sprintf("stable %s · %s", s.Lifecycle, s.Policy)
		}
		line := fmt.Sprintf("  20:%02d:%02d  %-13s %s", 42-shown, shown*7, truncate(s.ID, 13), truncate(observation, observationWidth))
		if indices[offset] == selectedSessionIndexOr(m, -1) {
			line = selectedStyle.Render("› " + strings.TrimPrefix(line, "  "))
		}
		rows = append(rows, line)
		shown++
	}
	if shown == 0 {
		rows = append(rows, mutedStyle.Render("No operations or stable observations in this fixture."))
	}
	return strings.Join(rows, "\n")
}

func (m app) operationsCompactDetail() string {
	idx, ok := m.selectedSessionIndex()
	if !ok {
		return mutedStyle.Render("No session selected")
	}
	s := m.sessions[idx]
	presence, agent := sessionSignals(s)
	result := titleStyle.Render("Selected operation context") + "  " + truncate(s.Project+" / "+s.Branch, 34) +
		"\n" + strings.Join([]string{s.Lifecycle, presence, agent, s.Policy}, " · ") +
		"\nactions  " + strings.Join(availableActions(s), " · ") + " · r recovery plan"
	if s.Operation != "" {
		result += "\nprogress " + s.Operation
	}
	return result
}

func selectedSessionIndexOr(m app, fallback int) int {
	idx, ok := m.selectedSessionIndex()
	if !ok {
		return fallback
	}
	return idx
}
