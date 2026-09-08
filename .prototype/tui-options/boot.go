package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

var bootLines = []string{
	"Checking retained runtime and session identity",
	"Booting runtime",
	"Starting systemd",
	"Activating project environment",
	"Starting terminal host",
	"Session running; ready to enter",
}

type bootProgress struct{ Generation, Step int }
type bootTick struct {
	ID               string
	Generation, Step int
}

func (m app) lifecycleLabel(state string) string {
	if m.browser && state == "ready" {
		return "running"
	}
	return state
}

func nextBootTick(id string, progress bootProgress) tea.Cmd {
	return tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg { return bootTick{id, progress.Generation, progress.Step} })
}

func (m *app) beginBoot(id string) tea.Cmd {
	m.bootViewID, m.screen = id, screenBoot
	if m.boots == nil {
		m.boots = make(map[string]bootProgress)
	}
	if progress, ok := m.boots[id]; ok && progress.Step < len(bootLines) {
		return nil
	}
	m.bootSequence++
	progress := bootProgress{Generation: m.bootSequence}
	m.boots[id] = progress
	for i := range m.sessions {
		if m.sessions[i].ID == id {
			m.sessions[i].Lifecycle = "starting"
			m.sessions[i].Operation = bootLines[0]
		}
	}
	return nextBootTick(id, progress)
}

func (m app) updateBoot(msg bootTick) (tea.Model, tea.Cmd) {
	progress, ok := m.boots[msg.ID]
	if !ok || progress.Generation != msg.Generation || progress.Step != msg.Step || progress.Step >= len(bootLines) {
		return m, nil
	}
	for i := range m.sessions {
		if m.sessions[i].ID != msg.ID {
			continue
		}
		if m.sessions[i].Lifecycle != "starting" {
			delete(m.boots, msg.ID)
			return m, nil
		}
		progress.Step++
		m.boots[msg.ID] = progress
		if progress.Step == len(bootLines) {
			m.sessions[i].Lifecycle = "ready"
			m.sessions[i].Operation = ""
			if m.screen == screenBoot && m.bootViewID == msg.ID && m.selectSession(msg.ID) {
				cmd := m.enterTerminal()
				return m, cmd
			}
			return m, nil
		}
		m.sessions[i].Operation = bootLines[progress.Step]
		return m, nextBootTick(msg.ID, progress)
	}
	delete(m.boots, msg.ID)
	return m, nil
}

func (m app) bootView() string {
	name := m.bootViewID
	for _, s := range m.sessions {
		if s.ID == m.bootViewID {
			name = s.Project + " / " + s.Branch
			break
		}
	}
	progress := m.boots[m.bootViewID]
	rows := []string{titleStyle.Render("STARTING SESSION (" + name + ")"), mutedStyle.Render("Simulated boot log"), ""}
	for i, line := range bootLines {
		if i < progress.Step {
			rows = append(rows, styledFact("[ OK ]")+" "+line)
		} else if i == progress.Step {
			rows = append(rows, fmt.Sprintf("[ .. ] %s", line))
			break
		}
	}
	if progress.Step >= len(bootLines) {
		rows = append(rows, "", "Opening terminal · q | Esc | Ctrl-C back to sessions")
	} else {
		rows = append(rows, "", "q | Esc | Ctrl-C back to sessions · startup continues")
	}
	return strings.Join(rows, "\n")
}
