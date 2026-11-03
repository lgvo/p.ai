package tui

import (
	"context"
	"errors"
	"fmt"
	"github.com/lgvo/p.ai/internal/plugin"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/lgvo/p.ai/internal/api"
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
	case api.ProjectCreate.Name, api.SessionCreate.Name, api.SessionRename.Name, api.SessionDiscard.Name, api.SessionDelete.Name, api.OperationRetry.Name:
		op := operationOf(v)
		m.operation = op
		m.operationID = op.ID
		m.attachOnComplete = v.method == api.SessionCreate.Name || v.method == api.ProjectCreate.Name
		m.navigate("progress")
		return m.begin(waitFor(m.operationID))
	case api.OperationInspect.Name, api.OperationWait.Name:
		r := resultOf(v, api.OperationInspect)
		if v.method == api.OperationWait.Name {
			r = resultOf(v, api.OperationWait)
		}
		m.operation = r.Operation
		if r.Operation.Status == "running" {
			// A wait that ends with the operation still running is a result:
			// wait again, keeping the intent to attach.
			return m.begin(waitFor(r.Operation.ID))
		}
		if r.Operation.Status == "completed" {
			if m.attachOnComplete && r.Operation.SessionUUID != "" {
				m.contextSession.UUID = r.Operation.SessionUUID
				m.operationID = ""
				return m.begin(callOf(api.SessionInspect, api.SessionInspectRequest{UUID: r.Operation.SessionUUID}))
			}
			m.attachOnComplete = false
			m.notice = "Operation completed. Back returns to sessions."
			m.refreshing = true
			return m.refresh()
		}
	case api.SessionRemovalPreview.Name:
		p := resultOf(v, api.SessionRemovalPreview).Preview
		if p.ConfirmationToken == "" || m.removal == nil || p.SessionUUID != m.removal.UUID || p.Kind != m.removal.Kind || !p.Runtime.RuntimeDataLost {
			m.notice = "No valid removal confirmation returned."
			return nil
		}
		m.review = removalReview(p)
		m.reviewOffset = 0
		if m.removal.Kind == "delete" {
			m.confirm = callOf(api.SessionDelete, api.SessionDeleteRequest{Key: newKey(), UUID: m.removal.UUID, ConfirmationToken: p.ConfirmationToken})
		} else {
			m.confirm = callOf(api.SessionDiscard, api.SessionDiscardRequest{Key: newKey(), UUID: m.removal.UUID, ConfirmationToken: p.ConfirmationToken})
		}
		m.removal = nil
		m.navigate("review")
	case api.SessionStart.Name, api.SessionInspect.Name, api.SessionStop.Name:
		r := sessionOf(v)
		m.contextSession = r.Session
		if v.method == api.SessionStop.Name {
			m.lastInteraction[r.Session.UUID] = time.Now()
			m.attachOnComplete = false
			m.operationID = ""
			m.navigate("progress")
			m.notice = "Stopped. Files and branch retained."
			m.refreshing = true
			return m.refresh()
		}
		if m.attachOnComplete && r.Session.Condition == "ready" {
			return m.launchAttachment()
		}
		if r.Session.Condition != "ready" && r.Session.Condition != "starting" {
			m.attachOnComplete = false
			m.notice = "Session is " + r.Session.Condition + ": " + r.Session.Diagnostic
		}
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
	case api.SessionDiscard.Name:
		return resultOf(v, api.SessionDiscard).Operation
	case api.SessionDelete.Name:
		return resultOf(v, api.SessionDelete).Operation
	default:
		return resultOf(v, api.OperationRetry).Operation
	}
}

// sessionOf is the session that start, stop or inspect returned.
func sessionOf(v actionDone) api.SessionResult {
	switch v.method {
	case api.SessionStart.Name:
		return resultOf(v, api.SessionStart)
	case api.SessionStop.Name:
		return resultOf(v, api.SessionStop)
	default:
		return resultOf(v, api.SessionInspect)
	}
}

// removalReview states the loss by category. P does not look inside the
// container, so the review names what it holds instead of listing it.
func removalReview(p api.RemovalPreview) string {
	action := "Discard"
	if p.Kind == "delete" {
		action = "Delete"
	}
	lines := []string{
		action + " " + p.Project + " / " + p.Branch + "?",
		"UUID: " + p.SessionUUID,
		"Review expires: " + p.ExpiresAt,
		"",
		"Container: " + p.Runtime.InstanceName + " in Incus project " + p.Runtime.IncusProject + " · " + p.Runtime.OriginalStatus,
	}
	if p.Runtime.OriginalStatus == "Running" {
		lines = append(lines, "P stops the running container before deleting it.")
	}
	lines = append(lines,
		"It is deleted with everything in it: uncommitted changes, files outside Git, commits not pushed to P, the home directory and runtime-local credentials.",
		"P does not look inside the container to list them. Push or copy out what you need before you authorize.",
		"")
	switch {
	case p.AssignedRefMissing:
		lines = append(lines, "Branch: "+p.AssignedRef+" is already gone from P.")
		if p.Kind == "delete" {
			lines = append(lines, "Delete removes nothing from P's repository.")
		} else {
			lines = append(lines, "Discard keeps no branch; it ends like Delete.")
		}
	case p.Kind == "delete":
		lines = append(lines, "Branch: "+p.AssignedRef+" at "+p.AssignedTip, "Delete removes this branch from P's repository.")
		commits := []string{}
		if p.BranchLoss != nil {
			commits = p.BranchLoss.CommitsLosingPReachability
		}
		if len(commits) == 0 {
			lines = append(lines, "No commits leave P: other P branches still reach them.")
		} else {
			lines = append(lines, fmt.Sprintf("Commits that leave P (%d):", len(commits)))
			lines = append(lines, commits...)
			lines = append(lines, "P does not check whether the external origin has them.")
		}
	default:
		lines = append(lines, "Branch: "+p.AssignedRef+" at "+p.AssignedTip, "Discard keeps this branch in P.")
	}
	return strings.Join(lines, "\n")
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
