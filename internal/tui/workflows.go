package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/errtext"
	"github.com/lgvo/p.ai/internal/plugin"
)

// branchInventory is what session creation and the retained-branch page show
// for one project: P's branches, the retained ones among them, and the
// origin's refs when the origin observation is fresh.
type branchInventory struct {
	Refs             []plugin.GitRef
	Retained         []api.RetainedBranch
	Origin           []plugin.GitOriginRef
	OriginURL        string
	OriginDiagnostic string
}

// branchPages bounds the pages of each branch list.
const branchPages = 128

var (
	errRepeatedCursor = errors.New("repeated pagination cursor")
	errPageBound      = errors.New("pagination exceeds bound")
)

// pageThrough reads the pages of m from the first, at most maxPages of them,
// and refuses a repeated cursor.
func pageThrough[Req api.Request, Res, T any](ctx context.Context, c api.Caller, m api.Method[Req, Res], maxPages int, page func(after string) Req, items func(Res) ([]T, string)) ([]T, error) {
	var all []T
	after := ""
	seen := map[string]bool{}
	for i := 0; i < maxPages; i++ {
		r, err := api.Do(ctx, c, m, page(after))
		if err != nil {
			return all, err
		}
		values, next := items(r)
		all = append(all, values...)
		if next == "" {
			return all, nil
		}
		if seen[next] || next == after {
			return all, errRepeatedCursor
		}
		seen[next] = true
		after = next
	}
	return all, errPageBound
}

func creationBranches(ctx context.Context, c api.Caller, project string) (branchInventory, error) {
	var out branchInventory
	var err error
	out.Refs, err = pageThrough(ctx, c, api.ProjectBranches, branchPages,
		func(after string) api.ProjectBranchesRequest {
			return api.ProjectBranchesRequest{Project: project, Limit: 8, After: after}
		},
		func(r api.ProjectBranchesResult) ([]plugin.GitRef, string) { return r.Refs, r.Next })
	if err == nil {
		out.Retained, err = pageThrough(ctx, c, api.ProjectRetainedBranches, branchPages,
			func(after string) api.ProjectRetainedBranchesRequest {
				return api.ProjectRetainedBranchesRequest{Project: project, Limit: 8, After: after}
			},
			func(r api.RetainedBranchesResult) ([]api.RetainedBranch, string) { return r.Branches, r.Next })
	}
	if errors.Is(err, errRepeatedCursor) || errors.Is(err, errPageBound) {
		return out, errors.New("branch pagination exceeds bounds or repeats")
	}
	if err != nil {
		return out, err
	}
	state, e := api.Do(ctx, c, api.OriginRefresh, api.OriginRefreshRequest{Project: project})
	if e != nil {
		out.OriginDiagnostic = "Origin observation unavailable; local P sources remain available."
	} else if state.Origin.Status == "fresh" {
		stale := false
		out.Origin, err = pageThrough(ctx, c, api.OriginSources, branchPages,
			func(after string) api.OriginSourcesRequest {
				return api.OriginSourcesRequest{Project: project, Limit: 16, After: after}
			},
			func(r api.OriginSourcesResult) ([]plugin.GitOriginRef, string) {
				if r.Status != "fresh" {
					stale = true
					return nil, ""
				}
				out.OriginURL = r.OriginURL
				return r.Refs, r.Next
			})
		if errors.Is(err, errRepeatedCursor) || errors.Is(err, errPageBound) {
			return out, errors.New("origin pagination exceeds bounds or repeats")
		}
		if err != nil {
			return out, err
		}
		if stale {
			out.Origin = nil
			out.OriginDiagnostic = "Origin observation is stale; refresh before selecting it."
		}
	} else if state.Origin.Status == "unknown" {
		out.OriginDiagnostic = "Origin observation unavailable; stale sources cannot be selected."
	}
	return out, nil
}

// creationBranchesCall reads the branch inventory of project.
func creationBranchesCall(project string) daemonCall {
	return daemonCall{"creation.branches", func(ctx context.Context, c api.Caller) (any, error) {
		return creationBranches(ctx, c, project)
	}}
}

// creationDraft is the project or session that the creation pages build.
type creationDraft struct {
	key, project string
	// url is the origin of a new project; empty for a local-only project.
	url            string
	branch, choice string
	source         sourceSelection
}

// sourceSelection is the commit a new branch starts from: a P commit, or an
// origin ref at the commit and URL the user reviewed.
type sourceSelection struct {
	source, originRef, expectedCommitOID, expectedOriginURL string
}

