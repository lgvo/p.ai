package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestOverlayHasOneCommandBlockOutsidePanel(t *testing.T) {
	for _, place := range []screen{screenCreate, screenCreateFailed, screenProjectFilter, screenBranches, screenPolicy, screenDeletePreview, screenDeleteProgress, screenHelp, screenVariantGallery} {
		m := newApp()
		m.browser, m.variant = true, variantTopology
		m.startCreate()
		m.screen = place
		if place == screenCreate {
			m.createStep = 2
			m.draft = createDraft{Project: "orbit", Branch: "main"}
		}
		view := m.View()
		if strings.Count(view, backCommands) != 1 {
			t.Fatalf("%v must have one back block:\n%s", place, view)
		}
		if strings.Index(view, backCommands) < strings.LastIndex(view, "╰") {
			t.Fatalf("%v commands inside panel:\n%s", place, view)
		}
		if strings.Contains(view, "frame clipped") {
			t.Fatalf("%v clipped commands", place)
		}
	}
}

func TestInspectorCommandsFollowListBeforeDetails(t *testing.T) {
	for _, key := range []rune{'A', 'S'} {
		m := newApp()
		m.browser, m.variant = true, variantTopology
		m = pressRune(t, m, key)
		for range 2 {
			view := m.View()
			if strings.Count(view, backCommands) != 1 {
				t.Fatalf("%c duplicated controls:\n%s", key, view)
			}
			details := "Instance "
			if key == 'S' {
				details = "State  "
			}
			if strings.Index(view, backCommands) > strings.Index(view, details) {
				t.Fatalf("%c commands follow details instead of list", key)
			}
			m = pressRune(t, m, 'j')
		}
	}
}

func TestJournalCommandsFollowLastEntry(t *testing.T) {
	m := newApp()
	m.browser, m.variant = true, variantTopology
	m = pressRune(t, m, 'S')
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	view := m.View()
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		if strings.Contains(line, "j/k scroll") {
			if i == 0 || strings.TrimSpace(lines[i-1]) == "" {
				t.Fatal("journal controls separated from entries by padding")
			}
			if strings.Count(view, backCommands) != 1 {
				t.Fatal("journal duplicated back")
			}
			return
		}
	}
	t.Fatal("journal controls missing")
}

func TestCreationSearchUsesFirstPanelRow(t *testing.T) {
	for _, step := range []string{"project", "branch", "source"} {
		for _, width := range []int{60, 80, 120} {
			m := newApp()
			m.browser, m.variant, m.width, m.height = true, variantTopology, width, 35
			m.startCreate()
			if step != "project" {
				m = chooseCreationOption(t, m, "forge")
			}
			if step == "source" {
				m = chooseCreationOption(t, m, newBranchOption)
			}
			m = searchCreation(t, m, "m")
			for range 2 {
				view := m.View()
				lines := strings.Split(view, "\n")
				searchRow := -1
				for row, line := range lines {
					if strings.Contains(line, "fuzzy search> ") {
						if searchRow >= 0 {
							t.Fatal("duplicate search prompt")
						}
						searchRow = row
					}
				}
				if searchRow < 1 || !strings.HasPrefix(strings.TrimSpace(lines[searchRow-1]), "╭") {
					t.Fatalf("%s/%d search must occupy first row inside panel:\n%s", step, width, view)
				}
				if !strings.HasPrefix(strings.TrimSpace(lines[searchRow]), "│ fuzzy search> ") {
					t.Fatal("search prompt must be inside panel")
				}
				if strings.Index(view, backCommands) < strings.LastIndex(view, "╰") {
					t.Fatal("commands must remain below the panel")
				}
				m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
			}
		}
	}
}

func panelBottomRow(t *testing.T, m app) int {
	t.Helper()
	for row, line := range strings.Split(m.View(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "╰") {
			return row
		}
	}
	t.Fatal("missing list panel bottom")
	return -1
}

func TestFuzzySearchKeepsPanelHeight(t *testing.T) {
	for _, width := range []int{48, 80, 120} {
		for _, place := range []string{"sessions", "project", "branch", "source"} {
			m := newApp()
			m.browser, m.variant, m.width, m.height = true, variantTopology, width, 35
			if width == 48 && place == "sessions" {
				m.height = 16
			}
			if place != "sessions" {
				m.startCreate()
				if place != "project" {
					m = chooseCreationOption(t, m, "forge")
				}
				if place == "source" {
					m = chooseCreationOption(t, m, newBranchOption)
				}
			}
			bottom := panelBottomRow(t, m)
			m = pressRune(t, m, '/')
			for _, query := range []string{"", "m", "zzzzzzzzzzzz"} {
				if place == "sessions" {
					m.filter.SetValue(query)
				} else {
					m.createSearch.SetValue(query)
				}
				if got := panelBottomRow(t, m); got != bottom {
					t.Fatalf("%s %d search %q changed panel bottom %d -> %d", place, width, query, bottom, got)
				}
			}
			m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
			if panelBottomRow(t, m) != bottom {
				t.Fatal("cancelling search resized panel")
			}
		}
	}
}

func TestProjectSelectorsKeepAllFittingProjectsVisible(t *testing.T) {
	m := newApp()
	m.browser, m.variant = true, variantTopology
	m.startCreate()
	for range len(m.projects()) {
		view := m.View()
		for _, project := range m.projects() {
			if !strings.Contains(view, project) {
				t.Fatalf("creation hid fitting project %q at selection %d", project, m.createChoice)
			}
		}
		m = pressRune(t, m, 'j')
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m = pressRune(t, m, 'P')
	for range len(m.projects()) + 1 {
		view := m.View()
		for _, project := range append([]string{"All projects"}, m.projects()...) {
			if !strings.Contains(view, project) {
				t.Fatalf("project filter hid fitting project %q", project)
			}
		}
		m = pressRune(t, m, 'j')
	}
}

func TestCreationProjectWindowKeepsSelectionAndHeight(t *testing.T) {
	m := newApp()
	m.browser = true
	for i := 0; i < 15; i++ {
		m.sessions = append(m.sessions, session{Project: fmt.Sprintf("project-%02d", i), Branch: "work"})
	}
	m.startCreate()
	bottom := panelBottomRow(t, m)
	for range len(m.projects()) + 1 {
		view := m.View()
		chosen := m.creationOptions()[m.createChoice]
		if !strings.Contains(view, "› "+chosen) {
			t.Fatalf("selection %s disappeared", chosen)
		}
		if panelBottomRow(t, m) != bottom {
			t.Fatal("project window changed height")
		}
		m = pressRune(t, m, 'j')
	}
}
