package control

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/strictjson"
)

// BuildVersion may be set at link time by the package recipe.
var BuildVersion = "development"

type rpcRequest struct {
	Method string
	Params json.RawMessage
	ID     json.RawMessage
	HasID  bool
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *api.RPCError   `json:"error,omitempty"`
}

type GitReader interface {
	ListRefsPage(context.Context, string, string, int) ([]plugin.GitRef, string, error)
}

func errorRPC(code int, kind, message string) *api.RPCError {
	return &api.RPCError{Code: code, Kind: kind, Message: message}
}

// Handler answers one control-API request. The connection dispatches the
// method name and raw params to it.
type Handler func(context.Context, string, json.RawMessage) (any, *api.RPCError)

// HandlerConfig names what trusted configuration enabled. Git, the lifecycle
// and origins stay nil until configured; each serves its methods.
type HandlerConfig struct {
	Store     *Store
	Git       GitReader
	GitInfo   *api.GitInfo
	Lifecycle LifecycleAPI
	Origins   *OriginAPI
}

// MethodTable maps each method of the protocol to its handler.
type MethodTable struct {
	handlers map[string]Handler
}

func NewMethodTable() *MethodTable { return &MethodTable{handlers: map[string]Handler{}} }

// Register serves m with h. The dispatch decodes the params into m's request
// type with strict JSON, runs its Validate, calls h and encodes its result.
// h returns an *api.RPCError to answer a specific error; any other error is
// mapped as a lifecycle error.
func Register[Req api.Request, Res any](t *MethodTable, m api.Method[Req, Res], h func(context.Context, Req) (Res, error)) {
	t.handlers[m.Name] = func(ctx context.Context, _ string, params json.RawMessage) (any, *api.RPCError) {
		var req Req
		if !isObject(params) || strictjson.Decode(params, &req) != nil {
			return nil, errorRPC(-32602, "invalid_params", "invalid "+m.Name+" params")
		}
		if err := req.Validate(); err != nil {
			return nil, errorRPC(-32602, "invalid_params", err.Error())
		}
		res, err := h(ctx, req)
		if err != nil {
			var rpcErr *api.RPCError
			if errors.As(err, &rpcErr) {
				return nil, rpcErr
			}
			return nil, lifecycleRPC(err)
		}
		return res, nil
	}
}

// isObject reports whether params is a JSON object; strict decoding also
// accepts null for a struct.
func isObject(params json.RawMessage) bool {
	trimmed := bytes.TrimLeft(params, " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] == '{'
}

// Handler dispatches to the registered handlers; any other name answers
// method_not_found.
func (t *MethodTable) Handler() Handler {
	return func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
		if h, ok := t.handlers[method]; ok {
			return h(ctx, method, params)
		}
		return nil, errorRPC(-32601, "method_not_found", "method is unknown")
	}
}

// Registered reports whether name has a handler.
func (t *MethodTable) Registered(name string) bool {
	_, ok := t.handlers[name]
	return ok
}

// registerNotConfigured answers every method of the protocol that has no
// handler: its capability is not configured.
func (t *MethodTable) registerNotConfigured() {
	for _, m := range api.Methods() {
		if m.Connection || t.Registered(m.Name) {
			continue
		}
		message := string(m.Capability) + " capability is not configured"
		t.handlers[m.Name] = func(context.Context, string, json.RawMessage) (any, *api.RPCError) {
			return nil, errorRPC(-32004, "unavailable", message)
		}
	}
}

// NewHandler builds the control-API surface for one configuration. Every
// method of the protocol is answered; a method whose capability is not
// configured, or whose optional lifecycle interface is missing, answers that
// its capability is not configured.
func NewHandler(c HandlerConfig) Handler { return newMethodTable(c).Handler() }

func newMethodTable(c HandlerConfig) *MethodTable {
	t := NewMethodTable()
	git := c.Git != nil && c.GitInfo != nil
	capabilities := []api.Capability{}
	if git {
		capabilities = append(capabilities, api.CapabilityGit)
	}
	if c.Lifecycle != nil {
		capabilities = append(capabilities, api.CapabilityRuntime)
		if _, ok := c.Lifecycle.(ServiceAPI); ok {
			capabilities = append(capabilities, api.CapabilityServices)
		}
	}
	addSystemMethods(t, c, capabilities)
	if git {
		addProjectReads(t, c.Store, c.Git)
	}
	if c.Lifecycle != nil {
		addLifecycleMethods(t, c.Store, c.Lifecycle)
	}
	if c.Origins != nil {
		addOriginMethods(t, *c.Origins)
	}
	t.registerNotConfigured()
	return t
}

