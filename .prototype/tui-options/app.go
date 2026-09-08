package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/sahilm/fuzzy"
)

type variant int

type experienceMode string

const (
	modeExplore experienceMode = "explore"
	modeStress  experienceMode = "stress"
)

const fixtureNotice = "Fixture data only — no daemon or project state is connected."

const (
	variantTable variant = iota
	variantNavigator
	variantAttention
	variantCommand
	variantFocus
	variantLanes
	variantOutline
	variantMatrix
	variantOperations
	variantWorkspace
	variantMinimal
	variantCards
	variantAttachment
	variantGovernance
	variantCompare
	variantTopology
	variantActions
	variantIntegrity
	variantCount
)

func (v variant) String() string {
	return [...]string{"Fleet table", "Project navigator", "Attention workspace", "Command center", "Focus deck", "Actionability lanes", "Expandable outline", "Project status matrix", "Operations console", "Task workspace", "Minimal stream ledger", "Responsive card grid", "Attachment dock", "Governance dashboard", "Session comparison", "Resource topology", "Action eligibility sheet", "Observation integrity"}[v]
}

func parseVariant(s string) (variant, bool) {
	switch strings.ToLower(s) {
	case "table", "fleet":
		return variantTable, true
	case "navigator", "project":
		return variantNavigator, true
	case "attention", "queue":
		return variantAttention, true
	case "command", "search", "palette":
		return variantCommand, true
	case "focus", "deck":
		return variantFocus, true
	case "lanes", "board":
		return variantLanes, true
	case "outline", "tree":
		return variantOutline, true
	case "matrix", "status-matrix":
		return variantMatrix, true
	case "operations", "ops", "recovery":
		return variantOperations, true
	case "workspace", "tabs", "task-tabs":
		return variantWorkspace, true
	case "minimal", "ledger", "stream":
		return variantMinimal, true
	case "cards", "grid", "responsive-grid":
		return variantCards, true
	case "attachment", "dock", "switcher":
		return variantAttachment, true
	case "governance", "dashboard", "questions":
		return variantGovernance, true
	case "compare", "comparison", "diff":
		return variantCompare, true
	case "topology", "map", "resources":
		return variantTopology, true
	case "actions", "eligibility", "action-sheet":
		return variantActions, true
	case "integrity", "freshness", "observation-quality":
		return variantIntegrity, true
	default:
		return 0, false
	}
}

type screen int

const (
	screenOverview screen = iota
	screenCreate
	screenCreateFailed
	screenBranches
	screenPolicy
	screenDeletePreview
	screenDeleteProgress
	screenHelp
	screenVariantGallery
	screenProjectFilter
	screenTerminal
	screenBoot
	screenServices
	screenAgents
	screenJournal
)

// Prototype-only process observations; independent of unattended agent signals.
type sessionProcess struct {
	Preview              []string
	ID                   string
	Description          string
	Journal              []string
	Internal             bool
	Label                string
	LastSignal           string
	LastReason           string
	ReportsSessionSignal bool
	Name                 string
	State                string
	Endpoint             string
}

type session struct {
	Agents          []sessionProcess
	Services        []sessionProcess
	ProcessesKnown  bool
	ID              string
	Project         string
	Branch          string
	Lifecycle       string
	AttachedCount   int
	PresenceUnknown bool
	Agent           string
	AgentReason     string
	Policy          string
	PolicyDiff      []string
	Operation       string
	Observation     string
	ObservationAge  string
}

type retainedBranch struct {
	Project string
	Name    string
	Tip     string
}

type createDraft struct {
	Project     string
	Source      string
	Branch      string
	Policy      string
	ReservedID  string
	OperationID string
	Replacing   bool
}

type deleteTarget struct {
	Name  string
	State string
}

