package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	ink       = lipgloss.AdaptiveColor{Light: "#15202B", Dark: "#E6EDF3"}
	muted     = lipgloss.AdaptiveColor{Light: "#52606D", Dark: "#8B949E"}
	accent    = lipgloss.AdaptiveColor{Light: "#5B21B6", Dark: "#C4A7FF"}
	urgent    = lipgloss.AdaptiveColor{Light: "#B42318", Dark: "#FF7B72"}
	warning   = lipgloss.AdaptiveColor{Light: "#9A6700", Dark: "#E3B341"}
	positive  = lipgloss.AdaptiveColor{Light: "#137333", Dark: "#56D364"}
	panelEdge = lipgloss.AdaptiveColor{Light: "#CBD5E1", Dark: "#30363D"}

	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(accent)
	mutedStyle    = lipgloss.NewStyle().Foreground(muted)
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(ink).Background(lipgloss.AdaptiveColor{Light: "#EDE9FE", Dark: "#312E81"})
	panelStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(panelEdge).Padding(0, 1)
)

func (m app) View() string {
	width, height := m.width, m.height
	if width < 48 || height < 16 {
		return m.tooSmallView(width, height)
	}
	if m.screen == screenOverview && (width < 72 || height < 22) {
		return m.compactResponsiveView(width, height)
	}

	header := m.header(width)
	var body string
	switch m.screen {
	case screenOverview:
		switch m.variant {
		case variantTable:
			body = m.tableView(width, height)
		case variantNavigator:
			body = m.navigatorView(width, height)
		case variantAttention:
			body = m.attentionView(width, height)
		case variantCommand:
			body = m.commandView(width, height)
		case variantFocus:
			body = m.focusView(width, height)
		case variantLanes:
			body = m.lanesView(width, height)
		case variantOutline:
			body = m.outlineView(width, height)
		case variantMatrix:
			body = m.matrixView(width, height)
		case variantOperations:
			body = m.operationsView(width, height)
		case variantWorkspace:
			body = m.workspaceView(width, height)
		case variantMinimal:
			body = m.minimalView(width, height)
		case variantCards:
			body = m.cardsView(width, height)
		case variantAttachment:
			body = m.attachmentView(width, height)
		case variantGovernance:
			body = m.governanceView(width, height)
		case variantCompare:
			body = m.comparisonView(width, height)
		case variantTopology:
			body = m.topologyView(width, height)
		case variantActions:
			body = m.actionSheetView(width, height)
		}
	case screenCreate:
		body = m.createView(width)
	case screenCreateFailed:
		body = m.createFailedView(width)
	case screenBranches:
		body = m.branchesView(width)
	case screenPolicy:
		body = m.policyView(width)
	case screenDeletePreview:
		body = m.deletePreviewView(width)
	case screenDeleteProgress:
		body = m.deleteProgressView(width)
	case screenHelp:
		body = m.helpView(width)
	case screenVariantGallery:
		body = m.variantGalleryView(width)
	}
	footer := m.footer(width)
	return fitLines(strings.Join([]string{header, body, footer}, "\n"), height)
}

func (m app) header(width int) string {
	line := fmt.Sprintf("P / probe · view %02d %s · fixture:%s · Tab cycle · v gallery", m.variant+1, m.variant, m.dataset)
	if m.mode == modeStress {
		line = fmt.Sprintf("P / probe · STRESS:%s · view %02d %s · Tab cycle · v gallery", m.dataset, m.variant+1, m.variant)
	}
	if m.filtering || m.filter.Value() != "" {
		line += " · / " + m.filter.Value()
	}
	return titleStyle.Copy().Width(width).Render(truncate(line, width))
}

func (m app) footer(width int) string {
	message := m.message
	if m.screen == screenOverview {
		if width < 100 {
			keys := mutedStyle.Render("j/k move · a attach · d detach · c create · b branches · p policy · X delete") + "\n" + mutedStyle.Render("/ filter · Tab cycle · v gallery · ? help · q quit")
			if message == fixtureNotice {
				message = keys
			} else {
				message = truncate(message, width) + "\n" + keys
			}
		} else {
			message += "\n" + mutedStyle.Render("j/k move · a attach · d detach · c create · b branches · p policy · X delete · / filter · Tab cycle · v gallery · ? help · q quit")
		}
	} else {
		message += "\n" + mutedStyle.Render("esc returns without mutating real state")
	}
	return lipgloss.NewStyle().Width(width).Render(message)
}

