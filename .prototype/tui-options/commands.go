package main

import (
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
	"strings"
)

const backCommands = "q | Esc | Ctrl-C back"

func commandBlock(width int, actions string) string {
	return lipgloss.NewStyle().Width(maxInt(1, width)).Render(mutedStyle.Render(actions))
}

func (m app) overlayCommands(width int) string {
	actions := ""
	switch m.screen {
	case screenCreate:
		if m.browser {
			actions = m.creationCommands()
		} else {
			actions = ""
		}
	case screenCreateFailed:
		actions = "r Exact Retry · t Try again with changes"
	case screenProjectFilter:
		actions = "j/k choose · Enter apply · / search\nPgUp/PgDn · ^B/^F page · ^U/^D half · gg/G ends"
		if m.projectSearching {
			actions = "Type to filter · ↑/↓ move · Enter apply"
		}
	case screenVariantGallery:
		actions = "j/k choose · Enter open"
	case screenBranches:
		actions = "n new session"
	case screenDeletePreview:
		actions = ""
	case screenDeleteProgress:
		actions = "r retry remaining deletion"
	}
	if actions != "" {
		actions += "\n"
	}
	return commandBlock(width, actions+backCommands)
}

// Every fuzzy selector uses the same prompt rendering in the first row of its list panel.
func fuzzySearchPrompt(input textinput.Model, editing bool, width int) string {
	if !editing && input.Value() == "" {
		return ""
	}
	input.Prompt = "fuzzy search> "
	input.Placeholder = ""
	input.Width = maxInt(1, width-len(input.Prompt)-1)
	if editing {
		return truncate(input.View(), width)
	}
	return truncate(input.Prompt+input.Value(), width)
}

func padContentRows(content string, height int) string {
	rows := strings.Split(content, "\n")
	for len(rows) < height {
		rows = append(rows, "")
	}
	return strings.Join(rows, "\n")
}

func (m app) confirmationActive() bool {
	return m.screen == screenStopConfirm || m.screen == screenDeletePreview ||
		(m.screen == screenCreate && (!m.browser || m.createStep == 2))
}

func confirmationPrompt(action string, destructive bool) string {
	color := warning
	if destructive {
		color = urgent
	}
	return lipgloss.NewStyle().Bold(true).Foreground(color).Render(action+" [y/N]") +
		" " + mutedStyle.Render("Enter = No")
}
