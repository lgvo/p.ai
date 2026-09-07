package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m app) cardsView(width, _ int) string {
	indices := m.visibleIndices()
	columns, pageSize := 1, 2
	if width >= 132 {
		columns, pageSize = 3, 6
	} else if width >= 100 {
		columns, pageSize = 2, 4
	}
	heading := titleStyle.Render("Responsive session cards") + fmt.Sprintf("  ·  %d columns at %d cells", columns, width) + "\n" +
		mutedStyle.Render(truncate("Identity, four independent facts, and eligible actions travel together as cards reflow.", width))
	if len(indices) == 0 {
		return heading + "\n\n" + mutedStyle.Render("No session cards in this fixture.")
	}
	start := (m.selected / pageSize) * pageSize
	end := start + pageSize
	if end > len(indices) {
		end = len(indices)
	}
	outerWidth := (width - (columns - 1)) / columns
	var rows []string
	for rowStart := start; rowStart < end; rowStart += columns {
		var row []string
		rowEnd := rowStart + columns
		if rowEnd > end {
			rowEnd = end
		}
		for position := rowStart; position < rowEnd; position++ {
			s := m.sessions[indices[position]]
			row = append(row, m.sessionCard(s, outerWidth, position == m.selected))
			if position != rowEnd-1 {
				row = append(row, " ")
			}
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, row...))
	}
	pager := fmt.Sprintf("cards %d–%d of %d · j/k moves and turns pages", start+1, end, len(indices))
	return heading + "\n\n" + strings.Join(rows, "\n") + "\n" + mutedStyle.Render(pager)
}

func (m app) sessionCard(s session, outerWidth int, selected bool) string {
	innerWidth := maxInt(8, outerWidth-panelStyle.GetHorizontalFrameSize())
	presence, agent := sessionSignals(s)
	lines := []string{
		truncate(s.Project+" / "+s.Branch, innerWidth),
		mutedStyle.Render(truncate("UUID "+s.ID, innerWidth)),
		fmt.Sprintf("life %-11s presence %s", s.Lifecycle, presence),
		fmt.Sprintf("agent %-10s policy %s", agent, s.Policy),
		"actions " + strings.Join(availableActions(s), " · "),
	}
	if s.Operation != "" {
		lines = append(lines, "progress "+s.Operation)
	}
	content := strings.Join(lines, "\n")
	style := panelStyle.Copy()
	if selected {
		style = style.BorderForeground(accent).Bold(true)
	}
	frame := style.GetHorizontalFrameSize()
	return style.Width(outerWidth - frame).MaxWidth(outerWidth).Render(content)
}