func (m app) tooSmallView(width, height int) string {
	lines := []string{
		"P / probe",
		"Terminal too small",
		fmt.Sprintf("received %dx%d", width, height),
		"minimum 48x16",
		"No facts are hidden behind a clipped view.",
	}
	for i := range lines {
		lines[i] = truncate(lines[i], maxInt(width, 1))
	}
	return fitLines(strings.Join(lines, "\n"), maxInt(height, 1))
}

func (m app) compactResponsiveView(width, height int) string {
	header := m.header(width)
	idx, ok := m.selectedSessionIndex()
	content := titleStyle.Render(fmt.Sprintf("%s · compact disclosure", m.variant))
	if !ok {
		content += "\n" + mutedStyle.Render("No sessions in this fixture.") +
			"\n\nDataset " + m.dataset + " · use v to compare views"
	} else {
		s := m.sessions[idx]
		presence, agent := sessionSignals(s)
		identityWidth := maxInt(width-8, 8)
		lines := []string{
			truncate(s.Project+" / "+s.Branch, identityWidth),
			mutedStyle.Render("UUID " + s.ID),
			"lifecycle  " + styledFact(s.Lifecycle),
			"presence   " + presence,
			"agent      " + styledFact(agent),
			"policy     " + styledFact(s.Policy),
			"actions    " + strings.Join(availableActions(s), " · "),
		}
		if s.Operation != "" && height >= 18 {
			lines = append(lines, "progress   "+s.Operation)
		}
		content += "\n" + strings.Join(lines, "\n")
	}
	body := renderPanel(width, content)
	footer := mutedStyle.Render("j/k move · a attach · Tab cycle · v views")
	if height >= 18 {
		footer += "\n" + mutedStyle.Render("? help · q quit · compact mode below 72x22")
	}
	return fitLines(strings.Join([]string{header, body, footer}, "\n"), height)
}

func (m app) variantGalleryView(width int) string {
	rows := []string{titleStyle.Render("UX exploration gallery"), mutedStyle.Render("Each entry changes the organizing model, not the underlying fixture facts."), ""}
	for v := variant(0); v < variantCount; v++ {
		line := fmt.Sprintf("  %02d  %-24s", v+1, v.String())
		if v == m.galleryChoice {
			line = selectedStyle.Render("› " + strings.TrimPrefix(line, "  "))
		}
		rows = append(rows, line)
	}
	rows = append(rows, "", "j/k choose · enter open · esc return")
	return centeredPanel(width, strings.Join(rows, "\n"))
}

func (m app) tableView(width, height int) string {
	indices := m.visibleIndices()
	rows := m.sessionRows(indices, width >= 100, maxRows(height, width))
	if width >= 110 {
		table := renderPanel(tableWidth(width), titleStyle.Render("All active sessions · cross-project")+"\n"+rows)
		detail := renderPanel(detailWidth(width), m.detailView())
		return lipgloss.JoinHorizontal(lipgloss.Top, table, " ", detail)
	}
	table := renderPanel(width, titleStyle.Render("All active sessions · cross-project")+"\n"+rows)
	detail := renderPanel(width, m.detailView())
	return lipgloss.JoinVertical(lipgloss.Left, table, detail)
}

func (m app) navigatorView(width, height int) string {
	projects := m.projects()
	var projectRows []string
	for i, p := range projects {
		count, urgentCount := 0, 0
		for _, s := range m.sessions {
			if s.Project == p {
				count++
				if urgency(s) < 4 {
					urgentCount++
				}
			}
		}
		line := fmt.Sprintf("%-10s %d sess · %d urgent", truncate(p, 10), count, urgentCount)
		if i == m.project {
			line = selectedStyle.Render("› " + line)
		} else {
			line = "  " + line
		}
		projectRows = append(projectRows, line)
	}
	indices := m.visibleIndices()
	rightContent := titleStyle.Render("Sessions in selected project") + "\n" + m.cardRows(indices, maxRows(height, width))
	if width >= 90 {
		rowLimit := 4
		if height >= 35 {
			rowLimit = 7
		}
		rightContent = titleStyle.Render("Sessions in selected project") + "\n" + m.cardRows(indices, rowLimit)
		left := renderPanel(navigatorWidth(width), titleStyle.Render("Projects")+"\n"+strings.Join(projectRows, "\n"))
		right := renderPanel(width-navigatorWidth(width)-1, rightContent+"\n\n"+m.detailView())
		return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
	}
	projectNames := make([]string, len(projects))
	for i, project := range projects {
		if i == m.project {
			projectNames[i] = selectedStyle.Render(" " + project + " ")
		} else {
			projectNames[i] = project
		}
	}
	left := renderPanel(width, titleStyle.Render("Projects")+"  "+strings.Join(projectNames, "  "))
	rightContent = titleStyle.Render("Sessions in selected project") + "\n" + m.compactCardRows(indices)
	right := renderPanel(width, rightContent+"\n\n"+m.compactSelectionView())
	return lipgloss.JoinVertical(lipgloss.Left, left, right)
}

