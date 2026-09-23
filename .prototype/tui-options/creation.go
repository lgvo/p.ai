package main

import (
	"crypto/rand"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

const newBranchOption = "Create new branch…"

// main is a committed fixture branch; this prototype does not query Git.
func (m app) creationBranches() []string {
	seen := map[string]bool{"main": true}
	for _, s := range m.sessions {
		if s.Project == m.draft.Project && s.Lifecycle != "creating" {
			seen[s.Branch] = true
		}
	}
	for _, b := range m.branches {
		if b.Project == m.draft.Project {
			seen[b.Name] = true
		}
	}
	result := []string{"main"}
	var rest []string
	for name := range seen {
		if name != "main" {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	return append(result, rest...)
}

func (m app) branchAssigned(name string) bool {
	for _, s := range m.sessions {
		if s.Project == m.draft.Project && s.Branch == name {
			return true
		}
	}
	return false
}

func (m app) unfilteredCreationOptions() []string {
	switch m.createStep {
	case 0:
		return m.projects()
	case 1:
		if m.createBranchStep == "source" {
			return m.creationBranches()
		}
		if m.createBranchStep == "name" {
			return nil
		}
		var available []string
		for _, name := range m.creationBranches() {
			if !m.branchAssigned(name) {
				available = append(available, name)
			}
		}
		return append(available, newBranchOption)
	default:
		return []string{"Current project policy"}
	}
}

func (m app) creationSearchable() bool {
	return m.createStep == 0 || (m.createStep == 1 && m.createBranchStep != "name")
}

func (m app) creationOptions() []string {
	options := m.unfilteredCreationOptions()
	if !m.creationSearchable() || m.createSearch.Value() == "" {
		return options
	}
	selector := newSelector(stringItems(options), m.createSearch.Value(), 0, 1, 1)
	filtered := make([]string, 0, len(options))
	for _, idx := range selector.indices() {
		filtered = append(filtered, options[idx])
	}
	return filtered
}

func (m *app) clearCreationSearch() {
	m.createSearch.SetValue("")
	m.createSearch.Blur()
	m.createSearching = false
	m.createChoice = 0
}

func (m *app) startCreationWizard() {
	m.draft = createDraft{}
	m.createSearch = textinput.New()
	m.createSearch.Prompt = "fuzzy search> "
	m.createSearch.CharLimit = 120
	m.createSearch.Width = 32
	m.clearCreationSearch()
	m.createStep, m.createChoice = 0, 0
	m.createBranchStep, m.createNotice = "", ""
	m.createName = textinput.New()
	m.createName.Prompt = "Branch name> "
	m.createName.Placeholder = "feature/login"
	m.createName.CharLimit = 120
	m.createName.Width = 32
	m.screen, m.message = screenCreate, ""
	project := m.topologyProject
	if project == "" {
		if i, ok := m.selectedSessionIndex(); ok {
			project = m.sessions[i].Project
		}
	}
	for i, p := range m.creationOptions() {
		if p == project {
			m.createChoice = i
		}
	}
}

func (m *app) backCreation() {
	m.createNotice = ""
	m.clearCreationSearch()
	switch {
	case m.createStep == 2 && m.draft.NewBranch:
		m.createStep, m.createBranchStep = 1, "name"
		m.createName.Focus()
	case m.createStep == 2:
		m.createStep, m.createBranchStep = 1, ""
	case m.createBranchStep == "name":
		m.createBranchStep = "source"
		m.createName.Blur()
	case m.createBranchStep == "source":
		m.createBranchStep = ""
		m.createName.Blur()
		m.draft.Source, m.draft.Branch, m.draft.Policy = "", "", ""
		m.draft.NewBranch = false
	default:
		m.createStep = 0
	}
	m.createChoice = 0
}

func validNewBranch(name string) bool {
	if name == "" || name == "@" || strings.HasPrefix(name, "-") || strings.HasSuffix(name, ".") || strings.Contains(name, "..") || strings.Contains(name, "@{") {
		return false
	}
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("~^:?*[\\", r) {
			return false
		}
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func (m app) newBranchError(name string) string {
	if !validNewBranch(name) {
		return "Enter a valid Git branch name."
	}
	for _, branch := range m.creationBranches() {
		if name == branch || strings.HasPrefix(name, branch+"/") || strings.HasPrefix(branch, name+"/") {
			return "That branch name already exists or conflicts with an existing branch."
		}
	}
	// Include names reserved by sessions still being created.
	for _, s := range m.sessions {
		if s.Project == m.draft.Project && (name == s.Branch || strings.HasPrefix(name, s.Branch+"/") || strings.HasPrefix(s.Branch, name+"/")) {
			return "That branch name is reserved by a session."
		}
	}
	return ""
}

func (m app) updateCreation(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.createStep == 1 && m.createBranchStep == "name" {
		if key == "enter" {
			name := m.createName.Value()
			m.createNotice = m.newBranchError(name)
			if m.createNotice != "" {
				return m, nil
			}
			m.draft.Branch = name
			m.createStep, m.createChoice = 2, 0
			m.createName.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.createName, cmd = m.createName.Update(msg)
		m.createNotice = ""
		return m, cmd
	}
	if m.creationSearchable() {
		if m.createSearching {
			switch key {
			case "enter":
				m.createSearching = false
				m.createSearch.Blur()
				return m, nil
			case "up", "down":
				options := m.creationOptions()
				if len(options) > 0 {
					delta := 1
					if key == "up" {
						delta = -1
					}
					m.createChoice = (m.createChoice + delta + len(options)) % len(options)
				}
				return m, nil
			}
			var cmd tea.Cmd
			m.createSearch, cmd = m.createSearch.Update(msg)
			m.createChoice = 0
			return m, cmd
		}
		if key == "/" {
			m.createSearching = true
			m.createSearch.Focus()
			return m, textinput.Blink
		}
	}
	options := m.creationOptions()
	if selected, handled := selectorNavigation(key, m.createChoice, len(options), m.creationCapacity()); handled && m.creationSearchable() {
		m.createChoice = selected
		return m, nil
	}
	if len(options) == 0 {
		return m, nil
	}
	switch key {
	case "j", "down":
		m.createChoice = (m.createChoice + 1) % len(options)
	case "k", "up":
		m.createChoice = (m.createChoice + len(options) - 1) % len(options)
	case "enter":
		choice := options[m.createChoice]
		m.clearCreationSearch()
		m.createNotice = ""
		switch m.createStep {
		case 0:
			m.draft = createDraft{Project: choice}
			m.createName.SetValue("")
			m.createStep, m.createChoice, m.createBranchStep = 1, 0, ""
		case 1:
			if m.createBranchStep == "source" {
				m.draft.Source = choice
				m.createBranchStep, m.createChoice = "name", 0
				m.createName.Focus()
				return m, textinput.Blink
			} else if choice == newBranchOption {
				m.draft.NewBranch = true
				m.draft.Source, m.draft.Branch, m.draft.Policy = "", "", ""
				m.createBranchStep, m.createChoice = "source", 0
			} else {
				m.draft.NewBranch = false
				m.draft.Branch, m.draft.Source, m.draft.Policy = choice, "", ""
				m.createStep, m.createChoice = 2, 0
			}
		case 2:
			if m.draft.NewBranch {
				m.createNotice = m.newBranchError(m.draft.Branch)
				if m.draft.Source == "" {
					m.createNotice = "Select a source for the new branch."
				}
			} else if m.branchAssigned(m.draft.Branch) {
				m.createNotice = "This branch already has a session. Go back and choose another branch."
			}
			if m.createNotice != "" {
				return m, nil
			}
			id := make([]byte, 16)
			if _, err := rand.Read(id); err != nil {
				m.createNotice = "Could not allocate session identity."
				return m, nil
			}
			id[6] = (id[6] & 0x0f) | 0x40
			id[8] = (id[8] & 0x3f) | 0x80
			m.draft.ReservedID = fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
			m.draft.Policy = choice
			m.sessions = append(m.sessions, session{ID: m.draft.ReservedID, Project: m.draft.Project, Branch: m.draft.Branch, Lifecycle: "stopped", Policy: "current"})
			// An assigned branch is no longer an unassigned retained branch.
			retained := make([]retainedBranch, 0, len(m.branches))
			for _, b := range m.branches {
				if b.Project != m.draft.Project || b.Name != m.draft.Branch {
					retained = append(retained, b)
				}
			}
			m.branches = retained
			m.filter.SetValue("")
			m.topologyProject = m.draft.Project
			m.selectSession(m.draft.ReservedID)
			cmd := m.beginBoot(m.draft.ReservedID)
			return m, cmd
		}
	}
	return m, nil
}

func (m app) creationView(width int) string {
	labels := []string{"Project", "Branch", "Policy"}
	rows := []string{titleStyle.Render("Create session"), fmt.Sprintf("Project → Branch → Policy · %d/3", m.createStep+1)}
	if m.createStep > 0 {
		rows = append(rows, "Project  "+m.draft.Project)
	}
	if m.createStep == 2 {
		label := "Branch  "
		if m.draft.NewBranch {
			label = "New branch  "
		}
		rows = append(rows, label+m.draft.Branch)
	}
	if m.draft.NewBranch && (m.createStep == 2 || m.createBranchStep == "name") {
		rows = append(rows, "Source  "+m.draft.Source)
	}
	label := labels[m.createStep]
	if m.createStep == 1 && m.createBranchStep == "source" {
		label = "Source for new branch"
	}
	rows = append(rows, "", label)
	if m.createStep == 1 && m.createBranchStep == "name" {
		input := m.createName
		input.Width = max(1, min(32, min(width, 96)-7-len(input.Prompt)))
		rows = append(rows, input.View())
	} else {
		options := m.unfilteredCreationOptions()
		capacity := max(1, min(m.creationCapacity(), len(options)))
		query := ""
		if m.creationSearchable() {
			query = m.createSearch.Value()
		}
		items := stringItems(options)
		nameWidth := branchNameMaxWidth
		if m.createStep == 0 {
			nameWidth = projectNameMaxWidth
		}
		for i := range items {
			items[i].text = "  " + truncate(options[i], nameWidth)
		}
		choices := newSelector(items, query, m.createChoice, m.layout().panelContentWidth, capacity)
		empty := "No matches."
		if m.createStep == 0 && len(options) == 0 {
			empty = "No projects available."
		}
		rows = append(rows, strings.Split(choices.rows(empty, len(options) > capacity), "\n")...)
	}

	if m.createStep == 2 {
		rows = append(rows, "", "Uses this project's configured grants.", "", confirmationPrompt("Create and start?", false))
	}
	if m.createNotice != "" {
		rows = append(rows, m.createNotice)
	}
	if m.creationSearchable() {
		if search := fuzzySearchPrompt(m.createSearch, m.createSearching, m.layout().panelContentWidth); search != "" {
			rows[0] = search
		}
	}
	for i := range rows {
		rows[i] = truncate(rows[i], max(1, m.layout().panelContentWidth))
	}
	return centeredPanel(width, strings.Join(rows, "\n"))
}

func (m app) creationCommands() string {
	action := "j/k move · Enter select"
	if m.creationSearchable() {
		action += " · / search\nPgUp/PgDn · ^B/^F page · ^U/^D half · gg/G ends"
	}
	if m.createSearching {
		action = "Type to filter · ↑/↓ move · Enter apply"
	}
	if m.createStep == 1 && m.createBranchStep == "name" {
		action = "Type branch name · Enter policy"
	}
	if m.createStep == 2 {
		action = ""
	}
	return action
}
