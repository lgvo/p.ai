package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/errtext"
)

type row struct{ id, label, detail string }
type loaded struct {
	data inventory
	err  error
}
type tick struct{}

// actionDone is the reply to a daemonCall: result has the call's result type.
type actionDone struct {
	epoch  int
	method string
	result any
	err    error
}
type attached struct {
	err  error
	said string
}
type removalIntent struct{ UUID, Kind string }

// Help suspends this captured removal plan read without replaying it.
// The original epoch stays bound until its one reply is consumed or abandoned.
type suspendedRemovalRead struct {
	epoch   int
	method  string
	intent  removalIntent
	working bool
	reply   *actionDone
}

type serviceIntent struct{ UUID, Unit, Action string }

type Model struct {
	theme                              Theme
	themeMode                          ThemeMode
	darkBackground                     bool
	styles                             styles
	client                             api.Caller
	socket                             string
	data                               inventory
	width, height                      int
	page                               string
	cursor                             int
	selected                           string
	scope, query                       string
	searching, refreshing, working, gg bool
	epoch                              int
	notice                             string
	stale                              bool
	contextSession                     api.SessionView
	services                           []api.ProjectService
	servicesFresh                      bool
	selectedServices                   selectedServiceObservation
	serviceUnit                        string
	pendingMethod                      string
	queuedService                      *serviceIntent
	journal                            []string
	journalOffset, pan                 int
	find                               string
	finding, follow                    bool
	confirm                            daemonCall
	review                             string
	reviewOffset                       int
	operation                          api.Operation
	operationID                        string
	attachOnComplete                   bool
	form                               string
	input                              string
	creation                           creationDraft
	choices                            []row
	sources                            []row
	sourceParams                       map[string]sourceSelection
	choiceQuery                        string
	choiceSearching                    bool
	name                               string
	removal                            *removalIntent
	sourceReview                       string
	sourceOriginURL                    string
	returnPage                         string
	inspectionReturn                   string
	helpBranchProject                  string
	helpServiceUnit                    string
	helpRemovalRead                    *suspendedRemovalRead
	lastInteraction                    map[string]time.Time
	attachmentScreen                   *attachmentScreen
	// resume is the read that follows the progress page's operation, or the
	// session it leads to, and answered an error or was cut off by Help: the
	// next tick on the page sends it again.
	resume daemonCall
	// reviewed is the removal the review page authorizes; reviewNotice says
	// why the next review shows a plan read again.
	reviewed     removalIntent
	reviewNotice string
}

func New(c api.Caller, socket string) Model {
	return NewWithTheme(c, socket, PrototypeTheme(), ThemeAuto)
}

// NewWithTheme is the injection seam for a future configured theme.
func NewWithTheme(c api.Caller, socket string, theme Theme, mode ThemeMode) Model {
	dark := mode != ThemeLight
	return Model{client: c, socket: socket, theme: theme, themeMode: mode, darkBackground: dark, styles: theme.styles(dark), width: 80, height: 24, page: "sessions", refreshing: true, lastInteraction: map[string]time.Time{}}
}
func (m Model) Init() tea.Cmd {
	if m.themeMode == ThemeAuto {
		return tea.Batch(m.refresh(), tickCmd(), tea.RequestBackgroundColor)
	}
	return tea.Batch(m.refresh(), tickCmd())
}
func tickCmd() tea.Cmd { return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return tick{} }) }
func (m Model) refresh() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callContext()
		defer cancel()
		v, e := loadInventory(ctx, m.client)
		return loaded{v, e}
	}
}
func (m Model) run(call daemonCall) tea.Cmd {
	epoch := m.epoch
	c := m.client
	return func() tea.Msg {
		ctx, cancel := callContext()
		defer cancel()
		result, e := call.run(ctx, c)
		return actionDone{epoch, call.method, result, e}
	}
}

// followInventory follows the operation on the progress page through the
// refreshed inventory. A running operation's wait reports its end, so only
// its phase is taken; one that is not running and that the inventory reads
// in another status changed elsewhere, such as by another client's Retry, and
// is read again.
func (m *Model) followInventory() tea.Cmd {
	if m.page != "progress" || m.operationID == "" {
		return nil
	}
	for _, op := range m.data.operations {
		switch {
		case op.ID != m.operationID:
		case m.operation.Status == "running":
			if op.Status == "running" {
				m.operation.Phase = op.Phase
			}
		case op.Status != m.operation.Status && !m.working:
			return m.begin(callOf(api.OperationInspect, api.OperationInspectRequest{ID: m.operationID}))
		}
	}
	return nil
}
func (m *Model) navigate(page string) {
	m.helpRemovalRead = nil
	m.page = page
	m.cursor = 0
	m.epoch++
	m.working = false
	m.notice = ""
	m.gg = false
	m.searching = false
	m.choiceSearching = false
	m.pendingMethod = ""
	m.queuedService = nil
	m.resume = daemonCall{}
	m.reviewNotice = ""
	if page == "creation-review" || page == "confirm" || page == "review" || m.inspectionPage() {
		m.reviewOffset = 0
	}
}

