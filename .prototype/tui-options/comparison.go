package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m *app) pinComparisonAnchor() {
	idx, ok := m.selectedSessionIndex()
	if !ok {
		m.message = "No session is available to pin."
		return
	}
	m.compareAnchor = m.sessions[idx].ID
	m.message = fmt.Sprintf("Pinned %s as comparison A; move to another session for B.", m.compareAnchor)
}

func (m app) comparisonView(width, _ int) string {
	anchor, anchorOK := m.sessionWithID(m.compareAnchor)
	selectedIndex, selectedOK := m.selectedSessionIndex()
	if !anchorOK || !selectedOK {
		return renderPanel(width, titleStyle.Render("Session comparison")+"\n"+mutedStyle.Render("Two sessions are required; this fixture cannot form a pair."))
	}
	candidate := m.sessions[selectedIndex]
	heading := titleStyle.Render("Session comparison") + "\n" +
		mutedStyle.Render("s pins the current session as A; j/k chooses B. Differences never merge identities.")
	if width >= 110 {
		columnWidth := (width - 1) / 2
		left := renderPanel(columnWidth, m.comparisonCard("A · pinned baseline", anchor))
		right := renderPanel(columnWidth, m.comparisonCard("B · current candidate", candidate))
		return heading + "\n\n" + lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right) + "\n" + m.comparisonDelta(anchor, candidate, width)
	}
	content := heading + "\n\n" + m.comparisonCompactCard("A · pinned", anchor) + "\n\n" +
		m.comparisonCompactCard("B · current", candidate) + "\n\n" + m.comparisonDelta(anchor, candidate, width-4)
	return renderPanel(width, content)
}

func (m app) comparisonCard(label string, s session) string {
	presence, agent := sessionSignals(s)
	lines := []string{
		titleStyle.Render(label),
		s.Project + " / " + s.Branch,
		mutedStyle.Render("UUID " + s.ID),
		"lifecycle  " + s.Lifecycle,
		"presence   " + presence,
		"agent      " + agent,
		"policy     " + s.Policy,
		"actions    " + strings.Join(availableActions(s), " · "),
	}
	if s.Operation != "" {
		lines = append(lines, "progress   "+s.Operation)
	}
	return strings.Join(lines, "\n")
}

func (m app) comparisonCompactCard(label string, s session) string {
	presence, agent := sessionSignals(s)
	result := titleStyle.Render(label) + "  " + truncate(s.Project+" / "+s.Branch, 34) + "  ·  " + s.ID + "\n" +
		strings.Join([]string{s.Lifecycle, presence, agent, s.Policy}, " · ") + "\n" +
		"actions  " + strings.Join(availableActions(s), " · ")
	if s.Operation != "" {
		result += "\nprogress " + s.Operation
	}
	return result
}

func (m app) comparisonDelta(a, b session, width int) string {
	fields := []string{
		"life " + deltaValue(a.Lifecycle, b.Lifecycle),
		"presence " + deltaValue(presenceValue(a), presenceValue(b)),
		"agent " + deltaValue(agentValue(a), agentValue(b)),
		"policy " + deltaValue(a.Policy, b.Policy),
	}
	return truncate(titleStyle.Render("A → B delta")+"  "+strings.Join(fields, "  ·  "), width)
}

func deltaValue(a, b string) string {
	if a == b {
		return "=" + a
	}
	return a + "→" + b
}

func presenceValue(s session) string {
	presence, _ := sessionSignals(s)
	return presence
}

func agentValue(s session) string {
	_, agent := sessionSignals(s)
	return agent
}

func (m app) sessionWithID(id string) (session, bool) {
	for _, s := range m.sessions {
		if s.ID == id {
			return s, true
		}
	}
	return session{}, false
}
