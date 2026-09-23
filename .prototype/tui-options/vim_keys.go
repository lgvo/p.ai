package main

import (
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
)

// Only browsing contexts interpret Vim keys. Terminal input and text fields
// reach their own handlers unchanged, including the terminal's Ctrl+B prefix.
func (m app) vimContext() string {
	if !m.browser || m.filtering || m.createSearching || m.projectSearching || m.journalEditing {
		return ""
	}
	switch m.screen {
	case screenOverview:
		if m.variant == variantTopology {
			return "sessions"
		}
	case screenCreate:
		if m.creationSearchable() {
			return fmt.Sprintf("create/%d/%s", m.createStep, m.createBranchStep)
		}
	case screenProjectFilter:
		return "projects"
	case screenServices:
		return "services"
	case screenAgents:
		return "agents"
	case screenJournal:
		return "journal"
	}
	return ""
}
func (m *app) vimKey(msg tea.KeyMsg) (tea.KeyMsg, bool) {
	context := m.vimContext()
	if context == "" {
		m.vimPending = ""
		return msg, false
	}
	if msg.String() == "g" {
		if m.vimPending == context {
			m.vimPending = ""
			return tea.KeyMsg{Type: tea.KeyHome}, false
		}
		m.vimPending = context
		return msg, true
	}
	m.vimPending = ""
	switch msg.String() {
	case "G":
		return tea.KeyMsg{Type: tea.KeyEnd}, false
	case "ctrl+b":
		return tea.KeyMsg{Type: tea.KeyPgUp}, false
	case "ctrl+f":
		return tea.KeyMsg{Type: tea.KeyPgDown}, false
	}
	return msg, false
}