func (m Model) selectedSession() (api.SessionView, bool) {
	for _, s := range m.data.sessions {
		if s.UUID == m.selected {
			return s, true
		}
	}
	return api.SessionView{}, false
}
func priority(s api.SessionView) int {
	if s.Condition == "ready" && s.LatestUnattendedCondition != nil && s.LatestUnattendedCondition.Condition == "attention" {
		return 0
	}
	if s.Condition == "ready" {
		return 1
	}
	return 2
}
func activity(s api.SessionView) string {
	if s.LatestUnattendedCondition == nil {
		return ""
	}
	switch s.LatestUnattendedCondition.Condition {
	case "attention":
		return " !waiting"
	case "failed":
		return " !failed"
	case "running":
		return " working"
	default:
		return " " + s.LatestUnattendedCondition.Condition
	}
}
func fuzzy(query, text string) bool {
	q := []rune(strings.ToLower(query))
	if len(q) == 0 {
		return true
	}
	i := 0
	for _, r := range strings.ToLower(text) {
		if r == q[i] {
			i++
			if i == len(q) {
				return true
			}
		}
	}
	return false
}
func (m Model) sessionRows() []row {
	sessions := append([]api.SessionView(nil), m.data.sessions...)
	sort.SliceStable(sessions, func(i, j int) bool {
		a, b := sessions[i], sessions[j]
		if priority(a) != priority(b) {
			return priority(a) < priority(b)
		}
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		if !m.lastInteraction[a.UUID].Equal(m.lastInteraction[b.UUID]) {
			return m.lastInteraction[a.UUID].After(m.lastInteraction[b.UUID])
		}
		if a.Branch != b.Branch {
			return a.Branch < b.Branch
		}
		return a.UUID < b.UUID
	})
	rows := []row{}
	for _, s := range sessions {
		if m.scope != "" && m.scope != s.Project {
			continue
		}
		state := s.Condition
		if state == "ready" {
			state = "running"
		}
		label := s.Project + " / " + s.Branch + " · " + state + activity(s)
		text := label
		if s.LatestUnattendedCondition != nil {
			text += " " + s.LatestUnattendedCondition.Reason + " " + s.LatestUnattendedCondition.Source
		}
		if fuzzy(m.query, text) {
			rows = append(rows, row{id: s.UUID, label: label})
		}
	}
	return rows
}
func (m Model) rows() []row {
	switch m.page {
	case "projects":
		type counted struct {
			row                              row
			waiting, running, stopped, total int
		}
		counts := []counted{}
		all := counted{}
		for _, p := range m.data.projects {
			c := counted{row: row{id: p.Path}}
			for _, s := range m.data.sessions {
				if s.Project != p.Path {
					continue
				}
				c.total++
				if priority(s) == 0 {
					c.waiting++
				} else if s.Condition == "ready" {
					c.running++
				} else if s.Condition == "stopped" {
					c.stopped++
				}
			}
			all.waiting += c.waiting
			all.running += c.running
			all.stopped += c.stopped
			all.total += c.total
			c.row.label = fmt.Sprintf("%s · !w %d  r %d  s %d  total %d", p.Path, c.waiting, c.running, c.stopped, c.total)
			counts = append(counts, c)
		}
		sort.Slice(counts, func(i, j int) bool {
			a, b := counts[i], counts[j]
			if a.waiting != b.waiting {
				return a.waiting > b.waiting
			}
			if a.running != b.running {
				return a.running > b.running
			}
			if a.total != b.total {
				return a.total > b.total
			}
			return a.row.id < b.row.id
		})
		rows := []row{{id: "", label: fmt.Sprintf("All projects · !w %d  r %d  s %d  total %d", all.waiting, all.running, all.stopped, all.total)}}
		for _, c := range counts {
			rows = append(rows, c.row)
		}
		return filterRows(rows, m.choiceQuery)
	case "create":
		return filterRows(m.choices, m.choiceQuery)
	case "services":
		rows := []row{}
		for _, s := range m.services {
			rows = append(rows, row{id: s.Name, label: s.Name + " · " + s.Detail, detail: s.Description})
		}
		return rows
	case "operations":
		rows := []row{}
		for _, op := range m.data.operations {
			rows = append(rows, row{id: op.ID, label: op.Project + " · " + op.Kind + " · " + op.Status + " / " + op.Phase, detail: op.Diagnostic})
		}
		return rows
	case "retained":
		return m.choices
	}
	return m.sessionRows()
}
func filterRows(rows []row, q string) []row {
	out := []row{}
	for _, r := range rows {
		if fuzzy(q, r.label) {
			out = append(out, r)
		}
	}
	return out
}
func (m *Model) clamp() {
	rows := m.rows()
	m.cursor = max(0, min(m.cursor, len(rows)-1))
	if m.page == "sessions" {
		m.selected = ""
		if len(rows) > 0 {
			m.selected = rows[m.cursor].id
		}
	}
}
func (m *Model) restoreSelection() {
	rows := m.sessionRows()
	for i, r := range rows {
		if r.id == m.selected {
			m.cursor = i
			return
		}
	}
	m.clamp()
}
func (m Model) capacity() int {
	n := m.height - 10
	switch m.page {
	case "sessions":
		width := min(m.width, 148)
		if width >= 110 {
			width = min(width*65/100, width-43)
		}
		return m.browserLayout().capacity
	case "create":
		n -= 2
	case "creation-review", "review", "agents", "policy", "help", "details":
		n = m.height - 8
	case "progress":
		// The text takes every row below its title down to its controls and
		// the two feedback rows.
		_, _, commands := m.inspectionContent()
		n = m.height - 3 - len(strings.Split(m.controls("j/k/Pg scroll · "+commands, m.pageWidth()), "\n"))
	case "retained":
		n--
	case "services":
		n -= 5
		if len(m.services) == 0 {
			n--
		}
		if !m.servicesFresh {
			n--
		}
	}
	// Existing layouts reserve two feedback rows. Longer diagnostics need
	// extra space without moving controls for a routine one-line notice.
	return max(1, n-max(0, m.feedbackHeight()-2))
}

