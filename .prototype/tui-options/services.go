package main

import (
	"fmt"
	"strings"
)

func projectServices(s session) []sessionProcess {
	result := []sessionProcess{}
	for _, p := range s.Services {
		if !p.Internal {
			result = append(result, p)
		}
	}
	return result
}

func unitState(s session, p sessionProcess) (string, string) {
	if observationWarning(s) != "" || s.Lifecycle == "missing" || s.Lifecycle == "unreachable" || !s.ProcessesKnown {
		return "unknown", "unknown"
	}
	if s.Lifecycle == "stopped" {
		return "inactive", "dead"
	}
	if s.Lifecycle != "ready" {
		return "unknown", "unknown"
	}
	switch p.State {
	case "running":
		return "active", "running"
	case "stopped":
		return "inactive", "dead"
	case "failed":
		return "failed", "failed"
	case "starting":
		return "activating", "start"
	default:
		return "unknown", "unknown"
	}
}

func (m app) servicesSession() (session, bool) {
	for _, s := range m.sessions {
		if s.ID == m.servicesSessionID {
			return s, true
		}
	}
	return session{}, false
}

func (m *app) openServices() {
	m.terminalInspector = false
	if idx, ok := m.selectedSessionIndex(); ok {
		m.servicesSessionID = m.sessions[idx].ID
		m.serviceChoice, m.journalOffset = 0, 0
		m.serviceNotice = ""
		m.screen = screenServices
	}
}

func (m *app) updateServices(key string) {
	s, ok := m.servicesSession()
	if !ok {
		return
	}
	units := projectServices(s)
	if len(units) == 0 {
		return
	}
	switch key {
	case "enter", "J":
		m.openJournal(units[m.serviceChoice].Name)
		return
	case "s", "r":
		m.serviceAction(key)
		return
	case "j", "down":
		m.serviceChoice = (m.serviceChoice + 1) % len(units)
		m.journalOffset = 0
		m.serviceNotice = ""
	case "k", "up":
		m.serviceChoice = (m.serviceChoice + len(units) - 1) % len(units)
		m.journalOffset = 0
		m.serviceNotice = ""

	}
	if m.journalOffset >= len(units[m.serviceChoice].Journal) {
		m.journalOffset = maxInt(0, len(units[m.serviceChoice].Journal)-1)
	}
}

func (m app) servicesView() string {
	s, ok := m.servicesSession()
	if !ok {
		return "Project services\nSession is no longer available.\nq | Esc | Ctrl-C back"
	}
	rows := []string{titleStyle.Render("Project services · " + s.Project + " / " + s.Branch), truncate(m.serviceNotice, m.width)}
	units := projectServices(s)
	if !s.ProcessesKnown || observationWarning(s) != "" || s.Lifecycle == "missing" || s.Lifecycle == "unreachable" {
		rows = append(rows, "Current service inventory unknown; observations unavailable.")
	}
	if len(units) == 0 {
		if s.ProcessesKnown {
			rows = append(rows, "No project service units observed.")
		}
		return strings.Join(append(rows, "", "q | Esc | Ctrl-C back"), "\n")
	}
	choice := m.serviceChoice
	if choice >= len(units) {
		choice = len(units) - 1
	}
	limit := maxInt(1, m.height/5)
	if limit > 6 {
		limit = 6
	}
	start, end := windowBounds(len(units), choice, limit)
	for i := start; i < end; i++ {
		active, sub := unitState(s, units[i])
		line := "  " + units[i].Name + " · " + styledFact(active) + " (" + sub + ")"
		if i == choice {
			line = selectedStyle.Render("› " + strings.TrimPrefix(line, "  "))
		}
		rows = append(rows, line)
	}
	unit := units[choice]
	active, sub := unitState(s, unit)
	rows = append(rows, "", titleStyle.Render(unit.Name), unit.Description, "State  "+styledFact(active)+" ("+sub+")")
	if unit.Endpoint != "" && active == "active" {
		rows = append(rows, "Listen "+unit.Endpoint+" (inside session)")
	}
	rows = append(rows, "", titleStyle.Render("Recent journal · Enter opens full log"))
	available := maxInt(0, m.height-len(rows)-3)
	count := len(unit.Journal)
	if count > 3 {
		count = 3
	}
	if count > available {
		count = available
	}
	offset := len(unit.Journal) - count
	if len(unit.Journal) == 0 && available > 0 {
		rows = append(rows, "No journal entries recorded.")
	} else {
		rows = append(rows, unit.Journal[offset:offset+count]...)
	}
	for len(rows) < m.height-3 {
		rows = append(rows, "")
	}
	rows = append(rows, serviceActionHints(s, unit), fmt.Sprintf("j/k unit (%d/%d) · Enter logs\nq | Esc | Ctrl-C back", choice+1, len(units)))
	return strings.Join(rows, "\n")
}

