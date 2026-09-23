package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"testing"
)

func TestCreationWizardScopesAndStartsNewSession(t *testing.T) {
	m := newApp()
	m.browser, m.variant, m.topologyProject = true, variantTopology, "forge"
	original := len(m.sessions)
	m = pressRune(t, m, 'c')
	if m.createStep != 0 || m.creationOptions()[m.createChoice] != "forge" {
		t.Fatal("creation must start at scoped project")
	}
	m = pressKey(t, m, creationAdvanceKey(m))
	if m.createStep != 1 || m.draft.Project != "forge" {
		t.Fatal("project selection did not advance to branch")
	}
	for _, b := range m.creationOptions() {
		if strings.Contains(b, "deliberately-long-name") {
			t.Fatal("another project's source leaked into branch choices")
		}
	}
	m = pressKey(t, m, creationAdvanceKey(m))
	if m.createStep != 2 || m.draft.Source != "" || m.draft.Branch != "main" || m.draft.NewBranch || len(m.sessions) != original {
		t.Fatal("branch selection must prepare, not create")
	}
	updated, cmd := m.Update(creationAdvanceKey(m))
	m = updated.(app)
	if cmd == nil || m.screen != screenBoot || len(m.sessions) != original+1 {
		t.Fatal("policy confirmation must create and start")
	}
	first := m.sessions[len(m.sessions)-1]
	if len(first.ID) != 36 || first.Project != "forge" || first.Lifecycle != "starting" {
		t.Fatalf("invalid created session: %+v", first)
	}
	for m.screen == screenBoot {
		p := m.boots[first.ID]
		updated, _ = m.Update(bootTick{first.ID, p.Generation, p.Step})
		m = updated.(app)
	}
	if m.screen != screenTerminal || m.clientAttach != first.ID {
		t.Fatal("creation boot must enter new terminal")
	}
	m.screen = screenOverview
	m.startCreate()
	for range 3 {
		m = pressKey(t, m, creationAdvanceKey(m))
	}
	second := m.sessions[len(m.sessions)-1]
	if first.ID == second.ID || first.Branch == second.Branch {
		t.Fatal("repeated creation reused identity or branch")
	}
}

func TestCreationBackAndProjectChange(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyCtrlC}, {Type: tea.KeyRunes, Runes: []rune{'q'}}} {
		m := newApp()
		m.browser = true
		m.startCreate()
		count := len(m.sessions)
		m = pressKey(t, m, creationAdvanceKey(m))
		m = pressKey(t, m, creationAdvanceKey(m))
		for _, step := range []int{1, 0} {
			m = pressKey(t, m, key)
			if m.screen != screenCreate || m.createStep != step {
				t.Fatal("back skipped a creation step")
			}
		}
		old := m.draft.Project
		for m.creationOptions()[m.createChoice] == old {
			m = pressRune(t, m, 'j')
		}
		m = pressKey(t, m, creationAdvanceKey(m))
		if m.draft.Project == old || m.draft.Source != "" || m.draft.Branch != "" {
			t.Fatal("project change retained dependent selections")
		}
		m = pressKey(t, m, key)
		m = pressKey(t, m, key)
		if m.screen != screenOverview || len(m.sessions) != count {
			t.Fatal("cancellation created a session")
		}
	}
}

func TestCreationWithoutProjects(t *testing.T) {
	m := newApp()
	m.browser = true
	m.sessions = nil
	m.startCreate()
	m = pressKey(t, m, creationAdvanceKey(m))
	if m.createStep != 0 || len(m.sessions) != 0 || !strings.Contains(m.creationView(100), "No projects") {
		t.Fatal("empty fixture must not create a fabricated project")
	}
}

func chooseCreationOption(t *testing.T, m app, choice string) app {
	t.Helper()
	for i, option := range m.creationOptions() {
		if option == choice {
			m.createChoice = i
			return pressKey(t, m, creationAdvanceKey(m))
		}
	}
	t.Fatalf("missing choice %q in %v", choice, m.creationOptions())
	return m
}

func TestNewBranchAsksForSourceThenName(t *testing.T) {
	m := newApp()
	m.browser, m.variant = true, variantTopology
	m.startCreate()
	m = chooseCreationOption(t, m, "forge")
	m = chooseCreationOption(t, m, newBranchOption)
	if m.createBranchStep != "source" || m.draft.Branch != "" {
		t.Fatal("new branch must ask for source first")
	}
	m = chooseCreationOption(t, m, "main")
	if m.createBranchStep != "name" || m.draft.Source != "main" {
		t.Fatal("source must advance to name")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("feature/logout")})
	m = pressKey(t, m, creationAdvanceKey(m))
	if m.createStep != 2 || m.draft.Source != "main" || !m.draft.NewBranch {
		t.Fatal("new branch must retain explicit source")
	}
	m = pressKey(t, m, creationAdvanceKey(m))
	if m.sessions[len(m.sessions)-1].Branch != "feature/logout" || m.screen != screenBoot {
		t.Fatal("creation ignored requested new branch")
	}
}