func (m Model) projectCounts(project string) [4]int {
	counts := [4]int{}
	for _, s := range m.data.sessions {
		if project != "" && s.Project != project {
			continue
		}
		counts[3]++
		if priority(s) == 0 {
			counts[0]++
		} else if s.Condition == "ready" {
			counts[1]++
		} else if s.Condition == "stopped" {
			counts[2]++
		}
	}
	return counts
}
func (m *Model) begin(call daemonCall) tea.Cmd {
	m.resume = daemonCall{}
	m.working = true
	m.pendingMethod = call.method
	m.notice = ""
	return m.run(call)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	// Every update can change a list, including background inventory/results.
	// Keep its selection valid before rendering or handling the next key.
	next.clamp()
	serviceCmd := next.syncSelectedServices(msg)
	return next, tea.Batch(cmd, serviceCmd)
}

func (m Model) update(msg tea.Msg) (Model, tea.Cmd) {
	switch v := msg.(type) {
	case selectedServiceDue:
		return m, m.readSelectedServices(v)
	case selectedServiceDone:
		m.acceptSelectedServices(v)
	case tea.BackgroundColorMsg:
		if m.themeMode == ThemeAuto {
			m.darkBackground = v.IsDark()
			m.styles = m.theme.styles(m.darkBackground)
		}
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
		m.clamp()
	case loaded:
		m.refreshing = false
		if v.err != nil {
			m.notice = errtext.Sentence(v.err)
			m.stale = true
		} else {
			m.data = v.data
			m.stale = false
			if m.page != "progress" {
				for _, s := range m.data.sessions {
					if s.UUID == m.contextSession.UUID {
						m.contextSession = s
						break
					}
				}
			}
			if m.page == "sessions" {
				m.restoreSelection()
			}
			return m, m.followInventory()
		}
	case tick:
		cmds := []tea.Cmd{tickCmd()}
		if !m.refreshing {
			m.refreshing = true
			cmds = append(cmds, m.refresh())
		}
		if !m.working {
			switch m.page {
			case "progress":
				// A follow read that answered an error, or that Help
				// interrupted, is sent again.
				if m.resume.method != "" {
					cmds = append(cmds, m.begin(m.resume))
				}
			case "services":
				cmds = append(cmds, m.begin(callOf(api.SessionServices, api.SessionServicesRequest{UUID: m.contextSession.UUID})))
			case "journal":
				if m.follow {
					cmds = append(cmds, m.begin(callOf(api.SessionServiceLogs, api.SessionServiceLogsRequest{UUID: m.contextSession.UUID, Name: m.serviceUnit})))
				}
			}
		}
		return m, tea.Batch(cmds...)
	case attached:
		m.lastInteraction[m.contextSession.UUID] = time.Now()
		m.selected = m.contextSession.UUID
		m.navigate("sessions")
		m.restoreSelection()
		if v.said != "" {
			m.notice = "Attachment ended: " + v.said
		} else if v.err != nil {
			m.notice = "Attachment ended: " + errtext.Sentence(v.err)
		}
		m.refreshing = true
		return m, m.refresh()
	case actionDone:
		if suspended := m.helpRemovalRead; suspended != nil && v.epoch == suspended.epoch && v.method == suspended.method {
			if !m.matchesRemovalRead(suspended) || m.page != "help" && m.page != "progress" {
				return m, nil
			}
			if m.page == "help" {
				if suspended.reply == nil {
					copy := *suspended
					copy.reply = &v
					m.helpRemovalRead = &copy
				}
				return m, nil
			}
			m.helpRemovalRead = nil
			v.epoch = m.epoch
		}
		// A completed background action must not reopen a page the user left.
		if v.epoch != m.epoch {
			return m, nil
		}
		m.working = false
		m.pendingMethod = ""
		if v.err != nil {
			m.notice = errtext.Sentence(v.err)
			if m.page == "review" && isRemoval(v.method) && isKind(v.err, api.KindPreconditionFailed) {
				return m, m.reviewAgain()
			}
			if call := m.followRead(v.method, v.err); call.method != "" {
				// The operation may still end, or the session be ready:
				// read again on the next tick and keep the intent to enter.
				m.resume = call
				return m, nil
			}
			m.attachOnComplete = false
			if strings.HasPrefix(v.method, "session.service") {
				m.servicesFresh = false
				if m.queuedService != nil {
					m.queuedService = nil
					m.notice += " The queued service action was not issued."
				}
			}
			return m, nil
		}
		cmd := m.accept(v)
		return m, cmd
	case tea.KeyPressMsg:
		cmd := m.key(v)
		return m, cmd
	}
	return m, nil
}

