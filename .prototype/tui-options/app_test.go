package main

import (
	"fmt"
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
			presence := "attached"
			if v == variantTopology {
				presence = "unattended" // The selected session; no fleet attachment summary.
			}
			for _, fact := range []string{"ready", presence, "attention", "outdated"} {
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
	for _, name := range []string{"table", "navigator", "attention", "command", "focus", "lanes", "outline", "matrix", "operations", "workspace", "minimal", "cards", "attachment", "governance", "compare", "topology", "actions", "integrity"} {
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
	for _, fragment := range []string{"A · pinned baseline", "23103d34-3e04-4071-9acf-3625490aea88", "B · current candidate", "dfcd667b-70e0-43b3-8607-4035f35a32e8", "A → B delta"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("comparison omitted %q", fragment)
		}
	}
	m.pinComparisonAnchor()
	if m.compareAnchor != "dfcd667b-70e0-43b3-8607-4035f35a32e8" {
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
	for _, fragment := range []string{"Attachment dock", "attached →", "23103d34-3e04-4071-9acf-3625490aea88", "switching does not stop", "a attach/switch"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("attachment dock omitted %q", fragment)
		}
	}
	m.attachSelected()
	view = m.View()
	if !strings.Contains(view, "attached →") || !strings.Contains(view, "dfcd667b-70e0-43b3-8607-4035f35a32e8") || !strings.Contains(view, "host remains") {
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
	m.selectSession("dfcd667b-70e0-43b3-8607-4035f35a32e8")
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
	if !ok || m.sessions[idx].ID != "dfcd667b-70e0-43b3-8607-4035f35a32e8" {
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
	if !m.selectSession("2e735f7a-6feb-43cb-aef0-ec05af7ea59b") {
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
	if len(visible) != 1 || m.sessions[visible[0]].ID != "2e735f7a-6feb-43cb-aef0-ec05af7ea59b" {
		t.Fatalf("invalid-policy search returned %#v", visible)
	}
	m.filter.SetValue("attached")
	visible = m.visibleIndices()
	if len(visible) == 0 || m.sessions[visible[0]].ID != "23103d34-3e04-4071-9acf-3625490aea88" {
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
		if !m.selectSession("69cfad8f-d131-4b02-bc60-acf2d27dfb1e") {
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
	m = pressRune(t, m, 'y')
	if m.screen != screenCreateFailed {
		t.Fatal("fixture submission did not expose failed creation")
	}
	m = pressRune(t, m, 't')
	if m.screen != screenCreate || !m.draft.Replacing {
		t.Fatal("Try again with changes did not open replacement request")
	}
	m = pressRune(t, m, 'y')
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

func TestExploreAndStressDatasetCatalogsAreSeparated(t *testing.T) {
	explore, stress := datasetsForMode(modeExplore), datasetsForMode(modeStress)
	if len(explore) != 6 || len(stress) != 12 {
		t.Fatalf("unexpected catalog sizes: explore=%d stress=%d", len(explore), len(stress))
	}
	if _, err := newAppForModeDataset(modeStress, "standard"); err == nil {
		t.Fatal("stress mode accepted an explore-only dataset")
	}
	stressApp, err := newAppForModeDataset(modeStress, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	view := stressApp.View()
	if !strings.Contains(view, "STRESS:baseline") {
		t.Fatalf("stress surface is not clearly labeled:\n%s", view)
	}
}

func TestIncompleteObservationsStayQualifiedAcrossEveryVariant(t *testing.T) {
	for _, dataset := range []string{"partial-observations", "stale-observations"} {
		for _, v := range allVariants() {
			for _, size := range stressViewportMatrix() {
				m, err := newAppForModeDataset(modeStress, dataset)
				if err != nil {
					t.Fatal(err)
				}
				m.variant, m.width, m.height = v, size.width, size.height
				assertBoundedSupportedView(t, m, dataset, v, size.width, size.height)
				warning := observationWarning(m.sessions[0])
				if !strings.Contains(m.View(), warning) {
					t.Errorf("%s/%s at %dx%d hides observation qualifier %q", dataset, v, size.width, size.height, warning)
				}
			}
		}
	}
}

func TestPartialAndStaleFactsCannotAuthorizeAttachment(t *testing.T) {
	for _, dataset := range []string{"partial-observations", "stale-observations"} {
		m, err := newAppForModeDataset(modeStress, dataset)
		if err != nil {
			t.Fatal(err)
		}
		before := m.sessions[0].AttachedCount
		m.attachSelected()
		if m.sessions[0].AttachedCount != before || m.clientAttach == m.sessions[0].ID {
			t.Errorf("%s observation authorized attachment", dataset)
		}
		if !strings.Contains(m.message, "refresh") {
			t.Errorf("%s attachment refusal lacks a refresh path: %q", dataset, m.message)
		}
	}
}

func TestPartialPresenceDoesNotInferUnattendedAgentState(t *testing.T) {
	m, _ := newAppForModeDataset(modeStress, "partial-observations")
	presence, agent := sessionSignals(m.sessions[0])
	if presence != "unknown" || agent != "not evaluated" {
		t.Fatalf("partial presence was guessed: presence=%q agent=%q", presence, agent)
	}
	if strings.Contains(strings.Join(availableActions(m.sessions[0]), " "), "attach") {
		t.Fatal("partial presence exposed attach as available")
	}
}

func TestChurnPreservesStableSelectionThenUsesExplicitFallback(t *testing.T) {
	m, err := newAppForModeDataset(modeStress, "churn")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []int{1, 2} {
		if err := m.applyStressStep(step); err != nil {
			t.Fatal(err)
		}
		idx, ok := m.selectedSessionIndex()
		if !ok || m.sessions[idx].ID != "s-churn-focus" {
			t.Fatalf("step %d lost stable selection", step)
		}
	}
	if err := m.applyStressStep(3); err != nil {
		t.Fatal(err)
	}
	idx, ok := m.selectedSessionIndex()
	if !ok || m.sessions[idx].ID != "s-churn-neighbor" {
		t.Fatalf("removal fallback selected %#v", m.sessions[idx])
	}
	if !strings.Contains(m.message, "s-churn-focus disappeared") || !strings.Contains(m.message, "neighbor") {
		t.Fatalf("removal fallback was not explained: %q", m.message)
	}
	if err := m.applyStressStep(4); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.selectedSessionIndex(); ok || !strings.Contains(m.message, "no sessions remain") {
		t.Fatalf("empty transition was not explicit: %q", m.message)
	}
}

func TestChurnSequenceAcrossEveryVariantAndTargetViewport(t *testing.T) {
	for step := 0; step <= maxStressStep; step++ {
		for _, v := range allVariants() {
			for _, size := range stressViewportMatrix() {
				m, err := newAppForModeDataset(modeStress, "churn")
				if err != nil {
					t.Fatal(err)
				}
				m.variant, m.width, m.height = v, size.width, size.height
				if err := m.applyStressStep(step); err != nil {
					t.Fatal(err)
				}
				assertBoundedSupportedView(t, m, fmt.Sprintf("churn@%d", step), v, size.width, size.height)
				view := m.View()
				if !strings.Contains(view, fmt.Sprintf("churn@%d", step)) {
					t.Errorf("%s at %dx%d hides churn revision", v, size.width, size.height)
				}
				if idx, ok := m.selectedSessionIndex(); ok && !strings.Contains(view, m.sessions[idx].ID) {
					t.Errorf("churn@%d/%s at %dx%d hides selected UUID", step, v, size.width, size.height)
				}
			}
		}
	}
}

func TestChurnClearsOnlyAttachmentsWhoseSessionDisappears(t *testing.T) {
	m, _ := newAppForModeDataset(modeStress, "churn")
	attached := m.clientAttach
	if attached == "" {
		t.Fatal("churn fixture lacks a client attachment")
	}
	for _, step := range []int{1, 2, 3} {
		if err := m.applyStressStep(step); err != nil {
			t.Fatal(err)
		}
		if m.clientAttach != attached {
			t.Fatalf("step %d lost surviving attachment %q", step, attached)
		}
	}
	if err := m.applyStressStep(4); err != nil {
		t.Fatal(err)
	}
	if m.clientAttach != "" {
		t.Fatalf("empty step retained phantom attachment %q", m.clientAttach)
	}
}

func TestStressStepIsScopedToChurn(t *testing.T) {
	m := newApp()
	if err := m.applyStressStep(1); err == nil {
		t.Fatal("exploration dataset accepted a churn step")
	}
	churn, _ := newAppForModeDataset(modeStress, "churn")
	if err := churn.applyStressStep(maxStressStep + 1); err == nil {
		t.Fatal("churn accepted an out-of-range step")
	}
}

func TestObservationIntegrityMakesEvidenceQualityThePrimaryAxis(t *testing.T) {
	m, err := newAppForModeDataset(modeStress, "mixed-observations")
	if err != nil {
		t.Fatal(err)
	}
	m.variant, m.width, m.height = variantIntegrity, 120, 35
	view := m.View()
	for _, fragment := range []string{"Observation integrity", "CURRENT 9", "PARTIAL 9", "STALE 9", "Selected evidence · PARTIAL", "refresh PARTIAL facts"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("observation integrity view omitted %q", fragment)
		}
	}
	for _, size := range stressViewportMatrix() {
		m.width, m.height = size.width, size.height
		assertBoundedSupportedView(t, m, "mixed-observations", variantIntegrity, size.width, size.height)
	}
}

func TestEveryStressDatasetAcrossEveryVariantAndTargetViewport(t *testing.T) {
	for _, definition := range datasetsForMode(modeStress) {
		for _, v := range allVariants() {
			for _, size := range stressViewportMatrix() {
				m, err := newAppForModeDataset(modeStress, definition.name)
				if err != nil {
					t.Fatal(err)
				}
				m.variant, m.width, m.height = v, size.width, size.height
				assertBoundedSupportedView(t, m, definition.name, v, size.width, size.height)
			}
		}
	}
}

func TestIdentityAndPresenceStressAcrossEveryVariantAndTargetViewport(t *testing.T) {
	for _, dataset := range []string{"ambiguous-identities", "high-attachments"} {
		for _, v := range allVariants() {
			for _, size := range stressViewportMatrix() {
				m, err := newAppForModeDataset(modeStress, dataset)
				if err != nil {
					t.Fatal(err)
				}
				m.variant, m.width, m.height = v, size.width, size.height
				assertBoundedSupportedView(t, m, dataset, v, size.width, size.height)
				idx, ok := m.selectedSessionIndex()
				if !ok || !strings.Contains(m.View(), m.sessions[idx].ID) {
					selectedID := "none"
					if ok {
						selectedID = m.sessions[idx].ID
					}
					t.Errorf("%s/%s at %dx%d hides selected stable UUID %q", dataset, v, size.width, size.height, selectedID)
				}
			}
		}
	}
}

func TestHighAttachmentCountsPreserveOtherClientsOnSwitch(t *testing.T) {
	m, _ := newAppForModeDataset(modeStress, "high-attachments")
	previousID := m.clientAttach
	previous, _ := m.sessionWithID(previousID)
	if previous.AttachedCount != 1 {
		t.Fatalf("fixture client anchor count=%d", previous.AttachedCount)
	}
	m.attachSelected()
	previous, _ = m.sessionWithID(previousID)
	if previous.AttachedCount != 0 || m.sessions[0].AttachedCount != 1 {
		t.Fatalf("switch changed wrong leases: previous=%d target=%d", previous.AttachedCount, m.sessions[0].AttachedCount)
	}
	for _, s := range m.sessions {
		if s.AttachedCount < 0 {
			t.Fatalf("negative attachment count for %s", s.ID)
		}
	}
}

func TestTerminalTextStressAcrossEveryVariantAndTargetViewport(t *testing.T) {
	for _, dataset := range []string{"unicode", "hostile-text"} {
		for _, v := range allVariants() {
			for _, size := range stressViewportMatrix() {
				m, err := newAppForModeDataset(modeStress, dataset)
				if err != nil {
					t.Fatal(err)
				}
				m.variant, m.width, m.height = v, size.width, size.height
				assertBoundedSupportedView(t, m, dataset, v, size.width, size.height)
			}
		}
	}
}

func TestHostileTextIsRenderedAsVisibleBoundedData(t *testing.T) {
	m, _ := newAppForModeDataset(modeStress, "hostile-text")
	allFields := ""
	for _, s := range m.sessions {
		for _, value := range []string{s.ID, s.Project, s.Branch, s.AgentReason, s.Operation} {
			if strings.ContainsAny(value, "\x00\x07\x1b\r\n\t\x7f") {
				t.Fatalf("unsafe control survived fixture boundary: %q", value)
			}
			allFields += value
		}
	}
	for _, visible := range []string{"<ESC>", "<LF>", "<TAB>", "<BIDI>"} {
		if !strings.Contains(allFields, visible) {
			t.Errorf("sanitized marker %q was discarded instead of made inspectable", visible)
		}
	}
	if len([]rune(m.sessions[2].Operation)) > 520 {
		t.Fatal("oversized diagnostic was not bounded at the fixture boundary")
	}
}

func TestTruncateUsesTerminalCellsAndGraphemeClusters(t *testing.T) {
	for _, value := range []string{"開発🚀alpha", "cafe\u0301-equipe", "👨‍👩‍👧‍👦-family"} {
		got := truncate(value, 6)
		if width := lipgloss.Width(got); width > 6 {
			t.Errorf("truncate(%q) rendered %d cells: %q", value, width, got)
		}
	}
}

func TestScaleStressFixturesHaveDistinctShapes(t *testing.T) {
	massive, _ := newAppForModeDataset(modeStress, "massive")
	many, _ := newAppForModeDataset(modeStress, "many-projects")
	skewed, _ := newAppForModeDataset(modeStress, "skewed")
	if len(massive.sessions) != 1000 || len(massive.projects()) != 25 {
		t.Fatalf("massive shape is sessions=%d projects=%d", len(massive.sessions), len(massive.projects()))
	}
	if len(many.sessions) != 180 || len(many.projects()) != 180 {
		t.Fatalf("many-projects shape is sessions=%d projects=%d", len(many.sessions), len(many.projects()))
	}
	urgent := 0
	for _, s := range skewed.sessions {
		if s.AttachedCount == 0 && s.Agent == "attention" {
			urgent++
		}
	}
	if urgent != 299 {
		t.Fatalf("skewed fixture has %d urgent sessions", urgent)
	}
}

func TestScaleStressAcrossEveryVariantAndTargetViewport(t *testing.T) {
	for _, dataset := range []string{"massive", "many-projects", "skewed"} {
		for _, v := range allVariants() {
			for _, size := range stressViewportMatrix() {
				m, err := newAppForModeDataset(modeStress, dataset)
				if err != nil {
					t.Fatal(err)
				}
				m.variant, m.width, m.height = v, size.width, size.height
				assertBoundedSupportedView(t, m, dataset, v, size.width, size.height)
			}
		}
	}
}

func TestManyProjectWindowsKeepDistinguishingSuffixes(t *testing.T) {
	for _, v := range []variant{variantNavigator, variantMatrix} {
		m, _ := newAppForModeDataset(modeStress, "many-projects")
		m.variant, m.width, m.height = v, 120, 35
		view := m.View()
		for _, identity := range []string{"proj…t-001", "proj…t-002"} {
			if !strings.Contains(view, identity) {
				t.Errorf("%s collapsed distinguishing project suffix %q", v, identity)
			}
		}
	}
}

func stressViewportMatrix() []struct{ width, height int } {
	return []struct{ width, height int }{{48, 16}, {60, 20}, {80, 24}, {120, 35}}
}

func assertBoundedSupportedView(t *testing.T, m app, dataset string, v variant, width, height int) {
	t.Helper()
	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) > height {
		t.Errorf("%s/%s at %dx%d rendered %d lines", dataset, v, width, height, len(lines))
	}
	for lineNo, line := range lines {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("%s/%s at %dx%d line %d rendered %d cells", dataset, v, width, height, lineNo+1, got)
		}
	}
	if strings.Contains(view, "frame clipped") {
		t.Errorf("%s/%s at %dx%d clipped", dataset, v, width, height)
	}
}

func TestEveryVariantAcrossEveryDatasetAndViewport(t *testing.T) {
	for _, dataset := range []string{"standard", "dense", "empty", "single", "long"} {
		for _, v := range allVariants() {
			for _, size := range viewportMatrix() {
				m, err := newAppForDataset(dataset)
				if err != nil {
					t.Fatal(err)
				}
				m.variant, m.width, m.height = v, size.width, size.height
				view := m.View()
				lines := strings.Split(view, "\n")
				if len(lines) > size.height {
					t.Errorf("%s/%s at %dx%d rendered %d lines", dataset, v, size.width, size.height, len(lines))
				}
				for lineNo, line := range lines {
					if got := lipgloss.Width(line); got > size.width {
						t.Errorf("%s/%s at %dx%d line %d rendered %d cells", dataset, v, size.width, size.height, lineNo+1, got)
					}
				}
				if size.width >= 48 && size.height >= 16 && strings.Contains(view, "frame clipped") {
					t.Errorf("%s/%s at supported %dx%d clipped", dataset, v, size.width, size.height)
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
	if !m.selectSession("dfcd667b-70e0-43b3-8607-4035f35a32e8") {
		t.Fatal("fixture session not selectable")
	}
	m.attachSelected()
	if m.clientAttach != "dfcd667b-70e0-43b3-8607-4035f35a32e8" {
		t.Fatalf("client attached to %q, want dfcd667b-70e0-43b3-8607-4035f35a32e8", m.clientAttach)
	}
	auth, docs := sessionByID(t, m.sessions, "dfcd667b-70e0-43b3-8607-4035f35a32e8"), sessionByID(t, m.sessions, "23103d34-3e04-4071-9acf-3625490aea88")
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
	auth = sessionByID(t, m.sessions, "dfcd667b-70e0-43b3-8607-4035f35a32e8")
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

func TestListWindowsKeepEverySelectionVisible(t *testing.T) {
	m := newApp()
	views := map[string]func() string{
		"topology":          func() string { return m.topologyRows(2, 100) },
		"table":             func() string { return m.sessionRows(m.visibleIndices(), true, 2) },
		"cards":             func() string { return m.cardRows(m.visibleIndices(), 2) },
		"command":           func() string { return m.commandRows(m.visibleIndices(), 2) },
		"radar":             func() string { return m.radarRows(m.visibleIndices(), 2, true) },
		"attention":         func() string { return m.attentionRows(m.visibleIndices(), 2) },
		"compact attention": func() string { return m.compactAttentionRows(m.visibleIndices(), 2) },
		"attachment":        func() string { return m.attachmentCandidateRows(2) },
		"integrity":         func() string { return m.integrityRows(2) },
	}
	for name, render := range views {
		for pos := range m.visibleIndices() {
			m.selected = pos
			if view := render(); strings.Count(view, "›") != 1 {
				t.Errorf("%s hides selection %d: %s", name, pos, view)
			}
		}
	}
}

func TestNavigationFramesOccupyTerminalExactly(t *testing.T) {
	m := newApp()
	for _, size := range [][2]int{{80, 24}, {120, 35}, {48, 16}} {
		m.width, m.height = size[0], size[1]
		for v := variant(0); v < variantCount; v++ {
			m.variant = v
			for pos := range m.visibleIndices() {
				m.selected = pos
				lines := strings.Split(m.View(), "\n")
				if len(lines) != m.height {
					t.Fatalf("%s selection %d: %d rows, want %d", v, pos, len(lines), m.height)
				}
				for _, line := range lines {
					if lipgloss.Width(line) != m.width {
						t.Fatalf("%s selection %d: row width %d, want %d", v, pos, lipgloss.Width(line), m.width)
					}
				}
			}
		}
	}
}

func TestTopologyUsesAvailableHeight(t *testing.T) {
	m, err := newAppForDataset("dense")
	if err != nil {
		t.Fatal(err)
	}
	m.variant = variantTopology
	m.width, m.height = 120, 50
	view := m.View()
	// The old fixed six-row window hid these entries even in a tall terminal.
	for _, idx := range m.visibleIndices()[:12] {
		if !strings.Contains(view, middleTruncate(m.sessions[idx].Branch, 20)) {
			t.Errorf("tall topology hides branch %s", m.sessions[idx].Branch)
		}
	}
	if strings.Contains(view, "frame clipped") {
		t.Fatal("topology overflows available height")
	}
}

func TestTopologyProjectFilter(t *testing.T) {
	m := newApp()
	m.variant = variantTopology
	key := func(value string) {
		var msg tea.KeyMsg
		switch value {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)}
		}
		next, _ := m.Update(msg)
		m = next.(app)
	}
	key("P")
	if m.screen != screenProjectFilter {
		t.Fatal("P did not open project picker")
	}
	for i, project := range m.orderedFilterProjects() {
		if project == "forge" {
			m.projectChoice = i
		}
	}
	key("enter")
	if m.screen != screenOverview || m.topologyProject != "forge" || len(m.visibleIndices()) != 2 {
		t.Fatal("project filter did not select forge")
	}
	for _, idx := range m.visibleIndices() {
		if m.sessions[idx].Project != "forge" {
			t.Fatal("other project leaked into filter")
		}
	}
	m.filter.SetValue("oauth")
	if len(m.visibleIndices()) != 1 {
		t.Fatal("text search must compose with project filter")
	}
	m.filter.SetValue("no-such-branch")
	if _, ok := m.selectedSessionIndex(); ok {
		t.Fatal("empty filter retained actionable selection")
	}
	m.filter.SetValue("")
	key("P")
	m.projectChoice = 0
	key("esc")
	if m.topologyProject != "forge" {
		t.Fatal("cancel changed filter")
	}
	key("P")
	m.projectChoice = 0
	key("enter")
	if len(m.visibleIndices()) != len(m.sessions) {
		t.Fatal("All projects did not restore list")
	}
}

func TestSessionBrowserHasNoGalleryControls(t *testing.T) {
	m := newApp()
	m.browser, m.variant = true, variantTopology
	for _, size := range [][2]int{{120, 35}, {80, 24}, {48, 16}} {
		m.width, m.height = size[0], size[1]
		for _, screen := range []screen{screenOverview, screenHelp} {
			m.screen = screen
			view := m.View()
			for _, text := range []string{"gallery", "Tab cycle", "probe", "fixture:", "view 16", "compact disclosure"} {
				if strings.Contains(view, text) {
					t.Errorf("browser exposes gallery text %q", text)
				}
			}
		}
	}
	m.screen = screenOverview
	for _, key := range []rune{'v', '1', '2', '[', ']'} {
		m = pressRune(t, m, key)
		if m.variant != variantTopology || m.screen != screenOverview {
			t.Fatal("gallery shortcut changed the browser")
		}
	}
	m = pressRune(t, m, 'P')
	if m.screen != screenProjectFilter {
		t.Fatal("project selector is unavailable")
	}
}

func TestFakeTerminalRoundTrip(t *testing.T) {
	m := newApp()
	m.browser, m.variant, m.clientAttach = true, variantTopology, ""
	id := m.sessions[0].ID
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenTerminal || m.clientAttach != id || !strings.Contains(m.View(), "TERMINAL SESSION") {
		t.Fatal("Enter did not open terminal")
	}
	for _, r := range "hello" {
		m = pressRune(t, m, r)
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.View(), "> hello") {
		t.Fatal("terminal did not retain typed input")
	}
	m = pressRune(t, m, 'x')
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.screen != screenTerminal {
		t.Fatal("Ctrl+C left terminal")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlB})
	m = pressRune(t, m, 'd')
	if m.screen != screenOverview || m.clientAttach != "" || m.sessions[0].Lifecycle != "ready" || m.sessions[0].AttachedCount != 0 {
		t.Fatal("detach did not preserve running session")
	}
	m = pressRune(t, m, 'j')
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if strings.Contains(m.View(), "> hello") {
		t.Fatal("terminal history leaked across sessions")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlB})
	m = pressRune(t, m, 'd')
	m = pressRune(t, m, 'k')
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.View(), "> hello") {
		t.Fatal("re-entry lost terminal history")
	}
}

func TestBrowserSearchPromptInFirstListRow(t *testing.T) {
	m := newApp()
	m.browser, m.variant = true, variantTopology
	m = pressRune(t, m, '/')
	for _, r := range "oauth" {
		m = pressRune(t, m, r)
	}
	for _, size := range [][2]int{{120, 35}, {80, 24}, {48, 16}} {
		m.width, m.height = size[0], size[1]
		view := m.View()
		if strings.Contains(m.header(m.width), "oauth") {
			t.Fatal("query remains in header")
		}
		lines := strings.Split(view, "\n")
		if len(lines) < 3 || !strings.HasPrefix(lines[2], "│ fuzzy search> oauth") {
			t.Fatal("query is not in first list row")
		}
		if strings.Contains(view, "frame clipped") {
			t.Fatalf("search clips %dx%d frame", m.width, m.height)
		}
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.filtering || m.screen != screenOverview || m.listSearch(48) != "fuzzy search> oauth" {
		t.Fatal("Enter should retain query and return to list navigation")
	}
}

func TestEscapeClearsFuzzySearch(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		m := newApp()
		m.browser, m.variant, m.topologyProject = true, variantTopology, "forge"
		m = pressRune(t, m, '/')
		for _, r := range "oauth" {
			m = pressRune(t, m, r)
		}
		if confirm {
			m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		}
		m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
		if m.filtering || m.filter.Value() != "" || m.listSearch(48) != "" || len(m.visibleIndices()) != 2 || m.topologyProject != "forge" {
			t.Fatal("Esc must clear search, retaining project scope")
		}
	}
}

func TestSessionBarStaysAtBottomAndIsConfigurable(t *testing.T) {
	m := newApp()
	m.browser, m.variant, m.clientAttach = true, variantTopology, ""
	if strings.Contains(m.View(), "Ctrl+B") {
		t.Fatal("picker exposes terminal controls")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m.sessionBarFields = []string{"project", "status"}
	for _, size := range [][2]int{{120, 35}, {80, 24}, {48, 16}, {25, 8}} {
		m.width, m.height = size[0], size[1]
		rows := strings.Split(m.View(), "\n")
		last := rows[len(rows)-1]
		if len(rows) != m.height || !strings.Contains(last, "Ctrl+B") || lipgloss.Width(last) != m.width {
			t.Fatalf("bar not anchored at %dx%d", m.width, m.height)
		}
		if strings.Contains(last, "oauth") {
			t.Fatal("unconfigured branch shown")
		}
	}
	m.width = 120
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlB})
	if !strings.Contains(m.View(), "[D]etach") {
		t.Fatal("prefix state missing")
	}
	m = pressRune(t, m, 'd')
	if m.screen != screenOverview || strings.Contains(m.View(), "Ctrl+B") {
		t.Fatal("detach did not restore picker mode")
	}
}

func TestSessionActivityKeepsProcessesSeparateFromAgentSignals(t *testing.T) {
	m := newApp()
	m.browser, m.variant, m.clientAttach = true, variantTopology, ""
	for _, size := range [][2]int{{120, 35}, {80, 24}} {
		m.width, m.height = size[0], size[1]
		view := m.View()
		for _, want := range []string{"Codex", "api.service", "postgresql.service", "waiting", ":3000"} {
			if !strings.Contains(view, want) {
				t.Errorf("missing workload detail %s", want)
			}
		}
		if strings.Contains(view, "frame clipped") {
			t.Fatal("workload details overflow")
		}
	}
	m.attachSelected()
	if !strings.Contains(m.selectedTopology(), "Codex") {
		t.Fatal("attachment erased process inventory")
	}
	s := m.sessions[0]
	for _, lifecycle := range []string{"stopped", "missing", "unreachable", "starting"} {
		s.Lifecycle = lifecycle
		view := strings.Join(processRows("Services", s.Services, s), "\n")
		if strings.Contains(view, "running") || strings.Contains(view, ":3000") {
			t.Fatalf("%s falsely advertises running service", lifecycle)
		}
	}
	s.Lifecycle, s.Observation = "ready", "stale"
	if !strings.Contains(strings.Join(processRows("Agents", s.Agents, s), "\n"), "unknown") {
		t.Fatal("stale inventory shown as current")
	}
}

func TestCtrlCUnwindsBeforeQuitting(t *testing.T) {
	m := newApp()
	m.browser, m.variant, m.clientAttach, m.topologyProject = true, variantTopology, "", "forge"
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = pressRune(t, m, 'x')
	cancel := func(want screen) {
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		m = next.(app)
		if cmd != nil || m.screen != want {
			t.Fatal("Ctrl+C quit before interactions were closed")
		}
	}
	cancel(screenTerminal)
	if len(m.terminals[m.clientAttach].Input) != 0 {
		t.Fatal("input not cancelled")
	}
	m = detachFakeTerminal(t, m)
	m = pressRune(t, m, '/')
	m = pressRune(t, m, 'a')
	cancel(screenOverview)
	if m.filtering || m.filter.Value() != "" {
		t.Fatal("search not cancelled")
	}
	m = pressRune(t, m, 'P')
	cancel(screenOverview)
	if m.topologyProject != "forge" {
		t.Fatal("dialog cancellation cleared outer project scope")
	}
	cancel(screenOverview)
	if m.topologyProject != "" {
		t.Fatal("project scope not cleared")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("idle Ctrl+C did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("expected quit command")
	}
}

func TestStopPreservesSessionAndRejectsAttachments(t *testing.T) {
	m := newApp()
	m.browser, m.variant, m.clientAttach = true, variantTopology, ""
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = pressRune(t, m, 'x')
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = detachFakeTerminal(t, m)
	original := m.sessions[0]
	m = pressRune(t, m, 's')
	m = confirmAndFinishStop(t, m)
	s := m.sessions[0]
	if s.Lifecycle != "stopped" || s.ID != original.ID || s.Branch != original.Branch || s.Policy != original.Policy {
		t.Fatal("stop did not preserve identity and policy")
	}
	if _, ok := m.terminals[s.ID]; ok {
		t.Fatal("stopped terminal survived")
	}
	if strings.Contains(m.sessionActivityDetails(s), " · running") {
		t.Fatal("stop left running processes")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenBoot || m.sessions[0].Lifecycle != "starting" {
		t.Fatal("Enter did not open startup log")
	}
	m = finishFakeBoot(m, s.ID)
	if m.screen != screenTerminal || m.sessions[0].Lifecycle != "ready" {
		t.Fatal("completed boot did not automatically enter terminal")
	}
	m = detachFakeTerminal(t, m)
	m = pressRune(t, m, 'j')
	m = pressRune(t, m, 's')
	if m.sessions[1].Lifecycle != "ready" || !strings.Contains(m.message, "detach all") {
		t.Fatal("stop closed another terminal")
	}
}

func TestAgentTreeAttributesReportsToTheirSource(t *testing.T) {
	s := fixtureSessions()[0]
	s.Agents = append(append([]sessionProcess(nil), s.Agents...), sessionProcess{Name: "Another agent", State: "running"})
	view := strings.Join(processRows("Agents", s.Agents, s), "\n")
	for _, want := range []string{"├─ Agents", "├─ Codex", "└─ Another agent", "· waiting", "· unknown"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing tree detail %q", want)
		}
	}
	if strings.Count(view, "· waiting") != 1 {
		t.Fatal("report attributed to multiple agents")
	}
	s.AttachedCount = 1
	if strings.Contains(strings.Join(processRows("Agents", s.Agents, s), "\n"), "permission required") {
		t.Fatal("attached session exposed cleared unattended report")
	}
}

func finishFakeBoot(m app, id string) app {
	for m.boots[id].Step < len(bootLines) {
		progress := m.boots[id]
		next, _ := m.Update(bootTick{id, progress.Generation, progress.Step})
		m = next.(app)
	}
	return m
}

func TestBootLogContinuesOutsideViewerAndIgnoresStoppedTicks(t *testing.T) {
	m := newApp()
	m.browser, m.variant, m.clientAttach = true, variantTopology, ""
	id := m.sessions[2].ID
	m.selectSession(id)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(app)
	if cmd == nil || m.screen != screenBoot || !strings.Contains(m.View(), "Checking retained runtime") {
		t.Fatal("startup did not schedule boot log")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.screen != screenOverview || m.sessions[2].Lifecycle != "starting" {
		t.Fatal("closing viewer interrupted startup")
	}
	m = finishFakeBoot(m, id)
	if m.screen != screenOverview || m.sessions[2].Lifecycle != "ready" || !strings.Contains(m.selectedTopology(), "Runtime running") {
		t.Fatal("background boot did not complete in picker")
	}
	m = pressRune(t, m, 's')
	m = confirmAndFinishStop(t, m)
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	progress := m.boots[id]
	stale := bootTick{id, progress.Generation, progress.Step}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m = pressRune(t, m, 's')
	m = confirmAndFinishStop(t, m)
	next, cmd = m.Update(stale)
	m = next.(app)
	if cmd != nil || m.sessions[2].Lifecycle != "stopped" {
		t.Fatal("late boot tick restarted stopped session")
	}
}

func TestProjectServicesPage(t *testing.T) {
	m := newApp()
	m.browser, m.variant, m.clientAttach = true, variantTopology, ""
	m.sessions[0].Services = append(append([]sessionProcess(nil), m.sessions[0].Services...), sessionProcess{Name: "p-interactive.service", Internal: true, State: "running"})
	if strings.Contains(m.selectedTopology(), "p-interactive") {
		t.Fatal("internal service leaked into tree")
	}
	m = pressRune(t, m, 'S')
	if m.screen != screenServices {
		t.Fatal("S did not open services page")
	}
	for _, size := range [][2]int{{120, 35}, {80, 24}, {48, 16}} {
		m.width, m.height = size[0], size[1]
		view := m.View()
		for _, want := range []string{"Project services", "api.service", "active (running)", "GET /health"} {
			if !strings.Contains(view, want) {
				t.Errorf("missing %q at %dx%d", want, m.width, m.height)
			}
		}
		if strings.Contains(view, "p-interactive") || strings.Contains(view, "frame clipped") {
			t.Fatal("services page exposes internal units or overflows")
		}
	}
	m.width, m.height = 120, 35
	m = pressRune(t, m, 'j')
	if !strings.Contains(m.View(), "Database system ready") {
		t.Fatal("unit selection did not update journal")
	}
	m = pressRune(t, m, 'J')
	if m.screen != screenJournal {
		t.Fatal("journal did not open")
	}
	m = pressRune(t, m, 'q')
	if m.screen != screenServices {
		t.Fatal("journal did not return to services")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.screen != screenOverview || m.selected != 0 {
		t.Fatal("return lost selected session")
	}
	s := m.sessions[0]
	s.Lifecycle = "stopped"
	active, sub := unitState(s, s.Services[0])
	if active != "inactive" || sub != "dead" {
		t.Fatal("stopped runtime has active unit")
	}
	s.Lifecycle, s.Observation = "ready", "stale"
	active, _ = unitState(s, s.Services[0])
	if active != "unknown" {
		t.Fatal("stale observations shown as current")
	}
}

func TestAllExitKeysUnwindInteraction(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyCtrlC}, {Type: tea.KeyRunes, Runes: []rune{'q'}}} {
		for _, place := range []screen{screenHelp, screenProjectFilter, screenServices, screenAgents, screenBoot, screenCreate, screenOverview} {
			m := newApp()
			m.browser, m.variant, m.screen = true, variantTopology, place
			if place == screenOverview {
				m.filtering = true
				m.filter.SetValue("oauth")
			}
			next, cmd := m.Update(key)
			m = next.(app)
			if cmd != nil || m.screen != screenOverview || m.filtering || m.filter.Value() != "" {
				t.Fatalf("%s did not close %v", key.String(), place)
			}
			_, cmd = m.Update(key)
			if cmd == nil {
				t.Fatalf("%s did not quit idle picker", key.String())
			}
		}
		m := newApp()
		m.browser, m.variant, m.clientAttach = true, variantTopology, ""
		m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		next, cmd := m.Update(key)
		m = next.(app)
		if cmd != nil || m.screen != screenTerminal || m.clientAttach == "" {
			t.Fatal("ordinary terminal key unexpectedly detached")
		}
	}
}

func TestProjectServiceActionsAreScopedAndUpdateJournal(t *testing.T) {
	m := newApp()
	m.browser, m.variant = true, variantTopology
	m = pressRune(t, m, 'S')
	originalOther := m.sessions[0].Services[1]
	if !strings.Contains(m.View(), "s stop") {
		t.Fatal("running service should offer stop")
	}
	m = pressRune(t, m, 's')
	if m.sessions[0].Services[0].State != "stopped" || m.sessions[0].Lifecycle != "ready" {
		t.Fatal("service stop changed wrong lifecycle")
	}
	if m.sessions[0].Services[1].State != originalOther.State || len(m.sessions[0].Services[1].Journal) != len(originalOther.Journal) {
		t.Fatal("action affected another service")
	}
	if !strings.Contains(m.View(), "Stopped api.service") || !strings.Contains(m.View(), "inactive (dead)") {
		t.Fatal("stop not reflected in state/journal")
	}
	if !strings.Contains(m.View(), "s start") {
		t.Fatal("inactive service should offer start")
	}
	m = pressRune(t, m, 's')
	if m.sessions[0].Services[0].State != "running" || !strings.Contains(m.View(), "Started api.service") {
		t.Fatal("start did not update service")
	}
	m = pressRune(t, m, 'r')
	if !strings.Contains(m.View(), "Restarting api.service") {
		t.Fatal("restart missing from journal")
	}
	count := len(m.sessions[0].Services[0].Journal)
	m.sessions[0].Observation = "stale"
	m = pressRune(t, m, 's')
	if len(m.sessions[0].Services[0].Journal) != count || m.sessions[0].Services[0].State != "running" {
		t.Fatal("stale observation authorized mutation")
	}
	m.sessions[0].Observation, m.sessions[0].Lifecycle = "", "stopped"
	m = pressRune(t, m, 's')
	if len(m.sessions[0].Services[0].Journal) != count {
		t.Fatal("service started in stopped runtime")
	}
	m.sessions[0].Lifecycle = "ready"
	m.sessions[0].Services[0].State = "failed"
	m = pressRune(t, m, 's')
	if m.sessions[0].Services[0].State != "running" {
		t.Fatal("restart did not recover failed fixture")
	}
}

func TestAgentsPageShowsInstanceContextAndDistinctReport(t *testing.T) {
	m := newApp()
	m.browser, m.variant, m.clientAttach = true, variantTopology, ""
	m = pressRune(t, m, 'A')
	if m.screen != screenAgents {
		t.Fatal("A did not open Agents page")
	}
	for _, size := range [][2]int{{120, 35}, {80, 24}, {48, 16}} {
		m.width, m.height = size[0], size[1]
		view := m.View()
		for _, want := range []string{"Codex [API work]", "Status   waiting", "permission required", m.sessions[0].Agents[0].ID} {
			if !strings.Contains(view, want) {
				t.Errorf("missing agent detail %q at %dx%d", want, m.width, m.height)
			}
		}
		if strings.Contains(view, "frame clipped") {
			t.Fatal("agent details overflow")
		}
	}
	m = pressRune(t, m, 'j')
	if !strings.Contains(m.View(), "checking OAuth tests") || strings.Contains(m.View(), "permission required") {
		t.Fatal("instance selection did not change report context")
	}
	m = pressRune(t, m, 'q')
	if m.screen != screenOverview || m.selected != 0 {
		t.Fatal("return lost P session selection")
	}
	if strings.Contains(m.selectedTopology(), "last:") {
		t.Fatal("tree retained repetitive last labels")
	}
	s := m.sessions[0]
	s.Lifecycle = "stopped"
	if len(activeAgentProcesses(s, s.Agents)) != 0 {
		t.Fatal("report incorrectly implies running process")
	}
	s.Lifecycle, s.Observation = "ready", "stale"
	if agentSummary(s, s.Agents[0]) != "unknown" {
		t.Fatal("stale process displayed as current")
	}
}

func TestJournalFullPageNavigationAndSearch(t *testing.T) {
	m := newApp()
	m.browser, m.variant, m.width, m.height = true, variantTopology, 80, 24
	lines := []string{}
	for i := 0; i < 60; i++ {
		lines = append(lines, fmt.Sprintf("event %02d", i))
	}
	lines[25] = "event 25 NEEDLE"
	lines[45] = "event 45 needle"
	m.sessions[0].Services[0].Journal = lines
	m = pressRune(t, m, 'S')
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenJournal || !strings.Contains(m.View(), "event 59") {
		t.Fatal("journal did not open at latest entries")
	}
	m = pressRune(t, m, 'g')
	m = pressRune(t, m, 'g')
	if m.journalFollow || m.journalOffset != 0 || !strings.Contains(m.View(), "event 00") {
		t.Fatal("top navigation failed")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	if m.journalOffset != m.journalCapacity() {
		t.Fatal("page down failed")
	}
	m = pressRune(t, m, '/')
	for _, r := range "needle" {
		m = pressRune(t, m, r)
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.journalMatch != 25 || !strings.Contains(m.View(), "match 1/2") {
		t.Fatal("case-insensitive journal search failed")
	}
	m = pressRune(t, m, 'n')
	if m.journalMatch != 45 {
		t.Fatal("next match failed")
	}
	m = pressRune(t, m, 'N')
	if m.journalMatch != 25 {
		t.Fatal("previous match failed")
	}
	m = pressRune(t, m, 'q')
	if m.screen != screenJournal || m.journalQuery != "" {
		t.Fatal("first back should clear find")
	}
	m = pressRune(t, m, 'G')
	if !m.journalFollow || !strings.Contains(m.View(), "event 59") {
		t.Fatal("follow/end failed")
	}
	for _, size := range [][2]int{{120, 35}, {80, 24}, {48, 16}} {
		m.width, m.height = size[0], size[1]
		if strings.Contains(m.View(), "frame clipped") {
			t.Fatal("journal overflow")
		}
	}
	m = pressRune(t, m, 'q')
	if m.screen != screenServices || m.serviceChoice != 0 {
		t.Fatal("back lost selected service")
	}
}

func TestAgentPreviewAndUnifiedStatus(t *testing.T) {
	m := newApp()
	m.browser, m.variant, m.width, m.height = true, variantTopology, 80, 24
	m = pressRune(t, m, 'A')
	view := m.View()
	if !strings.Contains(view, "Status   waiting") || !strings.Contains(view, "May I run the integration tests?") || strings.Contains(view, "Process  running") {
		t.Fatal("agent preview or unified status missing")
	}
	m = pressRune(t, m, 'j')
	if !strings.Contains(m.View(), "Running the focused OAuth tests") || strings.Contains(m.View(), "May I run") {
		t.Fatal("preview did not follow selected instance")
	}
	m.sessions[0].Lifecycle = "stopped"
	if !strings.Contains(m.View(), "No active agents observed") || strings.Contains(m.View(), "Codex") {
		t.Fatal("stopped agent remains visible")
	}
}

func TestOnlyActiveAgentsAreListedWithActivityStatus(t *testing.T) {
	m := newApp()
	m.browser, m.variant, m.clientAttach = true, variantTopology, ""
	m.sessions[0].Agents = append(append([]sessionProcess(nil), m.sessions[0].Agents...), sessionProcess{Name: "Stopped worker", State: "stopped", LastSignal: "attention"})
	tree := m.selectedTopology()
	if strings.Contains(tree, "Stopped worker") || !strings.Contains(tree, "Codex [Tests] · working") {
		t.Fatal("tree does not show only active agents with activity labels")
	}
	m = pressRune(t, m, 'A')
	if strings.Contains(m.View(), "Stopped worker") {
		t.Fatal("inactive agent leaked into page")
	}
	m = pressRune(t, m, 'j')
	if !strings.Contains(m.View(), "Status   working") {
		t.Fatal("running signal not presented as working")
	}
	m.sessions[0].Agents[1].LastSignal = ""
	if !strings.Contains(m.View(), "Status   unknown") {
		t.Fatal("missing report inferred working")
	}
}

func detachFakeTerminal(t *testing.T, m app) app {
	t.Helper()
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlB})
	return pressRune(t, m, 'd')
}

func TestTerminalUsesOnlyPrefixForNavigation(t *testing.T) {
	m := newApp()
	m.browser, m.variant, m.clientAttach = true, variantTopology, ""
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = pressRune(t, m, 'q')
	if string(m.terminals[m.clientAttach].Input) != "q" {
		t.Fatal("q was intercepted")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if string(m.terminals[m.clientAttach].Input) != "q" || m.screen != screenTerminal {
		t.Fatal("Esc left terminal or cleared input")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if len(m.terminals[m.clientAttach].Input) != 0 || m.screen != screenTerminal {
		t.Fatal("Ctrl+C navigated away")
	}
	if strings.Contains(m.sessionBar(), "q | Esc") || !strings.Contains(m.sessionBar(), "Ctrl+B") {
		t.Fatal("terminal bar advertises wrong navigation")
	}
	m = detachFakeTerminal(t, m)
	if m.screen != screenOverview || m.clientAttach != "" || m.sessions[0].Lifecycle != "ready" {
		t.Fatal("prefix detach failed")
	}
}

func TestTerminalPrefixPopupAndInspectorReturn(t *testing.T) {
	m := newApp()
	m.browser, m.variant, m.clientAttach = true, variantTopology, ""
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	id := m.clientAttach
	attached := m.sessions[0].AttachedCount
	m = pressRune(t, m, 'x')
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlB})
	for _, size := range [][2]int{{120, 35}, {48, 16}, {25, 8}} {
		m.width, m.height = size[0], size[1]
		for _, label := range []string{"[D]etach", "[A]gents", "[S]ervices"} {
			if !strings.Contains(m.View(), label) {
				t.Fatalf("popup missing %s at %dx%d", label, m.width, m.height)
			}
		}
	}
	m.width, m.height = 120, 35
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.terminalPrefix || m.screen != screenTerminal || string(m.terminals[id].Input) != "x" {
		t.Fatal("closing popup changed terminal input")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlB})
	m = pressRune(t, m, 'A')
	if m.screen != screenAgents || m.agentsSessionID != id {
		t.Fatal("prefix A did not inspect attached session")
	}
	m = pressRune(t, m, 'q')
	if m.screen != screenTerminal || m.clientAttach != id || m.sessions[0].AttachedCount != attached {
		t.Fatal("agents return detached terminal")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlB})
	m = pressRune(t, m, 's')
	if m.screen != screenServices || m.servicesSessionID != id {
		t.Fatal("prefix S did not inspect attached session")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenServices {
		t.Fatal("journal did not return to services")
	}
	m = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.screen != screenTerminal || string(m.terminals[id].Input) != "x" {
		t.Fatal("services return lost terminal")
	}
	m = detachFakeTerminal(t, m)
	if m.screen != screenOverview || m.clientAttach != "" {
		t.Fatal("popup detach failed")
	}
}

func TestSessionListShowsObservedAgentAndServiceCounts(t *testing.T) {
	m := newApp()
	m.browser, m.variant = true, variantTopology
	s := m.sessions[0]
	if got := sessionStats(s); got != "2 agents · 2 services · ! waiting" {
		t.Fatalf("unexpected session stats: %s", got)
	}
	if !strings.Contains(m.View(), "running !waiting") {
		t.Fatal("waiting session has no marker")
	}
	if len(strings.Split(m.topologyRows(99, 100), "\n")) != len(m.visibleIndices()) {
		t.Fatal("session list no longer uses one row per session")
	}
	s.Services = append(append([]sessionProcess(nil), s.Services...), sessionProcess{Name: "p-internal.service", State: "running", Internal: true})
	if !strings.Contains(sessionStats(s), "2 services") {
		t.Fatal("internal service counted")
	}
	s.Observation = "stale"
	if sessionStats(s) != "agents ? · services ?" {
		t.Fatal("stale counts presented as current")
	}
	s.Observation, s.Lifecycle = "", "stopped"
	if sessionStats(s) != "0 agents · 0 services" {
		t.Fatal("stopped processes counted as active")
	}
	m.width, m.height = 120, 35
	for pos := range m.visibleIndices() {
		m.selected = pos
		view := m.View()
		if strings.Count(view, "›") != 1 || strings.Contains(view, "frame clipped") {
			t.Fatal("stats caused selection loss or clipping")
		}
	}
}
