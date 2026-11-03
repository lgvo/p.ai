// tui-mock is a disposable, stateful development fixture. It serves the
// methods of the control protocol that the production TUI and attachment
// helper call, with the protocol's own request and result types (internal/api)
// and control's method table and connection handling; the production TUI has
// no fixture-specific code.
package main

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

type object = map[string]any

// fault is mock.configure's request: a one-shot fault for the next call of
// Method. That call waits DelayMS, then answers the error kind Error with
// Reason when Error is set; ServicesEmpty makes a session.services call
// observe no units.
type fault struct {
	Method        string `json:"method"`
	DelayMS       int    `json:"delay_ms,omitempty"`
	Error         string `json:"error,omitempty"`
	Reason        string `json:"reason,omitempty"`
	ServicesEmpty bool   `json:"services_empty,omitempty"`
}

func (r fault) Validate() error {
	if r.Method == "" || len(r.Method) > 128 || r.DelayMS < 0 || r.DelayMS > 5000 ||
		r.Error != "" && api.ErrorKind(r.Error).Code() == 0 || len(r.Reason) > 64 {
		return errors.New("supply method, an optional error kind and reason, and delay_ms 0..5000")
	}
	return nil
}

type mockConfigureResult struct{}

type mockInspectRequest struct{}

func (mockInspectRequest) Validate() error { return nil }

// mockInspectResult is the fixture's whole state, an oracle independent of the
// client under test.
type mockInspectResult struct {
	Projects   map[string]api.ProjectSummary   `json:"projects"`
	Sessions   map[string]api.SessionView      `json:"sessions"`
	Operations map[string]api.Operation        `json:"operations"`
	Services   map[string][]api.ProjectService `json:"services"`
	Refs       map[string]map[string]string    `json:"refs"`
	Calls      []call                          `json:"calls"`
}

// call is one request a client sent; Params is its JSON as sent.
type call struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// The fixture's own methods, served beside the protocol's.
var (
	mockInspect   = api.Method[mockInspectRequest, mockInspectResult]{Name: "mock.inspect"}
	mockConfigure = api.Method[fault, mockConfigureResult]{Name: "mock.configure"}
)

type preview struct {
	UUID, Kind string
	Expires    time.Time
}
type pending struct {
	UUID    string
	Expires time.Time
}
type attachmentLease struct {
	UUID      string
	Confirmed bool
}

// connection is one client connection: its attachment lease and the
// system.hello requests it sent that are not recorded yet.
type connection struct {
	lease attachmentLease
	// hellos are recorded with the connection's next call, or when it closes.
	// A call of the fixture's own methods drops them, so a connection that
	// only inspects or configures the fixture leaves no calls behind.
	hellos []call
}

type fixture struct {
	mu       sync.Mutex
	dir      string
	projects map[string]api.ProjectSummary
	sessions map[string]api.SessionView
	ops      map[string]api.Operation
	refs     map[string]map[string]string
	origins  map[string]string
	services map[string][]api.ProjectService
	previews map[string]preview
	tokens   map[string]pending
	keys     map[string]string
	calls    []call
	faults   map[string]fault
	native   *nativeServer
}

const (
	tip = "0123456789abcdef0123456789abcdef01234567"
	// observedAt is when the fixture's origins were observed.
	observedAt = "2026-10-01T12:00:00Z"
)

// instanceID is the fixture daemon's instance.
var instanceID = uuid.NewSHA1(uuid.NameSpaceURL, []byte("tui-mock")).String()