type app struct {
	terminalInspector  bool
	agentPreviewOffset int
	journalUnitName    string
	journalFollow      bool
	journalColumn      int
	journalQuery       string
	journalEditing     bool
	journalMatch       int
	journalNotice      string
	agentsSessionID    string
	agentChoice        int
	serviceNotice      string
	servicesSessionID  string
	serviceChoice      int
	journalOffset      int
	boots              map[string]bootProgress
	bootSequence       int
	bootViewID         string
	sessionBarFields   []string
	terminals          map[string]fakeTerminal
	terminalPrefix     bool
	browser            bool
	width, height      int
	variant            variant
	galleryChoice      variant
	mode               experienceMode
	dataset            string
	datasetNote        string
	stressStep         int
	screen             screen
	previous           screen
	selected           int
	project            int
	topologyProject    string
	projectChoice      int
	filter             textinput.Model
	filtering          bool
	sessions           []session
	branches           []retainedBranch
	clientAttach       string
	message            string
	draft              createDraft
	deleteProject      string
	deleteTargets      []deleteTarget
	outlineCursor      int
	collapsed          map[string]bool
	workspaceTab       int
	compareAnchor      string
}

func newApp() app {
	m, _ := newAppForDataset("standard")
	return m
}

func newAppForDataset(dataset string) (app, error) {
	return newAppForModeDataset(modeExplore, dataset)
}

func newAppForModeDataset(mode experienceMode, dataset string) (app, error) {
	filter := textinput.New()
	filter.Placeholder = "filter project, branch, or status"
	filter.CharLimit = 48
	filter.Width = 34

	m := app{
		width:         120,
		height:        35,
		variant:       variantTable,
		galleryChoice: variantTable,
		mode:          mode,
		dataset:       dataset,
		collapsed:     map[string]bool{},
		screen:        screenOverview,
		project:       1, // "forge" exposes attention, attachment, and policy drift together.
		filter:        filter,
		sessions:      fixtureSessions(),
		branches:      fixtureBranches(),
		clientAttach:  "23103d34-3e04-4071-9acf-3625490aea88",
		message:       fixtureNotice,
	}
	if err := m.applyDatasetForMode(mode, dataset); err != nil {
		return app{}, err
	}
	return m, nil
}