func TestExistingBranchDoesNotAskForSource(t *testing.T) {
	m := newApp()
	m.browser = true
	m.branches = append(m.branches, retainedBranch{Project: "forge", Name: "feature/resume", Tip: "abc123"})
	m.startCreate()
	m = chooseCreationOption(t, m, "forge")
	for _, option := range m.creationOptions() {
		if m.branchAssigned(option) {
			t.Fatal("assigned branch offered for another session")
		}
	}
	m = chooseCreationOption(t, m, "feature/resume")
	if m.createStep != 2 || m.draft.Source != "" || m.draft.NewBranch || strings.Contains(m.creationView(100), "Source") {
		t.Fatal("existing branch must go straight to policy without a source")
	}
	m = pressKey(t, m, creationAdvanceKey(m))
	if m.sessions[len(m.sessions)-1].Branch != "feature/resume" {
		t.Fatal("existing branch was replaced with another name")
	}
	for _, b := range m.branches {
		if b.Project == "forge" && b.Name == "feature/resume" {
			t.Fatal("assigned branch still listed as retained")
		}
	}
}

func TestNewBranchBackUnwindsSubsteps(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyCtrlC}, {Type: tea.KeyRunes, Runes: []rune{'q'}}} {
		m := newApp()
		m.browser = true
		m.startCreate()
		count := len(m.sessions)
		m = chooseCreationOption(t, m, "forge")
		m = chooseCreationOption(t, m, newBranchOption)
		m = chooseCreationOption(t, m, "main")
		m.createName.SetValue("feature/new")
		m = pressKey(t, m, creationAdvanceKey(m))
		for _, sub := range []string{"name", "source", ""} {
			m = pressKey(t, m, key)
			if m.screen != screenCreate || m.createStep != 1 || m.createBranchStep != sub {
				t.Fatalf("back should return to %q, got %d/%q", sub, m.createStep, m.createBranchStep)
			}
		}
		m = chooseCreationOption(t, m, "main")
		if m.draft.NewBranch || m.draft.Source != "" {
			t.Fatal("switching to existing branch retained new-branch source")
		}
		m = pressKey(t, m, key)
		m = pressKey(t, m, key)
		m = pressKey(t, m, key)
		if m.screen != screenOverview || len(m.sessions) != count {
			t.Fatal("cancelling substeps mutated sessions")
		}
	}
}

func TestCreationRejectsInvalidConflictingAndAssignedBranches(t *testing.T) {
	m := newApp()
	m.browser = true
	m.startCreate()
	m = chooseCreationOption(t, m, "forge")
	m = chooseCreationOption(t, m, newBranchOption)
	m = chooseCreationOption(t, m, "main")
	for _, name := range []string{"", "bad name", "a..b", "a.lock", "-bad", "main", "main/nested"} {
		m.createName.SetValue(name)
		m = pressKey(t, m, creationAdvanceKey(m))
		if m.createBranchStep != "name" || m.createNotice == "" {
			t.Fatalf("accepted invalid/conflicting branch %q", name)
		}
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m = chooseCreationOption(t, m, "main")
	m.sessions = append(m.sessions, session{ID: "concurrent", Project: "forge", Branch: "main"})
	count := len(m.sessions)
	m = pressKey(t, m, creationAdvanceKey(m))
	if m.screen != screenCreate || len(m.sessions) != count || m.createNotice == "" {
		t.Fatal("creation assigned a branch claimed during review")
	}
}

func TestCreationStepsKeepControlsVisible(t *testing.T) {
	m := newApp()
	m.browser, m.variant, m.width, m.height = true, variantTopology, 80, 24
	m.startCreate()
	check := func(m app) {
		t.Helper()
		view := m.View()
		if !strings.Contains(view, "Enter") || !strings.Contains(view, "Ctrl-C back") {
			t.Fatalf("creation controls hidden:\n%s", view)
		}
	}
	check(m)
	m = chooseCreationOption(t, m, "forge")
	check(m)
	m = chooseCreationOption(t, m, newBranchOption)
	check(m)
	m = chooseCreationOption(t, m, "main")
	check(m)
	m.createName.SetValue("feature/logout")
	m = pressKey(t, m, creationAdvanceKey(m))
	check(m)
}

func searchCreation(t *testing.T, m app, query string) app {
	t.Helper()
	m = pressRune(t, m, '/')
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(query)})
	return m
}

