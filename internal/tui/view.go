package tui

import (
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/lgvo/p.ai/internal/control"
)

// Never let service logs, descriptions, or daemon errors issue terminal controls.
func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return ' '
	}, ansi.Strip(s))
}
func clipped(s string, width int) string { return ansi.Truncate(s, max(1, width), "…") }

// Keep each key and action together, wrapping between actions rather than
// dropping the commands at the end of a narrow viewport.
func (m Model) controls(commands string, width int) string {
	lines, line := []string{}, ""
	for _, action := range strings.Split(commands, " · ") {
		if line != "" && ansi.StringWidth(line)+3+ansi.StringWidth(action) > width {
			lines = append(lines, m.styles.muted.Render(line))
			line = ""
		}
		if line != "" {
			line += " · "
		}
		line += clipped(action, width)
	}
	return strings.Join(append(lines, m.styles.muted.Render(line)), "\n")
}

func (m Model) browserControls(width int) string {
	if width < 72 {
		return m.controls("j/k · PgUp/PgDn page · Enter open · gg/G ends", width) + "\n" +
			m.controls("s stop · c create · P project · / search", width) + "\n" +
			m.controls("D details · A reports · S services · ? help", width) + "\n" +
			m.controls("O operations · p policy · b branches", width) + "\n" +
			m.controls("R rename · d discard · X delete · q/Esc back", width)
	}
	return m.controls("j/k move · PgUp/PgDn page · gg/G ends · Enter open · s stop · c create", width) + "\n" +
		m.controls("P project · / search · D details · A reports · S services · O operations · ? help", width) + "\n" +
		m.controls("p policy · b branches · R rename · d discard · X delete · q/Esc back", width)
}

func (m Model) noticeLines() []string {
	if m.notice == "" {
		return nil
	}
	width := min(m.width, 96)
	if m.page == "sessions" {
		width = min(m.width, 148)
	}
	return strings.Split(ansi.Hardwrap(safe(m.notice), max(1, width), true), "\n")
}

func (m Model) feedbackHeight() int {
	n := len(m.noticeLines())
	if m.working {
		n++
	}
	if m.stale {
		n++
	}
	return n
}

func (m Model) styledRuntime(s control.SessionView) string {
	state, style := s.Condition, m.styles.muted
	if state == "ready" {
		state, style = "running", m.styles.running
	} else if state == "missing" || state == "unreachable" {
		style = m.styles.danger
	} else if state != "stopped" {
		style = m.styles.warning
	}
	label := style.Render(state)
	if a := activity(s); a != "" {
		style := m.styles.muted
		if s.LatestUnattendedCondition.Condition == "attention" {
			style = m.styles.warning
		} else if s.LatestUnattendedCondition.Condition == "failed" {
			style = m.styles.danger
		}
		label += style.Render(a)
	}
	return label
}