func (m *Model) key(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	// Text fields receive literal q/g/G. Escape/Ctrl-C are cancellation.
	if m.page == "form" {
		switch key {
		case "esc", "ctrl+c":
			if m.form == "branch-name" {
				m.navigate("create")
				m.form = "source"
				m.choices = m.sources
			} else if m.form == "project-url" {
				m.navigate("form")
				m.form = "project-name"
				m.input = m.creation.project
			} else {
				m.navigate("sessions")
				m.restoreSelection()
			}
		case "enter":
			return m.submitForm()
		case "backspace":
			r := []rune(m.input)
			if len(r) > 0 {
				m.input = string(r[:len(r)-1])
			}
		default:
			m.addInput(&m.input, k)
		}
		return nil
	}
	if m.searching || m.choiceSearching || m.finding {
		p := &m.query
		if m.choiceSearching {
			p = &m.choiceQuery
		}
		if m.finding {
			p = &m.find
		}
		switch key {
		case "esc", "ctrl+c":
			*p = ""
			m.searching = false
			m.choiceSearching = false
			m.finding = false
		case "enter":
			m.searching = false
			m.choiceSearching = false
			m.finding = false
		case "backspace":
			r := []rune(*p)
			if len(r) > 0 {
				*p = string(r[:len(r)-1])
			}
		case "up", "down":
			m.move(key)
		default:
			m.addInput(p, k)
		}
		if m.page == "sessions" {
			m.clamp()
		} else if m.page == "projects" || m.page == "create" {
			m.clamp()
		}
		return nil
	}
	if key == "q" || key == "esc" || key == "ctrl+c" {
		return m.back()
	}
	if key == "g" {
		if m.gg {
			m.gg = false
			if m.page == "journal" {
				m.journalOffset = 0
				m.follow = false
			} else if m.page == "creation-review" || m.page == "review" || m.page == "confirm" || m.inspectionPage() {
				m.reviewOffset = 0
			} else {
				m.cursor = 0
				m.clamp()
			}
		} else {
			m.gg = true
		}
		return nil
	}
	m.gg = false
	if m.page == "creation-review" {
		if key == "enter" {
			if m.working {
				return nil
			}
			return m.begin(m.confirm)
		}
		m.scroll(key, &m.reviewOffset)
		return nil
	}
	if m.page == "confirm" {
		switch key {
		case "y", "Y":
			if m.working {
				return nil
			}
			return m.begin(m.confirm)
		case "n", "N", "enter":
			return m.back()
		default:
			m.scroll(key, &m.reviewOffset)
		}
		return nil
	}
	if m.page == "review" {
		switch key {
		case "y", "Y":
			if m.working {
				return nil
			}
			return m.begin(m.confirm)
		case "n", "N":
			return m.back()
		default:
			m.scroll(key, &m.reviewOffset)
			m.reviewOffset = min(m.reviewOffset, max(0, len(m.reviewLines())-m.capacity()))
		}
		return nil
	}
	if m.page == "journal" {
		switch key {
		case "/":
			m.finding = true
		case "n", "N":
			m.findNext(key == "N")
		case "f":
			m.follow = !m.follow
			if m.follow {
				m.journalOffset = max(0, len(m.journal)-m.capacity())
			}
		case "h", "left":
			m.pan = max(0, m.pan-8)
		case "l", "right":
			m.pan += 8
		default:
			m.follow = false
			m.scroll(key, &m.journalOffset)
			m.journalOffset = min(m.journalOffset, max(0, len(m.journal)-1))
		}
		return nil
	}
	if key == "?" && m.page != "help" {
		m.returnPage = m.page
		m.helpServiceUnit = ""
		if m.page == "services" && m.cursor < len(m.services) {
			m.helpServiceUnit = m.services[m.cursor].Name
		}
		m.helpBranchProject = ""
		if m.working && m.pendingMethod == "creation.branches" {
			if m.page == "create" && m.form == "branch" {
				m.helpBranchProject = m.creation.project
			} else if m.page == "retained" {
				m.helpBranchProject = m.contextSession.Project
			}
		}
		suspended := m.helpRemovalRead
		if m.page == "progress" && m.removal != nil && suspended == nil {
			switch m.pendingMethod {
			case "", api.SessionDiscard.Name, api.SessionDelete.Name:
				suspended = &suspendedRemovalRead{epoch: m.epoch, method: m.pendingMethod, intent: *m.removal, working: m.working}
			}
		}
		// Help drops the reply of a follow read in flight; it is sent again
		// on return, as one that failed is.
		resume, reviewNotice := m.resume, m.reviewNotice
		if m.working {
			if call := m.followRead(m.pendingMethod, nil); call.method != "" {
				resume = call
			}
		}
		m.navigate("help")
		m.helpRemovalRead = suspended
		m.resume, m.reviewNotice = resume, reviewNotice
		return nil
	}
	if isMove(key) {
		if m.inspectionPage() {
			m.scroll(key, &m.reviewOffset)
			_, lines, _ := m.inspectionContent()
			m.reviewOffset = min(m.reviewOffset, max(0, len(m.wrappedText(lines))-m.capacity()))
			return nil
		}
		m.move(key)
		return nil
	}
	rows := m.rows()
	var r row
	if len(rows) > 0 {
		r = rows[m.cursor]
	}
	switch m.page {
	case "details":
		switch key {
		case "A":
			m.inspectionReturn = "details"
			m.navigate("agents")
		case "p":
			m.inspectionReturn = "details"
			m.navigate("policy")
		case "S":
			m.inspectionReturn = "details"
			m.services = nil
			m.servicesFresh = false
			m.navigate("services")
			return m.begin(callOf(api.SessionServices, api.SessionServicesRequest{UUID: m.contextSession.UUID}))
		}
	case "sessions":
		s, ok := m.selectedSession()
		switch key {
		case "/":
			m.searching = true
		case "P":
			m.choiceQuery = ""
			m.navigate("projects")
		case "O":
			m.navigate("operations")
		case "c":
			m.creation = creationDraft{key: newKey()}
			m.choices = []row{{id: "\x00new-project", label: "Create a new project"}}
			for _, p := range m.data.projects {
				if p.Registry == "active" {
					m.choices = append(m.choices, row{id: p.Path, label: p.Path})
				}
			}
			m.form = "project"
			m.choiceQuery = ""
			m.navigate("create")
		case "enter":
			if !ok || m.stale {
				return nil
			}
			m.contextSession = s
			return m.enterSession()
		case "s":
			if !ok || m.stale {
				return nil
			}
			if s.Condition == "stopped" {
				m.notice = "Session is already stopped."
				return nil
			}
			m.contextSession = s
			m.confirm = callOf(api.SessionStop, api.SessionStopRequest{UUID: s.UUID})
			m.review = "Stop " + s.Project + " / " + s.Branch + "?\nUUID: " + s.UUID + "\nProcesses end; files and identity remain.\nAll attachments must be detached."
			m.navigate("confirm")
		case "D":
			if ok {
				m.contextSession = s
				m.navigate("details")
			}
		case "A":
			if ok {
				m.contextSession = s
				m.navigate("agents")
			}
		case "S":
			if ok {
				m.contextSession = s
				m.services = nil
				m.servicesFresh = false
				m.navigate("services")
				return m.begin(callOf(api.SessionServices, api.SessionServicesRequest{UUID: s.UUID}))
			}
		case "p":
			if ok {
				m.contextSession = s
				m.navigate("policy")
			}
		case "b":
			if ok {
				m.contextSession = s
				m.navigate("retained")
				m.choices = nil
				return m.begin(creationBranchesCall(s.Project))
			}
		case "R":
			if ok && s.Condition != "stopped" {
				m.notice = "Stop this session before renaming its branch."
				return nil
			}
			if ok {
				m.contextSession = s
				m.form = "rename"
				m.input = ""
				m.navigate("form")
			}
		case "d", "X":
			if !ok || m.stale {
				return nil
			}
			m.contextSession = s
			kind := "discard"
			if key == "X" {
				kind = "delete"
			}
			if s.AttachedCount != 0 {
				m.notice = "Detach every terminal before reviewing removal."
				return nil
			}
			// P stops a running container itself as part of removal; a
			// missing one's plan says its contents are already gone.
			switch s.Condition {
			case "stopped", "ready", "starting", "missing":
			default:
				m.notice = "Removal is unavailable while the session is " + s.Condition + "."
				return nil
			}
			m.operationID = ""
			m.attachOnComplete = false
			m.navigate("progress")
			m.removal = &removalIntent{UUID: s.UUID, Kind: kind}
			return m.begin(removalPlanCall(kind, s.UUID))
		case "r":
			if !m.refreshing {
				m.refreshing = true
				return m.refresh()
			}
		}
	case "projects":
		switch key {
		case "/":
			m.choiceSearching = true
		case "enter":
			if len(rows) > 0 {
				m.scope = r.id
				m.navigate("sessions")
				m.restoreSelection()
			}
		}
	case "create":
		switch key {
		case "/":
			m.choiceSearching = true
		case "enter":
			if !m.working && len(rows) > 0 {
				return m.choose(r)
			}
		}
	case "services":
		if !m.servicesFresh || len(rows) == 0 {
			return nil
		}
		action := ""
		switch key {
		case "enter", "J":
			action = "journal"
		case "r":
			action = "restart"
		case "s":
			action = "start"
			if m.services[m.cursor].State == api.ServiceRunning {
				action = "stop"
			}
		}
		if action == "" {
			return nil
		}
		intent := serviceIntent{m.contextSession.UUID, r.id, action}
		if m.working {
			if m.pendingMethod == "session.services" && m.queuedService == nil {
				m.queuedService = &intent
				m.notice = "Queued " + action + " for " + r.id + "; current inventory must finish. Back cancels."
			} else {
				m.notice = "A service request is already pending."
			}
			return nil
		}
		return m.dispatchService(intent)
	case "retained":
		if key == "r" && !m.working {
			m.choices = nil
			return m.begin(creationBranchesCall(m.contextSession.Project))
		}
	case "operations":
		if key == "enter" && len(rows) > 0 {
			m.navigate("progress")
			m.operationID = r.id
			// Show what the list read until the operation is read.
			m.operation = api.Operation{ID: r.id}
			for _, op := range m.data.operations {
				if op.ID == r.id {
					m.operation = api.Operation{ID: op.ID, Key: op.Key, Kind: op.Kind, Project: op.Project, SessionUUID: op.SessionUUID, Intent: op.Intent, Status: op.Status, Phase: op.Phase, Committed: op.Committed, Reason: op.Reason, Diagnostic: op.Diagnostic}
				}
			}
			m.attachOnComplete = false
			// An opened operation is not about the session last acted on.
			m.contextSession = api.SessionView{}
			return m.begin(callOf(api.OperationInspect, api.OperationInspectRequest{ID: r.id}))
		}
	case "progress":
		if key == "r" && m.retryable() && !m.working {
			return m.begin(callOf(api.OperationRetry, api.OperationRetryRequest{ID: m.operationID}))
		}
	}
	return nil
}