func fixtureSessions() []session {
	return []session{
		{ID: "dfcd667b-70e0-43b3-8607-4035f35a32e8", Project: "forge", Branch: "agent/oauth-device-flow", ProcessesKnown: true, Agents: []sessionProcess{{Name: "Codex", ID: "40e93cb8-a84e-4dc6-8852-13de6527e8cc", Description: "Implement OAuth device authorization", Preview: []string{"You: Implement device authorization.", "Codex: Added device-code polling and expiry checks.", "Codex: May I run the integration tests?"}, Label: "API work", ReportsSessionSignal: true, State: "running"}, {Name: "Codex", ID: "56cc3c2a-d1d6-450d-9d74-501caed1b87b", Description: "Run OAuth regression tests", Preview: []string{"You: Check the OAuth changes for regressions.", "Codex: Reviewing token expiry cases.", "Codex: Running the focused OAuth tests."}, Label: "Tests", LastSignal: "running", LastReason: "checking OAuth tests", State: "running"}}, Services: []sessionProcess{{Name: "api.service", Description: "Project API server", State: "running", Endpoint: ":3000", Journal: []string{"10:42:01 systemd: Starting project API server", "10:42:02 api: Listening on 0.0.0.0:3000", "10:42:03 api: Connected to database", "10:43:12 api: GET /health 200"}}, {Name: "postgresql.service", Description: "PostgreSQL project database", State: "running", Endpoint: ":5432", Journal: []string{"10:41:58 systemd: Starting PostgreSQL", "10:41:59 postgres: Listening on port 5432", "10:42:00 postgres: Database system ready to accept connections"}}}, Lifecycle: "ready", Agent: "attention", AgentReason: "permission required", Policy: "outdated", PolicyDiff: []string{"- filesystem:secrets (still effective)", "+ service:issue-tracker (not available)"}},
		{ID: "23103d34-3e04-4071-9acf-3625490aea88", Project: "forge", Branch: "docs/plugin-guide", ProcessesKnown: true, Agents: []sessionProcess{{Name: "Codex", ID: "df01b065-a3fb-4e33-8e15-732c3dc19426", Label: "Documentation", Description: "Update the plugin guide", Preview: []string{"You: Update the plugin documentation.", "Codex: Reviewing the existing examples."}, ReportsSessionSignal: true, State: "running"}}, Services: []sessionProcess{{Name: "docs-preview.service", Description: "Documentation preview server", State: "running", Endpoint: ":4321", Journal: []string{"10:40:01 preview: Built documentation", "10:40:02 preview: Listening on port 4321"}}}, Lifecycle: "ready", AttachedCount: 1, Policy: "current"},
		{ID: "b6944b3a-dbdd-4791-84d2-fa6936f52c5c", Project: "orbit", Branch: "fix/cache-race", ProcessesKnown: true, Agents: []sessionProcess{{Name: "Codex", ID: "a5cd7638-41e7-4d9e-b042-cc949ddfdd88", Label: "Cache fix", Description: "Investigate the cache race", ReportsSessionSignal: true, State: "stopped"}}, Services: []sessionProcess{{Name: "redis.service", Description: "Project cache", State: "stopped", Endpoint: ":6379", Journal: []string{"10:35:00 redis: User requested shutdown", "10:35:01 systemd: Stopped project cache"}}}, Lifecycle: "stopped", Agent: "failed", AgentReason: "tests exited 1", Policy: "current", Operation: "last start: host exited; bounded diagnostic available"},
		{ID: "69cfad8f-d131-4b02-bc60-acf2d27dfb1e", Project: "orbit", Branch: "feature/interactive-cli", Lifecycle: "starting", Agent: "running", Policy: "current", Operation: "activating environment · phase 3/5"},
		{ID: "2e735f7a-6feb-43cb-aef0-ec05af7ea59b", Project: "p.ai", Branch: "design/plugin-contract-reconciliation", Lifecycle: "unreachable", Agent: "unknown", Policy: "invalid", PolicyDiff: []string{"! external endpoint binding can no longer be enforced"}, Operation: "Incus inspection timed out"},
		{ID: "97d438c2-6f33-47b1-9d0f-043fbdc1576b", Project: "p.ai", Branch: "prototype/tui-options-with-a-deliberately-long-name", Lifecycle: "creating", Agent: "idle", Policy: "current", Operation: "building project environment · phase 2/6"},
		{ID: "6abf77d4-ce38-42e9-9f7c-77e692c6458b", Project: "labs", Branch: "cleanup/retained-state", Lifecycle: "missing", Policy: "outdated", PolicyDiff: []string{"- public-egress (still effective if runtime returns)"}, Operation: "expected runtime not found"},
		{ID: "e39d089b-fc98-40f6-a4ea-1efaf0ce58a6", Project: "demo", Branch: "discard/temporary-spike", Lifecycle: "discarding", Policy: "current", Operation: "tearing down runtime · branch will be retained"},
		{ID: "6c44c201-83d9-4378-8f33-3b73250e99a3", Project: "demo", Branch: "archive/old-experiment", Lifecycle: "deleting", Policy: "current", Operation: "credentials deleted · runtime remaining"},
	}
}

func fixtureBranches() []retainedBranch {
	return []retainedBranch{
		{Project: "forge", Name: "spike/token-cache", Tip: "8f21b4a"},
		{Project: "orbit", Name: "fix/old-parser", Tip: "9c17d20"},
		{Project: "p.ai", Name: "design/status-reducer", Tip: "1ab830e"},
	}
}

func (m app) Init() tea.Cmd { return nil }

