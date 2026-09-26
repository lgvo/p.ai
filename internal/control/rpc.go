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
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lgvo/p.ai/internal/plugin"
)

const (
	ProtocolVersion = "p.control/v1"
	MaxFrameBytes   = 65536
)

// BuildVersion may be set at link time by the package recipe.
var BuildVersion = "development"

type RPCError struct {
	Code    int    `json:"code"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

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
	Error   *RPCError       `json:"error,omitempty"`
}

type Handler func(context.Context, string, json.RawMessage) (any, *RPCError)

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

func errorRPC(code int, kind, message string) *RPCError {
	return &RPCError{Code: code, Kind: kind, Message: message}
}

// StateHandler exposes only facts the current binary can establish. Lifecycle
// method names are deliberately unavailable until their authority is wired.
func StateHandler(store *Store) Handler {
	return StateHandlerWithGit(store, nil, nil)
}

func StateHandlerWithGit(store *Store, git GitReader, info *GitInfo) Handler {
	return func(ctx context.Context, method string, params json.RawMessage) (any, *RPCError) {
		if method == "project.list" || method == "project.branches" {
			if git == nil || info == nil {
				return nil, errorRPC(-32004, "unavailable", "Git capability is not configured")
			}
			if method == "project.list" {
				var p struct {
					V     int    `json:"v"`
					After string `json:"after"`
					Limit int    `json:"limit"`
				}
				if strictDecode(params, &p) != nil || p.V != 1 || p.Limit < 1 || p.Limit > 100 || p.After != "" && !validProject(p.After) {
					return nil, errorRPC(-32602, "invalid_params", "project.list requires v=1 and limit 1..100")
				}
				projects, next, err := store.ListProjects(ctx, p.After, p.Limit)
				if err != nil {
					return nil, internalRPC(err)
				}
				if projects == nil {
					projects = []ProjectSummary{}
				}
				return map[string]any{"v": 1, "projects": projects, "next": next}, nil
			}
			var p struct {
				V       int    `json:"v"`
				Project string `json:"project"`
				After   string `json:"after"`
				Limit   int    `json:"limit"`
			}
			if strictDecode(params, &p) != nil || p.V != 1 || !validProject(p.Project) || p.Limit < 1 || p.Limit > 8 || p.After != "" && (!strings.HasPrefix(p.After, "refs/heads/") || !validBranch(strings.TrimPrefix(p.After, "refs/heads/"))) {
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
		}
		var p struct {
			V int `json:"v"`
		}
		if err := strictDecode(params, &p); err != nil || p.V != 1 {
			return nil, errorRPC(-32602, "invalid_params", "params must be {\"v\":1}")
		}
		switch method {
		case "system.hello":
			id, err := store.InstanceID(ctx)
			if err != nil {
				return nil, internalRPC(err)
			}
			return map[string]any{"v": 1, "protocol": ProtocolVersion, "build_version": BuildVersion, "instance_id": id}, nil
		case "system.health":
			if err := store.Ping(ctx); err != nil {
				return nil, internalRPC(err)
			}
			return map[string]any{"v": 1, "control_state": "ready"}, nil
		case "system.capabilities":
			available := []string{"system.hello", "system.health", "system.capabilities", "system.inspect"}
			result := map[string]any{"v": 1, "available": available, "lifecycle": "unavailable"}
			if git != nil && info != nil {
				result["available"] = append(available, "project.list", "project.branches")
				result["git"] = info
			}
			return result, nil
		case "system.inspect":
			projects, sessions, operations, err := store.Count(ctx)
			if err != nil {
				return nil, internalRPC(err)
			}
			result := map[string]any{"v": 1, "projects": projects, "sessions": sessions, "operations": operations}
			if git != nil && info != nil {
				result["git"] = info
			}
			return result, nil
		default:
			for _, prefix := range []string{"project.", "session.", "origin.", "runtime.", "status."} {
				if strings.HasPrefix(method, prefix) {
					return nil, errorRPC(-32004, "unavailable", "capability is not implemented")
				}
			}
			return nil, errorRPC(-32601, "method_not_found", "method is unknown")
		}
	}
}

func internalRPC(err error) *RPCError {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errorRPC(-32001, "cancelled", "request cancelled or timed out")
	}
	return errorRPC(-32603, "internal", "control state is unavailable")
}

func strictDecode(data []byte, dest any) error {
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	if err := rejectInexactStructFields(data, dest); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("multiple JSON values")
	}
	return nil
}

// encoding/json deliberately accepts case-insensitive struct field names.
// RPC schemas are closed and versioned, so require the exact JSON tag at
// every typed struct boundary. Maps, raw messages and custom decoders retain
// their own key semantics.
func rejectInexactStructFields(data []byte, dest any) error {
	t := reflect.TypeOf(dest)
	if t == nil || t.Kind() != reflect.Pointer || t.Elem().Kind() == reflect.Invalid {
		return errors.New("JSON destination must be a pointer")
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	return exactStructFields(value, t.Elem())
}

var jsonUnmarshalerType = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()

func exactStructFields(value any, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Implements(jsonUnmarshalerType) || reflect.PointerTo(t).Implements(jsonUnmarshalerType) {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		} // the normal decoder reports type mismatches
		fields := make(map[string]reflect.Type)
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" {
				continue
			}
			tag := strings.Split(field.Tag.Get("json"), ",")[0]
			if tag == "-" {
				continue
			}
			if tag == "" && field.Anonymous {
				for name, nested := range exactFieldTypes(field.Type) {
					fields[name] = nested
				}
				continue
			}
			if tag == "" {
				tag = field.Name
			}
			fields[tag] = field.Type
		}
		for name, child := range object {
			fieldType, ok := fields[name]
			if !ok {
				return errors.New("unknown or inexact JSON field")
			}
			if err := exactStructFields(child, fieldType); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if values, ok := value.([]any); ok {
			for _, child := range values {
				if err := exactStructFields(child, t.Elem()); err != nil {
					return err
				}
			}
		}
	case reflect.Map:
		if values, ok := value.(map[string]any); ok {
			for _, child := range values {
				if err := exactStructFields(child, t.Elem()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func exactFieldTypes(t reflect.Type) map[string]reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	fields := make(map[string]reflect.Type)
	if t.Kind() != reflect.Struct {
		return fields
	}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue
		}
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		if tag == "-" {
			continue
		}
		if tag == "" && field.Anonymous {
			for name, nested := range exactFieldTypes(field.Type) {
				fields[name] = nested
			}
			continue
		}
		if tag == "" {
			tag = field.Name
		}
		fields[tag] = field.Type
	}
	return fields
}

// rejectDuplicateKeys applies at every object depth, including method params
// and trusted policy. JSON decoder's normal last-value-wins behavior is unsafe
// at authority boundaries.
// RejectDuplicateKeys rejects ambiguous JSON keys at any nesting depth.
// Native proof responses use the same closed JSON boundary as public RPC.
func RejectDuplicateKeys(data []byte) error { return rejectDuplicateKeys(data) }

func rejectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]bool)
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return errors.New("duplicate or invalid JSON object key")
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err := dec.Token()
			return err
		case '[':
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err := dec.Token()
			return err
		default:
			return errors.New("invalid JSON delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("multiple JSON values")
	}
	return nil
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

func parseRequest(line []byte) (rpcRequest, *RPCError) {
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

func readFrame(reader *bufio.Reader) ([]byte, error) {
	line, err := reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) || len(line) > MaxFrameBytes {
		return nil, errors.New("frame exceeds 64 KiB")
	}
	if err != nil {
		if errors.Is(err, io.EOF) && len(line) > 0 {
			return nil, errors.New("unterminated frame")
		}
		return nil, err
	}
	return bytes.TrimSuffix(line, []byte{'\n'}), nil
}

func Serve(ctx context.Context, store *Store, stateDir string) error {
	return ServeWithHandler(ctx, stateDir, StateHandler(store))
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
// notification can interrupt a pending request on the same stream.
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
	write := func(id json.RawMessage, result any, rpcErr *RPCError) {
		writes.Lock()
		defer writes.Unlock()
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_ = json.NewEncoder(conn).Encode(rpcResponse{JSONRPC: "2.0", ID: id, Result: result, Error: rpcErr})
	}
	reader := bufio.NewReaderSize(conn, MaxFrameBytes+1)
	for {
		conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
		line, err := readFrame(reader)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				write(json.RawMessage("null"), nil, errorRPC(-32700, "parse_error", err.Error()))
			}
			break
		}
		request, parseErr := parseRequest(line)
		if parseErr != nil {
			id := request.ID
			if len(id) == 0 {
				id = json.RawMessage("null")
			}
			write(id, nil, parseErr)
			continue
		}
		if request.HasID {
			requestKey, _ := canonicalID(request.ID)
			if _, duplicated := active.Load(requestKey); duplicated {
				write(json.RawMessage("null"), nil, errorRPC(-32600, "invalid_request", "duplicate in-flight id"))
				continue
			}
		}
		if request.Method == "rpc.cancel" {
			var p struct {
				V  int             `json:"v"`
				ID json.RawMessage `json:"id"`
			}
			decodeErr := strictDecode(request.Params, &p)
			cancelKey, idErr := canonicalID(p.ID)
			if decodeErr != nil || p.V != 1 || idErr != nil {
				if request.HasID {
					write(request.ID, nil, errorRPC(-32602, "invalid_params", "cancel requires v and id"))
				}
				continue
			}
			if cancel, ok := active.Load(cancelKey); ok {
				cancel.(context.CancelFunc)()
			}
			if request.HasID {
				write(request.ID, map[string]any{"v": 1, "cancel_requested": true}, nil)
			}
			continue
		}
		if !request.HasID {
			// Unknown notifications have no reply and no side effects.
			continue
		}
		select {
		case semaphore <- struct{}{}:
		default:
			write(request.ID, nil, errorRPC(-32003, "busy", "too many concurrent requests"))
			continue
		}
		key, _ := canonicalID(request.ID)
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		if _, loaded := active.LoadOrStore(key, cancel); loaded {
			cancel()
			<-semaphore
			write(json.RawMessage("null"), nil, errorRPC(-32600, "invalid_request", "duplicate in-flight id"))
			continue
		}
		pending.Add(1)
		go func(request rpcRequest) {
			defer pending.Done()
			defer func() { active.Delete(key); cancel(); <-semaphore }()
			var result any
			var rpcErr *RPCError
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
	active.Range(func(_, value any) bool { value.(context.CancelFunc)(); return true })
	conn.Close()
	pending.Wait()
}

// Call sends one request and returns the complete response envelope.
func Call(ctx context.Context, socket, method string, params json.RawMessage) (json.RawMessage, error) {
	if len(method) == 0 || len(method) > 128 || len(params) > MaxFrameBytes/2 || !json.Valid(params) {
		return nil, ErrInvalid
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	request := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}{"2.0", 1, method, params}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return nil, err
	}
	line, err := readFrame(bufio.NewReaderSize(conn, MaxFrameBytes+1))
	if err != nil {
		return nil, err
	}
	var response struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *RPCError       `json:"error"`
	}
	if err := strictDecode(line, &response); err != nil || response.JSONRPC != "2.0" || response.ID != 1 {
		return nil, errors.New("invalid daemon response")
	}
	if (len(response.Result) == 0) == (response.Error == nil) {
		return nil, errors.New("daemon response must contain exactly one result or error")
	}
	return json.RawMessage(line), nil
}