func (d creationDraft) projectCreate() api.ProjectCreateRequest {
	return api.ProjectCreateRequest{Key: d.key, Project: d.project, URL: d.url}
}

func (d creationDraft) sessionCreate() api.SessionCreateRequest {
	return api.SessionCreateRequest{Key: d.key, Project: d.project, Branch: d.branch, Choice: d.choice,
		Source: d.source.source, OriginRef: d.source.originRef, ExpectedCommitOID: d.source.expectedCommitOID, ExpectedOriginURL: d.source.expectedOriginURL}
}

func (m *Model) choose(r row) tea.Cmd {
	m.choiceQuery = ""
	m.choiceSearching = false
	m.cursor = 0
	switch m.form {
	case "project":
		if r.id == "\x00new-project" {
			m.form = "project-name"
			m.input = ""
			m.navigate("form")
			return nil
		}
		m.creation = creationDraft{key: newKey(), project: r.id}
		m.navigate("create")
		m.form = "branch"
		m.choices = nil
		return m.begin(creationBranchesCall(r.id))
	case "branch":
		if r.id == "\x00new-branch" {
			m.navigate("create")
			m.form = "source"
			m.choices = m.sources
			return nil
		}
		m.creation.choice, m.creation.branch, m.creation.source = "existing", r.id, sourceSelection{}
		m.sourceReview = "Existing retained P branch: " + r.id + "\nObserved tip: " + r.detail
		m.confirmCreation(api.SessionCreate.Name)
	case "source":
		m.creation.source = m.sourceParams[r.id]
		m.creation.choice = "new"
		m.sourceReview = "Source: " + r.label + "\nCommit: " + r.detail
		if strings.HasPrefix(r.id, "origin:") {
			m.sourceReview += "\nExternal origin: " + m.sourceOriginURL
		}
		m.form = "branch-name"
		m.input = ""
		m.navigate("form")
	}
	return nil
}

func (m *Model) confirmCreation(method string) {
	m.confirm = callOf(api.SessionCreate, m.creation.sessionCreate())
	branch := m.creation.branch
	if method == api.ProjectCreate.Name {
		m.confirm = callOf(api.ProjectCreate, m.creation.projectCreate())
		branch = "main (only for an empty project/origin)"
	}
	m.review = fmt.Sprintf("Create %s / %s?\n\nPolicy: current trusted project configuration.\nThe daemon captures it at acceptance; this is not a grant editor.\nNew session gets a private workspace and home.\n\nRequest key: %s", m.creation.project, branch, m.creation.key)
	if method == api.SessionCreate.Name {
		m.review += "\n\n" + m.sourceReview
	} else if m.creation.url != "" {
		m.review += "\nExternal origin: " + m.creation.url
	} else {
		m.review += "\nLocal-only empty project; no source commit yet."
	}
	if method == api.ProjectCreate.Name {
		m.review += "\nAn empty project/origin boots and enters main; a nonempty origin creates the project only."
	}
	m.navigate("creation-review")
}

func (m *Model) submitForm() tea.Cmd {
	if m.working {
		return nil
	}
	switch m.form {
	case "project-name":
		if strings.TrimSpace(m.input) == "" {
			m.notice = "Enter a project path."
			return nil
		}
		m.creation.project = m.input
		m.navigate("form")
		m.form = "project-url"
		m.input = ""
	case "project-url":
		m.creation.url = m.input
		m.confirmCreation(api.ProjectCreate.Name)
	case "branch-name":
		if strings.TrimSpace(m.input) == "" {
			m.notice = "Enter a new branch name."
			return nil
		}
		m.creation.branch = m.input
		m.confirmCreation(api.SessionCreate.Name)
	case "rename":
		if strings.TrimSpace(m.input) == "" {
			m.notice = "Enter the new branch name."
			return nil
		}
		m.name = m.input
		return m.begin(creationBranchesCall(m.contextSession.Project))
	}
	return nil
}