func (m app) attentionView(width, height int) string {
	indices := m.visibleIndices()
	urgentIndices := make([]int, 0)
	steadyIndices := make([]int, 0)
	for _, idx := range indices {
		if urgency(m.sessions[idx]) < 4 {
			urgentIndices = append(urgentIndices, idx)
		} else {
			steadyIndices = append(steadyIndices, idx)
		}
	}
	workspaceContent := titleStyle.Render("Everything else") + "\n" + m.attentionRows(steadyIndices, 3) + "\n\n" + m.detailView()
	if width >= 100 {
		queue := renderPanel(attentionWidth(width), titleStyle.Render(fmt.Sprintf("Urgent to inspect · %d", len(urgentIndices)))+"\n"+m.attentionRows(urgentIndices, 3))
		workspace := renderPanel(width-attentionWidth(width)-1, workspaceContent)
		return lipgloss.JoinHorizontal(lipgloss.Top, queue, " ", workspace)
	}
	queue := renderPanel(width, titleStyle.Render(fmt.Sprintf("Urgent to inspect · %d", len(urgentIndices)))+"\n"+m.compactAttentionRows(urgentIndices, 3))
	workspaceContent = titleStyle.Render("Everything else") + "\n" + m.compactAttentionRows(steadyIndices, 3) + "\n\n" + m.compactSelectionView()
	workspace := renderPanel(width, workspaceContent)
	return lipgloss.JoinVertical(lipgloss.Left, queue, workspace)
}

func (m app) compactCardRows(indices []int) string {
	if len(indices) == 0 {
		return mutedStyle.Render("No sessions in this project")
	}
	var rows []string
	for pos, idx := range indices {
		s := m.sessions[idx]
		presence := "unattended"
		if s.AttachedCount > 0 {
			presence = fmt.Sprintf("attached:%d", s.AttachedCount)
		}
		agent := firstNonEmpty(s.Agent, "—")
		if s.AttachedCount > 0 {
			agent = "—"
		}
		line := fmt.Sprintf("  %-28s %-10s %-11s %-9s %s", truncate(s.Branch, 28), s.Lifecycle, presence, agent, s.Policy)
		rows = append(rows, m.selectableLine(pos, line, s))
	}
	return strings.Join(rows, "\n")
}

func (m app) compactAttentionRows(indices []int, limit int) string {
	if len(indices) == 0 {
		return mutedStyle.Render("Nothing in this group")
	}
	visible := m.visibleIndices()
	position := map[int]int{}
	for pos, idx := range visible {
		position[idx] = pos
	}
	var rows []string
	for row, idx := range indices {
		if row >= limit {
			rows = append(rows, mutedStyle.Render(fmt.Sprintf("… %d more", len(indices)-row)))
			break
		}
		s := m.sessions[idx]
		presence := "unattended"
		if s.AttachedCount > 0 {
			presence = fmt.Sprintf("attached:%d", s.AttachedCount)
		}
		agent := firstNonEmpty(s.Agent, "—")
		if s.AttachedCount > 0 {
			agent = "—"
		}
		name := truncate(s.Project+"/"+s.Branch, 27)
		line := fmt.Sprintf("  %-27s %-11s %-11s %-9s %s", name, s.Lifecycle, presence, agent, s.Policy)
		rows = append(rows, m.selectableLine(position[idx], line, s))
	}
	return strings.Join(rows, "\n")
}

func (m app) compactSelectionView() string {
	idx, ok := m.selectedSessionIndex()
	if !ok {
		return mutedStyle.Render("No session selected")
	}
	s := m.sessions[idx]
	result := titleStyle.Render("Selected") + "  " + truncate(s.Project+"/"+s.Branch, 34) + "  ·  actions: " + strings.Join(availableActions(s), " / ")
	if s.Operation != "" {
		result += "\nprogress  " + s.Operation
	}
	return result
}

