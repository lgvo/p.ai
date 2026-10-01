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

var headingStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
var selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("229")).Background(lipgloss.Color("237"))
var mutedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
var warningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
var dangerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))

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
func plainLines(s string) []string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = safe(lines[i])
	}
	return lines
}

func (m Model) View() tea.View {
	if m.width < 48 || m.height < 16 {
		v := tea.NewView(clipped("P · terminal too small (minimum 48×16); resize or q to quit", m.width))
		v.AltScreen = true
		return v
	}
	width := min(m.width, 148)
	if m.page != "sessions" {
		width = min(width, 96)
	}
	var content string
	switch m.page {
	case "sessions":
		content = m.browser(width)
	case "projects":
		content = m.listPage("Select project", m.rows(), width, "Enter select · / search · q/Esc back", nil)
	case "create":
		content = m.listPage("Create · "+m.form, m.rows(), width, "Enter choose · / search · q/Esc back", []string{"Project → Branch → Policy", safe(m.notice)})
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
		content = m.listPage("Project services", m.rows(), width, "Enter/J journal · s start/stop · r restart · q/Esc back", details)

	case "agents", "policy", "progress", "help":
		title, lines, commands := m.inspectionContent()
		content = m.textPage(title, lines, width, commands)
	case "operations":
		content = m.listPage("Durable operations", m.rows(), width, "Enter inspect · j/k select · q/Esc back", nil)

	case "confirm":
		lines := m.reviewLines()
		start := min(m.reviewOffset, max(0, len(lines)-1))
		lines = lines[start:min(len(lines), start+m.capacity())]
		lines = append(lines, "", warningStyle.Render("Confirm [y/N] · Enter means No"))
		content = m.textPage("Confirm action", lines, width, "j/k/Pg scroll · y confirm · n/Enter decline · q/Esc back")
	case "review":
		lines := m.reviewLines()
		m.reviewOffset = min(m.reviewOffset, max(0, len(lines)-1))
		end := min(len(lines), m.reviewOffset+m.capacity())
		shown := lines[m.reviewOffset:end]
		content = m.textPage(dangerStyle.Render("Removal loss preview"), shown, width, "j/k/PgUp/PgDn scroll · y authorize · n/q/Esc cancel")
		content += "\n" + dangerStyle.Render("[y/N] Default No · review all losses; daemon rechecks freshness and ownership")
	case "form":
		prompt := map[string]string{"project-name": "Project path", "project-url": "SSH origin URL (Enter for local-only)", "branch-name": "New branch name", "rename": "New assigned branch name"}[m.form]
		content = m.textPage("Create / rename", []string{prompt + "> " + m.input + "▏", "", "Letters, including q/g/G, are ordinary text here."}, width, "Enter continue · Esc/Ctrl-C cancel/back")
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
		content += "\n" + mutedStyle.Render("Waiting for daemon… (Back leaves accepted work running)")
	}
	if m.notice != "" {
		content += "\n" + warningStyle.Render(clipped(safe(m.notice), width))
	}
	if m.stale {
		content += "\n" + dangerStyle.Render("STALE · connection failed; actions need a fresh observation")
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
	return m.list(title, rows, width, capacity) + "\n" + clipped(mutedStyle.Render(commands), width) + "\n" + strings.Join(renderLines(details, width), "\n")
}
func renderLines(lines []string, width int) []string {
	out := []string{}
	for _, s := range lines {
		out = append(out, clipped(safe(s), width))
	}
	return out
}

func (m Model) reviewLines() []string {
	width := max(1, min(m.width, 96)-4)
	return strings.Split(ansi.Hardwrap(strings.Join(plainLines(m.review), "\n"), width, true), "\n")
}
func (m Model) inspectionPage() bool {
	switch m.page {
	case "agents", "policy", "progress", "help":
		return true
	}
	return false
}
func (m Model) inspectionContent() (string, []string, string) {
	switch m.page {
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
			lines = append(lines, "", "Environment: "+e.Selection, "Source commit: "+e.CommitOID, "Cache: "+e.Cache, "Image: "+e.ImageFingerprint)
		}
		return "Session policy and environment", lines, "q/Esc back"
	case "progress":
		lines := []string{}
		if m.operationID != "" {
			op := m.operation
			lines = append(lines, op.Kind+" · "+op.Status+" / "+op.Phase, "Operation: "+m.operationID, "Key: "+op.Key, "Session: "+op.SessionUUID, "", op.Diagnostic, "", "Leaving this screen does not cancel an accepted operation.", "Retry resumes exact captured intent; changed inputs need a new request.")
		} else {
			lines = append(lines, m.contextSession.Project+" / "+m.contextSession.Branch, "Session: "+m.contextSession.UUID, "Condition: "+m.contextSession.Condition, "", m.contextSession.Diagnostic, "", "Leaving startup prevents automatic entry when it finishes.")
		}
		return "Operation / readiness", lines, "r retry operation · q/Esc back"
	case "help":
		return "P · keys", []string{"j/k/arrows: select · PgUp/PgDn or Ctrl+B/F: page", "Home/End, gg/G: first/last · Ctrl+U/D: half page", "P: exact project scope · /: fuzzy search across branch/status/reports", "Enter: Start if needed, then real terminal attachment", "s: confirmed Stop · c: Create Project → Branch → Policy", "A: actual unattended report · S: project services and journal", "p: captured policy/environment · b: retained branches · R: Rename", "O: durable operations · d: Discard · X: Delete session/branch", "Back closes the current page, then search, then project scope, then exits.", "In text input q is ordinary text; Esc/Ctrl-C cancels.", "", "Inside the real terminal: tmux owns input; Ctrl+B then lowercase d detaches.", "Detach before browsing Agents/Services; attachment leases stay daemon-owned.", "Authenticated Codex acceptance remains your manual test."}, "q/Esc back"
	}
	return "", nil, ""
}
func (m Model) wrappedText(lines []string) []string {
	return strings.Split(ansi.Hardwrap(strings.Join(plainLines(strings.Join(lines, "\n")), "\n"), max(1, min(m.width, 96)-4), true), "\n")
}
func (m Model) textPage(title string, lines []string, width int, commands string) string {
	body := renderLines(lines, width-4)
	capacity := max(1, m.height-8)
	if m.inspectionPage() {
		body = m.wrappedText(lines)
		capacity = m.capacity()
		start := min(m.reviewOffset, max(0, len(body)-capacity))
		body = body[start:]
		commands = "j/k/Pg scroll · " + commands
	}
	if len(body) > capacity {
		body = body[:capacity]
	}
	return headingStyle.Render(clipped(safe(title), width)) + "\n" + strings.Join(body, "\n") + "\n" + clipped(mutedStyle.Render(commands), width)
}
func (m Model) list(title string, rows []row, width, capacity int) string {
	start := max(0, m.cursor-capacity+1)
	if m.cursor < capacity {
		start = 0
	}
	end := min(len(rows), start+capacity)
	lines := []string{headingStyle.Render(clipped(safe(title), width-4))}
	for i := start; i < end; i++ {
		text := "  " + safe(rows[i].label)
		if i == m.cursor {
			text = "› " + safe(rows[i].label)
			text = selectedStyle.Render(clipped(text, width-4))
		}
		lines = append(lines, clipped(text, width-4))
	}
	if len(rows) == 0 {
		lines = append(lines, mutedStyle.Render("No matching entries"))
	}
	for len(lines) < capacity+1 {
		lines = append(lines, "")
	}
	position := 0
	if len(rows) > 0 {
		position = m.cursor + 1
	}
	lines = append(lines, mutedStyle.Render(fmt.Sprintf("%d–%d / %d · selected %d", min(start+1, len(rows)), end, len(rows), position)))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Width(width - 2).Render(strings.Join(lines, "\n"))
}
func (m Model) browser(width int) string {
	title := "All project sessions"
	if m.scope != "" {
		title = "[" + m.scope + "] project sessions"
	}
	if m.query != "" || m.searching {
		title = "fuzzy search> " + m.query
	}
	commands := "Enter open · s stop · c create · P project · / search"
	more := "A agents · S services · O operations · ? help"
	advanced := "p policy · b branches · R rename · d discard · X delete"
	s, ok := m.selectedSession()
	details := []string{"Select a session to inspect its real state."}
	if ok {
		details = sessionDetails(s)
	}
	if width >= 110 {
		left := width * 65 / 100
		right := width - left - 3
		list := m.list(title, m.rows(), left, m.capacity()) + "\n" + clipped(mutedStyle.Render(commands), left) + "\n" + clipped(mutedStyle.Render(more), left) + "\n" + clipped(mutedStyle.Render(advanced), left)
		return lipgloss.JoinHorizontal(lipgloss.Top, list, "   ", strings.Join(renderLines(details, right), "\n"))
	}
	capacity := m.capacity()
	if m.width < 72 || m.height < 22 {
		capacity = max(1, m.height-10)
		details = nil
		commands = "Enter open · s stop · c create · P project"
		advanced = ""
	} else if len(details) > 4 {
		details = details[:4]
	}
	out := m.list(title, m.rows(), width, capacity) + "\n" + clipped(mutedStyle.Render(commands), width) + "\n" + clipped(mutedStyle.Render(more), width)
	if advanced != "" {
		out += "\n" + clipped(mutedStyle.Render(advanced), width)
	}
	return out + "\n" + strings.Join(renderLines(details, width), "\n")
}
func sessionDetails(s control.SessionView) []string {
	state := s.Condition
	if state == "ready" {
		state = "running"
	}
	lines := []string{s.Project + " / " + s.Branch, "Runtime: " + state, fmt.Sprintf("Attached: %d", s.AttachedCount), "Policy: " + s.PolicyCondition, "UUID:", s.UUID}
	if r := s.LatestUnattendedCondition; r != nil {
		lines = append(lines, "", "Unattended report: "+r.Condition, r.Adapter+" · "+r.Source, r.Reason, "Received: "+r.ReceivedAt)
	} else {
		lines = append(lines, "", "Agent activity: unknown (no unattended report)")
	}
	if s.Diagnostic != "" {
		lines = append(lines, "", s.Diagnostic)
	}
	return lines
}