func serviceActionBlock(s session, unit sessionProcess, key string) string {
	if unit.Internal {
		return "Internal P services are not controlled here."
	}
	if observationWarning(s) != "" || !s.ProcessesKnown {
		return "Refresh service observations before acting."
	}
	if s.Lifecycle != "ready" {
		return "Session must be running to control its services."
	}
	active, _ := unitState(s, unit)
	if active == "unknown" || active == "activating" {
		return "Service state is not stable or known."
	}
	if key == "start" && active == "active" {
		return "Service is already running."
	}
	if key == "stop" && active != "active" {
		return "Service is not running."
	}
	return ""
}

func serviceActionHints(s session, unit sessionProcess) string {
	active, _ := unitState(s, unit)
	toggle := "start"
	if active == "active" {
		toggle = "stop"
	}
	label := "s " + toggle
	if active == "unknown" || active == "activating" {
		label = "s start/stop"
	}
	if serviceActionBlock(s, unit, toggle) != "" {
		label = mutedStyle.Render(label + " [—]")
	}
	restart := "r restart"
	if serviceActionBlock(s, unit, "restart") != "" {
		restart = mutedStyle.Render(restart + " [—]")
	}
	return label + " · " + restart
}

func (m *app) serviceAction(key string) {
	for sessionIndex := range m.sessions {
		if m.sessions[sessionIndex].ID != m.servicesSessionID {
			continue
		}
		s := &m.sessions[sessionIndex]
		position := 0
		for unitIndex, unit := range s.Services {
			if unit.Internal {
				continue
			}
			if position != m.serviceChoice {
				position++
				continue
			}
			action := "restart"
			if key == "s" {
				active, _ := unitState(*s, unit)
				action = "start"
				if active == "active" {
					action = "stop"
				}
			} else if key != "r" {
				return
			}
			if reason := serviceActionBlock(*s, unit, action); reason != "" {
				m.serviceNotice = reason
				return
			}
			s.Services = append([]sessionProcess(nil), s.Services...)
			unit.Journal = append([]string(nil), unit.Journal...)
			switch action {
			case "start":
				unit.State = "running"
				unit.Journal = append(unit.Journal, "[simulated] systemd: Starting "+unit.Name, "[simulated] systemd: Started "+unit.Name)
				m.serviceNotice = "Started " + unit.Name
			case "stop":
				unit.State = "stopped"
				unit.Journal = append(unit.Journal, "[simulated] systemd: Stopping "+unit.Name, "[simulated] systemd: Stopped "+unit.Name)
				m.serviceNotice = "Stopped " + unit.Name
			case "restart":
				unit.State = "running"
				unit.Journal = append(unit.Journal, "[simulated] systemd: Restarting "+unit.Name, "[simulated] systemd: Started "+unit.Name)
				m.serviceNotice = "Restarted " + unit.Name
			}
			if len(unit.Journal) > 200 {
				unit.Journal = unit.Journal[len(unit.Journal)-200:]
			}
			s.Services[unitIndex] = unit
			m.journalOffset = maxInt(0, len(unit.Journal)-2)
			return
		}
	}
	m.serviceNotice = "Service is no longer available."
}
