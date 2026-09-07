package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m app) matrixView(width, _ int) string {
	indices := m.visibleIndices()
	attached := 0
	for _, idx := range indices {
		attached += m.sessions[idx].AttachedCount
	}
	summary := titleStyle.Render("Project × intervention matrix") +
		fmt.Sprintf("  ·  %d sessions  ·  attached %d", len(indices), attached) + "\n" +
		mutedStyle.Render("Columns answer where attention, routine work, recovery, and removal accumulate.") + "\n\n" +
		m.matrixRows(width >= 115, matrixProjectLimit(width))
	if width >= 115 {
		matrixWidth := width * 68 / 100
		return lipgloss.JoinHorizontal(lipgloss.Top, renderPanel(matrixWidth, summary), " ", renderPanel(width-matrixWidth-1, m.detailView()))
	}
	return renderPanel(width, summary+"\n\n"+m.matrixCompactDetail())
}

func (m app) matrixCompactDetail() string {
	idx, ok := m.selectedSessionIndex()
	if !ok {
		return mutedStyle.Render("No session selected")
	}
	s := m.sessions[idx]
	presence, agent := sessionSignals(s)
	result := titleStyle.Render("Selected cell") + "  " + truncate(s.Project+" / "+s.Branch, 38) +
		"\n" + strings.Join([]string{s.Lifecycle, presence, agent, s.Policy}, " · ") +
		"\nactions  " + strings.Join(availableActions(s), " · ")
	if s.Operation != "" {
		result += "\nprogress " + s.Operation
	}
	return result
}

func matrixProjectLimit(width int) int {
	if width >= 115 {
		return 10
	}
	return 4
}

func (m app) matrixRows(expanded bool, limit int) string {
	projects := m.projects()
	if len(projects) == 0 {
		return mutedStyle.Render("No projects to place in the matrix.")
	}
	if len(projects) <= 5 {
		limit = len(projects)
	}
	selectedIndex, selected := m.selectedSessionIndex()
	projectCursor := 0
	if selected {
		selectedProject := m.sessions[selectedIndex].Project
		for i, project := range projects {
			if project == selectedProject {
				projectCursor = i
				break
			}
		}
	}
	start, end := windowBounds(len(projects), projectCursor, limit)
	if !expanded {
		rows := []string{mutedStyle.Render("  PROJECT           INSPECT   WORK   RECOVER   REMOVE")}
		if start > 0 {
			rows = append(rows, mutedStyle.Render(fmt.Sprintf("  … %d projects above", start)))
		}
		for _, project := range projects[start:end] {
			counts := [4]int{}
			marker := " "
			for i, s := range m.sessions {
				if s.Project == project {
					counts[laneFor(s)]++
					if selected && i == selectedIndex {
						marker = "›"
					}
				}
			}
			rows = append(rows, fmt.Sprintf("%s %s %7d %6d %9d %8d", marker, padRight(middleTruncate(project, 16), 16), counts[0], counts[1], counts[2], counts[3]))
		}
		if end < len(projects) {
			rows = append(rows, mutedStyle.Render(fmt.Sprintf("  … %d projects below", len(projects)-end)))
		}
		return strings.Join(rows, "\n")
	}
	rows := []string{mutedStyle.Render("  PROJECT     INSPECT       WORK          RECOVER       REMOVE")}
	if start > 0 {
		rows = append(rows, mutedStyle.Render(fmt.Sprintf("  … %d projects above", start)))
	}
	for _, project := range projects[start:end] {
		cells := [4]string{"—", "—", "—", "—"}
		counts := [4]int{}
		marker := " "
		for i, s := range m.sessions {
			if s.Project != project {
				continue
			}
			lane := laneFor(s)
			counts[lane]++
			cell := truncate(s.Branch, 12)
			if cells[lane] != "—" {
				cell = truncate(fmt.Sprintf("%s +%d", cells[lane], counts[lane]-1), 12)
			}
			cells[lane] = cell
			if selected && i == selectedIndex {
				marker = "›"
			}
		}
		rows = append(rows, fmt.Sprintf("%s %s %s %s %s %s", marker, padRight(middleTruncate(project, 10), 10), padRight(cells[0], 13), padRight(cells[1], 13), padRight(cells[2], 13), cells[3]))
	}
	if end < len(projects) {
		rows = append(rows, mutedStyle.Render(fmt.Sprintf("  … %d projects below", len(projects)-end)))
	}
	return strings.Join(rows, "\n")
}
