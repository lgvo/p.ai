package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var workspaceTabNames = []string{"Sessions", "Activity", "Policy", "Resources"}

func (m app) workspaceView(width, height int) string {
	tabs := make([]string, len(workspaceTabNames))
	for i, name := range workspaceTabNames {
		if i == m.workspaceTab {
			tabs[i] = selectedStyle.Render(" " + name + " ")
		} else {
			tabs[i] = mutedStyle.Render(name)
		}
	}
	content := titleStyle.Render("Task-oriented workspace") + "  " + strings.Join(tabs, "  ") + "\n" +
		mutedStyle.Render("t changes work mode while session selection and identity remain stable.") + "\n\n" +
		m.workspaceTabContent(width, height)
	if width >= 110 {
		leftWidth := width * 61 / 100
		return lipgloss.JoinHorizontal(lipgloss.Top, renderPanel(leftWidth, content), " ", renderPanel(width-leftWidth-1, m.detailView()))
	}
	return renderPanel(width, content+"\n\n"+m.workspaceCompactDetail())
}

func (m app) workspaceTabContent(width, _ int) string {
	switch m.workspaceTab {
	case 1:
		observationWidth := 40
		if width < 110 {
			observationWidth = 46
		}
		return m.operationRows(5, observationWidth)
	case 2:
		rows := []string{mutedStyle.Render("  SESSION        SNAPSHOT    NEXT STEP")}
		for _, idx := range m.visibleIndices() {
			s := m.sessions[idx]
			next := "none"
			if s.Policy == "outdated" {
				next = "diff / recreate"
			} else if s.Policy == "invalid" {
				next = "inspect constraint"
			}
			rows = append(rows, fmt.Sprintf("  %s %s %s", padRight(s.ID, 13), padRight(s.Policy, 11), next))
			if len(rows) == 7 {
				break
			}
		}
		return strings.Join(rows, "\n")
	case 3:
		rows := []string{mutedStyle.Render("  PROJECT      RETAINED BRANCH                 TIP")}
		for _, branch := range m.branches {
			rows = append(rows, fmt.Sprintf("  %s %s %s", padRight(branch.Project, 12), padRight(branch.Name, 31), branch.Tip))
		}
		rows = append(rows, "", "Branches are Git resources, not sessions; they have no runtime or attachment.")
		return strings.Join(rows, "\n")
	default:
		return m.sessionRows(m.visibleIndices(), width >= 110, 5)
	}
}

func (m app) workspaceCompactDetail() string {
	idx, ok := m.selectedSessionIndex()
	if !ok {
		return mutedStyle.Render("No session selected")
	}
	s := m.sessions[idx]
	presence, agent := sessionSignals(s)
	result := titleStyle.Render("Persistent selection") + "  " + truncate(s.Project+" / "+s.Branch, 36) +
		"\n" + strings.Join([]string{s.Lifecycle, presence, agent, s.Policy}, " · ") +
		"\nactions  " + strings.Join(availableActions(s), " · ")
	if s.Operation != "" {
		result += "\nprogress " + s.Operation
	}
	return result
}
