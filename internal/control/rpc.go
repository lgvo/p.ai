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
	"strconv"
	"strings"
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

type GitInfo struct {
	Endpoint        string `json:"endpoint"`
	URLTemplate     string `json:"url_template"`
	HostPublicKey   string `json:"host_public_key"`
	KnownHosts      string `json:"known_hosts"`
	ClientPublicKey string `json:"client_public_key"`
	ClientKeyPath   string `json:"client_key_path"`
	SourcePluginID  string `json:"source_plugin_id"`
	SourceSHA256    string `json:"source_sha256"`
}

func errorRPC(code int, kind, message string) *api.RPCError {
	return &api.RPCError{Code: code, Kind: kind, Message: message}
}

// Handler answers one control-API request; method tells table entries that
// share a handler apart.
type Handler func(context.Context, string, json.RawMessage) (any, *api.RPCError)

// HandlerConfig names what trusted configuration enabled. Git, the lifecycle
// and origins stay nil until configured; each adds its methods to the surface.
type HandlerConfig struct {
	Store     *Store
	Git       GitReader
	GitInfo   *GitInfo
	Lifecycle LifecycleAPI
	Origins   *OriginAPI
}

// methodTable is the control-API surface of one configured daemon. Dispatch and
// system.capabilities both read it, in the order methods were added.
type methodTable struct {
	handlers   map[string]Handler
	advertised []string
}

// add dispatches name to h and advertises it.
func (t *methodTable) add(name string, h Handler) {
	t.handlers[name] = h
	t.advertised = append(t.advertised, name)
}

// addHidden dispatches name to h without advertising it.
func (t *methodTable) addHidden(name string, h Handler) { t.handlers[name] = h }

// unavailable dispatches names whose capability is configured but whose
// implementation is absent; they answer that the record is unavailable.
func (t *methodTable) unavailable(names ...string) {
	for _, name := range names {
		t.handlers[name] = func(context.Context, string, json.RawMessage) (any, *api.RPCError) {
			return nil, lifecycleRPC(ErrNotFound)
		}
	}
}

// NewHandler builds the control-API surface for one configuration. It exposes
// only facts the current binary can establish: a method is dispatched only
// when the capability that owns it is configured.
func NewHandler(c HandlerConfig) Handler {
	t := &methodTable{handlers: map[string]Handler{}}
	git := c.Git != nil && c.GitInfo != nil
	addSystemMethods(t, c, git)
	if git {
		addProjectReads(t, c.Store, c.Git)
	}
	if c.Lifecycle != nil {
		addLifecycleMethods(t, c.Store, c.Lifecycle)
	}
	if c.Origins != nil {
		addOriginMethods(t, *c.Origins)
	}
	return func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
		if h, ok := t.handlers[method]; ok {
			return h(ctx, method, params)
		}
		return unconfiguredMethod(method, params)
	}
}

// exactVersionParams accepts only {"v":1}.
func exactVersionParams(params json.RawMessage) *api.RPCError {
	var p struct {
		V int `json:"v"`
	}
	if err := strictjson.Decode(params, &p); err != nil || p.V != 1 {
		return errorRPC(-32602, "invalid_params", "params must be {\"v\":1}")
	}
	return nil
}

// systemMethod answers after checking that params are exactly {"v":1}.
func systemMethod(answer func(context.Context) (any, *api.RPCError)) Handler {
	return func(ctx context.Context, _ string, params json.RawMessage) (any, *api.RPCError) {
		if rpcErr := exactVersionParams(params); rpcErr != nil {
			return nil, rpcErr
		}
		return answer(ctx)
	}
}

func addSystemMethods(t *methodTable, c HandlerConfig, git bool) {
	store := c.Store
	t.add("system.hello", systemMethod(func(ctx context.Context) (any, *api.RPCError) {
		id, err := store.InstanceID(ctx)
		if err != nil {
			return nil, internalRPC(err)
		}
		return map[string]any{"v": 1, "protocol": api.ProtocolVersion, "build_version": BuildVersion, "instance_id": id}, nil
	}))
	t.add("system.health", systemMethod(func(ctx context.Context) (any, *api.RPCError) {
		if err := store.Ping(ctx); err != nil {
			return nil, internalRPC(err)
		}
		return map[string]any{"v": 1, "control_state": "ready"}, nil
	}))
	t.add("system.capabilities", systemMethod(func(context.Context) (any, *api.RPCError) {
		result := map[string]any{"v": 1, "available": append([]string(nil), t.advertised...), "lifecycle": "unavailable"}
		if c.Lifecycle != nil {
			result["lifecycle"] = "partial"
		}
		if git {
			result["git"] = c.GitInfo
		}
		return result, nil
	}))
	t.add("system.inspect", systemMethod(func(ctx context.Context) (any, *api.RPCError) {
		projects, sessions, operations, err := store.Count(ctx)
		if err != nil {
			return nil, internalRPC(err)
		}
		result := map[string]any{"v": 1, "projects": projects, "sessions": sessions, "operations": operations}
		if git {
			result["git"] = c.GitInfo
		}
		return result, nil
	}))
}