func (m app) commandView(width, height int) string {
	indices := m.visibleIndices()
	query := strings.TrimSpace(m.filter.Value())
	prompt := "Press / and type any project, branch, lifecycle, presence, agent, or policy fact"
	if query != "" {
		prompt = fmt.Sprintf("Query %q · %d matches", query, len(indices))
	}
	results := titleStyle.Render("Search-first command center") + "\n" + mutedStyle.Render(prompt) + "\n\n" + m.commandRows(indices, maxRows(height, width))
	commands := titleStyle.Render("Act on the highlighted result") + "\n" + m.compactSelectionView() + "\n\n" + mutedStyle.Render("The palette never invents eligibility; every action shown comes from the fixture's daemon plan.")
	if width >= 105 {
		leftWidth := width * 58 / 100
		return lipgloss.JoinHorizontal(lipgloss.Top, renderPanel(leftWidth, results), " ", renderPanel(width-leftWidth-1, commands+"\n\n"+m.detailView()))
	}
	return lipgloss.JoinVertical(lipgloss.Left, renderPanel(width, results), renderPanel(width, commands))
}

func (m app) commandRows(indices []int, limit int) string {
	if len(indices) == 0 {
		return mutedStyle.Render("No match. Clear the query to recover the full session set.")
	}
	var rows []string
	for pos, idx := range indices {
		if pos >= limit {
			rows = append(rows, mutedStyle.Render(fmt.Sprintf("… %d more matches", len(indices)-pos)))
			break
		}
		s := m.sessions[idx]
		presence, agent := sessionSignals(s)
		line := fmt.Sprintf("  %-23s %-10s %-10s %-8s %s", truncate(s.Project+"/"+s.Branch, 23), s.Lifecycle, presence, agent, s.Policy)
		rows = append(rows, m.selectableLine(pos, line, s))
	}
	return strings.Join(rows, "\n")
}

func (m app) focusView(width, height int) string {
	indices := m.visibleIndices()
	idx, ok := m.selectedSessionIndex()
	if !ok {
		return renderPanel(width, "No matching sessions")
	}
	s := m.sessions[idx]
	position := m.selected + 1
	focus := titleStyle.Render(fmt.Sprintf("Focus deck · %d/%d", position, len(indices))) + "\n" + m.focusCard(s)
	radar := titleStyle.Render("Stream radar · context without leaving focus") + "\n" + m.radarRows(indices, maxRows(height, width), width < 110)
	if width >= 110 {
		focusWidth := width * 57 / 100
		return lipgloss.JoinHorizontal(lipgloss.Top, renderPanel(focusWidth, focus), " ", renderPanel(width-focusWidth-1, radar))
	}
	return lipgloss.JoinVertical(lipgloss.Left, renderPanel(width, focus), renderPanel(width, radar))
}

func (m app) focusCard(s session) string {
	presence, agent := sessionSignals(s)
	lines := []string{
		fmt.Sprintf("%s / %s", s.Project, s.Branch),
		mutedStyle.Render("UUID " + s.ID),
		"",
		fmt.Sprintf("Lifecycle  %-12s  Presence  %s", styledFact(s.Lifecycle), presence),
		fmt.Sprintf("Agent      %-12s  Policy    %s", styledFact(agent), styledFact(s.Policy)),
		"",
		"Primary actions  " + strings.Join(availableActions(s), " · "),
	}
	if s.Operation != "" {
		lines = append(lines, "Progress  "+s.Operation)
	}
	return strings.Join(lines, "\n")
}

func (m app) radarRows(indices []int, limit int, compact bool) string {
	var rows []string
	for pos, idx := range indices {
		if pos >= limit {
			rows = append(rows, mutedStyle.Render(fmt.Sprintf("… %d more", len(indices)-pos)))
			break
		}
		s := m.sessions[idx]
		presence, agent := sessionSignals(s)
		var line string
		if compact {
			line = fmt.Sprintf("  %-23s %-10s %-10s %-8s %s", truncate(s.Project+"/"+s.Branch, 23), s.Lifecycle, presence, agent, s.Policy)
		} else {
			line = fmt.Sprintf("  %s\n    %s · %s · %s · %s", truncate(s.Project+"/"+s.Branch, 35), s.Lifecycle, presence, agent, s.Policy)
		}
		rows = append(rows, m.selectableLine(pos, line, s))
	}
	return strings.Join(rows, "\n")
}

