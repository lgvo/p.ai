package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestFixtureCoversPresentationContract(t *testing.T) {
	gotLifecycle := map[string]bool{}
	gotAgent := map[string]bool{}
	gotPolicy := map[string]bool{}
	attached := false
	for _, s := range fixtureSessions() {
		gotLifecycle[s.Lifecycle] = true
		gotAgent[s.Agent] = true
		gotPolicy[s.Policy] = true
		attached = attached || s.AttachedCount > 0
	}
	for _, condition := range []string{"creating", "starting", "ready", "stopped", "missing", "unreachable", "discarding", "deleting"} {
		if !gotLifecycle[condition] {
			t.Errorf("fixture missing lifecycle condition %q", condition)
		}
	}
	for _, condition := range []string{"attention", "failed", "running", "idle", "unknown"} {
		if !gotAgent[condition] {
			t.Errorf("fixture missing agent condition %q", condition)
		}
	}
	for _, condition := range []string{"current", "outdated", "invalid"} {
		if !gotPolicy[condition] {
			t.Errorf("fixture missing policy condition %q", condition)
		}
	}
	if !attached {
		t.Error("fixture missing confirmed attachment")
	}
}

func TestViewsStayWithinRequestedWidth(t *testing.T) {
	for _, v := range allVariants() {
		for _, size := range viewportMatrix() {
			m := newApp()
			m.variant, m.width, m.height = v, size.width, size.height
			lines := strings.Split(m.View(), "\n")
			if len(lines) > size.height {
				t.Errorf("variant %s at %dx%d rendered %d lines", v, size.width, size.height, len(lines))
			}
			for lineNo, line := range lines {
				if got := lipgloss.Width(line); got > size.width {
					t.Errorf("variant %s width %d line %d rendered %d cells", v, size.width, lineNo+1, got)
				}
			}
		}
	}
}

func TestVariantsRenderIndependentFactsAtSupportedWidths(t *testing.T) {
	for _, v := range allVariants() {
		for _, size := range []struct{ width, height int }{{80, 24}, {120, 35}, {160, 50}} {
			m := newApp()
			m.variant, m.width, m.height = v, size.width, size.height
			view := m.View()
			for _, fact := range []string{"ready", "attached", "attention", "outdated"} {
				if !strings.Contains(view, fact) {
					t.Errorf("variant %s at %dx%d omitted %q", v, size.width, size.height, fact)
				}
			}
			if strings.Contains(view, "frame clipped") {
				t.Errorf("variant %s at %dx%d clipped its primary surface", v, size.width, size.height)
			}
		}
	}
}

func TestCompactDisclosurePreservesSelectedSessionFactsAndActions(t *testing.T) {
	for _, v := range allVariants() {
		for _, size := range []struct{ width, height int }{{48, 16}, {60, 20}} {
			m := newApp()
			m.variant, m.width, m.height = v, size.width, size.height
			view := m.View()
			for _, fact := range []string{"lifecycle", "presence", "agent", "policy", "actions"} {
				if !strings.Contains(view, fact) {
					t.Errorf("variant %s at %dx%d omitted selected-session %q", v, size.width, size.height, fact)
				}
			}
		}
	}
}

func TestAllVariantNamesParse(t *testing.T) {
	for _, name := range []string{"table", "navigator", "attention", "command", "focus", "lanes", "outline", "matrix", "operations", "workspace", "minimal", "cards", "attachment", "governance", "compare", "topology", "actions"} {
		if _, ok := parseVariant(name); !ok {
			t.Errorf("variant %q is not addressable from the CLI", name)
		}
	}
}

func TestActionSheetExplainsUnavailableActions(t *testing.T) {
	m := newApp()
	m.variant, m.width, m.height = variantActions, 120, 35
	view := m.View()
	for _, fragment := range []string{"AVAILABLE NOW", "UNAVAILABLE · EXPLAINED", "attach / switch", "detach this client", "attached elsewhere"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("action sheet omitted %q", fragment)
		}
	}
}

