package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m app) attachmentView(width, _ int) string {
	description := "This client may occupy one persistent session host; switching does not stop either host."
	if width >= 110 {
		leftWidth := width * 40 / 100
		dock := titleStyle.Render("Attachment dock") + "\n" + mutedStyle.Render(description) + "\n\n" + m.currentAttachmentView()
		candidates := titleStyle.Render("Switch targets") + "\n" + m.attachmentCandidateRows(5) + "\n\n" + m.attachmentCandidateDetail()
		return lipgloss.JoinHorizontal(lipgloss.Top, renderPanel(leftWidth, dock), " ", renderPanel(width-leftWidth-1, candidates))
	}
	dock := titleStyle.Render("Attachment dock") + "\n" + mutedStyle.Render(truncate(description, width-8)) + "\n\n" + m.currentAttachmentView()
	candidates := titleStyle.Render("Switch targets") + "\n" + m.attachmentCandidateRows(2) + "\n\n" + m.attachmentCandidateDetail()
	return renderPanel(width, dock+"\n\n"+candidates)
}

func (m app) currentAttachmentView() string {
	if m.clientAttach == "" {
		return "CLIENT  detached\nNo session host was stopped; select a target and press a."
	}
	for _, s := range m.sessions {
		if s.ID == m.clientAttach {
			presence, agent := sessionSignals(s)
			return strings.Join([]string{
				"CLIENT  attached → " + s.ID,
				truncate(s.Project+" / "+s.Branch, 42),
				strings.Join([]string{s.Lifecycle, presence, agent, s.Policy}, " · "),
				"d detaches this client; host remains " + s.Lifecycle,
			}, "\n")
		}
	}
	return "CLIENT  attachment target is no longer present in this fixture"
}

func (m app) attachmentCandidateRows(limit int) string {
	indices := m.visibleIndices()
	if len(indices) == 0 {
		return mutedStyle.Render("No switch targets.")
	}
	rows := make([]string, 0, limit+1)
	for position, idx := range indices {
		if position >= limit {
			rows = append(rows, mutedStyle.Render(fmt.Sprintf("  … %d more targets", len(indices)-position)))
			break
		}
		s := m.sessions[idx]
		presence, _ := sessionSignals(s)
		eligibility := "inspect only"
		if s.Lifecycle == "ready" || (s.Lifecycle == "stopped" && s.Policy != "invalid") {
			eligibility = "a attach/switch"
		}
		line := fmt.Sprintf("  %s %s %s %s", padRight(s.Project+" / "+s.Branch, 20), padRight(s.Lifecycle, 11), padRight(presence, 12), eligibility)
		rows = append(rows, m.selectableLine(position, line, s))
	}
	return strings.Join(rows, "\n")
}

func (m app) attachmentCandidateDetail() string {
	idx, ok := m.selectedSessionIndex()
	if !ok {
		return mutedStyle.Render("No candidate selected")
	}
	s := m.sessions[idx]
	presence, agent := sessionSignals(s)
	result := titleStyle.Render("Candidate facts") + "  " + s.ID + "\n" +
		strings.Join([]string{s.Lifecycle, presence, agent, s.Policy}, " · ") +
		"\nactions  " + strings.Join(availableActions(s), " · ")
	if s.Operation != "" {
		result += "\nprogress " + s.Operation
	}
	return result
}