func addSystemMethods(t *MethodTable, c HandlerConfig, capabilities []api.Capability) {
	store := c.Store
	var git *api.GitInfo
	if c.Git != nil {
		git = c.GitInfo
	}
	// ServeConn checks the protocol before it dispatches system.hello.
	Register(t, api.SystemHello, func(ctx context.Context, _ api.HelloRequest) (api.HelloResult, error) {
		id, err := store.InstanceID(ctx)
		if err != nil {
			return api.HelloResult{}, internalRPC(err)
		}
		return api.HelloResult{Protocol: api.Protocol, BuildVersion: BuildVersion, InstanceID: id,
			Capabilities: append([]api.Capability{}, capabilities...)}, nil
	})
	Register(t, api.SystemHealth, func(ctx context.Context, _ api.HealthRequest) (api.HealthResult, error) {
		if err := store.Ping(ctx); err != nil {
			return api.HealthResult{}, internalRPC(err)
		}
		return api.HealthResult{ControlState: "ready"}, nil
	})
	Register(t, api.SystemInspect, func(ctx context.Context, _ api.SystemInspectRequest) (api.SystemInspectResult, error) {
		projects, sessions, operations, err := store.Count(ctx)
		if err != nil {
			return api.SystemInspectResult{}, internalRPC(err)
		}
		return api.SystemInspectResult{Projects: projects, Sessions: sessions, Operations: operations, Git: git}, nil
	})
}

func addProjectReads(t *MethodTable, store *Store, git GitReader) {
	Register(t, api.ProjectList, func(ctx context.Context, r api.ProjectListRequest) (api.ProjectListResult, error) {
		projects, next, err := store.ListProjects(ctx, r.After, r.Limit)
		if err != nil {
			return api.ProjectListResult{}, internalRPC(err)
		}
		if projects == nil {
			projects = []api.ProjectSummary{}
		}
		return api.ProjectListResult{Projects: projects, Next: next}, nil
	})
	Register(t, api.ProjectBranches, func(ctx context.Context, r api.ProjectBranchesRequest) (api.ProjectBranchesResult, error) {
		active, err := store.HasActiveProject(ctx, r.Project)
		if err != nil {
			return api.ProjectBranchesResult{}, internalRPC(err)
		}
		if !active {
			return api.ProjectBranchesResult{}, errorRPC(-32004, "unavailable", "project is unavailable")
		}
		refs, next, err := git.ListRefsPage(ctx, r.Project, r.After, r.Limit)
		if err != nil {
			return api.ProjectBranchesResult{}, errorRPC(-32004, "unavailable", "Git refs are unavailable")
		}
		if refs == nil {
			refs = []plugin.GitRef{}
		}
		return api.ProjectBranchesResult{Project: r.Project, Refs: refs, Next: next}, nil
	})
}

func internalRPC(err error) *api.RPCError {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errorRPC(-32001, "cancelled", "request cancelled or timed out")
	}
	return errorRPC(-32603, "internal", "control state is unavailable")
}

func parseRequest(line []byte) (rpcRequest, *api.RPCError) {
	var request rpcRequest
	dec := json.NewDecoder(bytes.NewReader(line))
	tok, err := dec.Token()
	if err != nil {
		return request, errorRPC(-32700, "parse_error", "invalid JSON")
	}
	if tok != json.Delim('{') {
		return request, errorRPC(-32600, "invalid_request", "expected one JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return request, errorRPC(-32700, "parse_error", "invalid JSON")
		}
		key, ok := keyToken.(string)
		if !ok {
			return request, errorRPC(-32700, "parse_error", "invalid JSON key")
		}
		if _, duplicate := fields[key]; duplicate {
			return request, errorRPC(-32600, "invalid_request", "duplicate request field")
		}
		if key != "jsonrpc" && key != "method" && key != "params" && key != "id" {
			return request, errorRPC(-32600, "invalid_request", "unknown request field")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return request, errorRPC(-32700, "parse_error", "invalid JSON")
		}
		fields[key] = raw
	}
	if _, err := dec.Token(); err != nil {
		return request, errorRPC(-32700, "parse_error", "incomplete JSON")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return request, errorRPC(-32700, "parse_error", "multiple JSON values")
	}
	if raw, ok := fields["id"]; ok {
		request.HasID = true
		request.ID = raw
		if _, err := api.CanonicalID(request.ID); err != nil {
			request.ID = nil
			return request, errorRPC(-32600, "invalid_request", err.Error())
		}
	}
	var version string
	if err := json.Unmarshal(fields["jsonrpc"], &version); err != nil || version != "2.0" {
		return request, errorRPC(-32002, "unsupported_version", "jsonrpc must be 2.0")
	}
	if err := json.Unmarshal(fields["method"], &request.Method); err != nil || request.Method == "" || len(request.Method) > 128 {
		return request, errorRPC(-32600, "invalid_request", "method must be a bounded string")
	}
	request.Params = fields["params"]
	if len(request.Params) == 0 {
		return request, errorRPC(-32602, "invalid_params", "params are required")
	}
	return request, nil
}