func TestTopologyKeepsResourceLifetimesSeparate(t *testing.T) {
	m := newApp()
	m.variant, m.width, m.height = variantTopology, 120, 35
	view := m.View()
	for _, fragment := range []string{"Resource topology", "GIT REF", "HOST", "CLIENTS", "AGENT", "POLICY", "retained independently", "immutable snapshot"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("topology omitted %q", fragment)
		}
	}
}

func TestSessionComparisonPinsStableIdentityAndShowsDelta(t *testing.T) {
	m := newApp()
	m.variant, m.width, m.height = variantCompare, 120, 35
	view := m.View()
	for _, fragment := range []string{"A · pinned baseline", "s-docs", "B · current candidate", "s-auth", "A → B delta"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("comparison omitted %q", fragment)
		}
	}
	m.pinComparisonAnchor()
	if m.compareAnchor != "s-auth" {
		t.Fatalf("comparison pin stored %q", m.compareAnchor)
	}
}

func TestGovernanceDashboardAnswersFourUserQuestions(t *testing.T) {
	m := newApp()
	m.variant, m.width, m.height = variantGovernance, 120, 35
	view := m.View()
	for _, fragment := range []string{"What needs attention?", "Where am I attached?", "What is changing?", "What could be unsafe?", "attached →"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("governance dashboard omitted %q", fragment)
		}
	}
}

func TestAttachmentDockMakesClientSwitchBoundaryPrimary(t *testing.T) {
	m := newApp()
	m.variant, m.width, m.height = variantAttachment, 120, 35
	view := m.View()
	for _, fragment := range []string{"Attachment dock", "attached → s-docs", "switching does not stop", "a attach/switch"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("attachment dock omitted %q", fragment)
		}
	}
	m.attachSelected()
	view = m.View()
	if !strings.Contains(view, "attached → s-auth") || !strings.Contains(view, "host remains") {
		t.Fatal("attachment dock lost switch/detach durability semantics")
	}
}

func TestCardsReflowAtThreeBreakpoints(t *testing.T) {
	for _, tc := range []struct {
		width, height int
		want          string
	}{{80, 24, "1 columns"}, {120, 35, "2 columns"}, {160, 50, "3 columns"}} {
		m := newApp()
		m.variant, m.width, m.height = variantCards, tc.width, tc.height
		view := m.View()
		if !strings.Contains(view, tc.want) || !strings.Contains(view, "four independent facts") {
			t.Errorf("card grid at %dx%d did not reflow to %q", tc.width, tc.height, tc.want)
		}
	}
}

func TestMinimalLedgerUsesInlineExpansionWithoutPanels(t *testing.T) {
	m := newApp()
	m.variant, m.width, m.height = variantMinimal, 80, 24
	view := m.View()
	for _, fragment := range []string{"Stream ledger", "current row expands in place", "actions", "attached:1"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("minimal ledger omitted %q", fragment)
		}
	}
	if strings.Contains(view, "╭") || strings.Contains(view, "Inspector") {
		t.Fatal("minimal ledger reintroduced panel chrome")
	}
}

func TestTaskWorkspaceCyclesModesWithoutLosingSelection(t *testing.T) {
	m := newApp()
	m.variant, m.width, m.height = variantWorkspace, 120, 35
	m.selectSession("s-auth")
	for tab, fragment := range []string{"PROJECT / BRANCH", "OBSERVATION", "SNAPSHOT", "RETAINED BRANCH"} {
		view := m.View()
		if !strings.Contains(view, fragment) {
			t.Errorf("workspace tab %d omitted %q", tab, fragment)
		}
		if tab < 3 {
			m = pressRune(t, m, 't')
		}
	}
	idx, ok := m.selectedSessionIndex()
	if !ok || m.sessions[idx].ID != "s-auth" {
		t.Fatal("workspace mode switch lost persistent selection")
	}
}

