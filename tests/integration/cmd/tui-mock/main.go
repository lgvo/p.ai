// tui-mock is a disposable, stateful development fixture. It implements the
// public socket contract; the production TUI has no fixture-specific code.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

type object = map[string]any
type fault struct {
	Method        string `json:"method"`
	DelayMS       int    `json:"delay_ms"`
	Error         string `json:"error"`
	ServicesEmpty bool   `json:"services_empty,omitempty"`
}
type preview struct {
	UUID, Kind, Loss string
	Expires          time.Time
}
type pending struct {
	UUID    string
	Expires time.Time
}
type attachmentLease struct {
	UUID      string
	Confirmed bool
}
type fixture struct {
	mu       sync.Mutex
	dir      string
	projects map[string]control.ProjectSummary
	sessions map[string]control.SessionView
	ops      map[string]control.Operation
	refs     map[string]map[string]string
	origins  map[string]string
	services map[string][]control.ProjectService
	previews map[string]preview
	tokens   map[string]pending
	keys     map[string]string
	calls    []object
	faults   map[string]fault
	native   *nativeServer
}

var methods = []string{"system.capabilities", "system.health", "project.list", "project.create", "project.branches", "project.retained_branches", "origin.refresh", "origin.sources", "session.list", "session.inspect", "session.create", "session.start", "session.stop", "session.rename", "session.discard", "session.delete", "session.attach", "session.services", "session.service.action", "session.service.journal", "workspace.loss.inspect", "session.removal.preview", "operation.list", "operation.inspect", "operation.retry"}

const tip = "0123456789abcdef0123456789abcdef01234567"

