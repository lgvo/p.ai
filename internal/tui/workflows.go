package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/lgvo/p.ai/internal/control"
)

type ref struct {
	Ref       string `json:"ref"`
	OID       string `json:"oid"`
	CommitOID string `json:"commit_oid"`
	Branch    string `json:"branch"`
}
type branchInventory struct {
	Refs             []ref
	Retained         []ref
	Origin           []ref
	OriginURL        string
	OriginDiagnostic string
}

func creationBranches(ctx context.Context, c Client, project string) (json.RawMessage, error) {
	var out branchInventory
	for _, method := range []string{"project.branches", "project.retained_branches"} {
		after := ""
		seen := map[string]bool{}
		for i := 0; i < 128; i++ {
			r, e := request[struct {
				Refs     []ref  `json:"refs"`
				Branches []ref  `json:"branches"`
				Next     string `json:"next"`
			}](ctx, c, method, params{"v": 1, "project": project, "limit": 8, "after": after})
			if e != nil {
				return nil, e
			}
			out.Refs = append(out.Refs, r.Refs...)
			out.Retained = append(out.Retained, r.Branches...)
			if r.Next == "" {
				break
			}
			if seen[r.Next] || r.Next == after || i == 127 {
				return nil, errors.New("branch pagination exceeds bounds or repeats")
			}
			seen[r.Next] = true
			after = r.Next
		}
	}
	state, e := request[struct {
		Origin struct {
			Status string `json:"status"`
			URL    string `json:"url"`
		} `json:"origin"`
	}](ctx, c, "origin.refresh", params{"v": 1, "project": project})
	if e != nil {
		out.OriginDiagnostic = "Origin observation unavailable; local P sources remain available."
	} else if state.Origin.Status == "fresh" {
		after := ""
		seen := map[string]bool{}
		for i := 0; i < 128; i++ {
			r, e := request[struct {
				Refs      []ref  `json:"refs"`
				Next      string `json:"next"`
				Status    string `json:"status"`
				OriginURL string `json:"origin_url"`
			}](ctx, c, "origin.sources", params{"v": 1, "project": project, "limit": 16, "after": after})
			if e != nil {
				return nil, e
			}
			if r.Status != "fresh" {
				out.Origin = nil
				out.OriginDiagnostic = "Origin observation is stale; refresh before selecting it."
				break
			}
			out.Origin = append(out.Origin, r.Refs...)
			out.OriginURL = r.OriginURL
			if r.Next == "" {
				break
			}
			if seen[r.Next] || r.Next == after || i == 127 {
				return nil, errors.New("origin pagination exceeds bounds or repeats")
			}
			seen[r.Next] = true
			after = r.Next
		}
	} else if state.Origin.Status == "unknown" {
		out.OriginDiagnostic = "Origin observation unavailable; stale sources cannot be selected."
	}
	return json.Marshal(out)
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
		m.creation = params{"v": 1, "key": newKey(), "project": r.id}
		m.navigate("create")
		m.form = "branch"
		m.choices = nil
		return m.begin("creation.branches", params{"project": r.id})
	case "branch":
		if r.id == "\x00new-branch" {
			m.navigate("create")
			m.form = "source"
			m.choices = m.sources
			return nil
		}
		m.creation["choice"] = "existing"
		m.creation["branch"] = r.id
		m.sourceReview = "Existing retained P branch: " + r.id + "\nObserved tip: " + r.detail
		delete(m.creation, "source")
		delete(m.creation, "origin_ref")
		delete(m.creation, "expected_commit_oid")
		delete(m.creation, "expected_origin_url")
		m.confirmCreation("session.create")
	case "source":
		for _, k := range []string{"source", "origin_ref", "expected_commit_oid", "expected_origin_url"} {
			delete(m.creation, k)
		}
		for k, v := range m.sourceParams[r.id] {
			m.creation[k] = v
		}
		m.creation["choice"] = "new"
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
	m.confirmMethod = method
	m.confirmParams = cloneParams(m.creation)
	branch, _ := m.creation["branch"].(string)
	if method == "project.create" {
		branch = "main (only for an empty project/origin)"
	}
	m.review = fmt.Sprintf("Create %s / %s?\n\nPolicy: current trusted project configuration.\nThe daemon captures it at acceptance; this is not a grant editor.\nNew session gets a private workspace and home.\n\nRequest key: %s", m.creation["project"], branch, m.creation["key"])
	if method == "session.create" {
		m.review += "\n\n" + m.sourceReview
	} else if url, ok := m.creation["url"].(string); ok {
		m.review += "\nExternal origin: " + url
	} else {
		m.review += "\nLocal-only empty project; no source commit yet."
	}
	m.navigate("confirm")
}
func cloneParams(p params) params {
	out := params{}
	for k, v := range p {
		out[k] = v
	}
	return out
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
		m.creation["project"] = m.input
		m.navigate("form")
		m.form = "project-url"
		m.input = ""
	case "project-url":
		if m.input != "" {
			m.creation["url"] = m.input
		} else {
			delete(m.creation, "url")
		}
		m.confirmCreation("project.create")
	case "branch-name":
		if strings.TrimSpace(m.input) == "" {
			m.notice = "Enter a new branch name."
			return nil
		}
		m.creation["branch"] = m.input
		m.confirmCreation("session.create")
	case "rename":
		if strings.TrimSpace(m.input) == "" {
			m.notice = "Enter the new branch name."
			return nil
		}
		m.name = m.input
		return m.begin("creation.branches", params{"project": m.contextSession.Project})
	}
	return nil
}