func (m app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case bootTick:
		return m.updateBoot(msg)
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		if m.screen == screenTerminal {
			return m.updateTerminal(msg)
		}
		if msg.String() == "ctrl+c" || msg.String() == "esc" || msg.String() == "q" {
			return m.cancelInteraction(true)
		}
		if m.screen == screenJournal {
			m.updateJournal(msg)
			return m, nil
		}
		if m.filtering {
			switch msg.String() {
			case "esc", "enter":
				if msg.String() == "esc" {
					m.filter.SetValue("")
				}
				m.filtering = false
				m.filter.Blur()
				m.selected = 0
				return m, nil
			}
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			m.selected = 0
			return m, cmd
		}

		if m.screen != screenOverview {
			return m.updateOverlay(msg)
		}

		if m.browser {
			switch msg.String() {
			case "tab", "shift+tab", "[", "]", "1", "2", "3", "4", "5", "6", "v":
				return m, nil
			}
		}
		switch msg.String() {
		case "q":
			return m, tea.Quit
		case "tab", "]":
			m.variant = (m.variant + 1) % variantCount
			m.selected = 0
		case "shift+tab", "[":
			m.variant = (m.variant + variantCount - 1) % variantCount
			m.selected = 0
		case "1":
			m.variant, m.selected = variantTable, 0
		case "2":
			m.variant, m.selected = variantNavigator, 0
		case "3":
			m.variant, m.selected = variantAttention, 0
		case "4":
			m.variant, m.selected = variantCommand, 0
		case "5":
			m.variant, m.selected = variantFocus, 0
		case "6":
			m.variant, m.selected = variantLanes, 0
		case "v":
			m.galleryChoice = m.variant
			m.previous, m.screen = m.screen, screenVariantGallery
		case "j", "down":
			if m.variant == variantOutline {
				m.moveOutline(1)
			} else {
				m.moveSelection(1)
			}
		case "k", "up":
			if m.variant == variantOutline {
				m.moveOutline(-1)
			} else {
				m.moveSelection(-1)
			}
		case "h", "left":
			if m.variant == variantNavigator {
				m.moveProject(-1)
			} else if m.variant == variantOutline {
				m.setOutlineCollapsed(true)
			}
		case "l", "right":
			if m.variant == variantNavigator {
				m.moveProject(1)
			} else if m.variant == variantOutline {
				m.setOutlineCollapsed(false)
			}
		case "esc":
			m.filter.SetValue("")
			m.selected = 0
		case "/":
			m.filtering = true
			m.filter.Focus()
			return m, textinput.Blink
		case "a":
			if m.browser {
				cmd := m.enterTerminal()
				return m, cmd
			} else {
				m.attachSelected()
			}
		case "enter", " ":
			if m.variant == variantOutline && m.toggleOutlineNode() {
				break
			}
			if m.browser {
				cmd := m.enterTerminal()
				return m, cmd
			} else {
				m.attachSelected()
			}
		case "d":
			if !m.browser {
				m.detachSelected()
			}
		case "c":
			m.startCreate()
		case "b":
			m.previous, m.screen = m.screen, screenBranches
		case "P":
			if m.variant == variantTopology {
				m.projectChoice = 0
				for i, project := range m.projects() {
					if project == m.topologyProject {
						m.projectChoice = i + 1
					}
				}
				m.screen = screenProjectFilter
			}
		case "p":
			m.previous, m.screen = m.screen, screenPolicy
		case "r":
			if m.variant == variantOperations {
				m.previewRecoveryPlan()
			}
		case "t":
			if m.variant == variantWorkspace {
				m.workspaceTab = (m.workspaceTab + 1) % 4
				m.message = fmt.Sprintf("Workspace mode: %s.", workspaceTabNames[m.workspaceTab])
			}
		case "s":
			if m.browser {
				m.stopSelected()
				break
			}
			if m.variant == variantCompare {
				m.pinComparisonAnchor()
			}
		case "A":
			if m.browser {
				m.openAgents()
			}
		case "S":
			if m.browser {
				m.openServices()
			}
		case "X":
			m.startDeletePreview()
		case "?":
			m.previous, m.screen = m.screen, screenHelp
		}
	}
	return m, nil
}