func (m *Model) addInput(p *string, k tea.KeyPressMsg) {
	if len(*p)+len(k.Text) > 512 {
		return
	}
	for _, r := range k.Text {
		if unicode.IsPrint(r) {
			*p += string(r)
		}
	}
}
func isMove(k string) bool {
	switch k {
	case "j", "k", "up", "down", "pgup", "pgdown", "ctrl+b", "ctrl+f", "ctrl+u", "ctrl+d", "home", "end", "G":
		return true
	}
	return false
}
func (m *Model) move(k string) {
	switch k {
	case "j", "down":
		m.cursor++
	case "k", "up":
		m.cursor--
	case "pgup", "ctrl+b":
		m.cursor -= m.capacity()
	case "pgdown", "ctrl+f":
		m.cursor += m.capacity()
	case "ctrl+u":
		m.cursor -= max(1, m.capacity()/2)
	case "ctrl+d":
		m.cursor += max(1, m.capacity()/2)
	case "home":
		m.cursor = 0
	case "end", "G":
		m.cursor = len(m.rows()) - 1
	}
	m.clamp()
}
func (m *Model) scroll(k string, p *int) {
	switch k {
	case "j", "down":
		*p++
	case "k", "up":
		*p--
	case "pgup", "ctrl+b":
		*p -= m.capacity()
	case "pgdown", "ctrl+f":
		*p += m.capacity()
	case "ctrl+u":
		*p -= max(1, m.capacity()/2)
	case "ctrl+d":
		*p += max(1, m.capacity()/2)
	case "home":
		*p = 0
	case "end", "G":
		if m.page == "journal" {
			*p = max(0, len(m.journal)-m.capacity())
		} else if m.inspectionPage() {
			_, lines, _ := m.inspectionContent()
			*p = max(0, len(m.wrappedText(lines))-m.capacity())
		} else {
			*p = max(0, len(m.reviewLines())-m.capacity())
		}
	}
	*p = max(0, *p)
}
func (m *Model) findNext(backward bool) {
	if m.find == "" || len(m.journal) == 0 {
		return
	}
	for i := 1; i <= len(m.journal); i++ {
		n := (m.journalOffset + i) % len(m.journal)
		if backward {
			n = (m.journalOffset - i + len(m.journal)*2) % len(m.journal)
		}
		if strings.Contains(strings.ToLower(m.journal[n]), strings.ToLower(m.find)) {
			m.journalOffset = n
			m.follow = false
			return
		}
	}
	m.notice = "No journal match"
}