func (m Model) rowLabel(r row, width int) string {
	if m.page == "services" {
		for _, service := range m.services {
			if service.Unit != r.id {
				continue
			}
			state, style := service.ActiveState+" ("+service.SubState+")", m.styles.muted
			if service.ActiveState == "unknown" && service.SubState == "not-loaded" {
				state = "installed"
			}
			if service.ActiveState == "active" {
				style = m.styles.running
			} else if service.ActiveState == "failed" {
				style = m.styles.danger
			}
			space := max(3, width-ansi.StringWidth(state)-2)
			name := clipped(safe(service.Unit), space)
			return m.styles.text.Render(name+strings.Repeat(" ", max(2, width-ansi.StringWidth(name)-ansi.StringWidth(state)))) + style.Render(state)
		}
	}
	if m.page == "sessions" {
		for _, s := range m.data.sessions {
			if s.UUID != r.id {
				continue
			}
			status := m.styledRuntime(s)
			statusWidth := max(7, len("running !waiting"))
			if width < 54 {
				statusWidth = ansi.StringWidth(status)
			}
			projectWidth := min(24, max(6, (width-statusWidth-5)/3))
			branchWidth := max(2, width-projectWidth-statusWidth-5)
			project := clipped(safe(s.Project), projectWidth)
			branch := clipped(safe(s.Branch), branchWidth)
			connector := "└─ "
			rows := m.sessionRows()
			for i, r := range rows {
				if r.id == s.UUID && i+1 < len(rows) {
					for _, next := range m.data.sessions {
						if next.UUID == rows[i+1].id && next.Project == s.Project && priority(next) == priority(s) {
							connector = "├─ "
						}
					}
				}
			}
			return m.styles.text.Render(project+strings.Repeat(" ", projectWidth-ansi.StringWidth(project))+" "+connector+branch+strings.Repeat(" ", branchWidth-ansi.StringWidth(branch))+" ") + status

		}
	}
	if m.page == "projects" {
		counts := m.projectCounts(r.id)
		all := m.projectCounts("")
		numbers := []int{counts[0], counts[1], counts[2], counts[3]}
		labels := []string{"!waiting", "running", "stopped", "total"}
		if width < 76 {
			labels = []string{"!w", "r", "s", "t"}
		}
		parts := []string{}
		for i, n := range numbers {
			parts = append(parts, fmt.Sprintf("%s %*d", labels[i], len(fmt.Sprint(all[i])), n))
		}
		parts[0], parts[1], parts[2] = m.styles.warning.Render(parts[0]), m.styles.running.Render(parts[1]), m.styles.muted.Render(parts[2])
		parts[3] = m.styles.text.Render(parts[3])
		suffix := strings.Join(parts, m.styles.text.Render(" · "))
		name := r.id
		if name == "" {
			name = "All projects"
		}
		space := max(3, width-ansi.StringWidth(suffix)-2)
		name = clipped(safe(name), min(28, space))
		return m.styles.text.Render(name+strings.Repeat(" ", max(2, width-ansi.StringWidth(name)-ansi.StringWidth(suffix)))) + suffix
	}
	return m.styles.text.Render(clipped(safe(r.label), width))
}
func plainLines(s string) []string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = safe(lines[i])
	}
	return lines
}

