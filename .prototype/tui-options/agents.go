package main

import (
	"fmt"
	"strings"
)

func agentReport(s session, p sessionProcess) (string, string) {
	signal, reason := firstNonEmpty(p.LastSignal, "no report"), p.LastReason
	if p.ReportsSessionSignal {
		_, signal = sessionSignals(s)
		if signal == "—" {
			signal = "no report"
		}
		reason = s.AgentReason
	}
	if s.AttachedCount > 0 {
		return "no report", "Reports cleared while a terminal is attached"
	}
	if s.PresenceUnknown {
		return "not evaluated", "Attachment observations unavailable"
	}
	return signal, reason
}

func agentProcessState(s session, p sessionProcess) string {
	if observationWarning(s) != "" || !s.ProcessesKnown || s.Lifecycle == "missing" || s.Lifecycle == "unreachable" {
		return "unknown"
	}
	if s.Lifecycle == "stopped" {
		return "stopped"
	}
	if s.Lifecycle != "ready" {
		return "unknown"
	}
	return p.State
}

func agentSummary(s session, p sessionProcess) string {
	if agentProcessState(s, p) != "running" {
		return "unknown"
	}
	signal, _ := agentReport(s, p)
	switch signal {
	case "running":
		return "working"
	case "attention", "waiting":
		return "waiting"
	case "idle", "failed":
		return signal
	default:
		return "unknown"
	}
}

func activeAgentProcesses(s session, processes []sessionProcess) []sessionProcess {
	active := []sessionProcess{}
	for _, p := range processes {
		if agentProcessState(s, p) == "running" {
			active = append(active, p)
		}
	}
	return active
}

func agentName(p sessionProcess) string {
	if p.Label != "" {
		return p.Name + " [" + p.Label + "]"
	}
	return p.Name
}

func (m app) agentsSession() (session, bool) {
	for _, s := range m.sessions {
		if s.ID == m.agentsSessionID {
			return s, true
		}
	}
	return session{}, false
}

func (m *app) openAgents() {
	m.terminalInspector = false
	if idx, ok := m.selectedSessionIndex(); ok {
		m.agentsSessionID = m.sessions[idx].ID
		m.agentChoice = 0
		m.agentPreviewOffset = 0
		m.screen = screenAgents
	}
}

func (m *app) updateAgents(key string) {
	s, ok := m.agentsSession()
	agents := activeAgentProcesses(s, s.Agents)
	if !ok || len(agents) == 0 {
		return
	}
	if m.agentChoice >= len(agents) {
		m.agentChoice = len(agents) - 1
	}
	switch key {
	case "j", "down":
		m.agentChoice = (m.agentChoice + 1) % len(agents)
		m.agentPreviewOffset = 0
	case "k", "up":
		m.agentChoice = (m.agentChoice + len(agents) - 1) % len(agents)
		m.agentPreviewOffset = 0
	case "pgup", "K":
		m.agentPreviewOffset++
		if m.agentPreviewOffset > len(agents[m.agentChoice].Preview) {
			m.agentPreviewOffset = len(agents[m.agentChoice].Preview)
		}
	case "pgdown", "J":
		m.agentPreviewOffset = maxInt(0, m.agentPreviewOffset-1)
	}
}

func (m app) agentsView() string {
	s, ok := m.agentsSession()
	agents := activeAgentProcesses(s, s.Agents)
	if !ok {
		return "Agents\nSession is no longer available.\nq | Esc | Ctrl-C back"
	}
	rows := []string{titleStyle.Render("Agents · " + s.Project + " / " + s.Branch), ""}
	if len(agents) == 0 {
		message := "No active agents observed."
		if !s.ProcessesKnown || observationWarning(s) != "" || s.Lifecycle == "missing" || s.Lifecycle == "unreachable" {
			message = "Agent inventory not observed."
		}
		return strings.Join(append(rows, message, "", "q | Esc | Ctrl-C back"), "\n")
	}
	choice := m.agentChoice
	if choice >= len(agents) {
		choice = len(agents) - 1
	}
	limit := maxInt(1, m.height/6)
	if limit > 6 {
		limit = 6
	}
	start, end := windowBounds(len(agents), choice, limit)
	for i := start; i < end; i++ {
		line := "  " + agentName(agents[i]) + " · " + styledFact(agentSummary(s, agents[i]))
		if i == choice {
			line = selectedStyle.Render("› " + strings.TrimPrefix(line, "  "))
		}
		rows = append(rows, line)
	}
	p := agents[choice]
	_, reason := agentReport(s, p)
	rows = append(rows, "", titleStyle.Render(agentName(p)),
		"Instance "+firstNonEmpty(p.ID, "not reported"),
		"Task     "+firstNonEmpty(p.Description, "not reported"),
		"Status   "+styledFact(agentSummary(s, p)),
		"Context  "+firstNonEmpty(reason, "No additional context reported"))
	rows = append(rows, titleStyle.Render("Recorded preview · simulated"))
	available := maxInt(0, m.height-len(rows)-2)
	count := len(p.Preview)
	if count > available {
		count = available
	}
	startPreview := maxInt(0, len(p.Preview)-count-m.agentPreviewOffset)
	if len(p.Preview) == 0 && available > 0 {
		rows = append(rows, "No preview recorded.")
	} else {
		rows = append(rows, p.Preview[startPreview:startPreview+count]...)
	}
	for len(rows) < m.height-2 {
		rows = append(rows, "")
	}
	rows = append(rows, fmt.Sprintf("j/k agent (%d/%d) · PgUp/PgDn preview", choice+1, len(agents)), "q | Esc | Ctrl-C back")
	return strings.Join(rows, "\n")
}
