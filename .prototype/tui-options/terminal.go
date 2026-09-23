package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type fakeTerminal struct {
	Input []rune
	Lines []string
}

func (m *app) enterTerminal() tea.Cmd {
	idx, ok := m.selectedSessionIndex()
	if !ok {
		return nil
	}
	s := &m.sessions[idx]
	if observationWarning(*s) != "" || (s.Lifecycle != "ready" && s.Lifecycle != "stopped" && s.Lifecycle != "starting") {
		m.message = "Session is not available to enter."
		return nil
	}
	if s.Lifecycle == "stopped" || s.Lifecycle == "starting" {
		if s.Policy == "invalid" {
			m.message = "Cannot start: project policy is invalid."
			return nil
		}
		return m.beginBoot(s.ID)
	}
	m.attachSelected()
	if m.clientAttach != s.ID {
		return nil
	}
	if m.terminals == nil {
		m.terminals = make(map[string]fakeTerminal)
	}
	if _, exists := m.terminals[s.ID]; !exists {
		m.terminals[s.ID] = fakeTerminal{Lines: []string{"Simulated terminal — commands are not executed.", "Type anything and press Enter."}}
	}
	m.recordInteraction(s.ID)
	m.terminalPrefix = false
	m.screen = screenTerminal
	return nil
}

func (m app) updateTerminal(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	terminal := m.terminals[m.clientAttach]
	if m.terminalPrefix {
		m.terminalPrefix = false
		if key.String() == "d" || key.String() == "D" {
			for i := range m.sessions {
				if m.sessions[i].ID == m.clientAttach && m.sessions[i].AttachedCount > 0 {
					m.sessions[i].AttachedCount--
				}
			}
			m.clientAttach = ""
			m.screen = screenOverview
			m.message = ""
			return m, nil
		}
		if key.String() == "a" || key.String() == "A" {
			m.agentsSessionID = m.clientAttach
			m.agentChoice, m.agentPreviewOffset = 0, 0
			m.terminalInspector = true
			m.screen = screenAgents
			return m, nil
		}
		if key.String() == "s" || key.String() == "S" {
			m.servicesSessionID = m.clientAttach
			m.serviceChoice, m.journalOffset = 0, 0
			m.serviceNotice = ""
			m.terminalInspector = true
			m.screen = screenServices
			return m, nil
		}
		// Other prefix keys cancel the prefix without reaching terminal input.
		return m, nil
	}
	switch key.String() {
	case "ctrl+b":
		m.terminalPrefix = true
	case "ctrl+c":
		terminal.Input = nil
		terminal.Lines = append(terminal.Lines, "^C")
	case "enter":
		terminal.Lines = append(terminal.Lines, "> "+string(terminal.Input), "[simulated] Input received.")
		terminal.Input = nil
	case "backspace", "ctrl+h":
		if len(terminal.Input) > 0 {
			terminal.Input = terminal.Input[:len(terminal.Input)-1]
		}
	default:
		if key.Type == tea.KeyRunes && len(terminal.Input) < 512 {
			for _, r := range key.Runes {
				if r >= 32 && r != 127 && len(terminal.Input) < 512 {
					terminal.Input = append(terminal.Input, r)
				}
			}
		} else if key.Type == tea.KeySpace && len(terminal.Input) < 512 {
			terminal.Input = append(terminal.Input, ' ')
		}
	}
	if len(terminal.Lines) > 200 {
		terminal.Lines = terminal.Lines[len(terminal.Lines)-200:]
	}
	m.terminals[m.clientAttach] = terminal
	return m, nil
}

func (m app) terminalView() string {
	name := m.clientAttach
	for _, s := range m.sessions {
		if s.ID == m.clientAttach {
			name = s.Project + " / " + s.Branch
			break
		}
	}
	terminal := m.terminals[m.clientAttach]
	rows := []string{titleStyle.Render(fmt.Sprintf("TERMINAL SESSION (%s)", name)), ""}
	limit := maxInt(0, m.height-6)
	start := maxInt(0, len(terminal.Lines)-limit)
	rows = append(rows, terminal.Lines[start:]...)
	rows = append(rows, "> "+string(terminal.Input)+"█", "")
	// The session bar always occupies the last physical terminal row.
	height := maxInt(1, m.height)
	if len(rows) > height-1 {
		rows = rows[:height-1]
	}
	for len(rows) < height-1 {
		rows = append(rows, "")
	}
	rows = append(rows, m.sessionBar())
	if m.terminalPrefix {
		rows = m.terminalPopup(rows)
	}
	return strings.Join(rows, "\n")
}

func (m app) sessionBar() string {
	width := maxInt(1, m.width)
	hint := "Ctrl+B actions"
	if m.terminalPrefix {
		hint = "D detach · A agents · S services"
	}
	if width < 40 {
		return selectedStyle.Render(padRight(truncate(hint, width), width))
	}
	var current session
	for _, s := range m.sessions {
		if s.ID == m.clientAttach {
			current = s
			break
		}
	}
	fields := m.sessionBarFields
	if fields == nil {
		fields = []string{"project", "branch", "status"}
	}
	values := map[string]string{"project": current.Project, "branch": current.Branch, "status": m.lifecycleLabel(current.Lifecycle), "id": current.ID}
	mode := titleStyle.Copy().Background(panelEdge).Render(" IN SESSION ")
	remaining := width - lipgloss.Width(mode) - len(hint) - 2
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		if values[field] != "" {
			parts = append(parts, values[field])
		}
	}
	identity := middleTruncate(strings.Join(parts, " ▸ "), maxInt(0, remaining))
	middle := selectedStyle.Render(padRight(identity, maxInt(0, remaining)))
	return mode + middle + selectedStyle.Render(" "+hint+" ")
}

func (m app) terminalPopup(rows []string) []string {
	width := maxInt(1, m.width)
	popupWidth := 32
	if popupWidth > width {
		popupWidth = width
	}
	popup := renderPanel(popupWidth, strings.Join([]string{
		titleStyle.Render("Session actions"),
		"[D]etach", "[A]gents", "[S]ervices", mutedStyle.Render("Esc cancel"),
	}, "\n"))
	lines := strings.Split(popup, "\n")
	x := maxInt(0, (width-popupWidth)/2)
	y := maxInt(0, (len(rows)-1-len(lines))/2)
	for i, line := range lines {
		if y+i >= len(rows)-1 {
			break
		}
		base := padRight(rows[y+i], width)
		rows[y+i] = ansi.Cut(base, 0, x) + line + ansi.Cut(base, x+lipgloss.Width(line), width)
	}
	return rows
}
