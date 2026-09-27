package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/lgvo/p.ai/internal/control"
)

type row struct{ id, label, detail string }
type loaded struct {
	data inventory
	err  error
}
type tick struct{}
type actionDone struct {
	epoch  int
	method string
	raw    json.RawMessage
	err    error
}
type attached struct{ err error }
type removalIntent struct{ UUID, Kind, LossOperationID string }
type serviceIntent struct{ UUID, Unit, Action string }

type Model struct {
	client                             Client
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
	contextSession                     control.SessionView
	services                           []control.ProjectService
	servicesFresh                      bool
	serviceUnit                        string
	pendingMethod                      string
	queuedService                      *serviceIntent
	journal                            []string
	journalOffset, pan                 int
	find                               string
	finding, follow                    bool
	confirmMethod                      string
	confirmParams                      params
	review                             string
	reviewOffset                       int
	operation                          control.Operation
	operationID                        string
	attachOnComplete                   bool
	form                               string
	input                              string
	creation                           params
	choices                            []row
	sources                            []row
	sourceParams                       map[string]params
	choiceQuery                        string
	choiceSearching                    bool
	name                               string
	removal                            *removalIntent
	sourceReview                       string
	sourceOriginURL                    string
	returnPage                         string
	lastInteraction                    map[string]time.Time
}

func New(c Client, socket string) Model {
	return Model{client: c, socket: socket, width: 80, height: 24, page: "sessions", refreshing: true, lastInteraction: map[string]time.Time{}}
}
func (m Model) Init() tea.Cmd { return tea.Batch(m.refresh(), tickCmd()) }
func tickCmd() tea.Cmd        { return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return tick{} }) }
func (m Model) refresh() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callContext()
		defer cancel()
		v, e := loadInventory(ctx, m.client)
		return loaded{v, e}
	}
}
func (m Model) call(method string, p params) tea.Cmd {
	epoch := m.epoch
	c := m.client
	return func() tea.Msg {
		ctx, cancel := callContext()
		defer cancel()
		var raw json.RawMessage
		var e error
		if method == "creation.branches" {
			raw, e = creationBranches(ctx, c, p["project"].(string))
		} else {
			raw, e = c.Call(ctx, method, p)
		}
		return actionDone{epoch, method, raw, e}
	}
}
func (m *Model) navigate(page string) {
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
	if page == "confirm" || page == "review" || m.inspectionPage() {
		m.reviewOffset = 0
	}
}

func (m Model) selectedSession() (control.SessionView, bool) {
	for _, s := range m.data.sessions {
		if s.UUID == m.selected {
			return s, true
		}
	}
	return control.SessionView{}, false
}
func priority(s control.SessionView) int {
	if s.Condition == "ready" && s.LatestUnattendedCondition != nil && s.LatestUnattendedCondition.Condition == "attention" {
		return 0
	}
	if s.Condition == "ready" {
		return 1
	}
	return 2
}
func activity(s control.SessionView) string {
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
	sessions := append([]control.SessionView(nil), m.data.sessions...)
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
			rows = append(rows, row{id: s.Unit, label: s.Unit + " · " + s.ActiveState + " (" + s.SubState + ")", detail: s.Description})
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
		if m.width >= 110 {
			n--
		} else if m.width >= 72 && m.height >= 22 {
			n -= 4
		}
	case "create":
		n -= 2
	case "agents", "policy", "progress", "help":
		n = m.height - 8
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
	return max(1, n)
}
func (m *Model) begin(method string, p params) tea.Cmd {
	m.working = true
	m.pendingMethod = method
	m.notice = ""
	return m.call(method, p)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	// Every update can change a list, including background inventory/results.
	// Keep its selection valid before rendering or handling the next key.
	next.clamp()
	return next, cmd
}