func TestOperationsConsoleExposesActivityAndBoundedRecovery(t *testing.T) {
	m := newApp()
	m.variant, m.width, m.height = variantOperations, 120, 35
	view := m.View()
	for _, fragment := range []string{"Operations / recovery console", "activating environment", "degraded", "Recovery contract"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("operations console omitted %q", fragment)
		}
	}
	if !m.selectSession("s-plugin") {
		t.Fatal("recovery fixture unavailable")
	}
	m.previewRecoveryPlan()
	if !strings.Contains(m.message, "inspect runtime") || !strings.Contains(m.message, "authorized action") {
		t.Fatalf("recovery plan lost its boundary: %q", m.message)
	}
}

func TestMatrixExposesProjectByInterventionComparison(t *testing.T) {
	m := newApp()
	m.variant, m.width, m.height = variantMatrix, 120, 35
	view := m.View()
	for _, fragment := range []string{"Project × intervention matrix", "INSPECT", "WORK", "RECOVER", "REMOVE", "attached 1"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("matrix omitted %q", fragment)
		}
	}
}

func TestOutlineNavigatesAndCollapsesProjectNodes(t *testing.T) {
	m := newApp()
	m.variant = variantOutline
	nodes := m.outlineNodes()
	if len(nodes) <= len(m.sessions) || !strings.Contains(m.View(), "Projects / sessions") {
		t.Fatal("outline did not add hierarchical project nodes")
	}
	for m.outlineCursor > 0 && !nodes[m.outlineCursor].isProject {
		m.moveOutline(-1)
		nodes = m.outlineNodes()
	}
	project := nodes[m.outlineCursor].project
	before := len(nodes)
	m.setOutlineCollapsed(true)
	if !m.collapsed[project] || len(m.outlineNodes()) >= before {
		t.Fatalf("project %q did not collapse", project)
	}
	m.setOutlineCollapsed(false)
	if m.collapsed[project] || len(m.outlineNodes()) != before {
		t.Fatalf("project %q did not expand", project)
	}
}

func TestFuzzyFilterUsesAllIndependentFacts(t *testing.T) {
	m := newApp()
	m.variant = variantCommand
	m.filter.SetValue("invalid")
	visible := m.visibleIndices()
	if len(visible) != 1 || m.sessions[visible[0]].ID != "s-plugin" {
		t.Fatalf("invalid-policy search returned %#v", visible)
	}
	m.filter.SetValue("attached")
	visible = m.visibleIndices()
	if len(visible) == 0 || m.sessions[visible[0]].ID != "s-docs" {
		t.Fatalf("attachment-presence search returned %#v", visible)
	}
}

func TestEveryVariantCanExposeStartingProgress(t *testing.T) {
	for _, v := range allVariants() {
		m := newApp()
		m.variant, m.width, m.height = v, 80, 24
		if v == variantNavigator {
			for i, project := range m.projects() {
				if project == "orbit" {
					m.project = i
				}
			}
		}
		if !m.selectSession("s-cli") {
			t.Fatalf("variant %s cannot select the starting fixture", v)
		}
		view := m.View()
		if !strings.Contains(view, "activating") {
			t.Errorf("variant %s hides current startup progress", v)
		}
		if strings.Contains(view, "Needs a decision") || strings.Contains(view, "Decide ·") {
			t.Errorf("variant %s presents a lossy signal as an unresolved decision", v)
		}
	}
}