func (m app) lanesView(width, _ int) string {
	indices := m.visibleIndices()
	type lane struct {
		title   string
		indices []int
	}
	lanes := []lane{{title: "Inspect first"}, {title: "In progress"}, {title: "Recover"}, {title: "Removing"}}
	for _, idx := range indices {
		switch laneFor(m.sessions[idx]) {
		case 0:
			lanes[0].indices = append(lanes[0].indices, idx)
		case 1:
			lanes[1].indices = append(lanes[1].indices, idx)
		case 2:
			lanes[2].indices = append(lanes[2].indices, idx)
		case 3:
			lanes[3].indices = append(lanes[3].indices, idx)
		}
	}
	if width < 100 {
		panels := make([]string, 0, len(lanes))
		for _, lane := range lanes {
			content := titleStyle.Render(fmt.Sprintf("%s · %d", lane.title, len(lane.indices))) + "\n" + m.laneRows(lane.indices, true, 1)
			panels = append(panels, renderPanel(width, content))
		}
		return lipgloss.JoinVertical(lipgloss.Left, panels...)
	}
	available := width - (len(lanes) - 1)
	base := available / len(lanes)
	remainder := available % len(lanes)
	panels := make([]string, 0, len(lanes)*2-1)
	for i, lane := range lanes {
		laneWidth := base
		if i < remainder {
			laneWidth++
		}
		limit := 2
		if width >= 132 {
			limit = 4
		}
		content := titleStyle.Render(fmt.Sprintf("%s · %d", lane.title, len(lane.indices))) + "\n" + m.laneRows(lane.indices, false, limit)
		panels = append(panels, renderPanel(laneWidth, content))
		if i != len(lanes)-1 {
			panels = append(panels, " ")
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, panels...)
}

func laneFor(s session) int {
	if s.Lifecycle == "deleting" || s.Lifecycle == "discarding" {
		return 3
	}
	if s.Lifecycle == "missing" || s.Lifecycle == "unreachable" {
		return 2
	}
	if (s.AttachedCount == 0 && (s.Agent == "attention" || s.Agent == "failed")) || s.Policy == "invalid" {
		return 0
	}
	return 1
}

func (m app) laneRows(indices []int, compact bool, limit int) string {
	if len(indices) == 0 {
		return mutedStyle.Render("No streams")
	}
	visible := m.visibleIndices()
	position := map[int]int{}
	for pos, idx := range visible {
		position[idx] = pos
	}
	selectedIndex, selected := m.selectedSessionIndex()
	selectedRow := 0
	if selected {
		for row, idx := range indices {
			if idx == selectedIndex {
				selectedRow = row
				break
			}
		}
	}
	start := selectedRow - limit/2
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
	var rows []string
	if start > 0 {
		rows = append(rows, mutedStyle.Render(fmt.Sprintf("… %d above", start)))
	}
	for row := start; row < end; row++ {
		idx := indices[row]
		s := m.sessions[idx]
		presence, agent := sessionSignals(s)
		var line string
		if compact {
			line = fmt.Sprintf("  %-12s %-11s %-11s %-9s %-8s %s", truncate(s.Project+"/"+s.Branch, 12), s.Lifecycle, presence, agent, s.Policy, truncate(s.Operation, 12))
		} else {
			line = fmt.Sprintf("  %s\n    %s\n    %s\n    %s · %s", truncate(s.Project+"/"+s.Branch, 20), s.Lifecycle, presence, agent, s.Policy)
			if s.Operation != "" {
				line += "\n    " + truncate(s.Operation, 20)
			}
		}
		rows = append(rows, m.selectableLine(position[idx], line, s))
	}
	if end < len(indices) {
		rows = append(rows, mutedStyle.Render(fmt.Sprintf("… %d below", len(indices)-end)))
	}
	return strings.Join(rows, "\n")
}

func sessionSignals(s session) (presence, agent string) {
	presence = "unattended"
	if s.AttachedCount > 0 {
		presence = fmt.Sprintf("attached:%d", s.AttachedCount)
	}
	agent = firstNonEmpty(s.Agent, "—")
	if s.AttachedCount > 0 {
		agent = "—"
	}
	return presence, agent
}

func (m app) sessionRows(indices []int, wide bool, limit int) string {
	if len(indices) == 0 {
		return mutedStyle.Render("No matching sessions")
	}
	header := "  PROJECT / BRANCH        LIFE       PRESENCE   AGENT    POLICY"
	if !wide {
		header = "  PROJECT / BRANCH              LIFE       SIGNAL / POLICY"
	}
	rows := []string{mutedStyle.Render(header)}
	for pos, idx := range indices {
		if pos >= limit {
			rows = append(rows, mutedStyle.Render(fmt.Sprintf("  … %d more", len(indices)-pos)))
			break
		}
		s := m.sessions[idx]
		presence := "unattended"
		if s.AttachedCount > 0 {
			presence = fmt.Sprintf("attached:%d", s.AttachedCount)
		}
		agent := s.Agent
		if s.AttachedCount > 0 {
			agent = "—"
		} else if agent == "" {
			agent = "—"
		}
		name := truncate(s.Project+" / "+s.Branch, 31)
		var line string
		if wide {
			line = fmt.Sprintf("  %-24s %-10s %-10s %-8s %s", truncate(name, 24), s.Lifecycle, presence, agent, s.Policy)
		} else {
			signal := presence + " · " + agent + " / " + s.Policy
			line = fmt.Sprintf("  %-27s %-10s %s", truncate(name, 27), s.Lifecycle, signal)
		}
		rows = append(rows, m.selectableLine(pos, line, s))
	}
	return strings.Join(rows, "\n")
}

func (m app) cardRows(indices []int, limit int) string {
	if len(indices) == 0 {
		return mutedStyle.Render("No sessions in this project")
	}
	var rows []string
	for pos, idx := range indices {
		if pos >= limit {
			rows = append(rows, mutedStyle.Render(fmt.Sprintf("… %d more", len(indices)-pos)))
			break
		}
		s := m.sessions[idx]
		presence := "unattended"
		if s.AttachedCount > 0 {
			presence = fmt.Sprintf("attached:%d", s.AttachedCount)
		}
		agent := s.Agent
		if agent == "" || s.AttachedCount > 0 {
			agent = "—"
		}
		line := fmt.Sprintf("  %-32s\n    %s · %s · %s · %s", truncate(s.Branch, 32), s.Lifecycle, presence, agent, s.Policy)
		rows = append(rows, m.selectableLine(pos, line, s))
	}
	return strings.Join(rows, "\n")
}

func (m app) attentionRows(indices []int, limit int) string {
	if len(indices) == 0 {
		return mutedStyle.Render("Nothing in this group")
	}
	visible := m.visibleIndices()
	position := map[int]int{}
	for pos, idx := range visible {
		position[idx] = pos
	}
	var rows []string
	for n, idx := range indices {
		if n >= limit {
			rows = append(rows, mutedStyle.Render(fmt.Sprintf("… %d more", len(indices)-n)))
			break
		}
		s := m.sessions[idx]
		presence := "unattended"
		if s.AttachedCount > 0 {
			presence = fmt.Sprintf("attached:%d", s.AttachedCount)
		}
		reason := s.AgentReason
		if reason == "" {
			reason = s.Operation
		}
		line := fmt.Sprintf("  %-10s %s\n    %s · %s\n    %s · %s · %s", truncate(s.Project, 10), truncate(s.Branch, 27), s.Lifecycle, presence, firstNonEmpty(s.Agent, "no signal"), s.Policy, truncate(reason, 22))
		rows = append(rows, m.selectableLine(position[idx], line, s))
	}
	return strings.Join(rows, "\n")
}

func (m app) selectableLine(position int, line string, s session) string {
	prefix := "  "
	if position == m.selected {
		prefix = "› "
	}
	line = prefix + strings.TrimPrefix(line, "  ")
	if position == m.selected {
		return selectedStyle.Render(line)
	}
	if s.AttachedCount == 0 && (s.Agent == "attention" || s.Agent == "failed") {
		return lipgloss.NewStyle().Foreground(urgent).Render(line)
	}
	if s.Policy != "current" || s.Lifecycle == "missing" || s.Lifecycle == "unreachable" {
		return lipgloss.NewStyle().Foreground(warning).Render(line)
	}
	return line
}

func (m app) detailView() string {
	idx, ok := m.selectedSessionIndex()
	if !ok {
		return titleStyle.Render("Inspector") + "\n" + mutedStyle.Render("No session selected")
	}
	s := m.sessions[idx]
	presence := "unattended"
	if s.AttachedCount > 0 {
		presence = fmt.Sprintf("attached (%d confirmed)", s.AttachedCount)
	}
	agent := firstNonEmpty(s.Agent, "no unattended signal")
	if s.AttachedCount > 0 {
		agent = "suppressed while attached"
	}
	actions := availableActions(s)
	lines := []string{
		titleStyle.Render("Inspector"),
		truncate(fmt.Sprintf("%s / %s", s.Project, s.Branch), 52),
		mutedStyle.Render("UUID " + s.ID),
		"lifecycle  " + styledFact(s.Lifecycle),
		"presence   " + presence,
		"agent      " + styledFact(agent),
		"policy     " + styledFact(s.Policy),
	}
	if s.Operation != "" {
		lines = append(lines, "operation  "+s.Operation)
	}
	lines = append(lines, "actions    "+strings.Join(actions, " · "))
	return strings.Join(lines, "\n")
}

func availableActions(s session) []string {
	actions := []string{"inspect"}
	switch s.Lifecycle {
	case "ready":
		actions = append(actions, "attach")
	case "stopped":
		if s.Policy != "invalid" {
			actions = append(actions, "start", "attach")
		}
	case "missing", "unreachable":
		actions = append(actions, "repair plan")
	}
	if s.AttachedCount > 0 {
		actions = append(actions, "detach this client")
	}
	if s.Policy == "outdated" {
		actions = append(actions, "policy diff", "recreate")
	}
	return actions
}

func (m app) createView(width int) string {
	kind := "NEW CREATE"
	identity := "reserves UUID s-new-019 · operation op-create-77"
	if m.draft.Replacing {
		kind = "TRY AGAIN WITH CHANGES · REPLACEMENT CREATE"
		identity = "NEW UUID s-new-020 · NEW operation op-create-78 · supersedes failed request"
	}
	content := strings.Join([]string{
		titleStyle.Render(kind),
		"",
		"Project     " + m.draft.Project,
		"Source      " + m.draft.Source,
		"Branch      " + m.draft.Branch,
		"Policy      " + m.draft.Policy,
		"",
		lipgloss.NewStyle().Foreground(warning).Render(identity),
		"",
		"enter submits fixture request · esc cancels",
	}, "\n")
	return centeredPanel(width, content)
}

func (m app) createFailedView(width int) string {
	content := strings.Join([]string{
		titleStyle.Render("CREATION FAILED · bounded fixture diagnostic"),
		"",
		"Environment preparation failed: synthetic cache checksum mismatch.",
		fmt.Sprintf("Reserved UUID  %s", m.draft.ReservedID),
		fmt.Sprintf("Operation      %s", m.draft.OperationID),
		fmt.Sprintf("Immutable      %s · %s · %s", m.draft.Project, m.draft.Source, m.draft.Branch),
		"",
		lipgloss.NewStyle().Foreground(positive).Render("r  Exact Retry") + " — same UUID, operation, source, branch, and policy",
		lipgloss.NewStyle().Foreground(warning).Render("t  Try again with changes") + " — integrated replacement with new identity",
		"esc  leave the failed operation visible",
	}, "\n")
	return centeredPanel(width, content)
}

func (m app) branchesView(width int) string {
	rows := []string{titleStyle.Render("Retained branches · Git resources, not sessions"), ""}
	for _, b := range m.branches {
		rows = append(rows, fmt.Sprintf("%-10s  %-30s  %s", b.Project, b.Name, b.Tip))
	}
	rows = append(rows, "", "They have no UUID, runtime, attachment, agent condition, or policy condition.", "n new session from selected source · r rename · p publish · x loss preview · esc back")
	return centeredPanel(width, strings.Join(rows, "\n"))
}

func (m app) policyView(width int) string {
	idx, ok := m.selectedSessionIndex()
	if !ok {
		return centeredPanel(width, "No session selected")
	}
	s := m.sessions[idx]
	rows := []string{
		titleStyle.Render("Policy comparison · " + s.Policy),
		"",
		fmt.Sprintf("%s / %s", s.Project, s.Branch),
		"Effective snapshot remains immutable; configuration reload did not update it.",
		"",
	}
	if len(s.PolicyDiff) == 0 {
		rows = append(rows, "No typed differences.")
	} else {
		rows = append(rows, s.PolicyDiff...)
	}
	if s.Policy == "outdated" {
		rows = append(rows, "", "Recreate with current policy retains the old branch as source and creates a new UUID.")
	}
	if s.Policy == "invalid" {
		rows = append(rows, "", "Invalid blocks a later Start; it does not silently mutate or stop a running session.")
	}
	return centeredPanel(width, strings.Join(rows, "\n"))
}

func (m app) deletePreviewView(width int) string {
	attached, sessions := 0, 0
	for _, s := range m.sessions {
		if s.Project == m.deleteProject {
			sessions++
			attached += s.AttachedCount
		}
	}
	content := strings.Join([]string{
		lipgloss.NewStyle().Bold(true).Foreground(urgent).Render("DELETE PROJECT AND ALL P DATA"),
		"",
		"Project                 " + m.deleteProject,
		fmt.Sprintf("Sessions               %d", sessions),
		fmt.Sprintf("Live attachments       %d (confirmation terminates these)", attached),
		"Assigned/retained refs  3 · 2 commits may lose their last P reference",
		"Origin preservation     unknown (refresh failed)",
		"Runtime-local loss      1 modified file · 2 untracked files",
		"Owned resources         credentials · runtimes · image cache · repository",
		"Not deleted             external mount contents · delivered event logs",
		"",
		"Confirmation fingerprint: fp:forge:8d7f (fixture)",
		lipgloss.NewStyle().Foreground(urgent).Render("y confirms exact reviewed facts") + " · esc cancels",
	}, "\n")
	return centeredPanel(width, content)
}

func (m app) deleteProgressView(width int) string {
	rows := []string{titleStyle.Render("Project deletion · ensure absent"), "", "Every retry re-inspects all targets; completed deletion is never rolled back.", ""}
	complete := true
	for _, target := range m.deleteTargets {
		state := target.State
		if state != "deleted" && state != "already_absent" {
			complete = false
		}
		rows = append(rows, fmt.Sprintf("%-34s %s", target.Name, styledFact(state)))
	}
	rows = append(rows, "")
	if complete {
		rows = append(rows, lipgloss.NewStyle().Foreground(positive).Render("All owned targets are confirmed absent; registry tombstone can be removed."))
	} else {
		rows = append(rows, lipgloss.NewStyle().Foreground(warning).Render("Partial success is expected. r retries only the same authorized ensure-absent outcome."))
	}
	return centeredPanel(width, strings.Join(rows, "\n"))
}

func (m app) helpView(width int) string {
	content := strings.Join([]string{
		titleStyle.Render("Prototype controls"),
		mutedStyle.Render("Compare and switch structural variants without changing fixture state."),
		"",
		"1–6            direct shortcuts for initial variants",
		"Tab / v        cycle variants / open full gallery",
		"j/k            move between sessions",
		"h/l            move projects, or collapse/expand outline groups",
		"/              fuzzy filter using sahilm/fuzzy",
		"a or Enter     attach, or switch this prototype client",
		"d              detach this prototype client only",
		"c              creation and retry/replacement probe",
		"b              retained branches",
		"p              typed policy comparison",
		"X              aggregate project deletion preview",
		"esc            return/cancel",
		"q              quit from overview",
	}, "\n")
	return centeredPanel(width, content)
}

func styledFact(value string) string {
	switch value {
	case "attention", "failed", "invalid", "unreachable", "missing", "remaining":
		return lipgloss.NewStyle().Foreground(urgent).Render(value)
	case "outdated", "starting", "creating", "unknown":
		return lipgloss.NewStyle().Foreground(warning).Render(value)
	case "ready", "current", "deleted", "already_absent":
		return lipgloss.NewStyle().Foreground(positive).Render(value)
	default:
		return value
	}
}

func centeredPanel(width int, content string) string {
	panelWidth := width - 4
	if panelWidth > 92 {
		panelWidth = 92
	}
	if panelWidth < 4 {
		panelWidth = 4
	}
	return renderPanel(panelWidth+4, content)
}

func tableWidth(width int) int {
	if width >= 110 {
		return width * 62 / 100
	}
	return width
}

func detailWidth(width int) int {
	return width - tableWidth(width) - 1
}

func navigatorWidth(width int) int {
	if width < 90 {
		return width
	}
	return width * 30 / 100
}

func attentionWidth(width int) int {
	if width < 100 {
		return width
	}
	return width * 43 / 100
}

func renderPanel(outerWidth int, content string) string {
	if outerWidth < 8 {
		outerWidth = 8
	}
	frame := panelStyle.GetHorizontalFrameSize()
	return panelStyle.Copy().Width(outerWidth - frame).MaxWidth(outerWidth).Render(content)
}

func maxRows(height, width int) int {
	if width < 100 || height < 28 {
		return 4
	}
	return 8
}

func fitLines(s string, height int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= height {
		return s
	}
	if height < 2 {
		return strings.Join(lines[:height], "\n")
	}
	return strings.Join(append(lines[:height-1], mutedStyle.Render("… frame clipped; enlarge terminal to inspect remaining content")), "\n")
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + "…"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