func (m Model) update(msg tea.Msg) (Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
		m.clamp()
	case loaded:
		m.refreshing = false
		if v.err != nil {
			m.notice = v.err.Error()
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
				if m.operationID != "" && m.operation.Status != "completed" {
					cmds = append(cmds, m.begin("operation.inspect", params{"v": 1, "id": m.operationID}))
				} else if m.contextSession.UUID != "" && (m.contextSession.Condition == "starting" || m.attachOnComplete) {
					cmds = append(cmds, m.begin("session.inspect", params{"v": 1, "uuid": m.contextSession.UUID}))
				}
			case "services":
				cmds = append(cmds, m.begin("session.services", params{"v": 1, "uuid": m.contextSession.UUID}))
			case "journal":
				if m.follow {
					cmds = append(cmds, m.begin("session.service.journal", params{"v": 1, "uuid": m.contextSession.UUID, "unit": m.serviceUnit}))
				}
			}
		}
		return m, tea.Batch(cmds...)
	case attached:
		m.lastInteraction[m.contextSession.UUID] = time.Now()
		m.navigate("sessions")
		m.restoreSelection()
		if v.err != nil {
			m.notice = "Attachment ended: " + v.err.Error()
		}
		m.refreshing = true
		return m, m.refresh()
	case actionDone:
		// A completed background action must not reopen a page the user left.
		if v.epoch != m.epoch {
			return m, nil
		}
		m.working = false
		m.pendingMethod = ""
		if v.err != nil {
			m.notice = v.err.Error()
			m.attachOnComplete = false
			if strings.HasPrefix(v.method, "session.service") {
				m.servicesFresh = false
				if m.queuedService != nil {
					m.queuedService = nil
					m.notice += " · queued service action was not issued"
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
				m.input = m.creation["project"].(string)
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
			} else if m.page == "review" || m.page == "confirm" || m.inspectionPage() {
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
	if m.page == "confirm" {
		switch key {
		case "y", "Y":
			if m.working {
				return nil
			}
			return m.begin(m.confirmMethod, m.confirmParams)
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
			return m.begin(m.confirmMethod, m.confirmParams)
		case "n", "N":
			return m.back()
		default:
			m.scroll(key, &m.reviewOffset)
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
	if key == "?" {
		m.returnPage = m.page
		m.navigate("help")
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
			m.creation = params{"v": 1, "key": newKey()}
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
			m.contextSession = s
			m.confirmMethod = "session.stop"
			m.confirmParams = params{"v": 1, "uuid": s.UUID}
			m.review = "Stop " + s.Project + " / " + s.Branch + "?\nUUID: " + s.UUID + "\nProcesses end; files and identity remain.\nAll attachments must be detached."
			m.navigate("confirm")
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
				return m.begin("session.services", params{"v": 1, "uuid": s.UUID})
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
				return m.begin("creation.branches", params{"project": s.Project})
			}
		case "R":
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
			if s.AttachedCount != 0 || s.Condition != "stopped" {
				m.notice = "Detach and Stop this session before reviewing removal."
				return nil
			}
			m.navigate("progress")
			m.removal = &removalIntent{UUID: s.UUID, Kind: kind}
			return m.begin("workspace.loss.inspect", params{"v": 1, "key": newKey(), "uuid": s.UUID})
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
			if m.services[m.cursor].ActiveState == "active" {
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
	case "operations":
		if key == "enter" && len(rows) > 0 {
			m.navigate("progress")
			m.operationID = r.id
			m.attachOnComplete = false
			return m.begin("operation.inspect", params{"v": 1, "id": r.id})
		}
	case "progress":
		if key == "r" && m.operationID != "" && !m.working {
			return m.begin("operation.retry", params{"v": 1, "id": m.operationID})
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
			return m.begin("creation.branches", params{"project": m.creation["project"]})
		default:
			m.navigate("sessions")
			m.restoreSelection()
		}
		return nil
	}
	if m.page == "confirm" && m.confirmMethod == "session.create" {
		if m.creation["choice"] == "new" {
			m.navigate("form")
			m.form = "branch-name"
			m.input = m.creation["branch"].(string)
		} else {
			m.navigate("create")
			m.form = "branch"
			return m.begin("creation.branches", params{"project": m.creation["project"]})
		}
		return nil
	}
	if m.page == "confirm" && m.confirmMethod == "project.create" {
		m.navigate("form")
		m.form = "project-url"
		m.input = ""
		return nil
	}
	if m.page == "journal" {
		if m.find != "" {
			m.find = ""
			return nil
		}
		m.navigate("services")
		return m.begin("session.services", params{"v": 1, "uuid": m.contextSession.UUID})
	}
	if m.page == "help" {
		page := m.returnPage
		if page == "" {
			page = "sessions"
		}
		m.navigate(page)
		if page == "sessions" {
			m.restoreSelection()
		}
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
	return m.begin("session.start", params{"v": 1, "uuid": m.contextSession.UUID})
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
	return tea.ExecProcess(cmd, func(e error) tea.Msg { return attached{e} })
}

func newKey() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return "tui-" + hex.EncodeToString(b[:])
}

// Snapshot uses the same live transport and renderer without an input terminal.
func Snapshot(ctx context.Context, c Client, socket, page, id string, width, height int) (string, error) {
	m := New(c, socket)
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
	if page != "" && page != "sessions" {
		s, ok := m.selectedSession()
		if !ok {
			return "", fmt.Errorf("session UUID not present")
		}
		m.contextSession = s
		m.page = page
		switch page {
		case "services":
			r, e := request[control.ServiceResult](ctx, c, "session.services", params{"v": 1, "uuid": id})
			if e != nil {
				return "", e
			}
			m.services = r.Services
			m.servicesFresh = true
		case "agents", "policy":
		default:
			return "", fmt.Errorf("unsupported snapshot page")
		}
	}
	return m.View().Content, nil
}