func (m *Model) accept(v actionDone) tea.Cmd {
	switch v.method {
	case "creation.branches":
		b := v.result.(branchInventory)
		if m.page == "form" && m.form == "rename" {
			tip := ""
			for _, r := range b.Refs {
				if r.Ref == "refs/heads/"+m.contextSession.Branch {
					tip = r.OID
				}
			}
			if tip == "" {
				m.notice = "Assigned P branch has no committed tip; Rename unavailable."
				return nil
			}
			m.confirm = callOf(api.SessionRename, api.SessionRenameRequest{Key: newKey(), UUID: m.contextSession.UUID, NewBranch: m.name, ExpectedOldTip: tip})
			m.review = "Rename " + m.contextSession.Project + " / " + m.contextSession.Branch + " to " + m.name + "?\nUUID: " + m.contextSession.UUID + "\nP tip: " + tip + "\nPrivate files and UUID remain; external origin is unchanged."
			m.navigate("confirm")
			return nil
		}
		m.choices = []row{}
		for _, r := range b.Retained {
			m.choices = append(m.choices, row{id: r.Branch, label: r.Branch, detail: r.OID})
		}
		if m.page == "retained" {
			return nil
		}
		m.choices = append([]row{{id: "\x00new-branch", label: "Create new branch"}}, m.choices...)
		m.sources = nil
		m.sourceOriginURL = b.OriginURL
		m.sourceParams = map[string]sourceSelection{}
		for _, r := range b.Refs {
			id := "local:" + r.Ref
			m.sources = append(m.sources, row{id: id, label: "P · " + r.Ref, detail: r.OID})
			m.sourceParams[id] = sourceSelection{source: r.OID}
		}
		for _, r := range b.Origin {
			if r.CommitOID == "" {
				continue
			}
			id := "origin:" + r.Ref
			m.sources = append(m.sources, row{id: id, label: "External origin · " + r.Ref, detail: r.CommitOID})
			m.sourceParams[id] = sourceSelection{originRef: r.Ref, expectedCommitOID: r.CommitOID, expectedOriginURL: b.OriginURL}
		}
		m.notice = b.OriginDiagnostic
	case api.ProjectCreate.Name, api.SessionCreate.Name, api.SessionRename.Name, api.OperationRetry.Name:
		return m.accepted(operationOf(v), v.method)
	case api.SessionDiscard.Name, api.SessionDelete.Name:
		r := resultOf(v, api.SessionDiscard)
		if v.method == api.SessionDelete.Name {
			r = resultOf(v, api.SessionDelete)
		}
		if r.Operation != nil {
			return m.accepted(*r.Operation, v.method)
		}
		return m.removalPlanned(r)
	case api.OperationInspect.Name:
		// An operation opened from O, or read again after a change made
		// elsewhere, is observed: the page did not ask for it.
		return m.followOperation(resultOf(v, api.OperationInspect).Operation, false)
	case api.OperationWait.Name:
		return m.followOperation(resultOf(v, api.OperationWait).Operation, true)
	case api.SessionStart.Name, api.SessionStop.Name:
		r := resultOf(v, api.SessionStart)
		if v.method == api.SessionStop.Name {
			r = resultOf(v, api.SessionStop)
		}
		m.navigate("progress")
		if r.Operation != nil {
			// A new start or stop, or the unfinished one of the same kind.
			m.operationID = r.Operation.ID
			return m.followOperation(*r.Operation, true)
		}
		// Already as asked: nothing was recorded.
		m.operationID = ""
		if v.method == api.SessionStop.Name {
			return m.stopped(r.Session.UUID)
		}
		return m.sessionObserved(*r.Session)
	case api.SessionInspect.Name:
		return m.sessionObserved(resultOf(v, api.SessionInspect).Session)
	case api.SessionServices.Name:
		r := resultOf(v, api.SessionServices)
		if r.UUID != m.contextSession.UUID {
			m.notice = "Invalid service observation; queued request was not issued."
			m.servicesFresh = false
			m.queuedService = nil
			return nil
		}
		selected := ""
		if m.cursor < len(m.services) {
			selected = m.services[m.cursor].Name
		}
		m.services = r.Services
		m.servicesFresh = true
		m.observeSelectedServices(r)
		for i, s := range m.services {
			if s.Name == selected {
				m.cursor = i
			}
		}
		m.clamp()
		m.notice = ""
		if intent := m.queuedService; intent != nil {
			m.queuedService = nil
			if m.page == "services" && m.contextSession.UUID == intent.UUID {
				for _, service := range m.services {
					if service.Name == intent.Unit {
						return m.dispatchService(*intent)
					}
				}
			}
			m.notice = "Queued service request was not issued: its session/unit is no longer observed."
		}
	case api.SessionServiceAction.Name:
		return m.begin(callOf(api.SessionServices, api.SessionServicesRequest{UUID: m.contextSession.UUID}))
	case api.SessionServiceLogs.Name:
		r := resultOf(v, api.SessionServiceLogs)
		m.journal = strings.Split(strings.TrimSuffix(r.Logs, "\n"), "\n")
		if m.follow {
			m.journalOffset = max(0, len(m.journal)-m.capacity())
		}
		m.notice = ""
	}
	return nil
}

// waitSeconds is how long one operation.wait call waits. With connecting,
// the hello and the reply it fits callContext's 25-second budget.
const waitSeconds = 20