func (m Model) View() tea.View {
	if m.width < 48 || m.height < 16 {
		v := tea.NewView(m.styles.text.Render(clipped("P · terminal too small (minimum 48×16); resize or q to quit", m.width)))
		v.AltScreen = true
		return v
	}
	width := min(m.width, 148)
	if m.page == "sessions" {
		width = min(m.width-2, 148)
	}
	if m.page != "sessions" {
		width = min(width, 96)
	}
	var content string
	switch m.page {
	case "sessions":
		content = m.styles.heading.Render(clipped("P · Sessions · waiting → running → other", width)) + "\n" + m.browser(width)
	case "projects":
		content = m.listPage("Select project", m.rows(), width, "Enter select · / search · q/Esc back", nil)
	case "create":
		content = m.listPage("Create · "+m.form, m.rows(), width, "Enter choose · / search · q/Esc back", []string{"Project → Branch → Policy"})
	case "retained":
		content = m.listPage("Retained branches · "+m.contextSession.Project, m.rows(), width, "j/k select · q/Esc back", []string{"Unassigned P branches. Create with choice existing to resume one."})
	case "services":
		details := []string{"Session: " + m.contextSession.Project + " / " + m.contextSession.Branch, "Session-user p-project-*.service units only.", "Ports are session-local; no host publication is added."}
		if !m.servicesFresh {
			details = append(details, "Service observation unavailable/loading; controls disabled.")
		}
		if len(m.services) > 0 && m.cursor < len(m.services) {
			details = append(details, "", m.services[m.cursor].Description)
		} else if m.servicesFresh {
			details = append(details, "", "No project units observed. Install a user unit inside the session.", "Use ~/.config/systemd/user/p-project-NAME.service and systemctl --user.")
		}
		commands := "q/Esc back"
		if m.servicesFresh && len(m.services) > 0 {
			action := "start"
			if m.services[m.cursor].ActiveState == "active" {
				action = "stop"
			}
			commands = "Enter/J journal · s " + action + " · r restart · q/Esc back"
			if m.services[m.cursor].ActiveState == "unknown" && m.services[m.cursor].SubState == "not-loaded" {
				details = append(details, "Start is available; the manager has no loaded active state.")
			}
		}
		content = m.listPage("Project services", m.rows(), width, commands, details)

	case "agents", "policy", "progress", "help", "details":
		title, lines, commands := m.inspectionContent()
		content = m.textPage(title, lines, width, commands)
	case "operations":
		content = m.listPage("Durable operations", m.rows(), width, "Enter inspect · j/k select · q/Esc back", nil)

	case "confirm":
		lines := m.reviewLines()
		start := min(m.reviewOffset, max(0, len(lines)-1))
		lines = lines[start:min(len(lines), start+m.capacity())]
		content = m.textPage("Confirm action", lines, width, "j/k/Pg scroll · y confirm · n/Enter decline · q/Esc back")
		content += "\n" + m.styles.warning.Render("Confirm [y/N] · Enter means No")
	case "review":
		lines := m.reviewLines()
		m.reviewOffset = min(m.reviewOffset, max(0, len(lines)-1))
		end := min(len(lines), m.reviewOffset+m.capacity())
		shown := lines[m.reviewOffset:end]
		content = m.textPage("Removal loss preview", shown, width, "j/k/PgUp/PgDn scroll · y authorize · n/q/Esc cancel")
		content += "\n" + m.styles.danger.Render("[y/N] Default No · review all losses; daemon rechecks freshness and ownership")
	case "form":
		prompt := map[string]string{"project-name": "Project path", "project-url": "SSH origin URL (Enter for local-only)", "branch-name": "New branch name", "rename": "New assigned branch name"}[m.form]
		input := safe(m.input)
		available := max(1, width-4-ansi.StringWidth(prompt)-3)
		if ansi.StringWidth(input) > available {
			input = "…" + ansi.TruncateLeft(input, ansi.StringWidth(input)-available+1, "")
		}
		content = m.textPage("Create / rename", []string{prompt + "> " + input + "▏", "", "Letters, including q/g/G, are ordinary text here."}, width, "Enter continue · Esc/Ctrl-C cancel/back")
	case "journal":
		lines := []string{}
		end := min(len(m.journal), m.journalOffset+m.capacity())
		start := min(m.journalOffset, end)
		for i := start; i < end; i++ {
			text := safe(m.journal[i])
			if m.pan > 0 {
				text = ansi.TruncateLeft(text, m.pan, "")
			}
			lines = append(lines, fmt.Sprintf("%4d %s", i+1, text))
		}
		title := fmt.Sprintf("%s · journal tail · %d–%d/%d · follow %t", m.serviceUnit, start+1, end, len(m.journal), m.follow)
		if m.finding {
			title = "find> " + m.find
		} else if m.find != "" {
			title += " · find: " + m.find
		}
		content = m.textPage(title, lines, width, "j/k/Pg scroll · h/l pan · / find · n/N match · f follow · q/Esc back")

	}
	if m.working {
		content += "\n" + m.styles.muted.Render("Waiting for daemon… (Back leaves accepted work running)")
	}
	if m.notice != "" {
		content += "\n" + m.styles.warning.Render(strings.Join(m.noticeLines(), "\n"))
	}
	if m.stale {
		content += "\n" + m.styles.danger.Render("STALE · connection failed; actions need a fresh observation")
	}
	// A frame must fit the current terminal; controls never wrap into older frames.
	lines := strings.Split(content, "\n")
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	for i := range lines {
		lines[i] = clipped(lines[i], width)
	}
	content = strings.Join(lines, "\n")
	if m.width > width {
		content = lipgloss.NewStyle().MarginLeft((m.width - width) / 2).Render(content)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func (m Model) listPage(title string, rows []row, width int, commands string, details []string) string {
	if m.choiceSearching || m.choiceQuery != "" {
		title = "fuzzy search> " + m.choiceQuery
	}
	capacity := m.capacity()
	return m.list(title, rows, width, capacity) + "\n" + m.controls(commands, width) + "\n" + strings.Join(m.renderLines(details, width), "\n")
}
func (m Model) renderLines(lines []string, width int) []string {
	out := []string{}
	for _, s := range lines {
		out = append(out, m.factLine(s, width))
	}
	return out
}

func (m Model) reviewLines() []string {
	width := max(1, min(m.width, 96)-4)
	return strings.Split(ansi.Hardwrap(strings.Join(plainLines(m.review), "\n"), width, true), "\n")
}
func (m Model) inspectionPage() bool {
	switch m.page {
	case "agents", "policy", "progress", "help", "details":
		return true
	}
	return false
}
func (m Model) inspectionContent() (string, []string, string) {
	switch m.page {
	case "details":
		return "Session details", m.fullSessionDetails(m.contextSession), "A reports · S services · p policy · q/Esc back"
	case "agents":
		lines := []string{"Session: " + m.contextSession.Project + " / " + m.contextSession.Branch, "P retains one latest unattended report; this is not an active-agent inventory.", "Live instance discovery and conversation previews are unavailable."}
		if r := m.contextSession.LatestUnattendedCondition; r != nil {
			lines = append(lines, "", fmt.Sprintf("%s · %s", r.Adapter, r.Condition), "Source: "+r.Source, "Reason: "+r.Reason, "Received: "+r.ReceivedAt, "Report clears on confirmed attachment; idle does not prove all work finished.")
		} else {
			lines = append(lines, "", "No retained unattended report. Agent activity is unknown.")
		}
		return "Agent reports", lines, "q/Esc back"
	case "policy":
		lines := []string{"Session: " + m.contextSession.Project + " / " + m.contextSession.Branch, "Captured policy digest: " + m.contextSession.PolicySHA256, "Policy condition: " + m.contextSession.PolicyCondition, "", "Host configuration owns grants; this page does not edit them.", "Outdated sessions retain captured policy. Invalid policy can block Start."}
		if e := m.contextSession.Environment; e != nil {
			lines = append(lines, "", "Environment: "+e.Selection, "Source commit: "+e.CommitOID, "Cache: "+e.Cache, "System: "+e.System, "Environment key: "+e.Key, "Base image: "+e.BaseImageFingerprint, "Image: "+e.ImageFingerprint)
		}
		return "Session policy and environment", lines, "q/Esc back"
	case "progress":
		lines := []string{}
		commands := "r retry operation · q/Esc back"
		if m.operationID != "" {
			op := m.operation
			lines = append(lines, op.Kind+" · "+op.Status+" / "+op.Phase, "Operation: "+m.operationID, "Key: "+op.Key, "Session: "+op.SessionUUID, "", op.Diagnostic, "", "Leaving this screen does not cancel an accepted operation.")
			if control.WorkspaceInspectionRequiresFreshRequest(op) {
				lines = append(lines, "Inspection finished with an error. Return and request a fresh inspection.")
				commands = "q/Esc back"
			} else {
				lines = append(lines, "Retry resumes exact captured intent; changed inputs need a new request.")
			}
			if op.Kind == "session.create" && op.Status == "running" && op.Phase == "branch-assigned" {
				lines = append(lines[:5], append([]string{"Preparing captured source and environment; the runtime is not ready.", "Large source trees can take time. A pending phase is not a failure."}, lines[5:]...)...)
			}
		} else {
			lines = append(lines, m.contextSession.Project+" / "+m.contextSession.Branch, "Session: "+m.contextSession.UUID, "Condition: "+m.contextSession.Condition, "", m.contextSession.Diagnostic, "", "Leaving startup prevents automatic entry when it finishes.")
		}
		return "Operation / readiness", lines, commands
	case "help":
		return "P · keys", []string{"j/k/arrows: select · PgUp/PgDn or Ctrl+B/F: page", "Home/End, gg/G: first/last · Ctrl+U/D: half page", "P: exact project scope · /: fuzzy search across branch/status/reports", "Enter: Start if needed, then real terminal attachment", "s: confirmed Stop · c: Create Project → Branch → Policy", "D: complete session details · A: actual unattended report · S: project services", "p: captured policy/environment · b: retained branches · R: Rename", "O: durable operations · d: Discard · X: Delete session/branch", "Back closes the current page, then search, then project scope, then exits.", "In text input q is ordinary text; Esc/Ctrl-C cancels.", "", "Inside the real terminal: tmux owns input; Ctrl+B then lowercase d detaches.", "Detach before browsing Agents/Services; attachment leases stay daemon-owned.", "Authenticated Codex acceptance remains your manual test."}, "q/Esc back"
	}
	return "", nil, ""
}
func (m Model) wrappedText(lines []string) []string {
	return strings.Split(ansi.Wrap(strings.Join(plainLines(strings.Join(lines, "\n")), "\n"), max(1, min(m.width, 96)-4), ""), "\n")
}
func (m Model) textPage(title string, lines []string, width int, commands string) string {
	body := m.renderLines(lines, width-4)
	capacity := max(1, m.height-8-max(0, m.feedbackHeight()-2))
	if m.inspectionPage() {
		body = m.wrappedText(lines)
		capacity = m.capacity()
		start := min(m.reviewOffset, max(0, len(body)-capacity))
		body = m.renderLines(body[start:], width-4)
		commands = "j/k/Pg scroll · " + commands
	}
	if len(body) > capacity {
		body = body[:capacity]
	}
	titleStyle := m.styles.heading
	if m.page == "review" {
		titleStyle = m.styles.danger.Bold(true)
	}
	return titleStyle.Render(clipped(safe(title), width)) + "\n" + strings.Join(body, "\n") + "\n" + m.controls(commands, width)
}
func (m Model) emptyListLabel() string {
	if m.page == "services" && !m.servicesFresh {
		if m.working {
			return "Loading service inventory…"
		}
		return "Service inventory unavailable"
	}
	return "No matching entries"
}

// Panels own their horizontal inset; callers size their outer cell bounds.
func (m Model) panel(content string, width int) string {
	return m.styles.panel.Width(width).Padding(0, 1).Render(content)
}
func (m Model) list(title string, rows []row, width, capacity int) string {
	start := max(0, m.cursor-capacity+1)
	if m.cursor < capacity {
		start = 0
	}
	end := min(len(rows), start+capacity)
	position := 0
	if len(rows) > 0 {
		position = m.cursor + 1
	}
	lines := []string{m.styles.heading.Render(clipped(safe(title), width-4)), m.styles.muted.Render(clipped(fmt.Sprintf("%d–%d of %d · selected %d", min(start+1, len(rows)), end, len(rows), position), width-4))}
	for i := start; i < end; i++ {
		text := "  " + m.rowLabel(rows[i], width-6)
		if i == m.cursor {
			text = m.styles.selected.Render(clipped("› "+m.rowLabel(rows[i], width-6), width-4))
		}
		lines = append(lines, clipped(text, width-4))
	}
	if len(rows) == 0 {
		lines = append(lines, m.styles.muted.Render(m.emptyListLabel()))
	}
	for len(lines) < capacity+2 {
		lines = append(lines, "")
	}
	return m.panel(strings.Join(lines, "\n"), width)
}

type browserLayout struct {
	width, left, right, capacity, detailsHeight int
	wide, compact                               bool
}

func (m Model) browserLayout() browserLayout {
	l := browserLayout{width: min(m.width-2, 148)}
	l.compact = l.width < 72 || m.height < 22
	l.wide = l.width >= 110 && !l.compact
	l.left = l.width
	if l.wide {
		l.left = l.width * 65 / 100
		l.right = l.width - l.left - 1
	}
	controls := len(strings.Split(m.browserControls(l.left), "\n"))
	available := m.height - 1 - controls - max(2, m.feedbackHeight())
	if l.wide {
		l.capacity = max(1, available-4)
		l.detailsHeight = available + controls
	} else if l.compact {
		l.capacity = 1
		l.detailsHeight = max(5, available)
	} else {
		l.detailsHeight = 10
		l.capacity = max(1, available-4-l.detailsHeight)
	}
	return l
}
func (m Model) browser(width int) string {
	l := m.browserLayout()
	title := "All project sessions"
	if m.scope != "" {
		title = "[" + m.scope + "] project sessions"
	}
	if m.query != "" || m.searching {
		title = "fuzzy search> " + m.query
	}
	s, ok := m.selectedSession()
	details := []string{"Select a session to inspect its real state."}
	if ok {
		details = m.browserDetails(s, l.wide)
	}
	if l.compact {
		rows := m.rows()
		position := 0
		if len(rows) > 0 {
			position = m.cursor + 1
		}
		lines := []string{fmt.Sprintf("%d–%d of %d · selected %d", position, position, len(rows), position)}
		if ok {
			lines = append(lines, s.Project+" / "+s.Branch, runtimeLabel(s)+" · Policy "+s.PolicyCondition+" · "+compactPresence(s), "Agents [A] · latest: "+reportSummary(s), "Project services [S]: "+m.serviceSummary())
		} else {
			lines = append(lines, "No matching sessions")
		}
		return m.detailPanel(title, lines, width, l.detailsHeight) + "\n" + m.browserControls(width)
	}
	list := m.list(title, m.rows(), l.left, l.capacity) + "\n" + m.browserControls(l.left)
	if l.wide {
		return lipgloss.JoinHorizontal(lipgloss.Top, list, " ", m.detailPanel("Session details", details, l.right, l.detailsHeight))
	}
	return list + "\n" + m.detailPanel("Session details", details, width, l.detailsHeight)
}
func runtimeLabel(s control.SessionView) string {
	if s.Condition == "ready" {
		return "running"
	}
	return s.Condition
}
func (m Model) detailPanel(title string, lines []string, width, height int) string {
	body := []string{m.styles.heading.Render(clipped(safe(title), width-4))}
	for _, line := range lines {
		body = append(body, m.factLine(line, width-4))
	}
	if len(body) > height-2 {
		body = body[:max(1, height-2)]
	}
	for len(body) < height-2 {
		body = append(body, "")
	}
	return m.panel(strings.Join(body, "\n"), width)
}
func compactPresence(s control.SessionView) string {
	if s.AttachedCount == 0 {
		return "unattended"
	}
	return fmt.Sprintf("%d attached", s.AttachedCount)
}
func terminalPresence(s control.SessionView) string {
	if s.AttachedCount == 0 {
		return "Terminals: unattended (0 attached)"
	}
	return fmt.Sprintf("Terminals: %d attached", s.AttachedCount)
}
func reportSummary(s control.SessionView) string {
	r := s.LatestUnattendedCondition
	if r == nil {
		return "unknown · no retained report"
	}
	condition := r.Condition
	if condition == "attention" {
		condition = "waiting"
	}
	source := r.Source
	if source == "" {
		source = r.Adapter
	}
	return source + " · " + condition + " · " + r.Reason
}
func (m Model) serviceSummary() string {
	o := m.selectedServices
	switch o.state {
	case "observed":
		if len(o.units) == 0 {
			return "no project units observed"
		}
		return fmt.Sprintf("%d units observed", len(o.units))
	case "not running":
		return "not running · Start to inspect"
	case "unavailable":
		return "unavailable"
	case "stale":
		return "unavailable · stale inventory"
	default:
		return "loading…"
	}
}
func (m Model) browserDetails(s control.SessionView, wide bool) []string {
	if !wide {
		lines := []string{s.Project + " / " + s.Branch, "UUID: " + s.UUID, "Runtime: " + runtimeLabel(s) + " · Policy: " + s.PolicyCondition + " · Terminals: " + compactPresence(s), "├─ Agents [A] · latest: " + reportSummary(s), "└─ Project services [S]: " + m.serviceSummary()}
		if len(m.selectedServices.units) > 0 {
			u := m.selectedServices.units[0]
			lines = append(lines, "   "+u.Unit, "   "+u.ActiveState+" ("+u.SubState+")")
		}
		lines = append(lines, "D complete details · A report provenance · S all units")
		return lines
	}
	lines := []string{"Project: " + s.Project, "Branch: " + s.Branch, "UUID:", s.UUID, "Runtime: " + runtimeLabel(s) + " · Policy: " + s.PolicyCondition, terminalPresence(s), "", "├─ Agents [A] · latest report"}
	if r := s.LatestUnattendedCondition; r != nil {
		lines = append(lines, "│  "+reportSummary(s), "│  Adapter: "+r.Adapter, "│  Source: "+r.Source, "│  Received: "+r.ReceivedAt)
	} else {
		lines = append(lines, "│  Activity unknown · no retained report")
	}
	lines = append(lines, "│", "└─ Project services [S] · "+m.serviceSummary())
	for _, u := range m.selectedServices.units {
		lines = append(lines, "   "+u.Unit, "   "+u.ActiveState+" ("+u.SubState+")")
	}
	if s.Diagnostic != "" {
		lines = append(lines, "Diagnostic: "+s.Diagnostic)
	}
	lines = append(lines, "", "D complete details · A report · S all units")
	// Wrap identity/provenance and unit state inside the wide panel.
	out := []string{}
	for _, line := range lines {
		out = append(out, strings.Split(ansi.Wrap(safe(line), max(1, m.browserLayout().right-4), ""), "\n")...)
	}
	return out
}
func sessionDetails(s control.SessionView) []string {
	lines := []string{"Project: " + s.Project, "Branch: " + s.Branch, "UUID: " + s.UUID, "Runtime: " + runtimeLabel(s), "Policy: " + s.PolicyCondition, terminalPresence(s), "", "Agents [A] · latest unattended report"}
	if r := s.LatestUnattendedCondition; r != nil {
		lines = append(lines, reportSummary(s), "Adapter: "+r.Adapter+" / "+r.AdapterVersion, fmt.Sprintf("Receive sequence: %d", r.ReceiveSequence), "Source: "+r.Source, "Reported condition: "+r.Condition, "Received: "+r.ReceivedAt)
	} else {
		lines = append(lines, "Activity unknown · no retained report")
	}
	return lines
}
func (m Model) fullSessionDetails(s control.SessionView) []string {
	lines := sessionDetails(s)
	lines = append(lines, "", "Project services [S]: "+m.serviceSummary())
	if m.selectedServices.diagnostic != "" {
		lines = append(lines, m.selectedServices.diagnostic)
	}
	for _, u := range m.selectedServices.units {
		lines = append(lines, u.Unit+" · "+u.ActiveState+" ("+u.SubState+")", u.Description)
	}
	lines = append(lines, "", "Policy digest: "+s.PolicySHA256)
	if e := s.Environment; e != nil {
		lines = append(lines, "Environment: "+e.Selection, "Source commit: "+e.CommitOID, "Cache: "+e.Cache, "System: "+e.System, "Environment key: "+e.Key, "Base image: "+e.BaseImageFingerprint, "Image: "+e.ImageFingerprint)
	}
	if s.Diagnostic != "" {
		lines = append(lines, "Diagnostic: "+s.Diagnostic)
	}
	return lines
}

// Styling happens after sanitizing and clipping daemon-owned text. Recognize
// displayed facts, never trust incoming ANSI or style arbitrary name tokens.
func (m Model) factLine(text string, width int) string {
	text = clipped(safe(text), width)
	replace := func(token string, style lipgloss.Style) { text = strings.Replace(text, token, style.Render(token), 1) }
	plain := strings.TrimSpace(text)
	if strings.HasPrefix(plain, "Runtime:") || strings.Contains(plain, " · Policy") && (strings.HasPrefix(plain, "running") || strings.HasPrefix(plain, "stopped") || strings.HasPrefix(plain, "missing") || strings.HasPrefix(plain, "unreachable") || strings.HasPrefix(plain, "starting") || strings.HasPrefix(plain, "creating")) {
		for _, state := range []string{"running", "stopped", "missing", "unreachable", "starting", "creating", "discarding", "deleting"} {
			style := m.styles.warning
			if state == "running" {
				style = m.styles.running
			} else if state == "stopped" {
				style = m.styles.muted
			} else if state == "missing" || state == "unreachable" {
				style = m.styles.danger
			}
			if strings.Contains(text, state) {
				replace(state, style)
				break
			}
		}
	}
	if strings.Contains(plain, "Policy") {
		for _, state := range []string{"current", "outdated", "invalid"} {
			style := m.styles.running
			if state == "outdated" {
				style = m.styles.warning
			} else if state == "invalid" {
				style = m.styles.danger
			}
			if strings.Contains(text, state) {
				replace(state, style)
				break
			}
		}
	}
	for _, state := range []string{"waiting", "working", "running", "idle", "failed", "attention"} {
		if strings.Contains(plain, " · "+state+" · ") || strings.HasPrefix(plain, "Reported condition: "+state) {
			style := m.styles.muted
			if state == "waiting" || state == "attention" {
				style = m.styles.warning
			} else if state == "failed" {
				style = m.styles.danger
			} else if state == "working" || state == "running" {
				style = m.styles.running
			}
			if strings.Contains(plain, " · "+state+" · ") {
				text = strings.Replace(text, " · "+state+" · ", " · "+style.Render(state)+" · ", 1)
			} else {
				replace("Reported condition: "+state, style)
			}
			break
		}
	}
	for _, state := range []string{"active", "inactive", "failed", "unknown"} {
		token := state + " ("
		if (strings.HasPrefix(plain, token) || strings.Contains(plain, ".service · "+token)) && strings.Contains(plain, token) {
			style := m.styles.muted
			if state == "active" {
				style = m.styles.running
			} else if state == "failed" {
				style = m.styles.danger
			}
			start := strings.Index(text, token)
			end := strings.Index(text[start:], ")")
			if end >= 0 {
				replace(text[start:start+end+1], style)
			} else {
				replace(state, style)
			}
			break
		}
	}
	return m.styles.text.Render(text)
}