func (m app) updateOverlay(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "esc" {
		m.screen = screenOverview
		m.message = "Action cancelled; fixture state is unchanged."
		return m, nil
	}

	switch m.screen {
	case screenAgents:
		m.updateAgents(key)
	case screenServices:
		m.updateServices(key)
	case screenBoot:
		if key == "enter" {
			for _, s := range m.sessions {
				if s.ID == m.bootViewID && s.Lifecycle == "ready" {
					m.selectSession(s.ID)
					cmd := m.enterTerminal()
					return m, cmd
				}
			}
		}
	case screenProjectFilter:
		projects := append([]string{""}, m.projects()...)
		switch key {
		case "j", "down":
			m.projectChoice = (m.projectChoice + 1) % len(projects)
		case "k", "up":
			m.projectChoice = (m.projectChoice + len(projects) - 1) % len(projects)
		case "enter":
			selectedID := ""
			if idx, ok := m.selectedSessionIndex(); ok {
				selectedID = m.sessions[idx].ID
			}
			m.topologyProject = projects[m.projectChoice]
			m.selected = 0
			for pos, idx := range m.visibleIndices() {
				if m.sessions[idx].ID == selectedID {
					m.selected = pos
				}
			}
			m.screen = screenOverview
		}
	case screenVariantGallery:
		switch key {
		case "j", "down":
			m.galleryChoice = (m.galleryChoice + 1) % variantCount
		case "k", "up":
			m.galleryChoice = (m.galleryChoice + variantCount - 1) % variantCount
		case "enter":
			m.variant, m.selected = m.galleryChoice, 0
			m.screen = screenOverview
			m.message = fmt.Sprintf("Exploring %s with the %s fixture.", m.variant, m.dataset)
		}
	case screenCreate:
		if key == "enter" {
			m.submitCreate()
		}
	case screenCreateFailed:
		switch key {
		case "r":
			m.exactRetry()
		case "t":
			m.tryAgainWithChanges()
		}
	case screenDeletePreview:
		if key == "y" {
			m.beginDelete()
		}
	case screenDeleteProgress:
		if key == "r" {
			m.retryDelete()
		}
	case screenBranches:
		if key == "n" {
			m.startCreate()
		}
	}
	return m, nil
}

func (m *app) moveSelection(delta int) {
	visible := m.visibleIndices()
	if len(visible) == 0 {
		m.selected = 0
		return
	}
	m.selected = (m.selected + delta + len(visible)) % len(visible)
}

func (m *app) moveProject(delta int) {
	projects := m.projects()
	if len(projects) == 0 {
		return
	}
	m.project = (m.project + delta + len(projects)) % len(projects)
	m.selected = 0
}

func (m app) projects() []string {
	seen := map[string]bool{}
	var result []string
	for _, s := range m.sessions {
		if !seen[s.Project] {
			seen[s.Project] = true
			result = append(result, s.Project)
		}
	}
	sort.Strings(result)
	return result
}

func (m app) visibleIndices() []int {
	indices := make([]int, 0, len(m.sessions))
	projects := m.projects()
	activeProject := ""
	if m.variant == variantTopology {
		activeProject = m.topologyProject
	}
	if m.variant == variantNavigator && len(projects) > 0 {
		if m.project >= len(projects) {
			m.project = 0
		}
		activeProject = projects[m.project]
	}
	for i, s := range m.sessions {
		if activeProject == "" || s.Project == activeProject {
			indices = append(indices, i)
		}
	}
	if m.variant == variantAttention {
		sort.SliceStable(indices, func(i, j int) bool {
			return urgency(m.sessions[indices[i]]) < urgency(m.sessions[indices[j]])
		})
	}
	query := strings.TrimSpace(m.filter.Value())
	if query == "" {
		return indices
	}
	haystack := make([]string, len(indices))
	for i, idx := range indices {
		s := m.sessions[idx]
		presence := "unattended"
		if s.AttachedCount > 0 {
			presence = "attached"
		}
		haystack[i] = strings.Join([]string{s.Project, s.Branch, s.Lifecycle, presence, s.Agent, s.Policy}, " ")
		for _, agent := range activeAgentProcesses(s, s.Agents) {
			haystack[i] += " " + agentSummary(s, agent)
		}
	}
	matches := fuzzy.Find(query, haystack)
	filtered := make([]int, 0, len(matches))
	for _, match := range matches {
		filtered = append(filtered, indices[match.Index])
	}
	return filtered
}