func Serve(ctx context.Context, store *Store, stateDir string) error {
	return ServeWithHandler(ctx, stateDir, NewHandler(HandlerConfig{Store: store}))
}

func ServeWithHandler(ctx context.Context, stateDir string, handler Handler) error {
	path := filepath.Join(stateDir, "control.sock")
	if info, err := os.Lstat(path); err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || info.Mode()&os.ModeSocket == 0 || stat.Uid != uint32(os.Geteuid()) {
			return errors.New("control socket path is not an owned socket")
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var suffix [8]byte
	if _, err := randRead(suffix[:]); err != nil {
		return err
	}
	tempPath := filepath.Join(stateDir, fmt.Sprintf(".control-%x.sock", suffix))
	listener, err := net.Listen("unix", tempPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(tempPath)
	if err := os.Chmod(tempPath, 0600); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	defer os.Remove(path)
	go func() {
		<-ctx.Done()
		listener.Close()
	}()
	connections := make(chan struct{}, 64)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		select {
		case connections <- struct{}{}:
			go func() {
				defer func() { <-connections }()
				ServeConn(ctx, conn, handler)
			}()
		default:
			conn.Close()
		}
	}
}

var randRead = rand.Read

// ServeConn handles independent requests concurrently so a cancellation
// notification can interrupt a pending request on the same stream. The first
// request must be system.hello, which agrees the protocol for the connection;
// any other request before it is refused with hello_required. A panic while
// serving a request answers it internal and is logged with its stack; the
// connection and the daemon keep serving.
func ServeConn(parent context.Context, conn net.Conn, handler Handler) {
	peerPID, peerUID := connectionPeer(conn)
	owner := &Connection{peerPID: peerPID, peerUID: peerUID}
	defer owner.close()
	parent = context.WithValue(parent, connectionKey{}, owner)
	defer conn.Close()
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-parent.Done():
			conn.Close()
		case <-finished:
		}
	}()
	var writes sync.Mutex
	var active sync.Map // canonical JSON ID -> context.CancelFunc
	var pending sync.WaitGroup
	semaphore := make(chan struct{}, 16)
	write := func(id json.RawMessage, result any, rpcErr *api.RPCError) {
		writes.Lock()
		defer writes.Unlock()
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_ = json.NewEncoder(conn).Encode(rpcResponse{JSONRPC: "2.0", ID: id, Result: result, Error: rpcErr})
	}
	// agreed is set by a successful system.hello. Only the read loop, which
	// answers the hello inline, reads or writes it.
	agreed := false
	hello := func(request rpcRequest) (any, *api.RPCError) {
		if owner.dedicatedAttachment() {
			return nil, lifecycleRPC(ErrConflict)
		}
		var p api.HelloRequest
		if !isObject(request.Params) || strictjson.Decode(request.Params, &p) != nil || p.Validate() != nil {
			return nil, errorRPC(-32602, "invalid_params", "system.hello requires a protocol")
		}
		if p.Protocol != api.Protocol {
			return nil, &api.RPCError{Code: -32002, Kind: "unsupported_version", Message: "protocol is not supported",
				Data: &api.ErrorData{Reason: api.ReasonProtocol, Supported: []string{api.Protocol}}}
		}
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		defer cancel()
		result, rpcErr := envelope(handler(ctx, request.Method, request.Params))
		agreed = agreed || rpcErr == nil
		return result, rpcErr
	}
	serveFrame := func(line []byte) {
		var request rpcRequest
		notification := false
		defer func() {
			if value := recover(); value != nil {
				rpcErr := panicRPC("control", request.Method, value)
				if !notification {
					write(replyID(request.ID), nil, rpcErr)
				}
			}
		}()
		request, parseErr := parseRequest(line)
		if parseErr != nil {
			write(replyID(request.ID), nil, parseErr)
			return
		}
		notification = !request.HasID
		var key string
		if request.HasID {
			key, _ = api.CanonicalID(request.ID)
			if _, duplicated := active.Load(key); duplicated {
				write(json.RawMessage("null"), nil, errorRPC(-32600, "invalid_request", "duplicate in-flight id"))
				return
			}
		}
		if request.Method == api.SystemHello.Name {
			if request.HasID {
				// Answered before the next line is read, so a client may send its
				// first request together with the hello.
				result, rpcErr := hello(request)
				write(request.ID, result, rpcErr)
			}
			return
		}
		if !agreed {
			if request.HasID {
				write(request.ID, nil, &api.RPCError{Code: -32600, Kind: "invalid_request", Message: "send system.hello first",
					Data: &api.ErrorData{Reason: api.ReasonHelloRequired}})
			}
			return
		}
		if request.Method == api.RPCCancel.Name {
			var p api.CancelRequest
			if !isObject(request.Params) || strictjson.Decode(request.Params, &p) != nil || p.Validate() != nil {
				if request.HasID {
					write(request.ID, nil, errorRPC(-32602, "invalid_params", "rpc.cancel requires the id of a request"))
				}
				return
			}
			cancelKey, _ := api.CanonicalID(p.ID)
			if cancel, ok := active.Load(cancelKey); ok {
				cancel.(context.CancelFunc)()
			}
			if request.HasID {
				write(request.ID, api.CancelResult{CancelRequested: true}, nil)
			}
			return
		}
		if !request.HasID {
			// Unknown notifications have no reply and no side effects.
			return
		}
		select {
		case semaphore <- struct{}{}:
		default:
			write(request.ID, nil, errorRPC(-32003, "busy", "too many concurrent requests"))
			return
		}
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		if _, loaded := active.LoadOrStore(key, cancel); loaded {
			cancel()
			<-semaphore
			write(json.RawMessage("null"), nil, errorRPC(-32600, "invalid_request", "duplicate in-flight id"))
			return
		}
		pending.Add(1)
		go func(request rpcRequest) {
			defer pending.Done()
			defer func() { active.Delete(key); cancel(); <-semaphore }()
			defer func() {
				if value := recover(); value != nil {
					write(request.ID, nil, panicRPC("control", request.Method, value))
				}
			}()
			var result any
			var rpcErr *api.RPCError
			if owner.dedicatedAttachment() && request.Method != "attachment.confirm" && request.Method != "attachment.ping" {
				rpcErr = lifecycleRPC(ErrConflict)
			} else {
				result, rpcErr = handler(ctx, request.Method, request.Params)
			}
			result, rpcErr = envelope(result, rpcErr)
			if ctx.Err() != nil {
				result, rpcErr = nil, internalRPC(ctx.Err())
			}
			write(request.ID, result, rpcErr)
		}(request)
	}
	reader := bufio.NewReaderSize(conn, api.MaxFrameBytes+1)
	for {
		conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
		line, err := api.ReadFrame(reader)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				write(json.RawMessage("null"), nil, errorRPC(-32700, "parse_error", err.Error()))
			}
			break
		}
		serveFrame(line)
	}
	active.Range(func(_, value any) bool { value.(context.CancelFunc)(); return true })
	conn.Close()
	pending.Wait()
}

// envelope keeps exactly one of a handler's result and error.
func envelope(result any, rpcErr *api.RPCError) (any, *api.RPCError) {
	if result != nil && rpcErr != nil {
		return nil, errorRPC(-32603, "internal", "method returned conflicting result and error")
	}
	if result == nil && rpcErr == nil {
		return nil, errorRPC(-32603, "internal", "method returned no result")
	}
	return result, rpcErr
}

// replyID is the id a response carries: the request's, or null when the
// request has none or its id is invalid.
func replyID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

// panicRPC logs a panic recovered while serving a request of server and
// returns the error that request answers.
func panicRPC(server, method string, value any) *api.RPCError {
	logPanic(fmt.Sprintf("%s request %q", server, method), value)
	return errorRPC(-32603, "internal", "request failed unexpectedly")
}

// logPanic logs a recovered panic with the stack of the goroutine that
// panicked. Call it from the deferred function that recovered.
func logPanic(what string, value any) {
	log.Printf("%s panicked: %v\n%s", what, value, debug.Stack())
}
