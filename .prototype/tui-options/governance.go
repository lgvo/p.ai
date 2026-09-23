package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m app) governanceView(width, _ int) string {
	answers := m.governanceAnswers()
	if width < 110 {
		rows := []string{
			titleStyle.Render("Four-question governance dashboard"),
			mutedStyle.Render("Navigate sessions normally; the dashboard continuously recomputes its answers."),
			"",
		}
		for _, answer := range answers {
			rows = append(rows, titleStyle.Render(answer.question), "  "+answer.summary)
		}
		rows = append(rows, "", m.governanceSelection())
		return renderPanel(width, strings.Join(rows, "\n"))
	}
	columnWidth := (width - 1) / 2
	panels := make([]string, 0, 4)
	for _, answer := range answers {
		content := titleStyle.Render(answer.question) + "\n" + answer.summary + "\n" + mutedStyle.Render(answer.detail)
		panels = append(panels, renderPanel(columnWidth, content))
	}
	top := lipgloss.JoinHorizontal(lipgloss.Top, panels[0], " ", panels[1])
	bottom := lipgloss.JoinHorizontal(lipgloss.Top, panels[2], " ", panels[3])
	selection := renderPanel(width, m.governanceSelection())
	return lipgloss.JoinVertical(lipgloss.Left, top, bottom, selection)
}

type governanceAnswer struct {
	question string
	summary  string
	detail   string
}

func (m app) governanceAnswers() []governanceAnswer {
	urgentCount, changingCount, driftCount := 0, 0, 0
	topUrgent, topChange, topRisk := "none", "none", "none"
	for _, idx := range m.visibleIndices() {
		s := m.sessions[idx]
		if urgency(s) < 4 {
			urgentCount++
			if topUrgent == "none" {
				topUrgent = s.Project + "/" + s.Branch
			}
		}
		if s.Operation != "" {
			changingCount++
			if topChange == "none" {
				topChange = s.Operation
			}
		}
		if s.Policy != "current" {
			driftCount++
			if topRisk == "none" {
				topRisk = s.Project + "/" + s.Branch + " · " + s.Policy
			}
		}
	}
	attachment := "detached"
	attachmentDetail := "No client attachment; persistent hosts are unaffected."
	for _, s := range m.sessions {
		if s.ID == m.clientAttach {
			attachment = "attached → " + s.Project + "/" + s.Branch
			attachmentDetail = "Detach removes only this client; host remains " + s.Lifecycle + "."
			break
		}
	}
	return []governanceAnswer{
		{"1 · What needs attention?", fmt.Sprintf("%d urgent · %s", urgentCount, truncate(topUrgent, 36)), "Independent agent and lifecycle conditions determine this answer."},
		{"2 · Where am I attached?", truncate(attachment, 48), attachmentDetail},
		{"3 · What is changing?", fmt.Sprintf("%d observed operations · %s", changingCount, truncate(topChange, 34)), "Progress is observation, never a guessed decision state."},
		{"4 · What could be unsafe?", fmt.Sprintf("%d policy risks · %s", driftCount, truncate(topRisk, 34)), "Destructive work still requires an aggregate preview and fingerprint."},
	}
}

func (m app) governanceSelection() string {
	idx, ok := m.selectedSessionIndex()
	if !ok {
		return mutedStyle.Render("No selected session; questions remain valid for the empty fixture.")
	}
	s := m.sessions[idx]
	presence, agent := sessionSignals(s)
	result := titleStyle.Render("Selected evidence · "+s.ID) + "  " + truncate(s.Project+" / "+s.Branch, 31) + "\n" +
		strings.Join([]string{s.Lifecycle, presence, agent, s.Policy}, " · ") +
		"\nactions  " + strings.Join(availableActions(s), " · ")
	if s.Operation != "" {
		result += "\nprogress " + s.Operation
	}
	return result
}
