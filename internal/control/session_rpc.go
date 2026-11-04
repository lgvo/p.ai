package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/strictjson"
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
func ServeSessionConn(ctx context.Context, conn net.Conn, store *Store, uuid string, onCommitted func(context.Context, api.UnattendedCondition) error) {
	defer conn.Close()
	if store != nil {
		// A line read before the session was removed is budgeted after the
		// removal released the session's state.
		defer store.forgetIfGone(ctx, uuid)
	}
	reader := bufio.NewReaderSize(conn, api.MaxFrameBytes+1)
	for {
		conn.SetDeadline(time.Now().Add(2 * time.Minute))
		line, err := reader.ReadSlice('\n')
		if err != nil || len(line) > api.MaxFrameBytes {
			if len(line) != 0 {
				_, _ = conn.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32700,\"message\":\"parse_error\"}}\n"))
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
			reply, _ = SessionReplyWithCommit(ctx, store, uuid, line, func(changed api.UnattendedCondition) {
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
func SessionReply(ctx context.Context, store *Store, uuid string, line []byte) ([]byte, *api.UnattendedCondition) {
	return SessionReplyWithCommit(ctx, store, uuid, line, nil)
}

// SessionReplyWithCommit accepts a callback that only enqueues a reduced event.
// It runs under the status reducer lock and must not inspect SQLite or invoke a
// handler, so concurrent reports and attachment clears retain commit order.
// A panic while handling the message answers internal and is logged with its
// stack, so the session's connection keeps serving.
func SessionReplyWithCommit(ctx context.Context, store *Store, uuid string, line []byte, onCommitted func(api.UnattendedCondition)) (encoded []byte, committed *api.UnattendedCondition) {
	if !utf8.Valid(line) {
		return []byte("{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32700,\"message\":\"parse_error\"}}\n"), nil
	}
	var req rpcRequest
	notification := false
	defer func() {
		if value := recover(); value != nil {
			rpcErr := panicRPC("session "+uuid, req.Method, req.ID, value)
			encoded, committed = nil, nil
			if notification {
				return
			}
			if data, err := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: replyID(req.ID), Error: rpcErr}); err == nil {
				encoded = append(data, '\n')
			}
		}
	}()
	req, parseErr := parseRequest(line)
	notification = !req.HasID && parseErr == nil
	reply := rpcResponse{JSONRPC: "2.0", ID: replyID(req.ID)}
	if parseErr != nil {
		reply.Error = parseErr
	} else {
		ctx = context.WithValue(ctx, requestIDKey{}, string(req.ID))
		var session Session
		var err error
		session, err = store.GetSession(ctx, uuid)
		switch {
		case err != nil && !errors.Is(err, ErrNotFound):
			reply.Error = answerError(ctx, err)
			logAnswer(ctx, "session "+uuid+" "+req.Method, reply.Error, err)
		case err != nil || session.Registry != "established":
			reply.Error = api.NewError(api.KindNotFound, api.ReasonSession)
			reply.Error.Data.Target = api.TargetSession(uuid)
		default:
			switch req.Method {
			case api.SessionIdentity.Name, api.SessionCapabilities.Name:
				var p api.SessionRPCRequest
				if err := strictjson.Decode(req.Params, &p); err != nil {
					reply.Error = api.InvalidParam(paramAtFault(req.Params, &p, err))
				} else if p.Validate() != nil {
					reply.Error = api.InvalidParam("v")
				} else if req.Method == api.SessionIdentity.Name {
					reply.Result = api.SessionIdentityResult{V: api.SessionRPCVersion, UUID: session.UUID, Branch: session.Branch}
				} else {
					policy, policyErr := ParseStoredProjectPolicy(session.Policy)
					if policyErr != nil {
						reply.Error = api.NewError(api.KindInternal, api.ReasonStore)
						logAnswer(ctx, "session "+uuid+" "+req.Method, reply.Error, policyErr)
					} else {
						mounts := make([]api.SessionMount, 0, len(policy.FilesystemMounts))
						for _, grant := range policy.FilesystemMounts {
							mounts = append(mounts, api.SessionMount{Name: grant.Name, Target: "/mnt/p/" + grant.Name, Type: grant.Type, Access: grant.Access, Executable: grant.Executable})
						}
						reply.Result = api.SessionCapabilitiesResult{V: api.SessionRPCVersion, UUID: session.UUID, Branch: session.Branch, PolicySHA256: session.PolicySHA256,
							EffectiveCapabilities: api.EffectiveCapabilities{Network: policy.Network, FilesystemMounts: mounts, SourceGit: true, StatusReport: true}}
					}
				}
			case api.StatusReportMethod.Name:
				if req.HasID {
					reply.Error = api.NewError(api.KindInvalidRequest, api.ReasonEnvelope)
					break
				}
				var report api.StatusReport
				if strictjson.Decode(req.Params, &report) != nil || report.Validate() != nil {
					return nil, nil
				}
				value, changed, e := store.RecordStatusWithCommit(ctx, uuid, report, onCommitted)
				if e != nil || !changed {
					return nil, nil
				}
				return nil, &value
			default:
				reply.Error = api.NewError(api.KindMethodNotFound, "")
			}
		}
	}
	if notification {
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
		reply.Error = api.NewError(api.KindNotFound, api.ReasonSession)
	}
	data, err := json.Marshal(reply)
	if err != nil {
		return nil
	}
	return append(data, '\n')
}