func urgency(s session) int {
	if s.AttachedCount == 0 && s.Agent == "attention" {
		return 0
	}
	if s.AttachedCount == 0 && s.Agent == "failed" {
		return 1
	}
	if s.Policy == "invalid" {
		return 2
	}
	if s.Lifecycle == "missing" || s.Lifecycle == "unreachable" {
		return 3
	}
	if s.Policy == "outdated" {
		return 4
	}
	if s.Lifecycle == "starting" || s.Lifecycle == "creating" {
		return 5
	}
	return 6
}

func (m app) selectedSessionIndex() (int, bool) {
	visible := m.visibleIndices()
	if len(visible) == 0 {
		return 0, false
	}
	selected := m.selected
	if selected >= len(visible) {
		selected = len(visible) - 1
	}
	return visible[selected], true
}

func (m *app) selectSession(id string) bool {
	visible := m.visibleIndices()
	for pos, idx := range visible {
		if m.sessions[idx].ID == id {
			m.selected = pos
			return true
		}
	}
	return false
}

func (m *app) attachSelected() {
	idx, ok := m.selectedSessionIndex()
	if !ok {
		return
	}
	target := &m.sessions[idx]
	if warning := observationWarning(*target); warning != "" {
		m.message = fmt.Sprintf("Attach unavailable: refresh %s facts before mutation.", warning)
		return
	}
	if target.Lifecycle != "ready" && target.Lifecycle != "stopped" {
		m.message = fmt.Sprintf("Attach unavailable while %s; the daemon plan remains authoritative.", target.Lifecycle)
		return
	}
	if m.clientAttach == target.ID {
		m.message = "This prototype client is already attached to that session."
		return
	}
	previous := ""
	for i := range m.sessions {
		if m.sessions[i].ID == m.clientAttach {
			previous = m.sessions[i].Project + "/" + m.sessions[i].Branch
			if m.sessions[i].AttachedCount > 0 {
				m.sessions[i].AttachedCount--
			}
		}
	}
	wasUnattended := target.AttachedCount == 0
	target.AttachedCount++
	if wasUnattended {
		target.Agent, target.AgentReason = "", ""
		target.Agents = append([]sessionProcess(nil), target.Agents...)
		for i := range target.Agents {
			target.Agents[i].LastSignal, target.Agents[i].LastReason = "", ""
		}
	}
	m.clientAttach = target.ID
	if previous == "" {
		m.message = fmt.Sprintf("Attached to %s/%s; first confirmed entry cleared unattended signal.", target.Project, target.Branch)
	} else {
		m.message = fmt.Sprintf("Persistent hosts remain alive; switched %s → %s/%s.", previous, target.Project, target.Branch)
	}
}

func (m *app) detachSelected() {
	idx, ok := m.selectedSessionIndex()
	if ok {
		if warning := observationWarning(m.sessions[idx]); warning != "" {
			m.message = fmt.Sprintf("Detach unavailable: refresh %s facts before mutation.", warning)
			return
		}
	}
	if !ok || m.sessions[idx].ID != m.clientAttach {
		m.message = "This prototype client has no attachment to detach from the selected session."
		return
	}
	if m.sessions[idx].AttachedCount > 0 {
		m.sessions[idx].AttachedCount--
	}
	m.clientAttach = ""
	m.message = "Detached temporary client; the persistent host and ready session remain alive."
}

