package main

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"strings"
	"testing"
)

func TestProjectCountColumnsAlign(t *testing.T) {
	m, _ := newAppForDataset("portfolio")
	for _, width := range []int{42, 74, 90} {
		positions := map[string]int{}
		for i, item := range m.projectFilterItems(width) {
			row := ansi.Strip(item.text)
			if lipgloss.Width(row) > width {
				t.Fatalf("row overflow at %d: %s", width, row)
			}
			for _, marker := range []string{"!", " · ", " * "} {
				pos := lipgloss.Width(row[:strings.Index(row, marker)])
				if i == 0 {
					positions[marker] = pos
				} else if positions[marker] != pos {
					t.Fatalf("misaligned %s at width %d", marker, width)
				}
			}
		}
	}
}

func TestSelectorCardsCenteredAndBounded(t *testing.T) {
	for _, place := range []screen{screenProjectFilter, screenCreate} {
		m, _ := newAppForDataset("portfolio")
		m.browser, m.variant = true, variantTopology
		m.width, m.height = 280, 45
		m.startCreate()
		m.screen = place
		lines := strings.Split(ansi.Strip(m.View()), "\n")
		for _, line := range lines {
			if start := strings.Index(line, "╭"); start >= 0 {
				card := strings.TrimSpace(line)
				if lipgloss.Width(card) > selectorMaxWidth || start < 80 {
					t.Fatalf("uncentered or unbounded card: %s", line)
				}
				returnCheck := (m.width - lipgloss.Width(card)) / 2
				if start < returnCheck-1 || start > returnCheck+1 {
					t.Fatal("unequal card margins")
				}
				break
			}
		}
	}
}

func TestAllScreensShareBoundedViewport(t *testing.T) {
	for _, place := range []screen{screenOverview, screenCreate, screenCreateFailed, screenBranches, screenPolicy, screenDeletePreview, screenDeleteProgress, screenHelp, screenVariantGallery, screenProjectFilter, screenTerminal, screenBoot, screenServices, screenAgents, screenJournal, screenStopConfirm, screenStopping} {
		m := newApp()
		m.browser, m.variant = true, variantTopology
		m.width, m.height, m.screen = 280, 45, place
		m.clientAttach, m.bootViewID, m.stopViewID = m.sessions[0].ID, m.sessions[0].ID, m.sessions[0].ID
		m.agentsSessionID, m.servicesSessionID = m.sessions[0].ID, m.sessions[0].ID
		margin := (m.width - m.viewportWidth()) / 2
		view := ansi.Strip(m.View())
		for _, line := range strings.Split(view, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			leading := len(line) - len(strings.TrimLeft(line, " "))
			if leading < margin {
				t.Fatalf("screen %v escaped centered viewport", place)
			}
			if lipgloss.Width(strings.TrimRight(line, " ")) > margin+m.viewportWidth()+1 {
				t.Fatalf("screen %v overflowed viewport", place)
			}
		}
	}
}

func TestTerminalUsesFullWidthAndInspectorsRemainBounded(t *testing.T) {
	m := newApp()
	m.browser, m.width, m.height = true, 280, 45
	m.screen, m.clientAttach = screenTerminal, m.sessions[0].ID
	if m.viewportWidth() != 280 {
		t.Fatal("terminal must use full width")
	}
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	if !strings.HasPrefix(lines[0], "TERMINAL SESSION") || len(lines) != 45 || lipgloss.Width(lines[44]) != 280 {
		t.Fatal("terminal or status bar is inset")
	}
	m.screen = screenAgents
	if m.viewportWidth() != selectorMaxWidth {
		t.Fatal("inspector lost its bounded layout")
	}
}
