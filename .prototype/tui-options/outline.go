package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type outlineNode struct {
	project      string
	sessionIndex int
	isProject    bool
}

func (m app) outlineNodes() []outlineNode {
	indices := m.visibleIndices()
	byProject := make(map[string][]int)
	for _, idx := range indices {
		byProject[m.sessions[idx].Project] = append(byProject[m.sessions[idx].Project], idx)
	}
	var nodes []outlineNode
	for _, project := range m.projects() {
		projectSessions := byProject[project]
		if len(projectSessions) == 0 && strings.TrimSpace(m.filter.Value()) != "" {
			continue
		}
		nodes = append(nodes, outlineNode{project: project, sessionIndex: -1, isProject: true})
		if !m.collapsed[project] {
			for _, idx := range projectSessions {
				nodes = append(nodes, outlineNode{project: project, sessionIndex: idx})
			}
		}
	}
	return nodes
}

func (m *app) moveOutline(delta int) {
	nodes := m.outlineNodes()
	if len(nodes) == 0 {
		m.outlineCursor = 0
		return
	}
	m.outlineCursor = (m.outlineCursor + delta + len(nodes)) % len(nodes)
	m.syncOutlineSelection(nodes[m.outlineCursor])
}

func (m *app) syncOutlineSelection(node outlineNode) {
	if node.isProject {
		return
	}
	for position, idx := range m.visibleIndices() {
		if idx == node.sessionIndex {
			m.selected = position
			return
		}
	}
}

func (m *app) toggleOutlineNode() bool {
	nodes := m.outlineNodes()
	if len(nodes) == 0 {
		return false
	}
	if m.outlineCursor >= len(nodes) {
		m.outlineCursor = len(nodes) - 1
	}
	node := nodes[m.outlineCursor]
	if !node.isProject {
		return false
	}
	m.collapsed[node.project] = !m.collapsed[node.project]
	return true
}

func (m *app) setOutlineCollapsed(collapsed bool) {
	nodes := m.outlineNodes()
	if len(nodes) == 0 {
		return
	}
	if m.outlineCursor >= len(nodes) {
		m.outlineCursor = len(nodes) - 1
	}
	node := nodes[m.outlineCursor]
	project := node.project
	m.collapsed[project] = collapsed
	if collapsed {
		for i, candidate := range m.outlineNodes() {
			if candidate.isProject && candidate.project == project {
				m.outlineCursor = i
				break
			}
		}
	}
}

func (m app) outlineView(width, height int) string {
	limit := 7
	if width < 110 {
		limit = 3
		if idx, ok := m.selectedSessionIndex(); ok && m.sessions[idx].Operation != "" {
			limit = 2
		}
	}
	if height >= 35 {
		limit = 12
	}
	tree := titleStyle.Render("Projects / sessions · expandable hierarchy") + "\n" + m.outlineRows(limit)
	if width >= 110 {
		leftWidth := width * 52 / 100
		return lipgloss.JoinHorizontal(lipgloss.Top, renderPanel(leftWidth, tree), " ", renderPanel(width-leftWidth-1, m.detailView()))
	}
	return lipgloss.JoinVertical(lipgloss.Left, renderPanel(width, tree), renderPanel(width, m.detailView()))
}

func (m app) outlineRows(limit int) string {
	nodes := m.outlineNodes()
	if len(nodes) == 0 {
		return mutedStyle.Render("No projects or sessions match this fixture.")
	}
	cursor := m.outlineCursor
	if cursor >= len(nodes) {
		cursor = len(nodes) - 1
	}
	start := cursor - limit/2
	if start < 0 {
		start = 0
	}
	if start+limit > len(nodes) {
		start = maxInt(0, len(nodes)-limit)
	}
	end := start + limit
	if end > len(nodes) {
		end = len(nodes)
	}
	rows := make([]string, 0, end-start+2)
	if start > 0 {
		rows = append(rows, mutedStyle.Render(fmt.Sprintf("  … %d nodes above", start)))
	}
	for position := start; position < end; position++ {
		node := nodes[position]
		var line string
		if node.isProject {
			marker := "▾"
			if m.collapsed[node.project] {
				marker = "▸"
			}
			count, urgentCount := 0, 0
			for _, s := range m.sessions {
				if s.Project == node.project {
					count++
					if urgency(s) < 4 {
						urgentCount++
					}
				}
			}
			line = fmt.Sprintf("  %s %s %d sessions · %d urgent", marker, padRight(node.project, 20), count, urgentCount)
		} else {
			s := m.sessions[node.sessionIndex]
			presence, agent := sessionSignals(s)
			line = fmt.Sprintf("      └─ %s\n         %s · %s · %s · %s", truncate(s.Branch, 38), s.Lifecycle, presence, agent, s.Policy)
		}
		if position == cursor {
			line = selectedStyle.Render("› " + strings.TrimPrefix(line, "  "))
		}
		rows = append(rows, line)
	}
	if end < len(nodes) {
		rows = append(rows, mutedStyle.Render(fmt.Sprintf("  … %d nodes below", len(nodes)-end)))
	}
	return strings.Join(rows, "\n")
}
