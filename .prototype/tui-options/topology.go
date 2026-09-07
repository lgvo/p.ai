package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m app) topologyView(width, _ int) string {
	attached := 0
	for _, s := range m.sessions {
		attached += s.AttachedCount
	}
	heading := titleStyle.Render("Resource topology") + "\n" +
		mutedStyle.Render("Edges expose ownership and lifetime; adjacent resources are not collapsed into session state.")
	selected := m.selectedTopology()
	if width >= 110 {
		leftWidth := width * 47 / 100
		fleet := titleStyle.Render(fmt.Sprintf("Portfolio edges · attached %d", attached)) + "\n" + m.topologyRows(6, leftWidth-8)
		return heading + "\n\n" + lipgloss.JoinHorizontal(lipgloss.Top, renderPanel(leftWidth, fleet), " ", renderPanel(width-leftWidth-1, selected))
	}
	rowLimit := 2
	if idx, ok := m.selectedSessionIndex(); ok && m.sessions[idx].Operation != "" {
		rowLimit = 1
	}
	return renderPanel(width, heading+"\n\n"+titleStyle.Render(fmt.Sprintf("Portfolio edges · attached %d", attached))+"\n"+m.topologyRows(rowLimit, width-8)+"\n\n"+selected)
}

func (m app) topologyRows(limit, lineWidth int) string {
	indices := m.visibleIndices()
	if len(indices) == 0 {
		return mutedStyle.Render("∅  no project, ref, session, host, or client edges")
	}
	rows := make([]string, 0, limit+1)
	for position, idx := range indices {
		if position >= limit {
			rows = append(rows, mutedStyle.Render(fmt.Sprintf("   … %d more session edges", len(indices)-position)))
			break
		}
		s := m.sessions[idx]
		presence, agent := sessionSignals(s)
		connector := "├─"
		if position == len(indices)-1 {
			connector = "└─"
		}
		line := fmt.Sprintf("  %s %s %s host:%s client:%s agent:%s", padRight(s.Project, 10), connector, padRight(s.ID, 11), padRight(s.Lifecycle, 11), padRight(presence, 11), agent)
		line = truncate(line, lineWidth)
		if position == m.selected {
			line = selectedStyle.Render("› " + strings.TrimPrefix(line, "  "))
		}
		rows = append(rows, line)
	}
	return strings.Join(rows, "\n")
}

func (m app) selectedTopology() string {
	idx, ok := m.selectedSessionIndex()
	if !ok {
		return titleStyle.Render("Selected relationship") + "\n" + mutedStyle.Render("No session relationship selected.")
	}
	s := m.sessions[idx]
	presence, agent := sessionSignals(s)
	lines := []string{
		titleStyle.Render("Selected relationship · explicit lifetimes"),
		truncate("PROJECT  "+s.Project, 50),
		"  ├─ GIT REF  " + truncate(s.Branch, 38),
		"  │    retained independently",
		"  └─ SESSION  " + s.ID + "  (stable identity)",
		"       ├─ HOST       " + s.Lifecycle + "  (persistent)",
		"       ├─ CLIENTS    " + presence + "  (temporary)",
		"       ├─ AGENT      " + agent + "  (unattended-only)",
		"       └─ POLICY     " + s.Policy + "  (immutable snapshot)",
		"actions  " + strings.Join(availableActions(s), " · "),
	}
	if s.Operation != "" {
		lines = append(lines, "progress "+s.Operation)
	}
	return strings.Join(lines, "\n")
}
