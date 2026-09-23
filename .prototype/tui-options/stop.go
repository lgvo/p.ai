package main

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var stopLines = []string{
	"Stopping agent processes",
	"Stopping project services",
	"Closing terminal host",
	"Shutting down container",
	"Container stopped; branch and files retained",
}

type stopTick struct {
	ID               string
	Generation, Step int
}

func nextStopTick(id string, p bootProgress) tea.Cmd {
	return tea.Tick(450*time.Millisecond, func(time.Time) tea.Msg { return stopTick{id, p.Generation, p.Step} })
}

func (m *app) beginStop() tea.Cmd {
	for i := range m.sessions {
		s := &m.sessions[i]
		if s.ID != m.stopViewID {
			continue
		}
		// Confirm the captured identity, and recheck preconditions at confirmation.
		if !canStop(*s) {
			m.screen, m.message = screenOverview, "Cannot stop: session conditions changed."
			return nil
		}
		m.stopSequence++
		p := bootProgress{Generation: m.stopSequence}
		if m.stops == nil {
			m.stops = map[string]bootProgress{}
		}
		m.stops[s.ID] = p
		delete(m.boots, s.ID)
		s.Lifecycle, s.Operation = "stopping", stopLines[0]
		m.selectSession(s.ID)
		m.screen = screenStopping
		return nextStopTick(s.ID, p)
	}
	m.screen = screenOverview
	return nil
}

func (m app) updateStop(msg stopTick) (tea.Model, tea.Cmd) {
	p, ok := m.stops[msg.ID]
	if !ok || p.Generation != msg.Generation || p.Step != msg.Step {
		return m, nil
	}
	if p.Step == len(stopLines) {
		delete(m.stops, msg.ID)
		if m.screen == screenStopping && m.stopViewID == msg.ID {
			m.screen = screenOverview
		}
		return m, nil
	}
	selectedID := ""
	if i, ok := m.selectedSessionIndex(); ok {
		selectedID = m.sessions[i].ID
	}
	for i := range m.sessions {
		s := &m.sessions[i]
		if s.ID != msg.ID {
			continue
		}
		if s.Lifecycle != "stopping" {
			delete(m.stops, msg.ID)
			return m, nil
		}
		p.Step++
		m.stops[msg.ID] = p
		if p.Step == 1 {
			s.Agents = append([]sessionProcess(nil), s.Agents...)
			for a := range s.Agents {
				s.Agents[a].State = "stopped"
			}
		}
		if p.Step == 2 {
			s.Services = append([]sessionProcess(nil), s.Services...)
			for a := range s.Services {
				s.Services[a].State = "stopped"
			}
		}
		if p.Step == 3 {
			delete(m.terminals, s.ID)
		}
		if p.Step == len(stopLines) {
			m.finishStop(s)
		} else {
			s.Operation = stopLines[p.Step]
		}
		if selectedID != "" {
			m.selectSession(selectedID)
		}
		return m, nextStopTick(msg.ID, p)
	}
	delete(m.stops, msg.ID)
	return m, nil
}

func (m app) stopView() string {
	name := m.stopViewID
	for _, s := range m.sessions {
		if s.ID == m.stopViewID {
			name = sessionDisplayName(s)
			break
		}
	}
	width := minInt(m.width, 76)
	rows := []string{titleStyle.Render("Stop session?"), name, "", "Ends agents, services, and the terminal.", "Branch and files are retained."}
	rows = append(rows, "", confirmationPrompt("Stop session?", true))
	commands := backCommands
	if m.screen == screenStopping {
		rows = []string{titleStyle.Render("STOPPING CONTAINER"), name, mutedStyle.Render("Simulated shutdown log"), ""}
		p := m.stops[m.stopViewID]
		for i, line := range stopLines {
			if i < p.Step {
				rows = append(rows, styledFact("[ OK ]")+" "+line)
			} else if i == p.Step {
				rows = append(rows, "[ .. ] "+line)
			} else {
				rows = append(rows, "")
			}
		}
		commands = backCommands + " · shutdown continues"
		if p.Step == len(stopLines) {
			commands = "Returning to sessions · " + backCommands
		}
	}
	body := renderPanel(width, strings.Join(rows, "\n")) + "\n" + commandBlock(width, commands)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, body)
}