func newFixture(dir, dataset string) *fixture {
	f := &fixture{dir: dir, projects: map[string]api.ProjectSummary{}, sessions: map[string]api.SessionView{}, ops: map[string]api.Operation{}, refs: map[string]map[string]string{}, origins: map[string]string{}, services: map[string][]api.ProjectService{}, previews: map[string]preview{}, tokens: map[string]pending{}, keys: map[string]string{}, calls: []call{}, faults: map[string]fault{}}
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
				s.LatestUnattendedCondition = &api.UnattendedCondition{Condition: "attention", Source: "Codex", Reason: "Waiting for review", ReceivedAt: "2026-10-01T12:00:00Z", Adapter: "fixture", AdapterVersion: "1", ReceiveSequence: 1}
			}
			if name == "forge" && i == 0 {
				// One session shows its source commit's image; the others were created without image builders.
				s.Image = &api.ImageView{CommitOID: tip, System: "x86_64-linux", Selection: "devShells.x86_64-linux.default", State: "reused", BaseImageFingerprint: strings.Repeat("d", 64), ImageFingerprint: strings.Repeat("b", 64)}
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
	f.ops[id] = api.Operation{ID: id, Key: "fixture-failed", Kind: "session.rename", Project: "forge", Status: "blocked", Phase: "rename", Reason: api.ReasonRuntime, Diagnostic: "Incus did not answer within 30 seconds", Request: json.RawMessage(`{}`)}
	return f
}
func (f *fixture) addProject(name, origin string) {
	f.projects[name] = api.ProjectSummary{Path: name, Registry: "active"}
	f.refs[name] = map[string]string{"main": tip}
	f.origins[name] = origin
}
func (f *fixture) addSession(id, project, branch string) api.SessionView {
	s := api.SessionView{UUID: id, Project: project, Branch: branch, Registry: "established", Condition: "stopped", PolicyCondition: "current", PolicySHA256: strings.Repeat("a", 64)}
	f.sessions[id] = s
	f.refs[project][branch] = tip
	f.services[id] = []api.ProjectService{
		{Name: "p-project-api.service", Description: "Mock notes API", State: api.ServiceRunning, Detail: "active (running)"},
		{Name: "p-project-database.service", Description: "Mock database", State: api.ServiceRunning, Detail: "active (running)"},
		{Name: "p-project-worker.service", Description: "Mock worker", State: api.ServiceStopped, Detail: "inactive (dead)"},
	}
	return s
}

// mockError is an error the fixture answers, as the daemon answers the same
// condition.
func mockError(kind api.ErrorKind, reason, target string) *api.Error {
	e := api.NewError(kind, reason)
	if e.Data != nil {
		e.Data.Target = target
	}
	return e
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

// serve answers one connection with control's connection handling. When the
// connection closes, its unrecorded hellos are recorded and its confirmed
// attachment ends.
func (f *fixture) serve(ctx context.Context, conn net.Conn) {
	c := &connection{}
	defer f.release(c)
	control.ServeConn(ctx, conn, f.handler(c))
}
func (f *fixture) release(c *connection) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c.hellos...)
	c.hellos = nil
	if s, ok := f.sessions[c.lease.UUID]; ok && c.lease.Confirmed && s.AttachedCount > 0 {
		s.AttachedCount--
		f.sessions[c.lease.UUID] = s
	}
}

// faultKey carries a call's configured fault to its handler.
type faultKey struct{}

