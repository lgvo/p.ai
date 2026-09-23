package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func (m app) journalUnit() (sessionProcess, bool) {
	s, ok := m.servicesSession()
	if !ok {
		return sessionProcess{}, false
	}
	for _, unit := range projectServices(s) {
		if unit.Name == m.journalUnitName {
			return unit, true
		}
	}
	return sessionProcess{}, false
}

func (m app) journalCapacity() int { return maxInt(1, m.height-5) }

func (m *app) openJournal(name string) {
	m.journalUnitName = name
	m.journalFollow = true
	m.journalEditing = false
	m.journalColumn, m.journalMatch = 0, -1
	m.journalQuery, m.journalNotice = "", ""
	m.screen = screenJournal
}

func (m *app) updateJournal(key tea.KeyMsg) {
	unit, ok := m.journalUnit()
	if !ok {
		return
	}
	if m.journalEditing {
		switch key.String() {
		case "enter":
			m.journalEditing = false
			m.journalMatch = -1
			m.findJournal(1)
		case "backspace", "ctrl+h":
			runes := []rune(m.journalQuery)
			if len(runes) > 0 {
				m.journalQuery = string(runes[:len(runes)-1])
			}
		case " ":
			if len(m.journalQuery) < 128 {
				m.journalQuery += " "
			}
		default:
			if key.Type == tea.KeyRunes {
				for _, r := range key.Runes {
					if r >= 32 && r != 127 && len(m.journalQuery) < 128 {
						m.journalQuery += string(r)
					}
				}
			}
		}
		return
	}
	end := maxInt(0, len(unit.Journal)-m.journalCapacity())
	if m.journalFollow {
		m.journalOffset = end
	}
	switch key.String() {
	case "j", "down":
		m.journalFollow = false
		m.journalOffset++
	case "k", "up":
		m.journalFollow = false
		m.journalOffset--
	case "ctrl+d":
		m.journalFollow = false
		m.journalOffset += max(1, m.journalCapacity()/2)
	case "ctrl+u":
		m.journalFollow = false
		m.journalOffset -= max(1, m.journalCapacity()/2)
	case "pgdown", "ctrl+f":
		m.journalFollow = false
		m.journalOffset += m.journalCapacity()
	case "pgup", "ctrl+b":
		m.journalFollow = false
		m.journalOffset -= m.journalCapacity()
	case "g", "home":
		m.journalFollow = false
		m.journalOffset = 0
	case "G", "end":
		m.journalFollow = true
		m.journalOffset = end
	case "f":
		m.journalFollow = !m.journalFollow
	case "h", "left":
		m.journalColumn = maxInt(0, m.journalColumn-8)
	case "l", "right":
		widest := 0
		for _, line := range unit.Journal {
			widest = maxInt(widest, ansi.StringWidth(line))
		}
		m.journalColumn += 8
		if m.journalColumn > widest {
			m.journalColumn = widest
		}
	case "/":
		m.journalEditing = true
		m.journalQuery = ""
		m.journalNotice = ""
	case "n":
		m.findJournal(1)
	case "N":
		m.findJournal(-1)
	}
	if m.journalOffset < 0 {
		m.journalOffset = 0
	}
	if m.journalOffset > end {
		m.journalOffset = end
	}
}

func (m *app) findJournal(direction int) {
	unit, ok := m.journalUnit()
	if !ok || m.journalQuery == "" {
		return
	}
	matches := []int{}
	for i, line := range unit.Journal {
		if strings.Contains(strings.ToLower(line), strings.ToLower(m.journalQuery)) {
			matches = append(matches, i)
		}
	}
	if len(matches) == 0 {
		m.journalMatch = -1
		m.journalNotice = "No matches: " + m.journalQuery
		return
	}
	choice := 0
	if direction > 0 {
		for i, line := range matches {
			if line > m.journalMatch {
				choice = i
				break
			}
		}
	} else {
		choice = len(matches) - 1
		for i := len(matches) - 1; i >= 0; i-- {
			if matches[i] < m.journalMatch {
				choice = i
				break
			}
		}
	}
	m.journalMatch = matches[choice]
	m.journalOffset = m.journalMatch
	m.journalFollow = false
	m.journalColumn = 0
	end := maxInt(0, len(unit.Journal)-m.journalCapacity())
	if m.journalOffset > end {
		m.journalOffset = end
	}
	m.journalNotice = fmt.Sprintf("match %d/%d: %s", choice+1, len(matches), m.journalQuery)
}

func (m app) journalView() string {
	unit, ok := m.journalUnit()
	if !ok {
		return "Service journal unavailable\nq | Esc | Ctrl-C back"
	}
	capacity := m.journalCapacity()
	start := m.journalOffset
	if m.journalFollow {
		start = maxInt(0, len(unit.Journal)-capacity)
	}
	if start > len(unit.Journal) {
		start = len(unit.Journal)
	}
	end := start + capacity
	if end > len(unit.Journal) {
		end = len(unit.Journal)
	}
	first := start + 1
	if len(unit.Journal) == 0 {
		first = 0
	}
	follow := "paused"
	if m.journalFollow {
		follow = "follow"
	}
	status := fmt.Sprintf("Recorded · %d–%d/%d · %s · col %d", first, end, len(unit.Journal), follow, m.journalColumn+1)
	if m.journalNotice != "" {
		status = m.journalNotice
	}
	rows := []string{titleStyle.Render("Journal · " + unit.Name), mutedStyle.Render(status)}
	for i := start; i < end; i++ {
		prefix := fmt.Sprintf("%4d ", i+1)
		line := prefix + ansi.Cut(unit.Journal[i], m.journalColumn, m.journalColumn+maxInt(1, m.width-len(prefix)))
		if i == m.journalMatch {
			line = selectedStyle.Render(line)
		}
		rows = append(rows, line)
	}
	if len(unit.Journal) == 0 {
		rows = append(rows, "No journal entries recorded.")
	}
	controls := "j/k scroll · PgUp/PgDn · gg/G top/end"
	if m.journalEditing {
		controls = "find> " + m.journalQuery + "█"
	}
	rows = append(rows, commandBlock(m.width, controls+"\n"+"h/l pan · / find · n/N match · f follow\n"+backCommands))
	return strings.Join(rows, "\n")
}