func (m *Model) matchesRemovalRead(suspended *suspendedRemovalRead) bool {
	return m.removal != nil && *m.removal == suspended.intent && m.contextSession.UUID == suspended.intent.UUID
}

func (m *Model) back() tea.Cmd {
	if m.page == "sessions" {
		if m.query != "" {
			m.query = ""
			m.restoreSelection()
			return nil
		}
		if m.scope != "" {
			m.scope = ""
			m.restoreSelection()
			return nil
		}
		return tea.Quit
	}
	if m.page == "projects" && m.choiceQuery != "" {
		m.choiceQuery = ""
		m.clamp()
		return nil
	}
	if m.page == "create" {
		if m.choiceQuery != "" {
			m.choiceQuery = ""
			m.clamp()
			return nil
		}
		switch m.form {
		case "branch":
			m.navigate("create")
			m.form = "project"
			m.choices = []row{{id: "\x00new-project", label: "Create a new project"}}
			for _, p := range m.data.projects {
				if p.Registry == "active" {
					m.choices = append(m.choices, row{id: p.Path, label: p.Path})
				}
			}
		case "source":
			m.navigate("create")
			m.form = "branch"
			m.choices = nil
			return m.begin(creationBranchesCall(m.creation.project))
		default:
			m.navigate("sessions")
			m.restoreSelection()
		}
		return nil
	}
	if m.page == "creation-review" && m.confirm.method == api.SessionCreate.Name {
		if m.creation.choice == "new" {
			m.navigate("form")
			m.form = "branch-name"
			m.input = m.creation.branch
		} else {
			m.navigate("create")
			m.form = "branch"
			return m.begin(creationBranchesCall(m.creation.project))
		}
		return nil
	}
	if m.page == "creation-review" && m.confirm.method == api.ProjectCreate.Name {
		m.navigate("form")
		m.form = "project-url"
		m.input = m.creation.url
		return nil
	}
	if m.page == "journal" {
		if m.find != "" {
			m.find = ""
			return nil
		}
		m.navigate("services")
		for i, service := range m.services {
			if service.Name == m.serviceUnit {
				m.cursor = i
				break
			}
		}
		return m.begin(callOf(api.SessionServices, api.SessionServicesRequest{UUID: m.contextSession.UUID}))
	}
	if m.page == "help" {
		page := m.returnPage
		branchProject := m.helpBranchProject
		serviceUnit := m.helpServiceUnit
		suspended := m.helpRemovalRead
		resume, reviewNotice := m.resume, m.reviewNotice
		contextChanged := suspended != nil && !m.matchesRemovalRead(suspended)
		if contextChanged {
			suspended = nil
			m.removal = nil
			m.operationID = ""
			m.attachOnComplete = false
		}
		m.returnPage, m.helpBranchProject = "", ""
		m.helpServiceUnit = ""
		if page == "" {
			page = "sessions"
		}
		m.navigate(page)
		if page == "progress" && !contextChanged {
			m.resume, m.reviewNotice = resume, reviewNotice
		}
		if page == "services" {
			for i, service := range m.services {
				if service.Name == serviceUnit {
					m.cursor = i
					break
				}
			}
		}
		if page == "sessions" {
			m.restoreSelection()
		}
		if contextChanged {
			m.notice = "Removal inspection context changed. Return to sessions to review again."
			return nil
		}
		if page == "progress" && suspended != nil {
			if suspended.reply != nil {
				reply := *suspended.reply
				reply.epoch = m.epoch
				next, cmd := m.update(reply)
				*m = next
				return cmd
			}
			if suspended.working {
				m.helpRemovalRead = suspended
				m.working = true
				m.pendingMethod = suspended.method
			}
			return nil
		}
		if branchProject != "" && (page == "retained" || page == "create" && m.form == "branch") {
			// Help invalidated the old response. Restart only its branch read,
			// bound to the same project; never replay a mutation on return.
			m.choices = nil
			return m.begin(creationBranchesCall(branchProject))
		}
		return nil
	}
	if m.inspectionReturn == "details" && (m.page == "agents" || m.page == "policy" || m.page == "services") {
		m.inspectionReturn = ""
		m.navigate("details")
		return nil
	}
	m.attachOnComplete = false
	m.operationID = ""
	m.removal = nil
	m.navigate("sessions")
	m.restoreSelection()
	return nil
}