func addProjectReads(t *methodTable, store *Store, git GitReader) {
	t.add("project.list", func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
		var p struct {
			V     int    `json:"v"`
			After string `json:"after"`
			Limit int    `json:"limit"`
		}
		if strictjson.Decode(params, &p) != nil || p.V != 1 || p.Limit < 1 || p.Limit > 100 || p.After != "" && !validProject(p.After) {
			return nil, errorRPC(-32602, "invalid_params", "project.list requires v=1 and limit 1..100")
		}
		projects, next, err := store.ListProjects(ctx, p.After, p.Limit)
		if err != nil {
			return nil, internalRPC(err)
		}
		if projects == nil {
			projects = []api.ProjectSummary{}
		}
		return map[string]any{"v": 1, "projects": projects, "next": next}, nil
	})
	t.add("project.branches", func(ctx context.Context, method string, params json.RawMessage) (any, *api.RPCError) {
		var p struct {
			V       int    `json:"v"`
			Project string `json:"project"`
			After   string `json:"after"`
			Limit   int    `json:"limit"`
		}
		if strictjson.Decode(params, &p) != nil || p.V != 1 || !validProject(p.Project) || p.Limit < 1 || p.Limit > 8 || p.After != "" && (!strings.HasPrefix(p.After, "refs/heads/") || !validBranch(strings.TrimPrefix(p.After, "refs/heads/"))) {
			return nil, errorRPC(-32602, "invalid_params", "project.branches requires v=1, project, and limit 1..8")
		}
		active, err := store.HasActiveProject(ctx, p.Project)
		if err != nil {
			return nil, internalRPC(err)
		}
		if !active {
			return nil, errorRPC(-32004, "unavailable", "project is unavailable")
		}
		refs, next, err := git.ListRefsPage(ctx, p.Project, p.After, p.Limit)
		if err != nil {
			return nil, errorRPC(-32004, "unavailable", "Git refs are unavailable")
		}
		if refs == nil {
			refs = []plugin.GitRef{}
		}
		return map[string]any{"v": 1, "project": p.Project, "refs": refs, "next": next}, nil
	})
}

// unconfiguredMethod answers a method that no configured capability provides.
func unconfiguredMethod(method string, params json.RawMessage) (any, *api.RPCError) {
	if method == "project.list" || method == "project.branches" {
		return nil, errorRPC(-32004, "unavailable", "Git capability is not configured")
	}
	if rpcErr := exactVersionParams(params); rpcErr != nil {
		return nil, rpcErr
	}
	for _, prefix := range []string{"project.", "session.", "origin.", "runtime.", "status."} {
		if strings.HasPrefix(method, prefix) {
			return nil, errorRPC(-32004, "unavailable", "capability is not implemented")
		}
	}
	return nil, errorRPC(-32601, "method_not_found", "method is unknown")
}

func internalRPC(err error) *api.RPCError {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errorRPC(-32001, "cancelled", "request cancelled or timed out")
	}
	return errorRPC(-32603, "internal", "control state is unavailable")
}

func canonicalID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", errors.New("missing id")
	}
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return "", err
	}
	switch v := value.(type) {
	case string:
		if len(v) > 128 {
			return "", errors.New("id is too long")
		}
		encoded, _ := json.Marshal(v)
		return string(encoded), nil
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return "", errors.New("id must be an integer")
		}
		return strconv.FormatInt(n, 10), nil
	default:
		return "", errors.New("id must be a string or integer")
	}
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
		if _, err := canonicalID(request.ID); err != nil {
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
		return request, errorRPC(-32602, "invalid_params", "params with v=1 are required")
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
// notification can interrupt a pending request on the same stream. A panic
// while serving a request answers it internal and is logged with its stack;
// the connection and the daemon keep serving.
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
			key, _ = canonicalID(request.ID)
			if _, duplicated := active.Load(key); duplicated {
				write(json.RawMessage("null"), nil, errorRPC(-32600, "invalid_request", "duplicate in-flight id"))
				return
			}
		}
		if request.Method == "rpc.cancel" {
			var p struct {
				V  int             `json:"v"`
				ID json.RawMessage `json:"id"`
			}
			decodeErr := strictjson.Decode(request.Params, &p)
			cancelKey, idErr := canonicalID(p.ID)
			if decodeErr != nil || p.V != 1 || idErr != nil {
				if request.HasID {
					write(request.ID, nil, errorRPC(-32602, "invalid_params", "cancel requires v and id"))
				}
				return
			}
			if cancel, ok := active.Load(cancelKey); ok {
				cancel.(context.CancelFunc)()
			}
			if request.HasID {
				write(request.ID, map[string]any{"v": 1, "cancel_requested": true}, nil)
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
			if result != nil && rpcErr != nil {
				result, rpcErr = nil, errorRPC(-32603, "internal", "method returned conflicting result and error")
			} else if result == nil && rpcErr == nil {
				rpcErr = errorRPC(-32603, "internal", "method returned no result")
			}
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