func TestKeyRoutingReachesComparisonAndLifecyclePaths(t *testing.T) {
	m := newApp()
	for i, want := range allVariants()[:6] {
		m = pressRune(t, m, rune('1'+i))
		if m.variant != want {
			t.Fatalf("key %d selected %s, want %s", i+1, m.variant, want)
		}
	}
	for _, want := range allVariants()[6:] {
		m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyTab})
		if m.variant != want {
			t.Fatalf("Tab reached %s, want %s", m.variant, want)
		}
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if m.variant != variantTable {
		t.Fatalf("Tab did not wrap variant comparison: %s", m.variant)
	}

	m = pressRune(t, m, 'c')
	if m.screen != screenCreate {
		t.Fatal("create key did not open creation probe")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenCreateFailed {
		t.Fatal("fixture submission did not expose failed creation")
	}
	m = pressRune(t, m, 't')
	if m.screen != screenCreate || !m.draft.Replacing {
		t.Fatal("Try again with changes did not open replacement request")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenOverview {
		t.Fatal("replacement request did not return to overview")
	}

	m = pressRune(t, m, 'X')
	if m.screen != screenDeletePreview {
		t.Fatal("destructive key bypassed or failed to open preview")
	}
	m = pressRune(t, m, 'y')
	if m.screen != screenDeleteProgress {
		t.Fatal("confirmed deletion did not expose partial progress")
	}
	m = pressRune(t, m, 'r')
	for _, target := range m.deleteTargets {
		if target.State != "deleted" {
			t.Fatalf("key-routed retry left %s in %s", target.Name, target.State)
		}
	}
}

func pressRune(t *testing.T, m app, key rune) app {
	t.Helper()
	return pressKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
}

func pressKey(t *testing.T, m app, key tea.KeyMsg) app {
	t.Helper()
	updated, _ := m.Update(key)
	result, ok := updated.(app)
	if !ok {
		t.Fatalf("Update returned %T, want app", updated)
	}
	return result
}

func allVariants() []variant {
	result := make([]variant, 0, variantCount)
	for v := variant(0); v < variantCount; v++ {
		result = append(result, v)
	}
	return result
}

func viewportMatrix() []struct{ width, height int } {
	return []struct{ width, height int }{{40, 12}, {48, 16}, {60, 20}, {80, 24}, {100, 30}, {120, 35}, {132, 40}, {160, 50}}
}

func TestMinimumSizeBehaviorIsExplicit(t *testing.T) {
	m := newApp()
	m.width, m.height = 40, 12
	view := m.View()
	if !strings.Contains(view, "Terminal too small") || !strings.Contains(view, "40x12") || !strings.Contains(view, "minimum 48x16") {
		t.Fatalf("unsupported viewport is not truthful:\n%s", view)
	}
}

func TestDatasetFixturesRenderAcrossResponsiveBoundaries(t *testing.T) {
	for _, dataset := range []string{"standard", "dense", "empty", "single", "long"} {
		m, err := newAppForDataset(dataset)
		if err != nil {
			t.Fatal(err)
		}
		for _, size := range []struct{ width, height int }{{48, 16}, {60, 20}, {80, 24}, {160, 50}} {
			m.width, m.height = size.width, size.height
			view := m.View()
			for lineNo, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > size.width {
					t.Errorf("dataset %s at %dx%d line %d rendered %d cells", dataset, size.width, size.height, lineNo+1, got)
				}
			}
		}
	}
}

func TestVariantGallerySelectsWithoutConsumingNumericSpace(t *testing.T) {
	m := pressRune(t, newApp(), 'v')
	if m.screen != screenVariantGallery {
		t.Fatal("v did not open variant gallery")
	}
	m = pressRune(t, m, 'j')
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenOverview || m.variant != variantNavigator {
		t.Fatalf("gallery selected screen=%v variant=%v", m.screen, m.variant)
	}
}

