package main

import (
	"fmt"
	"strings"
)

func (m app) minimalView(width, height int) string {
	indices := m.visibleIndices()
	attached := 0
	for _, idx := range indices {
		attached += m.sessions[idx].AttachedCount
	}
	limit := 6
	if height >= 35 {
		limit = 12
	}
	rows := []string{
		titleStyle.Render("Stream ledger") + fmt.Sprintf("  %d streams · attached %d", len(indices), attached),
		mutedStyle.Render(truncate("One reading axis; the current row expands in place. No panel boundary carries meaning.", width)),
		"",
	}
	if len(indices) == 0 {
		rows = append(rows, mutedStyle.Render("No streams in this fixture."))
		return strings.Join(rows, "\n")
	}
	start := m.selected - limit/2
	if start < 0 {
		start = 0
	}
	if start+limit > len(indices) {
		start = maxInt(0, len(indices)-limit)
	}
	end := start + limit
	if end > len(indices) {
		end = len(indices)
	}
	if start > 0 {
		rows = append(rows, mutedStyle.Render(fmt.Sprintf("  … %d earlier streams", start)))
	}
	for position := start; position < end; position++ {
		s := m.sessions[indices[position]]
		presence, agent := sessionSignals(s)
		if position == m.selected {
			nameWidth := maxInt(18, width-8)
			rows = append(rows,
				selectedStyle.Render("› "+truncate(s.Project+" / "+s.Branch, nameWidth)),
				"    "+s.ID,
				fmt.Sprintf("    %s · %s · %s · %s", s.Lifecycle, presence, agent, s.Policy),
				"    actions  "+strings.Join(availableActions(s), " · "),
			)
			if s.Operation != "" {
				rows = append(rows, "    progress "+truncate(s.Operation, maxInt(20, width-13)))
			}
			continue
		}
		line := fmt.Sprintf("  %s %s · %s · %s · %s", padRight(s.Project+" / "+s.Branch, 31), s.Lifecycle, presence, agent, s.Policy)
		rows = append(rows, truncate(line, width))
	}
	if end < len(indices) {
		rows = append(rows, mutedStyle.Render(fmt.Sprintf("  … %d later streams", len(indices)-end)))
	}
	return strings.Join(rows, "\n")
}
