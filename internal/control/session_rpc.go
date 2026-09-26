package control

import (
	"bufio"
	"context"
	"encoding/json"
	"log"
	"net"
	"sync"
	"time"
	"unicode/utf8"
)

var statusEventDiagnostic struct {
	sync.Mutex
	last time.Time
}

func logStatusEventFailure() {
	statusEventDiagnostic.Lock()
	defer statusEventDiagnostic.Unlock()
	if time.Since(statusEventDiagnostic.last) >= time.Minute {
		log.Print("session status event delivery failed")
		statusEventDiagnostic.last = time.Now()
	}
}

// ServeSessionConn processes the private socket stream under one bound UUID.
// Delivery errors are diagnostic; the callback runs only after SQLite commits.
func ServeSessionConn(ctx context.Context, conn net.Conn, store *Store, uuid string, onCommitted func(context.Context, UnattendedCondition) error) {
	defer conn.Close()
	reader := bufio.NewReaderSize(conn, MaxFrameBytes+1)
	for {
		conn.SetDeadline(time.Now().Add(2 * time.Minute))
		line, err := reader.ReadSlice('\n')
		if err != nil || len(line) > MaxFrameBytes {
			if len(line) != 0 {
				_, _ = conn.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32700,\"kind\":\"parse_error\",\"message\":\"invalid session frame\"}}\n"))
			}
			return
		}
		var reply []byte
		if store == nil {
			reply = SessionUnavailableReply(line)
		} else {
			if !store.AllowSessionAttempt(uuid) {
				return
			}
			eventFailed := false
			reply, _ = SessionReplyWithCommit(ctx, store, uuid, line, func(changed UnattendedCondition) {
				if onCommitted != nil && onCommitted(ctx, changed) != nil {
					eventFailed = true
				}
			})
			if eventFailed {
				logStatusEventFailure()
			}
		}
		if len(reply) != 0 {
			if _, err = conn.Write(reply); err != nil {
				return
			}
		}
	}
}

// SessionReply handles one message for the UUID bound to the accepted socket.
// It never accepts a caller-supplied target UUID. A committed status value is
// returned separately for post-commit event delivery by the daemon.
func SessionReply(ctx context.Context, store *Store, uuid string, line []byte) ([]byte, *UnattendedCondition) {
	return SessionReplyWithCommit(ctx, store, uuid, line, nil)
}

// SessionReplyWithCommit accepts a callback that only enqueues a reduced event.
// It runs under the status reducer lock and must not inspect SQLite or invoke a
// handler, so concurrent reports and attachment clears retain commit order.
func SessionReplyWithCommit(ctx context.Context, store *Store, uuid string, line []byte, onCommitted func(UnattendedCondition)) ([]byte, *UnattendedCondition) {
	if !utf8.Valid(line) {
		return []byte("{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32700,\"kind\":\"parse_error\",\"message\":\"invalid UTF-8\"}}\n"), nil
	}
	req, parseErr := parseRequest(line)
	reply := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if len(reply.ID) == 0 {
		reply.ID = json.RawMessage("null")
	}
	if parseErr != nil {
		reply.Error = parseErr
	} else {
		var session Session
		var err error
		session, err = store.GetSession(ctx, uuid)
		if err != nil || session.Registry != "established" {
			reply.Error = errorRPC(-32004, "unavailable", "session is unavailable")
		} else {
			switch req.Method {
			case "session.identity", "session.capabilities":
				var p struct {
					V int `json:"v"`
				}
				if strictDecode(req.Params, &p) != nil || p.V != 1 {
					reply.Error = errorRPC(-32602, "invalid_params", "params must be {\"v\":1}")
				} else if req.Method == "session.identity" {
					reply.Result = map[string]any{"v": 1, "uuid": session.UUID, "branch": session.Branch}
				} else {
					policy, policyErr := ParseStoredProjectPolicy(session.Policy)
					if policyErr != nil {
						reply.Error = errorRPC(-32603, "internal", "session policy is unavailable")
					} else {
						mounts := make([]map[string]any, 0, len(policy.FilesystemMounts))
						for _, grant := range policy.FilesystemMounts {
							mounts = append(mounts, map[string]any{"name": grant.Name, "target": "/mnt/p/" + grant.Name, "type": grant.Type, "access": grant.Access, "executable": grant.Executable})
						}
						reply.Result = map[string]any{"v": 1, "uuid": session.UUID, "branch": session.Branch, "policy_sha256": session.PolicySHA256, "effective_capabilities": map[string]any{"network": policy.Network, "filesystem_mounts": mounts, "source_git": true, "status_report": true}}
					}
				}
			case "status.report":
				if req.HasID {
					reply.Error = errorRPC(-32600, "invalid_request", "status.report must be a notification")
					break
				}
				var report StatusReport
				if strictDecode(req.Params, &report) != nil || !report.Valid() {
					return nil, nil
				}
				value, changed, e := store.RecordStatusWithCommit(ctx, uuid, report, onCommitted)
				if e != nil || !changed {
					return nil, nil
				}
				return nil, &value
			default:
				reply.Error = errorRPC(-32601, "method_not_found", "method is not available on session RPC")
			}
		}
	}
	if !req.HasID && parseErr == nil {
		return nil, nil
	}
	data, err := json.Marshal(reply)
	if err != nil {
		return nil, nil
	}
	return append(data, '\n'), nil
}

// SessionUnavailableReply serves only a deliberately unbound test endpoint.
func SessionUnavailableReply(line []byte) []byte {
	req, parseErr := parseRequest(line)
	if !req.HasID && parseErr == nil {
		return nil
	}
	reply := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if len(reply.ID) == 0 {
		reply.ID = json.RawMessage("null")
	}
	if parseErr != nil {
		reply.Error = parseErr
	} else {
		reply.Error = errorRPC(-32004, "unavailable", "session RPC capability is unavailable")
	}
	data, err := json.Marshal(reply)
	if err != nil {
		return nil
	}
	return append(data, '\n')
}