// waitFor waits for operation id to leave running.
func waitFor(id string) daemonCall {
	return callOf(api.OperationWait, api.OperationWaitRequest{ID: id, Timeout: waitSeconds})
}

// operationOf is the operation that a change returned.
func operationOf(v actionDone) api.Operation {
	switch v.method {
	case api.ProjectCreate.Name:
		return resultOf(v, api.ProjectCreate).Operation
	case api.SessionCreate.Name:
		return resultOf(v, api.SessionCreate).Operation
	case api.SessionRename.Name:
		return resultOf(v, api.SessionRename).Operation
	default:
		return resultOf(v, api.OperationRetry).Operation
	}
}

// accepted shows operation op, which a change of method recorded, and waits
// for it.
func (m *Model) accepted(op api.Operation, method string) tea.Cmd {
	m.operation = op
	m.operationID = op.ID
	m.attachOnComplete = method == api.SessionCreate.Name || method == api.ProjectCreate.Name
	m.navigate("progress")
	return m.begin(waitFor(m.operationID))
}

// removalPlanCall is the dry run of a discard or delete (kind) of session
// uuid: it returns the removal's plan.
func removalPlanCall(kind, uuid string) daemonCall {
	if kind == "delete" {
		return callOf(api.SessionDelete, api.SessionDeleteRequest{UUID: uuid})
	}
	return callOf(api.SessionDiscard, api.SessionDiscardRequest{UUID: uuid})
}

// removalPlanned shows the plan of the removal the page asked for and offers
// to run it with its change tag. A plan that is not allowed says why.
func (m *Model) removalPlanned(r api.ChangeResult[api.RemovalDetails]) tea.Cmd {
	intent := m.removal
	p := r.Plan
	if intent == nil || p == nil || p.Action != "session."+intent.Kind || p.Target != api.TargetSession(intent.UUID) {
		m.notice = "No valid removal plan returned."
		return nil
	}
	if !p.Allowed || r.ChangeTag == "" {
		sentences := []string{}
		for _, refusal := range p.Refusals {
			sentences = append(sentences, errtext.Refusal(refusal.Code))
		}
		if len(sentences) == 0 {
			sentences = append(sentences, "No valid removal plan returned.")
		}
		m.notice = strings.Join(sentences, " ")
		return nil
	}
	m.review = removalReview(intent.Kind, intent.UUID, *p, r.ChangeTag)
	m.reviewOffset = 0
	change := api.ChangeRequest{Key: newKey(), IfMatch: r.ChangeTag}
	if intent.Kind == "delete" {
		m.confirm = callOf(api.SessionDelete, api.SessionDeleteRequest{UUID: intent.UUID, ChangeRequest: change})
	} else {
		m.confirm = callOf(api.SessionDiscard, api.SessionDiscardRequest{UUID: intent.UUID, ChangeRequest: change})
	}
	m.removal = nil
	m.navigate("review")
	return nil
}

// followOperation shows op on the progress page and follows it to its end: a
// running operation is waited for, again while a wait answers it still
// running, and keeps the intent to enter its session; a completed start or
// creation enters the session once it reads ready. requested is false for an
// operation the page only observes, such as one opened from O: its end is
// reported as completed, never as the page's own Stop.
func (m *Model) followOperation(op api.Operation, requested bool) tea.Cmd {
	m.operation = op
	switch op.Status {
	case "running":
		return m.begin(waitFor(op.ID))
	case "completed":
	default:
		m.attachOnComplete = false
		return nil
	}
	if op.Kind == api.SessionStop.Name && requested {
		return m.stopped(op.SessionUUID)
	}
	if m.attachOnComplete && op.SessionUUID != "" {
		m.contextSession.UUID = op.SessionUUID
		m.operationID = ""
		return m.begin(callOf(api.SessionInspect, api.SessionInspectRequest{UUID: op.SessionUUID}))
	}
	m.attachOnComplete = false
	m.notice = "Operation completed. Back returns to sessions."
	m.refreshing = true
	return m.refresh()
}

// stopped reports session id stopped.
func (m *Model) stopped(id string) tea.Cmd {
	m.lastInteraction[id] = time.Now()
	m.attachOnComplete = false
	m.notice = "Stopped. Files and branch retained."
	m.refreshing = true
	return m.refresh()
}

// sessionObserved shows session s, read once its start needs none or has
// completed, and enters it when it is ready and entry was asked.
func (m *Model) sessionObserved(s api.SessionView) tea.Cmd {
	m.contextSession = s
	if m.attachOnComplete && s.Condition == "ready" {
		return m.launchAttachment()
	}
	if s.Condition != "ready" {
		m.attachOnComplete = false
		m.notice = "Session is " + s.Condition + ": " + s.Diagnostic
	}
	return nil
}