func (m *Model) enterSession() tea.Cmd {
	m.attachOnComplete = true
	m.operationID = ""
	m.navigate("progress")
	return m.begin(callOf(api.SessionStart, api.SessionStartRequest{UUID: m.contextSession.UUID}))
}
func (m *Model) launchAttachment() tea.Cmd {
	executable, err := os.Executable()
	if err != nil {
		m.notice = err.Error()
		return nil
	}
	m.attachOnComplete = false
	m.working = true
	cmd := exec.Command(executable, "attach", m.socket, m.contextSession.UUID)
	return attachmentProcess(cmd, m.attachmentScreen)
}

func newKey() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return "tui-" + hex.EncodeToString(b[:])
}

// Snapshot uses the same live transport and renderer without an input terminal.
func Snapshot(ctx context.Context, c api.Caller, socket, page, id string, width, height int) (string, error) {
	return snapshotWithTheme(ctx, c, socket, page, id, width, height, PrototypeTheme(), ThemeAuto)
}

func snapshotWithTheme(ctx context.Context, c api.Caller, socket, page, id string, width, height int, theme Theme, mode ThemeMode) (string, error) {
	m := NewWithTheme(c, socket, theme, mode)
	v, err := loadInventory(ctx, c)
	if err != nil {
		return "", err
	}
	m.data = v
	m.width, m.height = width, height
	m.selected = id
	if id != "" {
		found := false
		for _, s := range v.sessions {
			if s.UUID == id {
				found = true
			}
		}
		if !found {
			return "", fmt.Errorf("session UUID not present")
		}
	}
	m.restoreSelection()
	if s, ok := m.selectedSession(); ok {
		m.selectedServices.uuid = s.UUID
		m.selectedServices.condition = s.Condition
		if s.Condition != "ready" {
			m.selectedServices.state = "not running"
		} else if !v.hello.Has(api.CapabilityServices) {
			m.selectedServices.state = "unavailable"
		} else {
			readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			r, e := api.Do(readCtx, c, api.SessionServices, api.SessionServicesRequest{UUID: s.UUID})
			cancel()
			if e != nil || r.UUID != s.UUID {
				m.selectedServices.state = "unavailable"
			} else {
				m.selectedServices.state = "observed"
				m.selectedServices.units = r.Services
			}
		}
	}
	if page != "" && page != "sessions" {
		s, ok := m.selectedSession()
		if !ok {
			return "", fmt.Errorf("session UUID not present")
		}
		m.contextSession = s
		m.page = page
		switch page {
		case "services":
			r, e := api.Do(ctx, c, api.SessionServices, api.SessionServicesRequest{UUID: id})
			if e != nil {
				return "", e
			}
			m.services = r.Services
			m.servicesFresh = true
		case "agents", "policy", "details":
		default:
			return "", fmt.Errorf("unsupported snapshot page")
		}
	}
	return m.View().Content, nil
}
