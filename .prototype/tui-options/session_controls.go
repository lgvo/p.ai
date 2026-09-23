package main

import tea "github.com/charmbracelet/bubbletea"

func (m app) cancelInteraction(quitWhenIdle bool) (tea.Model, tea.Cmd) {
	if m.screen == screenProjectFilter && (m.projectSearching || m.projectSearch.Value() != "") {
		m.projectSearching = false
		m.projectSearch.SetValue("")
		m.projectSearch.Blur()
		m.projectChoice = 0
		return m, nil
	}
	if m.browser && m.screen == screenCreate && (m.createSearching || m.createSearch.Value() != "") {
		m.clearCreationSearch()
		return m, nil
	}
	if m.screen == screenJournal {
		if m.journalEditing || m.journalQuery != "" {
			m.journalEditing = false
			m.journalQuery, m.journalNotice = "", ""
			m.journalMatch = -1
			return m, nil
		}
		m.screen = screenServices
		return m, nil
	}
	if m.screen == screenStopConfirm || m.screen == screenStopping {
		m.screen = screenOverview
		m.message = ""
		return m, nil
	}
	if m.screen == screenTerminal {
		return m, nil
	}

	if m.terminalInspector && (m.screen == screenAgents || m.screen == screenServices) {
		m.terminalInspector = false
		m.terminalPrefix = false
		m.screen = screenTerminal
		return m, nil
	}
	if m.browser && m.screen == screenCreate && m.createStep > 0 {
		m.backCreation()
		return m, nil
	}
	if m.screen != screenOverview {
		creation := m.screen == screenCreate
		m.screen = screenOverview
		m.message = "Cancelled."
		if creation && m.browser {
			m.message = ""
		}
		return m, nil
	}
	if m.filtering || m.filter.Value() != "" {
		m.filtering = false
		m.filter.Blur()
		m.filter.SetValue("")
		m.selected = 0
		return m, nil
	}
	if m.browser && m.topologyProject != "" {
		m.topologyProject = ""
		m.selected = 0
		return m, nil
	}
	if quitWhenIdle {
		return m, tea.Quit
	}
	return m, nil
}

func canStop(s session) bool {
	return observationWarning(s) == "" && !s.PresenceUnknown && s.AttachedCount == 0 && (s.Lifecycle == "ready" || s.Lifecycle == "starting")
}

func (m *app) stopSelected() {
	idx, ok := m.selectedSessionIndex()
	if !ok {
		m.message = "No session selected."
		return
	}
	s := &m.sessions[idx]
	if observationWarning(*s) != "" || s.PresenceUnknown {
		m.message = "Cannot stop: refresh runtime and attachment observations first."
		return
	}
	if s.AttachedCount > 0 {
		m.message = "Cannot stop: detach all terminals from this session first."
		return
	}
	if s.Lifecycle == "stopped" {
		m.message = "Session is already stopped."
		return
	}
	if !canStop(*s) {
		m.message = "Cannot stop a session while " + s.Lifecycle + "."
		return
	}
	m.stopViewID, m.screen = s.ID, screenStopConfirm
	m.message = ""
}

func (m *app) finishStop(s *session) {
	s.Lifecycle = "stopped"
	s.Operation = ""
	s.Agents = append([]sessionProcess(nil), s.Agents...)
	s.Services = append([]sessionProcess(nil), s.Services...)
	for i := range s.Agents {
		s.Agents[i].State = "stopped"
	}
	for i := range s.Services {
		s.Services[i].State = "stopped"
	}
	m.recordInteraction(s.ID)
	delete(m.terminals, s.ID)
	delete(m.boots, s.ID)
	m.message = ""
}