// handler serves connection c. It records each call, applies the fault
// configured for its method, then dispatches it through c's method table
// under the fixture lock. ServeConn encodes a result after the handler
// returns, so the result is frozen as JSON under the lock: observation never
// races a mutation.
func (f *fixture) handler(c *connection) control.Handler {
	t := control.NewMethodTable()
	f.register(t, &c.lease)
	dispatch := t.Handler()
	return func(ctx context.Context, method string, params json.RawMessage) (any, *api.Error) {
		f.mu.Lock()
		injected := f.faults[method]
		delete(f.faults, method)
		f.record(c, method, params)
		f.mu.Unlock()
		if injected.DelayMS > 0 {
			select {
			case <-time.After(time.Duration(injected.DelayMS) * time.Millisecond):
			case <-ctx.Done():
				return nil, mockError(api.KindCancelled, "", "")
			}
		}
		if injected.Error != "" {
			return nil, mockError(api.ErrorKind(injected.Error), injected.Reason, "")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		result, problem := dispatch(context.WithValue(ctx, faultKey{}, injected), method, params)
		if problem != nil {
			return nil, problem
		}
		data, err := json.Marshal(result)
		if err != nil {
			return nil, mockError(api.KindInternal, api.ReasonEncoding, "")
		}
		return json.RawMessage(data), nil
	}
}

// record adds a call of c to the calls; see connection.hellos.
func (f *fixture) record(c *connection, method string, params json.RawMessage) {
	entry := call{Method: method, Params: append(json.RawMessage(nil), params...)}
	switch {
	case method == api.SystemHello.Name:
		c.hellos = append(c.hellos, entry)
	case strings.HasPrefix(method, "mock."):
		c.hellos = nil
	default:
		f.calls = append(f.calls, c.hellos...)
		c.hellos = nil
		f.calls = append(f.calls, entry)
	}
}

// register serves through t the fixture's own methods and the protocol's
// methods that the production TUI and attachment helper call; any other name
// answers method_not_found. Every handler runs under f.mu. lease is the
// connection's attachment lease.
func (f *fixture) register(t *control.MethodTable, lease *attachmentLease) {
	control.Register(t, mockInspect, func(context.Context, mockInspectRequest) (mockInspectResult, error) {
		return mockInspectResult{Projects: f.projects, Sessions: f.sessions, Operations: f.ops, Services: f.services, Refs: f.refs, Calls: f.calls}, nil
	})
	control.Register(t, mockConfigure, func(_ context.Context, r fault) (mockConfigureResult, error) {
		f.faults[r.Method] = r
		return mockConfigureResult{}, nil
	})
	control.Register(t, api.SystemHello, func(context.Context, api.HelloRequest) (api.HelloResult, error) {
		return api.HelloResult{Protocol: api.Protocol, BuildVersion: "fixture", InstanceID: instanceID,
			Capabilities: []api.Capability{api.CapabilityGit, api.CapabilityRuntime, api.CapabilityServices}}, nil
	})
	control.Register(t, api.SystemHealth, func(context.Context, api.HealthRequest) (api.HealthResult, error) {
		return api.HealthResult{ControlState: "ready"}, nil
	})
	f.registerReads(t)
	f.registerLifecycle(t)
	f.registerServices(t)
	f.registerAttachment(t, lease)
}

func (f *fixture) registerReads(t *control.MethodTable) {
	control.Register(t, api.ProjectList, func(_ context.Context, r api.ProjectListRequest) (api.ProjectListResult, error) {
		all := []api.ProjectSummary{}
		for _, v := range f.projects {
			all = append(all, v)
		}
		sort.Slice(all, func(i, j int) bool { return all[i].Path < all[j].Path })
		out, next := page(all, r.After, r.Limit, func(v api.ProjectSummary) string { return v.Path })
		return api.ProjectListResult{Projects: out, Next: next}, nil
	})
	control.Register(t, api.SessionList, func(_ context.Context, r api.SessionListRequest) (api.SessionListResult, error) {
		all := []api.SessionView{}
		for _, v := range f.sessions {
			all = append(all, v)
		}
		sort.Slice(all, func(i, j int) bool { return all[i].UUID < all[j].UUID })
		out, next := page(all, r.After, r.Limit, func(v api.SessionView) string { return v.UUID })
		return api.SessionListResult{Sessions: out, Next: next}, nil
	})
	control.Register(t, api.OperationList, func(_ context.Context, r api.OperationListRequest) (api.OperationListResult, error) {
		all := []api.OperationSummary{}
		for _, v := range f.ops {
			all = append(all, control.SummarizeOperation(v))
		}
		sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
		out, next := page(all, r.After, r.Limit, func(v api.OperationSummary) string { return v.ID })
		return api.OperationListResult{Operations: out, Next: next}, nil
	})
	control.Register(t, api.OperationInspect, func(_ context.Context, r api.OperationInspectRequest) (api.OperationResult, error) {
		op, ok := f.ops[r.ID]
		if !ok {
			return api.OperationResult{}, mockError(api.KindNotFound, api.ReasonOperation, api.TargetOperation(r.ID))
		}
		return api.OperationResult{Operation: op}, nil
	})
	// The fixture's operations never run, so a wait answers at once.
	control.Register(t, api.OperationWait, func(_ context.Context, r api.OperationWaitRequest) (api.OperationResult, error) {
		op, ok := f.ops[r.ID]
		if !ok {
			return api.OperationResult{}, mockError(api.KindNotFound, api.ReasonOperation, api.TargetOperation(r.ID))
		}
		return api.OperationResult{Operation: op}, nil
	})
	control.Register(t, api.OperationRetry, func(_ context.Context, r api.OperationRetryRequest) (api.OperationResult, error) {
		op, ok := f.ops[r.ID]
		if !ok {
			return api.OperationResult{}, mockError(api.KindNotFound, api.ReasonOperation, api.TargetOperation(r.ID))
		}
		op.Status = "completed"
		op.Phase = "complete"
		op.Reason, op.Diagnostic = "", ""
		f.ops[op.ID] = op
		return api.OperationResult{Operation: op}, nil
	})
	// Branch pages, like the daemon's, are ordered and resumed by full ref.
	control.Register(t, api.ProjectBranches, func(_ context.Context, r api.ProjectBranchesRequest) (api.ProjectBranchesResult, error) {
		branches, err := f.branches(r.Project, false)
		if err != nil {
			return api.ProjectBranchesResult{}, err
		}
		refs := []plugin.GitRef{}
		for _, b := range branches {
			refs = append(refs, plugin.GitRef{Ref: b.Ref, OID: b.OID})
		}
		out, next := page(refs, r.After, r.Limit, func(v plugin.GitRef) string { return v.Ref })
		return api.ProjectBranchesResult{Project: r.Project, Refs: out, Next: next}, nil
	})
	control.Register(t, api.ProjectRetainedBranches, func(_ context.Context, r api.ProjectRetainedBranchesRequest) (api.RetainedBranchesResult, error) {
		branches, err := f.branches(r.Project, true)
		if err != nil {
			return api.RetainedBranchesResult{}, err
		}
		out, next := page(branches, r.After, r.Limit, func(v api.RetainedBranch) string { return v.Ref })
		return api.RetainedBranchesResult{Branches: out, Next: next}, nil
	})
	control.Register(t, api.OriginRefresh, func(_ context.Context, r api.OriginRefreshRequest) (api.OriginResult, error) {
		state, _, err := f.origin(r.Project)
		return api.OriginResult{Origin: state}, err
	})
	control.Register(t, api.OriginSources, func(_ context.Context, r api.OriginSourcesRequest) (api.OriginSourcesResult, error) {
		state, refs, err := f.origin(r.Project)
		if err != nil {
			return api.OriginSourcesResult{}, err
		}
		out, next := page(refs, r.After, r.Limit, func(v plugin.GitOriginRef) string { return v.Ref })
		return api.OriginSourcesResult{Project: r.Project, OriginURL: state.URL, Status: state.Status, ObservationStatus: state.Status, ObservedAt: state.ObservedAt, Refs: out, Next: next}, nil
	})
}

// branches is the P branches of project ordered by ref; retained keeps only
// those no session is assigned.
func (f *fixture) branches(project string, retained bool) ([]api.RetainedBranch, error) {
	if _, ok := f.projects[project]; !ok {
		return nil, mockError(api.KindNotFound, api.ReasonProject, api.TargetProject(project))
	}
	assigned := map[string]bool{}
	for _, v := range f.sessions {
		if v.Project == project {
			assigned[v.Branch] = true
		}
	}
	all := []api.RetainedBranch{}
	for branch, oid := range f.refs[project] {
		if !retained || !assigned[branch] {
			all = append(all, api.RetainedBranch{Branch: branch, Ref: "refs/heads/" + branch, OID: oid})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Ref < all[j].Ref })
	return all, nil
}

// origin is the observation of project's origin: fresh with its main branch
// when the project has an origin, none otherwise.
func (f *fixture) origin(project string) (api.OriginState, []plugin.GitOriginRef, error) {
	if _, ok := f.projects[project]; !ok {
		return api.OriginState{}, nil, mockError(api.KindNotFound, api.ReasonProject, api.TargetProject(project))
	}
	url := f.origins[project]
	if url == "" {
		return api.OriginState{Project: project, Status: "none"}, []plugin.GitOriginRef{}, nil
	}
	refs := []plugin.GitOriginRef{{Ref: "refs/heads/main", OID: tip, CommitOID: tip}}
	return api.OriginState{Project: project, URL: url, Status: "fresh", ObservedAt: observedAt, RefCount: len(refs)}, refs, nil
}

// session is the session id names; one the fixture lacks is unavailable.
func (f *fixture) session(id string) (api.SessionView, error) {
	s, ok := f.sessions[id]
	if !ok {
		return s, mockError(api.KindNotFound, api.ReasonSession, api.TargetSession(id))
	}
	return s, nil
}

// registerChange serves m, a change that records an operation, for the
// session that change returns, under the request's idempotency key. A
// repeated key answers the operation it recorded when the request is the
// same, and conflict otherwise.
func registerChange[Req api.Request](t *control.MethodTable, f *fixture, m api.Method[Req, api.OperationResult], key func(Req) string, change func(Req) (api.SessionView, error)) {
	control.Register(t, m, func(_ context.Context, r Req) (api.OperationResult, error) {
		request, err := json.Marshal(r)
		if err != nil {
			return api.OperationResult{}, mockError(api.KindInternal, api.ReasonEncoding, "")
		}
		if id := f.keys[key(r)]; id != "" {
			op := f.ops[id]
			if op.Kind != m.Name || string(request) != string(op.Request) {
				return api.OperationResult{}, mockError(api.KindInvalid, api.ReasonKeyReused, "")
			}
			return api.OperationResult{Operation: op}, nil
		}
		s, err := change(r)
		if err != nil {
			return api.OperationResult{}, err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		op := api.Operation{ID: uuid.NewString(), Key: key(r), Kind: m.Name, Project: s.Project, SessionUUID: s.UUID, Status: "completed", Phase: "complete", Committed: true, Request: request, CreatedAt: now, UpdatedAt: now}
		f.ops[op.ID] = op
		f.keys[op.Key] = op.ID
		return api.OperationResult{Operation: op}, nil
	})
}

// sessionChange records a completed start or stop, kind, of session s; its
// key is its own ID, and it replaces the session's older one, as the
// daemon's do.
func (f *fixture) sessionChange(kind string, s api.SessionView, request any) *api.Operation {
	for id, old := range f.ops {
		if old.SessionUUID == s.UUID && (old.Kind == api.SessionStart.Name || old.Kind == api.SessionStop.Name) {
			delete(f.ops, id)
		}
	}
	raw, _ := json.Marshal(request)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	id := uuid.NewString()
	op := api.Operation{ID: id, Key: id, Kind: kind, Project: s.Project, SessionUUID: s.UUID, Status: "completed", Phase: "complete", Request: raw, CreatedAt: now, UpdatedAt: now}
	f.ops[id] = op
	return &op
}

func (f *fixture) registerLifecycle(t *control.MethodTable) {
	registerChange(t, f, api.ProjectCreate, func(r api.ProjectCreateRequest) string { return r.Key }, func(r api.ProjectCreateRequest) (api.SessionView, error) {
		if _, ok := f.projects[r.Project]; ok {
			return api.SessionView{}, mockError(api.KindConflict, api.ReasonProjectExists, api.TargetProject(r.Project))
		}
		f.addProject(r.Project, r.URL)
		s := f.addSession(uuid.NewString(), r.Project, "main")
		s.Condition = "ready"
		f.sessions[s.UUID] = s
		return s, nil
	})
	registerChange(t, f, api.SessionCreate, func(r api.SessionCreateRequest) string { return r.Key }, func(r api.SessionCreateRequest) (api.SessionView, error) {
		if _, ok := f.projects[r.Project]; !ok {
			return api.SessionView{}, mockError(api.KindNotFound, api.ReasonProject, api.TargetProject(r.Project))
		}
		for _, v := range f.sessions {
			if v.Project == r.Project && v.Branch == r.Branch {
				return api.SessionView{}, mockError(api.KindConflict, api.ReasonBranchAssigned, api.TargetBranch(r.Project, r.Branch))
			}
		}
		if r.Choice == "existing" {
			if f.refs[r.Project][r.Branch] == "" {
				return api.SessionView{}, mockError(api.KindNotFound, api.ReasonBranch, api.TargetBranch(r.Project, r.Branch))
			}
		} else if f.refs[r.Project][r.Branch] != "" {
			return api.SessionView{}, mockError(api.KindConflict, api.ReasonBranchExists, api.TargetBranch(r.Project, r.Branch))
		} else if r.OriginRef != "" {
			if r.ExpectedCommitOID != tip || r.ExpectedOriginURL != f.origins[r.Project] {
				return api.SessionView{}, mockError(api.KindPreconditionFailed, api.ReasonChanged, "")
			}
		} else if r.Source != tip {
			return api.SessionView{}, mockError(api.KindNotFound, api.ReasonSource, "")
		}
		s := f.addSession(uuid.NewString(), r.Project, r.Branch)
		s.Condition = "ready"
		f.sessions[s.UUID] = s
		return s, nil
	})
	control.Register(t, api.SessionInspect, func(_ context.Context, r api.SessionInspectRequest) (api.SessionResult, error) {
		s, err := f.session(r.UUID)
		return api.SessionResult{Session: s}, err
	})
	// Start and stop, like the daemon's, record nothing for a session already
	// as asked; otherwise the fixture's operation completes at once.
	control.Register(t, api.SessionStart, func(_ context.Context, r api.SessionStartRequest) (api.SessionChangeResult, error) {
		s, err := f.session(r.UUID)
		if err != nil {
			return api.SessionChangeResult{}, err
		}
		if s.Condition == "ready" {
			return api.SessionChangeResult{Session: &s}, nil
		}
		s.Condition = "ready"
		f.sessions[s.UUID] = s
		return api.SessionChangeResult{Changed: true, Operation: f.sessionChange(api.SessionStart.Name, s, r)}, nil
	})
	control.Register(t, api.SessionStop, func(_ context.Context, r api.SessionStopRequest) (api.SessionChangeResult, error) {
		s, err := f.session(r.UUID)
		if err != nil {
			return api.SessionChangeResult{}, err
		}
		if s.AttachedCount != 0 {
			return api.SessionChangeResult{}, mockError(api.KindConflict, api.ReasonAttachmentActive, api.TargetSession(r.UUID))
		}
		if s.Condition == "stopped" {
			return api.SessionChangeResult{Session: &s}, nil
		}
		f.native.stopSession(s.UUID)
		s.Condition = "stopped"
		f.sessions[s.UUID] = s
		return api.SessionChangeResult{Changed: true, Operation: f.sessionChange(api.SessionStop.Name, s, r)}, nil
	})
	registerChange(t, f, api.SessionRename, func(r api.SessionRenameRequest) string { return r.Key }, func(r api.SessionRenameRequest) (api.SessionView, error) {
		s, err := f.session(r.UUID)
		if err != nil {
			return s, err
		}
		if s.Condition != "stopped" {
			return s, mockError(api.KindConflict, api.ReasonSessionNotStopped, api.TargetSession(r.UUID))
		}
		if f.refs[s.Project][r.NewBranch] != "" {
			return s, mockError(api.KindConflict, api.ReasonBranchExists, api.TargetBranch(s.Project, r.NewBranch))
		}
		if r.ExpectedOldTip != f.refs[s.Project][s.Branch] {
			return s, mockError(api.KindPreconditionFailed, api.ReasonChanged, "")
		}
		delete(f.refs[s.Project], s.Branch)
		f.refs[s.Project][r.NewBranch] = tip
		s.Branch = r.NewBranch
		f.sessions[s.UUID] = s
		return s, nil
	})
	control.Register(t, api.SessionRemovalPreview, func(_ context.Context, r api.SessionRemovalPreviewRequest) (api.RemovalPreviewResult, error) {
		s, err := f.session(r.UUID)
		if err != nil {
			return api.RemovalPreviewResult{}, err
		}
		if s.AttachedCount != 0 {
			return api.RemovalPreviewResult{}, mockError(api.KindConflict, api.ReasonAttachmentActive, api.TargetSession(r.UUID))
		}
		status := map[string]string{"ready": "Running", "stopped": "Stopped"}[s.Condition]
		if status == "" {
			return api.RemovalPreviewResult{}, mockError(api.KindConflict, api.ReasonRuntimeUnsettled, api.TargetSession(r.UUID))
		}
		token := strings.ReplaceAll(uuid.NewString(), "-", "")
		expires := time.Now().Add(time.Minute)
		f.previews[token] = preview{UUID: s.UUID, Kind: r.Kind, Expires: expires}
		return api.RemovalPreviewResult{Preview: f.removalReview(s, r.Kind, status, token, expires)}, nil
	})
	registerChange(t, f, api.SessionDiscard, func(r api.SessionDiscardRequest) string { return r.Key }, func(r api.SessionDiscardRequest) (api.SessionView, error) {
		return f.remove("discard", r.UUID, r.ConfirmationToken)
	})
	registerChange(t, f, api.SessionDelete, func(r api.SessionDeleteRequest) string { return r.Key }, func(r api.SessionDeleteRequest) (api.SessionView, error) {
		return f.remove("delete", r.UUID, r.ConfirmationToken)
	})
}

// remove discards or deletes session id after the review that token names.
// Like P, it stops a running container itself before removing it.
func (f *fixture) remove(kind, id, token string) (api.SessionView, error) {
	s, err := f.session(id)
	if err != nil {
		return s, err
	}
	if s.AttachedCount != 0 {
		return s, mockError(api.KindConflict, api.ReasonAttachmentActive, api.TargetSession(id))
	}
	review, ok := f.previews[token]
	if !ok || time.Now().After(review.Expires) {
		return s, mockError(api.KindPreconditionFailed, api.ReasonExpired, "")
	}
	if review.UUID != id || review.Kind != kind {
		return s, mockError(api.KindPreconditionFailed, api.ReasonChanged, "")
	}
	delete(f.previews, token)
	f.native.stopSession(id)
	delete(f.sessions, id)
	delete(f.services, id)
	if kind == "delete" {
		delete(f.refs[s.Project], s.Branch)
	}
	return s, nil
}

func (f *fixture) registerServices(t *control.MethodTable) {
	control.Register(t, api.SessionServices, func(ctx context.Context, r api.SessionServicesRequest) (api.SessionServicesResult, error) {
		services, _, err := f.units(r.UUID, "")
		if err != nil {
			return api.SessionServicesResult{}, err
		}
		if injected, _ := ctx.Value(faultKey{}).(fault); injected.ServicesEmpty {
			services = []api.ProjectService{}
		}
		return api.SessionServicesResult{UUID: r.UUID, Services: services, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
	})
	control.Register(t, api.SessionServiceAction, func(_ context.Context, r api.SessionServiceActionRequest) (api.SessionServiceActionResult, error) {
		services, index, err := f.units(r.UUID, r.Name)
		if err != nil {
			return api.SessionServiceActionResult{}, err
		}
		if r.Action == "stop" {
			services[index].State, services[index].Detail = api.ServiceStopped, "inactive (dead)"
		} else {
			services[index].State, services[index].Detail = api.ServiceRunning, "active (running)"
		}
		return api.SessionServiceActionResult{UUID: r.UUID, Name: r.Name, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
	})
	control.Register(t, api.SessionServiceLogs, func(_ context.Context, r api.SessionServiceLogsRequest) (api.SessionServiceLogsResult, error) {
		if _, _, err := f.units(r.UUID, r.Name); err != nil {
			return api.SessionServiceLogsResult{}, err
		}
		logs := ""
		for i := 1; i <= 90; i++ {
			logs += fmt.Sprintf("mock %03d %s request=%d status=ok %s\n", i, r.Name, i, strings.Repeat("wide-journal ", 14))
		}
		return api.SessionServiceLogsResult{UUID: r.UUID, Name: r.Name, Logs: logs, LogsTruncated: true, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
	})
}

// units is the project services of running session id, and the index of
// unit among them. A unit that is named but absent is unavailable.
func (f *fixture) units(id, unit string) ([]api.ProjectService, int, error) {
	s, err := f.session(id)
	if err != nil {
		return nil, -1, err
	}
	if s.Condition != "ready" {
		return nil, -1, mockError(api.KindConflict, api.ReasonSessionNotRunning, api.TargetSession(id))
	}
	services := f.services[id]
	for i, v := range services {
		if v.Name == unit {
			return services, i, nil
		}
	}
	if unit != "" {
		// The daemon reaches the session's user manager, which refuses an
		// unknown unit.
		return nil, -1, mockError(api.KindFailed, api.ReasonServices, "")
	}
	return services, -1, nil
}

func (f *fixture) registerAttachment(t *control.MethodTable, lease *attachmentLease) {
	spec := func(id string) plugin.AttachSpec {
		return plugin.AttachSpec{Project: "p-mock", Instance: "p-" + id, Argv: []string{"/usr/libexec/p/attach"}}
	}
	control.Register(t, api.SessionAttach, func(_ context.Context, r api.SessionAttachRequest) (api.PendingAttachment, error) {
		s, err := f.session(r.UUID)
		if err != nil {
			return api.PendingAttachment{}, err
		}
		if s.Condition != "ready" {
			return api.PendingAttachment{}, mockError(api.KindConflict, api.ReasonSessionNotRunning, api.TargetSession(r.UUID))
		}
		token := strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")
		expires := time.Now().Add(time.Minute)
		f.tokens[token] = pending{UUID: s.UUID, Expires: expires}
		return api.PendingAttachment{Token: token, ExpiresAt: expires, Spec: spec(s.UUID)}, nil
	})
	control.Register(t, api.AttachmentClaim, func(_ context.Context, r api.AttachmentClaimRequest) (api.AttachmentLaunch, error) {
		claimed := f.tokens[r.Token]
		id := claimed.UUID
		if id == "" || f.sessions[id].Condition != "ready" || time.Now().After(claimed.Expires) || lease.UUID != "" {
			return api.AttachmentLaunch{}, mockError(api.KindPreconditionFailed, api.ReasonExpired, "")
		}
		delete(f.tokens, r.Token)
		lease.UUID = id
		return api.AttachmentLaunch{Socket: filepath.Join(f.dir, "native.sock"), Spec: spec(id)}, nil
	})
	control.Register(t, api.AttachmentConfirm, func(_ context.Context, r api.AttachmentConfirmRequest) (api.AttachmentResult, error) {
		s, ok := f.sessions[lease.UUID]
		if !ok || lease.Confirmed || !f.native.running(r.Operation, lease.UUID) {
			return api.AttachmentResult{}, mockError(api.KindPreconditionFailed, api.ReasonExpired, "")
		}
		s.AttachedCount++
		lease.Confirmed = true
		s.LatestUnattendedCondition = nil
		f.sessions[s.UUID] = s
		return api.AttachmentResult{}, nil
	})
	control.Register(t, api.AttachmentPing, func(context.Context, api.AttachmentPingRequest) (api.AttachmentResult, error) {
		if lease.UUID == "" {
			return api.AttachmentResult{}, mockError(api.KindInvalidRequest, api.ReasonConnection, "")
		}
		return api.AttachmentResult{}, nil
	})
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
