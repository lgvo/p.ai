package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m app) topologyView(width, height int) string {
	if m.browser {
		return m.browserTopologyView(width, height)
	}
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
			list = search + "\n" + list
		}
		return headingPrefix + lipgloss.JoinHorizontal(lipgloss.Top, list, " ", renderPanel(width-leftWidth-1, selected))
	}
	// Reserve the relationship inspector, headings, spacing, panel border,
	// header/footer, and one range indicator; use every remaining row.
	rowLimit := maxInt(1, (height - lipgloss.Height(m.header(width)) - lipgloss.Height(m.footer(width)) - lipgloss.Height(heading) - lipgloss.Height(selected) - lipgloss.Height(listHeading) - searchRows - 6))
	list := headingPrefix + listHeading + "\n" + m.topologyRows(rowLimit, width-8)
	if search := m.listSearch(width - 4); search != "" {
		list = search + "\n" + list
	}
	return renderPanel(width, list+"\n\n"+selected)
}

// List controls belong to the list column, never to the combined list/details row.
func (m app) browserTopologyView(width, height int) string {
	plan := m.layout()
	available, listWidth := plan.available, plan.listWidth
	controls := m.footer(listWidth)
	search := m.listSearch(listWidth - 6)
	baseline := m
	baseline.filter.SetValue("")
	baseline.filtering = false
	rowLimit := plan.sessions
	heading := titleStyle.Render(m.projectSessionsHeading())
	if search != "" {
		heading = search
	}
	listRows := m.topologyRows(rowLimit, listWidth-8)
	listRows = padContentRows(listRows, lipgloss.Height(baseline.topologyRows(rowLimit, listWidth-8)))
	list := renderPanel(listWidth, heading+"\n"+listRows)
	list += "\n" + controls
	detailsWidth, detailsHeight := width, available-lipgloss.Height(list)-1
	if width >= 110 {
		detailsWidth, detailsHeight = width-listWidth-1, available
	}
	details := renderPanel(detailsWidth, m.selectedTopology())
	lines := strings.Split(details, "\n")
	if len(lines) > detailsHeight && detailsHeight > 0 {
		lines = append(lines[:detailsHeight-1], mutedStyle.Render(truncate("… A agents · S services for more", detailsWidth)))
		details = strings.Join(lines, "\n")
	}
	if width >= 110 {
		return lipgloss.JoinHorizontal(lipgloss.Top, list, " ", details)
	}
	if detailsHeight <= 0 {
		return list
	}
	return list + "\n" + details
}

func (m app) topologyRows(limit, lineWidth int) string {
	if m.browser {
		return m.browserSelectorRows(limit, lineWidth)
	}
	indices := m.visibleIndices()
	if len(indices) == 0 {
		return mutedStyle.Render("∅  no project, ref, session, host, or client edges")
	}
	rows := make([]string, 0, limit+1)
	start, end := m.sessionWindow(indices, limit)
	if len(indices) > limit {
		rows = append(rows, mutedStyle.Render(listPosition(start, end, len(indices), m.selected)))
	}
	for position := start; position < end; position++ {
		idx := indices[position]
		s := m.sessions[idx]
		connector := "├─"
		if position == len(indices)-1 {
			connector = "└─"
		}
		prefix := "  " + padRight(truncate(s.Project, 10), 10) + " " + connector + " "
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
		line := prefix + padRight(middleTruncate(s.Branch, min(branchNameMaxWidth, branchWidth)), branchWidth) + " " + padRight(state, stateWidth)
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
	contentWidth := max(1, width-panelFrameColumns)
	total := len(m.projects()) + 1
	capacity := min(total, m.projectCapacity())
	choices := newSelector(m.projectFilterItems(contentWidth), "", m.projectChoice, contentWidth, capacity)
	heading := titleStyle.Render("Filter by project")
	if contentWidth < 65 {
		heading = titleStyle.Render("Projects · !w waiting / r run / s stop / t total")
	}
	if prompt := fuzzySearchPrompt(m.projectSearch, m.projectSearching, contentWidth); prompt != "" {
		heading = prompt
	}
	return renderPanel(width, truncate(heading, contentWidth)+"\n\n"+choices.rows("No matching projects.", total > capacity))
}

func (m app) projectSessionsHeading() string {
	if m.topologyProject == "" {
		return "All project sessions"
	}
	return "[" + m.topologyProject + "] project sessions"
}

func (m app) listSearch(width int) string {
	return fuzzySearchPrompt(m.filter, m.filtering, width)
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

func (m app) browserSelectorRows(limit, width int) string {
	indices := m.visibleIndices()
	items := make([]selectorItem, 0, len(indices))
	for position, idx := range indices {
		s := m.sessions[idx]
		connector := "├─"
		if position == len(indices)-1 || (m.sessions[indices[position+1]].Project != s.Project || sessionPriority(m.sessions[indices[position+1]]) != sessionPriority(s)) {
			connector = "└─"
		}
		prefix := "  " + padRight(truncate(s.Project, min(projectNameMaxWidth, max(10, width/3))), min(projectNameMaxWidth, max(10, width/3))) + " " + connector + " "
		if m.topologyProject != "" {
			prefix = "  " + connector + " "
		}
		state := styledFact(m.lifecycleLabel(s.Lifecycle))
		if s.Lifecycle == "ready" {
			state = lipgloss.NewStyle().Foreground(positive).Render("running")
		}
		if flag := sessionFlag(s); flag != "" {
			color := warning
			if flag == "failed" {
				color = urgent
			}
			state += " " + lipgloss.NewStyle().Foreground(color).Render("!"+flag)
		}
		branchWidth := max(1, width-lipgloss.Width(prefix)-17)
		line := truncate(prefix+padRight(middleTruncate(s.Branch, min(branchNameMaxWidth, branchWidth)), branchWidth)+" "+padRight(state, 16), width)
		items = append(items, selectorItem{index: idx, text: line, search: s.Branch})
	}
	capacity := max(1, min(limit, len(indices)))
	choices := newSelector(items, "", m.selected, width, capacity)
	return choices.rows("No matching sessions.", len(indices) > limit)
}