// removalReview shows the plan p of a discard or delete (kind) of session
// uuid, whose change tag is tag: what is lost, by category, and what is
// already gone. P does not look inside the container, so the review names
// what it holds instead of listing it.
func removalReview(kind, uuid string, p api.Plan[api.RemovalDetails], tag string) string {
	d := p.Details
	action := "Discard"
	if kind == "delete" {
		action = "Delete"
	}
	lines := []string{action + " " + d.Project + " / " + d.Branch + "?", "UUID: " + uuid}
	if expiry, ok := api.ParseChangeTag(tag); ok {
		lines = append(lines, "Review expires: "+expiry.Format(time.RFC3339))
	}
	lines = append(lines, "")
	if d.Runtime.Condition == "present" {
		lines = append(lines, "Container: "+d.Runtime.InstanceName+" in Incus project "+d.Runtime.IncusProject+" · "+d.Runtime.OriginalStatus)
		if d.Runtime.OriginalStatus == "Running" {
			lines = append(lines, "P stops the running container before deleting it.")
		}
	} else {
		lines = append(lines, "Container: "+d.Runtime.InstanceName+" is missing from Incus project "+d.Runtime.IncusProject+".")
	}
	if len(p.Loss) > 0 {
		lines = append(lines, "This will lose work; push or copy out what you need before you authorize:")
		for _, item := range p.Loss {
			lines = append(lines, lossLines(item, d)...)
		}
	}
	if len(p.Gone) > 0 {
		lines = append(lines, "Already gone:")
		for _, item := range p.Gone {
			lines = append(lines, goneLines(item)...)
		}
	}
	lines = append(lines, "")
	switch {
	case d.AssignedRefMissing:
		lines = append(lines, "Branch: "+d.AssignedRef+" is already gone from P.")
		if kind == "delete" {
			lines = append(lines, "Delete removes nothing from P's repository.")
		} else {
			lines = append(lines, "Discard keeps no branch; it ends like Delete.")
		}
	case kind == "delete":
		lines = append(lines, "Branch: "+d.AssignedRef+" at "+d.AssignedTip, "Delete removes this branch from P's repository.")
		if d.BranchLoss == nil || len(d.BranchLoss.CommitsLosingPReachability) == 0 {
			lines = append(lines, "No commits leave P: other P branches still reach them.")
		}
	default:
		lines = append(lines, "Branch: "+d.AssignedRef+" at "+d.AssignedTip, "Discard keeps this branch in P.")
	}
	return strings.Join(lines, "\n")
}

// lossLines states loss item item of a removal planned as d.
func lossLines(item api.Loss, d api.RemovalDetails) []string {
	switch item.Kind {
	case api.LossRuntimeData:
		return []string{"- The container and everything in it: uncommitted changes, files outside Git, commits not pushed to P, the home directory and runtime-local credentials. P does not look inside the container to list them."}
	case api.LossPCommits:
		commits := fmt.Sprintf("%d commits on branch %s leave", item.Count, d.Branch)
		if item.Count == 1 {
			commits = "1 commit on branch " + d.Branch + " leaves"
		}
		lines := []string{"- " + commits + " P's repository; P does not check whether the origin has them:"}
		if d.BranchLoss != nil {
			for _, oid := range d.BranchLoss.CommitsLosingPReachability {
				lines = append(lines, "  "+oid)
			}
		}
		return lines
	}
	return []string{"- " + item.Kind + " at " + item.Target}
}

// goneLines states item, which was lost before the removal.
func goneLines(item api.Loss) []string {
	if item.Kind == api.LossRuntimeData {
		return []string{"- The runtime is already missing; whatever it held is gone."}
	}
	return []string{"- " + item.Kind + " at " + item.Target}
}

func (m *Model) dispatchService(intent serviceIntent) tea.Cmd {
	if intent.Action == "journal" {
		m.serviceUnit = intent.Unit
		m.navigate("journal")
		m.journal = nil
		m.journalOffset, m.pan = 0, 0
		m.follow = true
		m.find = ""
		return m.begin(callOf(api.SessionServiceLogs, api.SessionServiceLogsRequest{UUID: intent.UUID, Name: intent.Unit}))
	}
	return m.begin(callOf(api.SessionServiceAction, api.SessionServiceActionRequest{UUID: intent.UUID, Name: intent.Unit, Action: intent.Action}))
}