func TestAttachSwitchAndDetachPreserveHostSemantics(t *testing.T) {
	m := newApp()
	if !m.selectSession("s-auth") {
		t.Fatal("fixture session not selectable")
	}
	m.attachSelected()
	if m.clientAttach != "s-auth" {
		t.Fatalf("client attached to %q, want s-auth", m.clientAttach)
	}
	auth, docs := sessionByID(t, m.sessions, "s-auth"), sessionByID(t, m.sessions, "s-docs")
	if auth.AttachedCount != 1 || docs.AttachedCount != 0 {
		t.Fatalf("unexpected presence after switch: auth=%d docs=%d", auth.AttachedCount, docs.AttachedCount)
	}
	if auth.Agent != "" {
		t.Fatalf("first confirmed attachment did not clear unattended signal: %q", auth.Agent)
	}
	if !strings.Contains(m.message, "Persistent hosts remain alive") {
		t.Fatalf("switch message lost persistent-host boundary: %q", m.message)
	}
	m.detachSelected()
	auth = sessionByID(t, m.sessions, "s-auth")
	if auth.AttachedCount != 0 || auth.Lifecycle != "ready" {
		t.Fatalf("detach changed durable session semantics: count=%d lifecycle=%s", auth.AttachedCount, auth.Lifecycle)
	}
}

func TestExactRetryPreservesIdentity(t *testing.T) {
	m := newApp()
	m.startCreate()
	wantID, wantOperation := m.draft.ReservedID, m.draft.OperationID
	m.submitCreate()
	if m.screen != screenCreateFailed {
		t.Fatal("fixture create did not reach failed observation state")
	}
	m.exactRetry()
	created := sessionByID(t, m.sessions, wantID)
	if created.ID != wantID || !strings.Contains(m.message, wantOperation) {
		t.Fatalf("exact retry identity drifted: session=%q message=%q", created.ID, m.message)
	}
}

func TestTryAgainWithChangesUsesNewIdentity(t *testing.T) {
	m := newApp()
	m.startCreate()
	oldID := m.draft.ReservedID
	m.submitCreate()
	m.tryAgainWithChanges()
	if !m.draft.Replacing || m.draft.ReservedID == oldID {
		t.Fatalf("replacement did not establish new request identity: %#v", m.draft)
	}
	m.submitCreate()
	if sessionByID(t, m.sessions, "s-new-020").ID == oldID {
		t.Fatal("replacement reused failed identity")
	}
}

func TestDeletionRetryConvergesOnlyTowardAbsent(t *testing.T) {
	m := newApp()
	m.deleteProject = "forge"
	m.beginDelete()
	m.retryDelete()
	for _, target := range m.deleteTargets {
		if target.State != "deleted" {
			t.Fatalf("target %q did not converge: %s", target.Name, target.State)
		}
	}
}

func TestSnapshotScenariosRenderDecisionBoundaries(t *testing.T) {
	checks := map[string][]string{
		"attached-switch":    {"attached:1", "Persistent hosts remain alive"},
		"create-failed":      {"Exact Retry", "Try again with changes", "same UUID"},
		"replacement-create": {"NEW UUID", "supersedes failed"},
		"branches":           {"Git resources, not sessions", "no UUID"},
		"policy":             {"still effective", "immutable"},
		"delete-preview":     {"DELETE PROJECT AND ALL P DATA", "Not deleted", "fingerprint"},
		"delete-progress":    {"remaining", "unreachable", "ensure absent"},
		"delete-complete":    {"confirmed absent", "tombstone"},
		"help":               {"switch structural variants", "attach, or switch"},
	}
	for scenario, fragments := range checks {
		for _, size := range []struct{ width, height int }{{80, 24}, {120, 35}} {
			m := newApp()
			m.width, m.height = size.width, size.height
			if err := m.applyScenario(scenario); err != nil {
				t.Fatal(err)
			}
			view := m.View()
			for _, fragment := range fragments {
				if !strings.Contains(view, fragment) {
					t.Errorf("scenario %s at %dx%d omitted %q", scenario, size.width, size.height, fragment)
				}
			}
			if strings.Contains(view, "frame clipped") {
				t.Errorf("scenario %s at %dx%d clipped", scenario, size.width, size.height)
			}
		}
	}
}

func sessionByID(t *testing.T, sessions []session, id string) session {
	t.Helper()
	for _, s := range sessions {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("session %q not found", id)
	return session{}
}