func TestCreationFuzzySelectorsAndSearchReset(t *testing.T) {
	m := newApp()
	m.browser = true
	m.startCreate()
	m = searchCreation(t, m, "frg")
	if got := m.creationOptions(); len(got) != 1 || got[0] != "forge" {
		t.Fatalf("project fuzzy matches: %v", got)
	}
	if !strings.Contains(m.creationView(100), "fuzzy search> ") {
		t.Fatal("missing search prompt")
	}
	m = pressKey(t, m, creationAdvanceKey(m))
	if m.createStep != 0 || m.createSearching {
		t.Fatal("Enter should apply search before selection")
	}
	m = pressKey(t, m, creationAdvanceKey(m))
	if m.createStep != 1 || m.createSearch.Value() != "" {
		t.Fatal("project query leaked into branches")
	}
	m.branches = append(m.branches, retainedBranch{Project: "forge", Name: "feature/login"}, retainedBranch{Project: "orbit", Name: "feature/logout"})
	m = searchCreation(t, m, "ftlgn")
	if got := m.creationOptions(); len(got) != 1 || got[0] != "feature/login" {
		t.Fatalf("branch fuzzy matches: %v", got)
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m = chooseCreationOption(t, m, newBranchOption)
	m = searchCreation(t, m, "mn")
	if got := m.creationOptions(); len(got) != 1 || got[0] != "main" {
		t.Fatalf("source fuzzy matches: %v", got)
	}
	m = pressKey(t, m, creationAdvanceKey(m))
	m = pressKey(t, m, creationAdvanceKey(m))
	if m.createBranchStep != "name" || m.draft.Source != "main" || m.createSearch.Value() != "" {
		t.Fatal("source did not clear query and advance to name")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("feature/test")})
	if m.createName.Value() != "feature/test" || m.createSearching {
		t.Fatal("branch slash opened search while naming")
	}
}

func TestCreationEmptySearchAndCancellation(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyCtrlC}, {Type: tea.KeyRunes, Runes: []rune{'q'}}} {
		m := newApp()
		m.browser = true
		m.startCreate()
		count := len(m.sessions)
		m = searchCreation(t, m, "zzzzzzz")
		if len(m.creationOptions()) != 0 || !strings.Contains(m.creationView(100), "No matches") {
			t.Fatal("missing no-match state")
		}
		m = pressKey(t, m, creationAdvanceKey(m))
		m = pressKey(t, m, creationAdvanceKey(m))
		if m.createStep != 0 || len(m.sessions) != count {
			t.Fatal("empty match selected an item")
		}
		m = pressKey(t, m, key)
		if m.screen != screenCreate || m.createSearch.Value() != "" || m.createSearching {
			t.Fatal("cancel left creation instead of clearing search")
		}
		m = pressKey(t, m, key)
		if m.screen != screenOverview {
			t.Fatal("second cancel did not leave creation")
		}
	}
}

func TestCancelCreationReturnsSilently(t *testing.T) {
	m := newApp()
	m.browser = true
	m.startCreate()
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenOverview || m.message != "" || strings.Contains(m.View(), "Cancelled") {
		t.Fatal("creation cancellation must return silently")
	}
}

func TestSessionCommandsStayWithList(t *testing.T) {
	for _, width := range []int{80, 120, 160} {
		m := newApp()
		m.browser, m.variant, m.width, m.height = true, variantTopology, width, 35
		commandRow := -1
		for index := range m.visibleIndices() {
			m.selected = index
			lines := strings.Split(m.View(), "\n")
			found := -1
			for row, line := range lines {
				if strings.Contains(line, "j/k move") {
					if found >= 0 {
						t.Fatal("duplicate list controls")
					}
					found = row
				}
			}
			if found < 0 {
				t.Fatal("missing list controls")
			}
			if commandRow >= 0 && found != commandRow {
				t.Fatalf("details moved commands at width %d: %d -> %d", width, commandRow, found)
			}
			commandRow = found
			if strings.Contains(m.View(), "frame clipped") {
				t.Fatalf("browser clipped at width %d", width)
			}
		}
	}
}

// Advance selectors with Enter, but explicitly accept the final confirmation.
func creationAdvanceKey(m app) tea.KeyMsg {
	if m.confirmationActive() {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	}
	return tea.KeyMsg{Type: tea.KeyEnter}
}