func newFixture(dir, dataset string) *fixture {
	f := &fixture{dir: dir, projects: map[string]control.ProjectSummary{}, sessions: map[string]control.SessionView{}, ops: map[string]control.Operation{}, refs: map[string]map[string]string{}, origins: map[string]string{}, services: map[string][]control.ProjectService{}, previews: map[string]preview{}, tokens: map[string]pending{}, keys: map[string]string{}, faults: map[string]fault{}}
	if dataset == "empty" {
		return f
	}
	names := []string{"forge", "orbit"}
	if dataset == "portfolio" {
		names = []string{"atlas", "beacon", "cedar", "delta", "ember", "forge", "grove", "harbor", "iris", "juniper", "kepler", "lumen", "mesa", "nova", "orbit", "p.ai", "quartz", "ridge", "spruce", "terra", "umbra", "vale", "willow", "zenith"}
	}
	for _, name := range names {
		f.addProject(name, "https://example.invalid/"+name+".git")
		branches := []string{"main", "feat/ui", "fix/cache", "test/services", "docs/guide"}
		if name == "forge" {
			branches = []string{"feat/device-login", "fix/token-refresh", "docs/plugin-guide", "test/auth-regressions", "main"}
		}
		if name == "orbit" {
			branches = []string{"fix/cache-race", "feat/job-retries", "main", "test/cache", "docs/guide"}
		}
		if name == "p.ai" {
			branches = []string{"feat/session-creation", "fix/project-selector", "main", "test/tui", "docs/guide"}
		}
		if dataset == "small" {
			branches = branches[:2]
		}
		for i, branch := range branches {
			id := uuid.NewSHA1(uuid.NameSpaceURL, []byte(name+"/"+branch)).String()
			s := f.addSession(id, name, branch)
			if (name == "forge" && i < 4) || (name == "orbit" || name == "p.ai") && i < 2 {
				s.Condition = "ready"
			}
			if (name == "forge" || name == "orbit") && i == 0 {
				s.LatestUnattendedCondition = &control.UnattendedCondition{Condition: "attention", Source: "Codex", Reason: "Waiting for review", ReceivedAt: "2026-10-01T12:00:00Z", Adapter: "fixture", AdapterVersion: "1", ReceiveSequence: 1}
			}
			f.sessions[id] = s
		}
		count := 4
		if name == "forge" && dataset == "portfolio" {
			count = 32
		}
		for i := 0; i < count; i++ {
			f.refs[name][fmt.Sprintf("retained/%02d", i)] = tip
		}
	}
	id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("failed-operation")).String()
	f.ops[id] = control.Operation{ID: id, Key: "fixture-failed", Kind: "session.rename", Project: "forge", Status: "failed", Phase: "rename", Diagnostic: "Fixture failure: branch conflict. Press r to retry.", Request: json.RawMessage(`{"v":1}`)}
	return f
}
func (f *fixture) addProject(name, origin string) {
	f.projects[name] = control.ProjectSummary{Path: name, Registry: "active"}
	f.refs[name] = map[string]string{"main": tip}
	f.origins[name] = origin
}
func (f *fixture) addSession(id, project, branch string) control.SessionView {
	s := control.SessionView{UUID: id, Project: project, Branch: branch, Registry: "established", Condition: "stopped", PolicyCondition: "current", PolicySHA256: strings.Repeat("a", 64)}
	f.sessions[id] = s
	f.refs[project][branch] = tip
	f.services[id] = []control.ProjectService{{Unit: "p-project-api.service", Description: "Mock notes API", ActiveState: "active", SubState: "running"}, {Unit: "p-project-database.service", Description: "Mock database", ActiveState: "active", SubState: "running"}, {Unit: "p-project-worker.service", Description: "Mock worker", ActiveState: "inactive", SubState: "dead"}}
	return s
}
func rpcError(kind, message string) (any, *control.RPCError) {
	code := -32003
	if kind == "invalid_params" {
		code = -32602
	}
	if kind == "unavailable" {
		code = -32004
	}
	if kind == "cancelled" {
		code = -32001
	}
	return nil, &control.RPCError{Code: code, Kind: kind, Message: message}
}
func page[T any](values []T, after string, limit int, id func(T) string) ([]T, string) {
	out := []T{}
	next := ""
	for _, v := range values {
		if id(v) <= after {
			continue
		}
		if len(out) == limit {
			next = id(out[len(out)-1])
			break
		}
		out = append(out, v)
	}
	return out, next
}
func (f *fixture) operation(method string, p object, s control.SessionView) object {
	key, _ := p["key"].(string)
	if id := f.keys[key]; key != "" && id != "" {
		return object{"v": 1, "operation": f.ops[id]}
	}
	request, _ := json.Marshal(p)
	op := control.Operation{ID: uuid.NewString(), Key: key, Kind: method, Project: s.Project, SessionUUID: s.UUID, Status: "completed", Phase: "complete", Committed: method != "workspace.loss.inspect", Request: request, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	f.ops[op.ID] = op
	if key != "" {
		f.keys[key] = op.ID
	}
	return object{"v": 1, "operation": op}
}

// Each ServeConn closure owns its lease, independently of transient RPCs.
func (f *fixture) serve(ctx context.Context, conn net.Conn) {
	lease := attachmentLease{}
	defer func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if s, ok := f.sessions[lease.UUID]; ok && lease.Confirmed && s.AttachedCount > 0 {
			s.AttachedCount--
			f.sessions[lease.UUID] = s
		}
	}()
	control.ServeConn(ctx, conn, func(ctx context.Context, method string, raw json.RawMessage) (any, *control.RPCError) {
		return f.handle(ctx, method, raw, &lease)
	})
}
func (f *fixture) handle(ctx context.Context, method string, raw json.RawMessage, lease *attachmentLease) (result any, problem *control.RPCError) {
	var p object
	if control.RejectDuplicateKeys(raw) != nil || json.Unmarshal(raw, &p) != nil || p["v"] != float64(1) {
		return rpcError("invalid_params", "Fixture requires params with v=1")
	}
	if err := validateParams(method, p); err != nil {
		return rpcError("invalid_params", err.Error())
	}
	f.mu.Lock()
	injected := f.faults[method]
	delete(f.faults, method)
	if !strings.HasPrefix(method, "mock.") {
		f.calls = append(f.calls, object{"method": method, "params": p})
	}
	f.mu.Unlock()
	if injected.DelayMS > 0 {
		select {
		case <-time.After(time.Duration(injected.DelayMS) * time.Millisecond):
		case <-ctx.Done():
			return rpcError("cancelled", "Fixture request cancelled")
		}
	}
	if injected.Error != "" {
		return rpcError("fixture_failure", injected.Error)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// ServeConn encodes after this handler returns. Freeze mutable maps and
	// slices under the lock so concurrent observation never races mutations.
	defer func() {
		if result != nil {
			data, err := json.Marshal(result)
			if err != nil {
				result, problem = rpcError("internal", "Cannot encode fixture response")
			} else {
				result = json.RawMessage(data)
			}
		}
	}()
	str := func(k string) string { v, _ := p[k].(string); return v }
	id, project, after := str("uuid"), str("project"), str("after")
	if key := str("key"); key != "" && f.keys[key] != "" {
		op := f.ops[f.keys[key]]
		request, _ := json.Marshal(p)
		if op.Kind != method || string(request) != string(op.Request) {
			return rpcError("conflict", "Idempotency key belongs to another captured intent")
		}
		return object{"v": 1, "operation": op}, nil
	}
	limit := 8
	if n, ok := p["limit"].(float64); ok {
		if n < 1 || n > 100 || n != float64(int(n)) {
			return rpcError("invalid_params", "limit must be 1..100")
		}
		limit = int(n)
	}
	s, exists := f.sessions[id]
	if strings.HasPrefix(method, "session.") && method != "session.list" && method != "session.create" && !exists {
		return rpcError("unavailable", "Session is absent from this fixture")
	}
	switch method {
	case "mock.inspect":
		return object{"v": 1, "projects": f.projects, "sessions": f.sessions, "operations": f.ops, "services": f.services, "refs": f.refs, "calls": f.calls}, nil
	case "mock.configure":
		var config fault
		if json.Unmarshal(raw, &config) != nil || config.Method == "" || config.DelayMS < 0 || config.DelayMS > 5000 {
			return rpcError("invalid_params", "Supply method, optional error and delay_ms 0..5000")
		}
		f.faults[config.Method] = config
		return object{"v": 1}, nil
	case "system.health":
		return object{"v": 1, "control_state": "ready", "fixture": true}, nil
	case "system.capabilities":
		return object{"v": 1, "available": methods, "lifecycle": "fixture"}, nil
	case "project.list":
		all := []control.ProjectSummary{}
		for _, v := range f.projects {
			all = append(all, v)
		}
		sort.Slice(all, func(i, j int) bool { return all[i].Path < all[j].Path })
		out, next := page(all, after, limit, func(v control.ProjectSummary) string { return v.Path })
		return object{"v": 1, "projects": out, "next": next}, nil
	case "session.list":
		all := []control.SessionView{}
		for _, v := range f.sessions {
			if project == "" || project == v.Project {
				all = append(all, v)
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].UUID < all[j].UUID })
		out, next := page(all, after, limit, func(v control.SessionView) string { return v.UUID })
		return object{"v": 1, "sessions": out, "next": next}, nil
	case "operation.list":
		all := []control.OperationSummary{}
		for _, v := range f.ops {
			all = append(all, control.SummarizeOperation(v))
		}
		sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
		out, next := page(all, after, limit, func(v control.OperationSummary) string { return v.ID })
		return object{"v": 1, "operations": out, "next": next}, nil
	case "operation.inspect", "operation.retry":
		op, ok := f.ops[str("id")]
		if !ok {
			return rpcError("unavailable", "Operation absent")
		}
		if method == "operation.retry" {
			op.Status = "completed"
			op.Phase = "complete"
			op.Diagnostic = ""
			f.ops[op.ID] = op
		}
		return object{"v": 1, "operation": op}, nil
	case "project.branches", "project.retained_branches", "origin.refresh", "origin.sources":
		if _, ok := f.projects[project]; !ok {
			return rpcError("unavailable", "Project absent")
		}
		origin := f.origins[project]
		status := "fresh"
		if origin == "" {
			status = "none"
		}
		if method == "origin.refresh" {
			return object{"v": 1, "origin": object{"status": status, "url": origin}}, nil
		}
		all := []object{}
		field, cursor := "refs", "ref"
		if method == "origin.sources" {
			if origin != "" {
				all = append(all, object{"ref": "refs/heads/main", "oid": tip, "commit_oid": tip})
			}
		} else {
			assigned := map[string]bool{}
			for _, v := range f.sessions {
				if v.Project == project {
					assigned[v.Branch] = true
				}
			}
			for branch, oid := range f.refs[project] {
				if method == "project.retained_branches" {
					if !assigned[branch] {
						all = append(all, object{"branch": branch, "oid": oid})
					}
				} else {
					all = append(all, object{"ref": "refs/heads/" + branch, "oid": oid})
				}
			}
		}
		if method == "project.retained_branches" {
			field, cursor = "branches", "branch"
		}
		sort.Slice(all, func(i, j int) bool { return all[i][cursor].(string) < all[j][cursor].(string) })
		out, next := page(all, after, limit, func(v object) string { return v[cursor].(string) })
		return object{"v": 1, field: out, "next": next, "status": status, "origin_url": origin}, nil
	case "project.create", "session.create":
		if key := str("key"); key != "" && f.keys[key] != "" {
			return object{"v": 1, "operation": f.ops[f.keys[key]]}, nil
		}
		branch := str("branch")
		if method == "session.create" {
			var request control.ReserveSessionRequest
			data, _ := json.Marshal(p)
			_ = json.Unmarshal(data, &request)
			if !control.ValidSessionCreateRequest(request) {
				return rpcError("invalid_params", "Invalid session.create request")
			}
		}
		if method == "project.create" {
			if _, ok := f.projects[project]; ok || project == "" {
				return rpcError("conflict", "Project already exists or name is empty")
			}
			f.addProject(project, str("url"))
			branch = "main"
		} else {
			if _, ok := f.projects[project]; !ok {
				return rpcError("unavailable", "Project absent")
			}
			for _, v := range f.sessions {
				if v.Project == project && v.Branch == branch {
					return rpcError("conflict", "Branch already assigned to a session")
				}
			}
			if str("choice") == "existing" {
				if f.refs[project][branch] == "" {
					return rpcError("conflict", "Retained branch absent")
				}
			} else if str("choice") != "new" || branch == "" || f.refs[project][branch] != "" {
				return rpcError("conflict", "New branch conflicts or choice invalid")
			} else if str("origin_ref") != "" {
				if str("expected_commit_oid") != tip || str("expected_origin_url") != f.origins[project] {
					return rpcError("conflict", "Origin changed since review")
				}
			} else if str("source") != tip {
				return rpcError("conflict", "P source changed since review")
			}
		}
		s = f.addSession(uuid.NewString(), project, branch)
		s.Condition = "ready"
		f.sessions[s.UUID] = s
		return f.operation(method, p, s), nil
	case "session.inspect", "session.start", "session.stop":
		if method == "session.stop" {
			if s.AttachedCount != 0 {
				return rpcError("busy", "Detach before Stop")
			}
			f.native.stopSession(id)
			s.Condition = "stopped"
		} else if method == "session.start" {
			s.Condition = "ready"
		}
		f.sessions[id] = s
		return object{"v": 1, "session": s}, nil
	case "session.rename":
		branch := str("new_branch")
		if s.Condition != "stopped" || s.AttachedCount != 0 || branch == "" || f.refs[s.Project][branch] != "" || str("expected_old_tip") != f.refs[s.Project][s.Branch] {
			return rpcError("conflict", "Stop and detach; choose a new name and current tip")
		}
		delete(f.refs[s.Project], s.Branch)
		f.refs[s.Project][branch] = tip
		s.Branch = branch
		f.sessions[id] = s
		return f.operation(method, p, s), nil
	case "workspace.loss.inspect":
		if !exists {
			return rpcError("unavailable", "Session absent")
		}
		if s.Condition != "stopped" || s.AttachedCount != 0 {
			return rpcError("busy", "Stop and detach before loss inspection")
		}
		return f.operation(method, p, s), nil
	case "session.removal.preview":
		loss := f.ops[str("loss_operation_id")]
		kind := str("kind")
		if loss.Kind != "workspace.loss.inspect" || loss.SessionUUID != id || (kind != "discard" && kind != "delete") || s.Condition != "stopped" {
			return rpcError("conflict", "Loss inspection does not match review")
		}
		token := strings.ReplaceAll(uuid.NewString(), "-", "")
		expires := time.Now().Add(time.Minute)
		f.previews[token] = preview{UUID: id, Kind: kind, Loss: loss.ID, Expires: expires}
		return object{"v": 1, "preview": f.removalReview(s, kind, loss, token, expires)}, nil
	case "session.discard", "session.delete":
		token := str("confirmation_token")
		review, ok := f.previews[token]
		if !ok || review.UUID != id || "session."+review.Kind != method || time.Now().After(review.Expires) || s.Condition != "stopped" || s.AttachedCount != 0 {
			return rpcError("conflict", "Removal review expired or session changed")
		}
		delete(f.previews, token)
		f.native.stopSession(id)
		delete(f.sessions, id)
		delete(f.services, id)
		if method == "session.delete" {
			delete(f.refs[s.Project], s.Branch)
		}
		return f.operation(method, p, s), nil
	case "session.services", "session.service.action", "session.service.journal":
		if s.Condition != "ready" {
			return rpcError("unavailable", "Start session before inspecting project services")
		}
		services := f.services[id]
		unit := str("unit")
		index := -1
		for i, v := range services {
			if v.Unit == unit {
				index = i
			}
		}
		if method != "session.services" && index < 0 {
			return rpcError("unavailable", "Project unit absent")
		}
		r := control.ServiceResult{V: 1, UUID: id, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		if method == "session.services" {
			r.Services = services
			if injected.ServicesEmpty {
				r.Services = []control.ProjectService{}
			}
		} else if method == "session.service.action" {
			switch str("action") {
			case "start", "restart":
				services[index].ActiveState = "active"
				services[index].SubState = "running"
			case "stop":
				services[index].ActiveState = "inactive"
				services[index].SubState = "dead"
			default:
				return rpcError("invalid_params", "Unknown service action")
			}
			f.services[id] = services
			r.Unit = unit
		} else {
			r.Unit = unit
			for i := 1; i <= 90; i++ {
				r.Journal += fmt.Sprintf("mock %03d %s request=%d status=ok %s\n", i, unit, i, strings.Repeat("wide-journal ", 14))
			}
			r.JournalTruncated = true
		}
		return r, nil
	case "session.attach":
		if s.Condition != "ready" {
			return rpcError("unavailable", "Session is stopped")
		}
		token := strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")
		f.tokens[token] = pending{UUID: id, Expires: time.Now().Add(time.Minute)}
		return control.PendingAttachment{V: 1, Token: token, ExpiresAt: time.Now().Add(time.Minute), Spec: plugin.AttachSpec{Project: "p-mock", Instance: "p-" + id, Argv: []string{"/usr/libexec/p/attach"}}}, nil
	case "attachment.claim":
		pending := f.tokens[str("token")]
		id = pending.UUID
		if id == "" || f.sessions[id].Condition != "ready" || time.Now().After(pending.Expires) || lease.UUID != "" {
			return rpcError("conflict", "Attachment token is unknown or used")
		}
		delete(f.tokens, str("token"))
		lease.UUID = id
		return control.AttachmentLaunch{V: 1, Socket: filepath.Join(f.dir, "native.sock"), Spec: plugin.AttachSpec{Project: "p-mock", Instance: "p-" + id, Argv: []string{"/usr/libexec/p/attach"}}}, nil
	case "attachment.confirm":
		s, ok := f.sessions[lease.UUID]
		if !ok || lease.Confirmed || !f.native.running(str("operation"), lease.UUID) {
			return rpcError("conflict", "Attachment is not running")
		}
		s.AttachedCount++
		lease.Confirmed = true
		s.LatestUnattendedCondition = nil
		f.sessions[s.UUID] = s
		return object{"v": 1}, nil
	case "attachment.ping":
		if lease.UUID == "" {
			return rpcError("conflict", "No attachment lease")
		}
		return object{"v": 1}, nil
	default:
		return nil, &control.RPCError{Code: -32601, Kind: "method_not_found", Message: "Method is not supported by this fixture"}
	}
}

func main() {
	dir := flag.String("state-dir", "", "private temporary directory (required)")
	dataset := flag.String("dataset", "portfolio", "portfolio, small or empty")
	flag.Parse()
	if *dir == "" || (*dataset != "portfolio" && *dataset != "small" && *dataset != "empty") {
		fmt.Fprintln(os.Stderr, "tui-mock: require --state-dir DIR and --dataset portfolio|small|empty")
		os.Exit(2)
	}
	info, err := os.Lstat(*dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		fmt.Fprintln(os.Stderr, "tui-mock: state directory must exist with mode 0700")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := runFixture(ctx, *dir, *dataset); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func runFixture(parent context.Context, dir, dataset string) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	f := newFixture(dir, dataset)
	var err error
	f.native, err = newNative(dir)
	if err != nil {
		return err
	}
	defer f.native.close()
	listener, err := net.Listen("unix", filepath.Join(dir, "control.sock"))
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(filepath.Join(dir, "control.sock"), 0600); err != nil {
		return err
	}
	go func() { <-ctx.Done(); listener.Close() }()
	var clients sync.WaitGroup
	for {
		conn, err := listener.Accept()
		if err != nil {
			cancel()
			break
		}
		clients.Add(1)
		go func() { defer clients.Done(); f.serve(ctx, conn) }()
	}
	clients.Wait()
	return nil
}