func (m *app) startCreate() {
	m.screen = screenCreate
	m.draft = createDraft{
		Project: "forge", Source: "main@4e27a91", Branch: "feature/new-stream",
		Policy: "current project policy", ReservedID: "s-new-019", OperationID: "op-create-77",
	}
}

func (m *app) submitCreate() {
	if m.draft.Replacing {
		m.sessions = append(m.sessions, session{ID: "s-new-020", Project: m.draft.Project, Branch: m.draft.Branch, Lifecycle: "ready", Policy: "current"})
		m.screen = screenOverview
		m.message = "Replacement creation succeeded with NEW UUID s-new-020 and operation op-create-78; failed request is superseded."
		return
	}
	m.screen = screenCreateFailed
	m.message = "Fixture failure injected after environment preparation."
}

func (m *app) exactRetry() {
	m.sessions = append(m.sessions, session{ID: m.draft.ReservedID, Project: m.draft.Project, Branch: m.draft.Branch, Lifecycle: "ready", Policy: "current"})
	m.screen = screenOverview
	m.message = fmt.Sprintf("Exact Retry succeeded: UUID %s and operation %s were preserved.", m.draft.ReservedID, m.draft.OperationID)
}

func (m *app) tryAgainWithChanges() {
	m.draft.Replacing = true
	m.draft.Branch = "feature/revised-stream"
	m.draft.ReservedID = "s-new-020"
	m.draft.OperationID = "op-create-78"
	m.screen = screenCreate
	m.message = "Editing a replacement request; submitting creates a new identity and supersedes the failed request."
}

func (m *app) startDeletePreview() {
	idx, ok := m.selectedSessionIndex()
	if !ok {
		return
	}
	m.deleteProject = m.sessions[idx].Project
	m.screen = screenDeletePreview
}

func (m *app) beginDelete() {
	m.screen = screenDeleteProgress
	m.deleteTargets = []deleteTarget{
		{Name: "session credentials", State: "deleted"},
		{Name: "assigned and retained refs", State: "deleted"},
		{Name: "runtime dfcd667b-70e0-43b3-8607-4035f35a32e8", State: "remaining"},
		{Name: "environment image cache", State: "unreachable"},
		{Name: "registry tombstone", State: "remaining"},
	}
}

func (m *app) retryDelete() {
	for i := range m.deleteTargets {
		m.deleteTargets[i].State = "deleted"
	}
	m.message = "Retry re-inspected every target and reached the same monotonic result: absent."
}

func (m *app) applyScenario(name string) error {
	switch name {
	case "overview":
		return nil
	case "create-failed":
		m.startCreate()
		m.submitCreate()
	case "replacement-create":
		m.startCreate()
		m.submitCreate()
		m.tryAgainWithChanges()
	case "branches":
		m.screen = screenBranches
	case "policy":
		m.variant = variantTable
		m.selectSession("dfcd667b-70e0-43b3-8607-4035f35a32e8")
		m.screen = screenPolicy
	case "delete-preview":
		m.variant = variantTable
		m.selectSession("dfcd667b-70e0-43b3-8607-4035f35a32e8")
		m.startDeletePreview()
	case "delete-progress":
		m.deleteProject = "forge"
		m.beginDelete()
	case "delete-complete":
		m.deleteProject = "forge"
		m.beginDelete()
		m.retryDelete()
	case "attached-switch":
		m.variant = variantTable
		m.selectSession("dfcd667b-70e0-43b3-8607-4035f35a32e8")
		m.attachSelected()
	case "help":
		m.screen = screenHelp
	default:
		return fmt.Errorf("unknown scenario %q", name)
	}
	return nil
}

func timestamp() string { return time.Date(2026, 9, 6, 20, 0, 0, 0, time.UTC).Format(time.RFC3339) }
