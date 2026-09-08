package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m app) topologyView(width, height int) string {
	heading := titleStyle.Render("Resource topology") + "\n" +
		mutedStyle.Render("Edges expose ownership and lifetime; adjacent resources are not collapsed into session state.")
	if m.browser {
		heading = ""
	}
	listHeading := titleStyle.Render(m.projectSessionsHeading())
	headingPrefix := ""
	if heading != "" {
		headingPrefix = heading + "\n\n"
	}
	searchRows := 0
	if m.filtering || m.filter.Value() != "" {
		searchRows = 1
	}
	selected := m.selectedTopology()
	if width >= 110 {
		leftWidth := width * 47 / 100
		if m.browser {
			leftWidth = width * 65 / 100
		}
		fleet := listHeading + "\n" + m.topologyRows(maxInt(1, (height-lipgloss.Height(m.header(width))-lipgloss.Height(m.footer(width))-lipgloss.Height(heading)-lipgloss.Height(listHeading)-searchRows-5)), leftWidth-8)
		list := renderPanel(leftWidth, fleet)
		if search := m.listSearch(leftWidth); search != "" {
			list += "\n" + search
		}
		return headingPrefix + lipgloss.JoinHorizontal(lipgloss.Top, list, " ", renderPanel(width-leftWidth-1, selected))
	}
	// Reserve the relationship inspector, headings, spacing, panel border,
	// header/footer, and one range indicator; use every remaining row.
	rowLimit := maxInt(1, (height - lipgloss.Height(m.header(width)) - lipgloss.Height(m.footer(width)) - lipgloss.Height(heading) - lipgloss.Height(selected) - lipgloss.Height(listHeading) - searchRows - 6))
	list := headingPrefix + listHeading + "\n" + m.topologyRows(rowLimit, width-8)
	if search := m.listSearch(width - 4); search != "" {
		list += "\n" + search
	}
	return renderPanel(width, list+"\n\n"+selected)
}

func (m app) topologyRows(limit, lineWidth int) string {
	indices := m.visibleIndices()
	if len(indices) == 0 {
		return mutedStyle.Render("∅  no project, ref, session, host, or client edges")
	}
	rows := make([]string, 0, limit+1)
	start, end := m.sessionWindow(indices, limit)
	if len(indices) > limit {
		rows = append(rows, mutedStyle.Render(fmt.Sprintf("  %d–%d of %d", start+1, end, len(indices))))
	}
	for position := start; position < end; position++ {
		idx := indices[position]
		s := m.sessions[idx]
		connector := "├─"
		if position == len(indices)-1 {
			connector = "└─"
		}
		prefix := "  " + padRight(s.Project, 10) + " " + connector + " "
		if m.topologyProject != "" {
			prefix = "  " + connector + " "
		}
		stateWidth := 11
		if m.browser {
			stateWidth = 16
		}
		branchWidth := maxInt(1, lineWidth-lipgloss.Width(prefix)-stateWidth-1)
		state := styledFact(m.lifecycleLabel(s.Lifecycle))
		if m.browser && s.Lifecycle == "ready" {
			state = lipgloss.NewStyle().Foreground(positive).Render("running")
		}
		if m.browser {
			if flag := sessionFlag(s); flag != "" {
				color := warning
				if flag == "failed" {
					color = urgent
				}
				state += " " + lipgloss.NewStyle().Foreground(color).Render("!"+flag)
			}
		}
		line := prefix + padRight(middleTruncate(s.Branch, branchWidth), branchWidth) + " " + padRight(state, stateWidth)
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
	if m.browser {
		return m.sessionActivityDetails(s)
	}
	presence, agent := sessionSignals(s)
	lines := []string{
		titleStyle.Render("Session details"),
		truncate("PROJECT  "+s.Project, 50),
		"  ├─ GIT REF  " + truncate(s.Branch, 38),
		"  │    retained independently",
		"  └─ SESSION  " + s.ID,
		"       ├─ HOST       " + styledFact(m.lifecycleLabel(s.Lifecycle)) + "  (persistent)",
		"       ├─ CLIENTS    " + styledFact(presence) + "  (temporary)",
		"       ├─ AGENT      " + styledFact(agent) + "  (unattended-only)",
		"       └─ POLICY     " + styledFact(s.Policy) + "  (immutable snapshot)",
		"actions  " + strings.Join(availableActions(s), " · "),
	}
	if s.Operation != "" {
		lines = append(lines, "progress "+s.Operation)
	}
	return strings.Join(lines, "\n")
}

func (m app) projectFilterView(width, height int) string {
	projects := append([]string{""}, m.projects()...)
	limit := maxInt(1, height-10)
	start, end := windowBounds(len(projects), m.projectChoice, limit)
	rows := []string{titleStyle.Render("Filter by project"), mutedStyle.Render("j/k choose · enter apply · esc cancel"), ""}
	if len(projects) > limit {
		rows = append(rows, mutedStyle.Render(fmt.Sprintf("%d–%d of %d", start+1, end, len(projects))))
	}
	for i := start; i < end; i++ {
		label := truncate(firstNonEmpty(projects[i], "All projects"), maxInt(1, width-12))
		if i == m.projectChoice {
			label = selectedStyle.Render("› " + label)
		} else {
			label = "  " + label
		}
		rows = append(rows, label)
	}
	return renderPanel(width, strings.Join(rows, "\n"))
}

func (m app) projectSessionsHeading() string {
	if m.topologyProject == "" {
		return "All project sessions"
	}
	return "[" + m.topologyProject + "] project sessions"
}

func (m app) listSearch(width int) string {
	if m.filtering {
		input := m.filter
		input.Prompt = "fuzzy search> "
		input.Placeholder = ""
		input.Width = maxInt(1, width-len(input.Prompt)-1)
		return truncate(input.View(), width)
	}
	if m.filter.Value() != "" {
		return truncate("fuzzy search> "+m.filter.Value(), width)
	}
	return ""
}

func sessionStats(s session) string {
	if observationWarning(s) != "" {
		return "agents ? · services ?"
	}
	if s.Lifecycle == "stopped" {
		return "0 agents · 0 services"
	}
	if !s.ProcessesKnown || observationWarning(s) != "" || s.Lifecycle != "ready" {
		return "agents ? · services ?"
	}
	agents := activeAgentProcesses(s, s.Agents)
	services := 0
	flag := sessionFlag(s)
	for _, unit := range projectServices(s) {
		active, _ := unitState(s, unit)
		if active == "active" {
			services++
		}
	}
	text := fmt.Sprintf("%d agents · %d services", len(agents), services)
	if flag != "" {
		text += " · " + styledFact("!") + " " + styledFact(flag)
	}
	return text
}

func sessionFlag(s session) string {
	flag := ""
	for _, agent := range activeAgentProcesses(s, s.Agents) {
		switch agentSummary(s, agent) {
		case "waiting":
			return "waiting"
		case "failed":
			flag = "failed"
		}
	}
	return flag
}