func (m *Model) accept(v actionDone) tea.Cmd {
	switch v.method {
	case "creation.branches":
		var b branchInventory
		if e := json.Unmarshal(v.raw, &b); e != nil {
			m.notice = e.Error()
			return nil
		}
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
			m.confirmMethod = "session.rename"
			m.confirmParams = params{"v": 1, "key": newKey(), "uuid": m.contextSession.UUID, "new_branch": m.name, "expected_old_tip": tip}
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
		m.sourceParams = map[string]params{}
		for _, r := range b.Refs {
			id := "local:" + r.Ref
			m.sources = append(m.sources, row{id: id, label: "P · " + r.Ref, detail: r.OID})
			m.sourceParams[id] = params{"source": r.OID}
		}
		for _, r := range b.Origin {
			if r.CommitOID == "" {
				continue
			}
			id := "origin:" + r.Ref
			m.sources = append(m.sources, row{id: id, label: "External origin · " + r.Ref, detail: r.CommitOID})
			m.sourceParams[id] = params{"origin_ref": r.Ref, "expected_commit_oid": r.CommitOID, "expected_origin_url": b.OriginURL}
		}
		m.notice = b.OriginDiagnostic
	case "project.create", "session.create", "session.rename", "session.discard", "session.delete", "operation.retry":
		var r struct {
			Operation control.Operation `json:"operation"`
		}
		if e := json.Unmarshal(v.raw, &r); e != nil {
			m.notice = e.Error()
			return nil
		}
		m.operation = r.Operation
		m.operationID = r.Operation.ID
		m.attachOnComplete = v.method == "session.create" || v.method == "project.create"
		m.navigate("progress")
		return m.begin("operation.inspect", params{"v": 1, "id": m.operationID})
	case "workspace.loss.inspect":
		var r struct {
			Operation control.Operation `json:"operation"`
		}
		if e := json.Unmarshal(v.raw, &r); e != nil {
			m.notice = e.Error()
			return nil
		}
		if m.removal == nil || r.Operation.SessionUUID != m.removal.UUID || r.Operation.Kind != "workspace.loss.inspect" || r.Operation.ID == "" {
			m.notice = "Loss operation does not match the captured removal session."
			return nil
		}
		m.operation = r.Operation
		m.operationID = r.Operation.ID
		m.removal.LossOperationID = r.Operation.ID
		return m.begin("operation.inspect", params{"v": 1, "id": m.operationID})
	case "operation.inspect":
		var r struct {
			Operation control.Operation `json:"operation"`
		}
		if e := json.Unmarshal(v.raw, &r); e != nil {
			m.notice = e.Error()
			return nil
		}
		if m.removal != nil && (r.Operation.ID != m.removal.LossOperationID || r.Operation.SessionUUID != m.removal.UUID || r.Operation.Kind != "workspace.loss.inspect") {
			m.notice = "Loss operation does not match the captured removal session."
			return nil
		}
		m.operation = r.Operation
		if r.Operation.Status == "completed" {
			if r.Operation.Kind == "workspace.loss.inspect" && m.removal != nil && r.Operation.ID == m.removal.LossOperationID && r.Operation.SessionUUID == m.removal.UUID {
				return m.begin("session.removal.preview", params{"v": 1, "uuid": m.removal.UUID, "kind": m.removal.Kind, "loss_operation_id": r.Operation.ID})
			}
			if m.attachOnComplete && r.Operation.SessionUUID != "" {
				m.contextSession.UUID = r.Operation.SessionUUID
				m.operationID = ""
				return m.begin("session.inspect", params{"v": 1, "uuid": r.Operation.SessionUUID})
			}
			m.attachOnComplete = false
			m.notice = "Operation completed. Back returns to sessions."
			m.refreshing = true
			return m.refresh()
		}
	case "session.removal.preview":
		var r struct {
			Preview json.RawMessage `json:"preview"`
		}
		if e := json.Unmarshal(v.raw, &r); e != nil {
			m.notice = e.Error()
			return nil
		}
		var p struct {
			Token string `json:"confirmation_token"`
			UUID  string `json:"session_uuid"`
			Kind  string `json:"kind"`
		}
		if e := json.Unmarshal(r.Preview, &p); e != nil || p.Token == "" || m.removal == nil || p.UUID != m.removal.UUID || p.Kind != m.removal.Kind {
			m.notice = "No valid removal confirmation returned."
			return nil
		}
		var pretty any
		_ = json.Unmarshal(r.Preview, &pretty)
		text, _ := json.MarshalIndent(pretty, "", "  ")
		m.review = string(text)
		m.reviewOffset = 0
		m.confirmMethod = "session." + m.removal.Kind
		m.confirmParams = params{"v": 1, "key": newKey(), "uuid": m.removal.UUID, "confirmation_token": p.Token}
		m.removal = nil
		m.navigate("review")
	case "session.start", "session.inspect", "session.stop":
		var r struct {
			Session control.SessionView `json:"session"`
		}
		if e := json.Unmarshal(v.raw, &r); e != nil {
			m.notice = e.Error()
			return nil
		}
		m.contextSession = r.Session
		if v.method == "session.stop" {
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
	case "session.services":
		var r control.ServiceResult
		if e := json.Unmarshal(v.raw, &r); e != nil || r.V != 1 || r.UUID != m.contextSession.UUID {
			m.notice = "Invalid service observation; queued request was not issued."
			m.servicesFresh = false
			m.queuedService = nil
			return nil
		}
		selected := ""
		if m.cursor < len(m.services) {
			selected = m.services[m.cursor].Unit
		}
		m.services = r.Services
		m.servicesFresh = true
		for i, s := range m.services {
			if s.Unit == selected {
				m.cursor = i
			}
		}
		m.clamp()
		m.notice = ""
		if intent := m.queuedService; intent != nil {
			m.queuedService = nil
			if m.page == "services" && m.contextSession.UUID == intent.UUID {
				for _, service := range m.services {
					if service.Unit == intent.Unit {
						return m.dispatchService(*intent)
					}
				}
			}
			m.notice = "Queued service request was not issued: its session/unit is no longer observed."
		}
	case "session.service.action":
		return m.begin("session.services", params{"v": 1, "uuid": m.contextSession.UUID})
	case "session.service.journal":
		var r control.ServiceResult
		if e := json.Unmarshal(v.raw, &r); e != nil {
			m.notice = e.Error()
			return nil
		}
		m.journal = strings.Split(strings.TrimSuffix(r.Journal, "\n"), "\n")
		if m.follow {
			m.journalOffset = max(0, len(m.journal)-m.capacity())
		}
		m.notice = ""
	}
	return nil
}

func (m *Model) dispatchService(intent serviceIntent) tea.Cmd {
	if intent.Action == "journal" {
		m.serviceUnit = intent.Unit
		m.navigate("journal")
		m.journal = nil
		m.journalOffset, m.pan = 0, 0
		m.follow = true
		m.find = ""
		return m.begin("session.service.journal", params{"v": 1, "uuid": intent.UUID, "unit": intent.Unit})
	}
	return m.begin("session.service.action", params{"v": 1, "uuid": intent.UUID, "unit": intent.Unit, "action": intent.Action})
}
