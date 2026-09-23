package main

import "github.com/charmbracelet/lipgloss"

const (
	wideColumns         = 110
	compactColumns      = 72
	compactRows         = 22
	panelFrameColumns   = 6
	selectorMaxWidth    = 96
	projectNameMaxWidth = 28
	branchNameMaxWidth  = 48
)

type layoutPlan struct {
	compact, columns                                                 bool
	available, listWidth, detailWidth, panelWidth, panelContentWidth int
	sessions, projects, creation                                     int
}

// Bound the entire page before computing any panel or navigation dimensions.
func (m app) viewportWidth() int {
	if m.screen == screenTerminal {
		return m.width
	}
	limit := selectorMaxWidth
	if m.screen == screenOverview && m.width >= wideColumns {
		limit = 148
	}
	return min(m.width, limit)
}

func sessionDisplayName(s session) string {
	return truncate(s.Project, projectNameMaxWidth) + " / " + truncate(s.Branch, branchNameMaxWidth)
}

func (m app) layout() layoutPlan {
	m.width = m.viewportWidth()
	p := layoutPlan{compact: m.width < compactColumns || m.height < compactRows, columns: m.width >= wideColumns}
	p.available = m.height - lipgloss.Height(m.header(m.width)) - 1
	p.listWidth = m.width
	if p.columns {
		p.listWidth = m.width * 65 / 100
	}
	p.detailWidth = m.width - p.listWidth - 1
	p.panelWidth = min(m.width, selectorMaxWidth)
	p.panelContentWidth = max(1, p.panelWidth-panelFrameColumns)
	// Fixed chrome budgets include the prompt, range line, border and commands.
	p.creation = max(1, m.height-14)
	p.projects = max(1, m.height-10)
	p.sessions = max(1, p.available-lipgloss.Height(m.footer(p.listWidth))-5)
	if p.compact {
		p.sessions = 1
	} else if !p.columns {
		baseline := m
		baseline.filter.SetValue("")
		baseline.filtering = false
		detailsRows := 0
		for _, idx := range baseline.visibleIndices() {
			detailsRows = max(detailsRows, lipgloss.Height(renderPanel(m.width, m.sessionActivityDetails(m.sessions[idx]))))
		}
		p.sessions = max(1, p.sessions-detailsRows-1)
	}
	return p
}
func (m app) creationCapacity() int       { return m.layout().creation }
func (m app) projectCapacity() int        { return m.layout().projects }
func (m app) browserSessionCapacity() int { return m.layout().sessions }
