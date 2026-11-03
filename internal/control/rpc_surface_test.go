package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

// These tests state what the control-API handler answers, built the way the
// daemon builds it: the Git base handler, the lifecycle layer and the origin
// layer. They pin method availability, the configured capabilities, parameter
// checks, routing to the lifecycle and the error mapping.

const (
	surfaceUUID  = "11111111-2222-3333-4444-555555555555"
	surfaceUUID2 = "66666666-7777-8888-9999-aaaaaaaaaaaa"
	surfaceOID   = "0123456789abcdef0123456789abcdef01234567"
	surfaceToken = "0123456789abcdef0123456789abcdef"
	surfaceUnit  = "p-project-api.service"
)

type surfaceCalls struct {
	calls []string
	err   error
}

func (c *surfaceCalls) record(format string, args ...any) error {
	c.calls = append(c.calls, fmt.Sprintf(format, args...))
	return c.err
}

// coreLife implements only the required lifecycle methods.
type coreLife struct{ *surfaceCalls }

func (l coreLife) CreateProject(_ context.Context, r BlankProjectRequest) (api.Operation, error) {
	return api.Operation{ID: "created-project"}, l.record("CreateProject %+v", r)
}
func (l coreLife) CreateSession(_ context.Context, r ReserveSessionRequest) (api.Operation, error) {
	return api.Operation{ID: "created-session"}, l.record("CreateSession %+v", r)
}
func (l coreLife) Retry(_ context.Context, id string) (api.Operation, error) {
	return api.Operation{ID: id}, l.record("Retry %s", id)
}
func (l coreLife) WaitOperation(_ context.Context, id string, timeout time.Duration) (api.Operation, error) {
	return api.Operation{ID: id, Status: "completed"}, l.record("WaitOperation %s %s", id, timeout)
}
func (l coreLife) InspectSession(_ context.Context, id string) (api.SessionView, error) {
	return api.SessionView{UUID: id, Condition: "ready"}, l.record("InspectSession %s", id)
}
func (l coreLife) StartSession(_ context.Context, id string) (api.SessionChangeResult, error) {
	return api.SessionChangeResult{Changed: true, Operation: &api.Operation{ID: "start-" + id, Kind: "session.start", Status: "running"}}, l.record("StartSession %s", id)
}
func (l coreLife) StopSession(_ context.Context, id string) (api.SessionChangeResult, error) {
	return api.SessionChangeResult{Session: &api.SessionView{UUID: id, Condition: "stopped"}}, l.record("StopSession %s", id)
}

// fullLife implements every optional lifecycle API as well.
type fullLife struct{ coreLife }

func (l fullLife) SessionServices(_ context.Context, id string) (api.SessionServicesResult, error) {
	return api.SessionServicesResult{UUID: id, Services: []api.ProjectService{}, ObservedAt: "now"}, l.record("SessionServices %s", id)
}
func (l fullLife) SessionServiceAction(_ context.Context, id, name, action string) (api.SessionServiceActionResult, error) {
	return api.SessionServiceActionResult{UUID: id, Name: name, ObservedAt: "now"}, l.record("SessionServiceAction %s %s %s", id, name, action)
}
func (l fullLife) SessionServiceLogs(_ context.Context, id, name string) (api.SessionServiceLogsResult, error) {
	return api.SessionServiceLogsResult{UUID: id, Name: name, ObservedAt: "now"}, l.record("SessionServiceLogs %s %s", id, name)
}
func (l fullLife) AttachSession(_ context.Context, id string) (api.PendingAttachment, error) {
	return api.PendingAttachment{Token: "pending"}, l.record("AttachSession %s", id)
}
func (l fullLife) ClaimAttachment(_ context.Context, token string, _ *Connection) (api.AttachmentLaunch, error) {
	return api.AttachmentLaunch{}, l.record("ClaimAttachment %s", token)
}
func (l fullLife) ConfirmAttachment(_ context.Context, _ *Connection, operation string) error {
	return l.record("ConfirmAttachment %s", operation)
}
func (l fullLife) PreviewRemoval(_ context.Context, r RemovalPreviewRequest) (api.RemovalPreview, error) {
	return api.RemovalPreview{}, l.record("PreviewRemoval %+v", r)
}
func (l fullLife) DiscardSession(_ context.Context, r DiscardConfirmRequest) (api.Operation, error) {
	return api.Operation{ID: "discard"}, l.record("DiscardSession %+v", r)
}
func (l fullLife) DeleteSession(_ context.Context, r DiscardConfirmRequest) (api.Operation, error) {
	return api.Operation{ID: "delete"}, l.record("DeleteSession %+v", r)
}
func (l fullLife) RenameSession(_ context.Context, r RenameRequest) (api.Operation, error) {
	return api.Operation{ID: "rename"}, l.record("RenameSession %+v", r)
}
func (l fullLife) RenameRetainedBranch(_ context.Context, r RetainedRenameRequest) (api.Operation, error) {
	return api.Operation{ID: "retained-rename"}, l.record("RenameRetainedBranch %+v", r)
}
func (l fullLife) PreviewProjectDelete(_ context.Context, r ProjectDeletePreviewRequest) (api.ProjectDeletionPreview, error) {
	return api.ProjectDeletionPreview{}, l.record("PreviewProjectDelete %+v", r)
}
func (l fullLife) ConfirmProjectDelete(_ context.Context, r ProjectDeleteConfirmRequest) (api.Operation, error) {
	return api.Operation{ID: "project-delete"}, l.record("ConfirmProjectDelete %+v", r)
}
func (l fullLife) PreviewRetainedDelete(_ context.Context, project, branch string) (api.RetainedDeletePreview, error) {
	return api.RetainedDeletePreview{}, l.record("PreviewRetainedDelete %s %s", project, branch)
}
func (l fullLife) ConfirmRetainedDelete(_ context.Context, key, project, branch, token string) (api.Operation, error) {
	return api.Operation{ID: "retained-delete"}, l.record("ConfirmRetainedDelete %s %s %s %s", key, project, branch, token)
}
func (l fullLife) PreviewCreateReplace(_ context.Context, old string, r ReserveSessionRequest) (api.CreateReplacePreview, error) {
	return api.CreateReplacePreview{}, l.record("PreviewCreateReplace %s %+v", old, r)
}
func (l fullLife) ConfirmCreateReplace(_ context.Context, old, key, token string) (api.Operation, error) {
	return api.Operation{ID: "replace"}, l.record("ConfirmCreateReplace %s %s %s", old, key, token)
}
func (l fullLife) PreviewCreateCleanup(_ context.Context, id string) (api.CreateCleanupPreview, error) {
	return api.CreateCleanupPreview{}, l.record("PreviewCreateCleanup %s", id)
}
func (l fullLife) ConfirmCreateCleanup(_ context.Context, id, key, token string) (api.Operation, error) {
	return api.Operation{ID: "cleanup"}, l.record("ConfirmCreateCleanup %s %s %s", id, key, token)
}

type surfaceConfig int

const (
	surfaceStateOnly surfaceConfig = iota
	surfaceGit
	surfaceCoreLifecycle
	surfaceFullLifecycle
)

// surfaceHandler builds the handler as daemon.Serve does for each host
// configuration: no Git, Git without runtime, and Git with runtime.
func surfaceHandler(t *testing.T, config surfaceConfig) (Handler, *surfaceCalls) {
	t.Helper()
	calls := &surfaceCalls{}
	var life LifecycleAPI
	var ops OperationAPI
	switch config {
	case surfaceGit:
		// Without a runtime the daemon runs the journal's work itself;
		// only its OperationAPI methods are reachable.
		ops = coreLife{calls}
	case surfaceCoreLifecycle:
		life = coreLife{calls}
	case surfaceFullLifecycle:
		life = fullLife{coreLife{calls}}
	}
	h, _ := surfaceHandlerFor(t, config != surfaceStateOnly, life, ops)
	return h, calls
}

// surfaceHandlerFor builds the handler around life, or without runtime when
// life is nil, with ops running the journal's work then, and returns its
// store.
func surfaceHandlerFor(t *testing.T, git bool, life LifecycleAPI, ops OperationAPI) (Handler, *Store) {
	t.Helper()
	store, _ := openTestStore(t)
	if err := store.CreateProject(context.Background(), "team/app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if !git {
		return NewHandler(HandlerConfig{Store: store}), store
	}
	return NewHandler(HandlerConfig{Store: store, Git: fixtureGitReader{}, GitInfo: &api.GitInfo{Endpoint: "127.0.0.1:2222"}, Lifecycle: life, Operations: ops,
		Origins: &OriginAPI{Store: store, Runner: &fakePublication{}, Git: fixtureGitReader{}}}), store
}

func surfaceCall(t *testing.T, h Handler, ctx context.Context, method, params string) (map[string]any, *api.Error) {
	t.Helper()
	result, rpcErr := h(ctx, method, json.RawMessage(params))
	if rpcErr != nil {
		return nil, rpcErr
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("%s result does not encode: %v", method, err)
	}
	var decoded map[string]any
	if err = json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("%s result is not an object: %s", method, encoded)
	}
	return decoded, nil
}

func surfaceConnectionContext(dedicated bool) context.Context {
	return context.WithValue(context.Background(), connectionKey{}, &Connection{dedicated: dedicated})
}

func TestHelloReportsEachConfiguration(t *testing.T) {
	for _, test := range []struct {
		name         string
		config       surfaceConfig
		capabilities []api.Capability
	}{
		{"state only", surfaceStateOnly, []api.Capability{}},
		{"git", surfaceGit, []api.Capability{api.CapabilityGit}},
		{"core lifecycle", surfaceCoreLifecycle, []api.Capability{api.CapabilityGit, api.CapabilityRuntime}},
		{"full lifecycle", surfaceFullLifecycle, []api.Capability{api.CapabilityGit, api.CapabilityRuntime, api.CapabilityServices}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, _ := surfaceHandler(t, test.config)
			hello, rpcErr := callMethod(t, context.Background(), h, api.SystemHello, api.HelloRequest{Protocol: api.Protocol})
			if rpcErr != nil {
				t.Fatal(rpcErr)
			}
			if !reflect.DeepEqual(hello.Capabilities, test.capabilities) || hello.Protocol != api.Protocol || hello.BuildVersion != BuildVersion || !api.ValidUUID(hello.InstanceID) {
				t.Errorf("hello %+v, want capabilities %q", hello, test.capabilities)
			}
			inspect, rpcErr := callMethod(t, context.Background(), h, api.SystemInspect, api.SystemInspectRequest{})
			if rpcErr != nil || (inspect.Git != nil) != (test.config != surfaceStateOnly) {
				t.Errorf("inspect %+v %+v", inspect, rpcErr)
			}
		})
	}
}

// Every method of the protocol is answered in every configuration: served
// when its capability is configured, or refused as not configured, naming the
// capability, whatever its params.
func TestEveryProtocolMethodIsServedOrNotConfigured(t *testing.T) {
	for _, test := range []struct {
		name       string
		config     surfaceConfig
		configured map[api.Capability]bool
	}{
		{"state only", surfaceStateOnly, map[api.Capability]bool{"": true}},
		{"git", surfaceGit, map[api.Capability]bool{"": true, api.CapabilityGit: true}},
		{"full lifecycle", surfaceFullLifecycle, map[api.Capability]bool{"": true, api.CapabilityGit: true, api.CapabilityRuntime: true, api.CapabilityServices: true}},
	} {
		h, _ := surfaceHandler(t, test.config)
		for _, m := range api.Methods() {
			if m.Connection {
				continue
			}
			_, rpcErr := h(surfaceConnectionContext(false), m.Name, json.RawMessage(`{"unknown":1}`))
			notConfigured := api.NewError(api.KindNotConfigured, string(m.Capability))
			switch {
			case test.configured[m.Capability] && !reflect.DeepEqual(rpcErr, api.InvalidParam("unknown")):
				t.Errorf("%s: %s answered %v, want its params refused", test.name, m.Name, rpcErr)
			case !test.configured[m.Capability] && !reflect.DeepEqual(rpcErr, notConfigured):
				t.Errorf("%s: %s answered %v, want %v", test.name, m.Name, rpcErr, notConfigured)
			}
		}
	}
}

func TestTheTableRegistersExactlyTheProtocol(t *testing.T) {
	store, _ := openTestStore(t)
	for _, life := range []LifecycleAPI{nil, coreLife{&surfaceCalls{}}, fullLife{coreLife{&surfaceCalls{}}}} {
		c := HandlerConfig{Store: store, Git: fixtureGitReader{}, GitInfo: &api.GitInfo{}, Lifecycle: life,
			Origins: &OriginAPI{Store: store, Runner: &fakePublication{}, Git: fixtureGitReader{}}}
		table := newMethodTable(c)
		want := map[string]bool{}
		for _, m := range api.Methods() {
			if !m.Connection {
				want[m.Name] = true
			}
		}
		got := map[string]bool{}
		for name := range table.handlers {
			got[name] = true
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("registered %v, want %v", got, want)
		}
	}
}

func TestLifecycleMethodsRouteDecodedRequests(t *testing.T) {
	uuid, uuid2, oid, token := surfaceUUID, surfaceUUID2, surfaceOID, surfaceToken
	for _, test := range []struct {
		method, params, call string
		// The result object's keys, and for an operation or session envelope
		// the identity the lifecycle returned.
		keys     []string
		identity string
	}{
		{"project.create", `{"key":"k","project":"team/app","url":"ssh://host/repo"}`, "CreateProject {Key:k Project:team/app URL:ssh://host/repo}", []string{"operation"}, "created-project"},
		{"session.create", `{"key":"k","project":"team/app","branch":"work","choice":"existing"}`, "CreateSession {Key:k Project:team/app Branch:work Choice:existing Source: OriginRef: ExpectedCommitOID: ExpectedOriginURL:}", []string{"operation"}, "created-session"},
		{"session.create", `{"key":"k","project":"team/app","branch":"work","choice":"new","origin_ref":"refs/heads/main","expected_commit_oid":"` + oid + `","expected_origin_url":"ssh://host/repo"}`, "CreateSession {Key:k Project:team/app Branch:work Choice:new Source: OriginRef:refs/heads/main ExpectedCommitOID:" + oid + " ExpectedOriginURL:ssh://host/repo}", []string{"operation"}, "created-session"},
		{"session.create.cleanup.preview", `{"uuid":"` + uuid + `"}`, "PreviewCreateCleanup " + uuid, []string{"preview"}, ""},
		{"session.create.cleanup.confirm", `{"uuid":"` + uuid + `","key":"k","confirmation_token":"` + token + `"}`, "ConfirmCreateCleanup " + uuid + " k " + token, []string{"operation"}, "cleanup"},
		{"session.create.replace.preview", `{"old_uuid":"` + uuid + `","key":"k","project":"team/app","branch":"work","choice":"new","source":"refs/heads/main"}`, "PreviewCreateReplace " + uuid + " {Key:k Project:team/app Branch:work Choice:new Source:refs/heads/main OriginRef: ExpectedCommitOID: ExpectedOriginURL:}", []string{"preview"}, ""},
		{"session.create.replace.confirm", `{"old_uuid":"` + uuid + `","key":"k","confirmation_token":"` + token + `"}`, "ConfirmCreateReplace " + uuid + " k " + token, []string{"operation"}, "replace"},
		{"session.removal.preview", `{"uuid":"` + uuid + `","kind":"discard"}`, "PreviewRemoval {UUID:" + uuid + " Kind:discard AcknowledgeMissingRuntime:false}", []string{"preview"}, ""},
		{"session.removal.preview", `{"uuid":"` + uuid + `","kind":"delete","acknowledge_missing_runtime":true}`, "PreviewRemoval {UUID:" + uuid + " Kind:delete AcknowledgeMissingRuntime:true}", []string{"preview"}, ""},
		{"session.discard", `{"key":"k","uuid":"` + uuid + `","confirmation_token":"` + token + `"}`, "DiscardSession {Key:k UUID:" + uuid + " ConfirmationToken:" + token + "}", []string{"operation"}, "discard"},
		{"session.delete", `{"key":"k","uuid":"` + uuid + `","confirmation_token":"` + token + `"}`, "DeleteSession {Key:k UUID:" + uuid + " ConfirmationToken:" + token + "}", []string{"operation"}, "delete"},
		{"session.rename", `{"key":"k","uuid":"` + uuid + `","new_branch":"next","expected_old_tip":"` + oid + `"}`, "RenameSession {Key:k UUID:" + uuid + " NewBranch:next ExpectedOldTip:" + oid + "}", []string{"operation"}, "rename"},
		{"project.retained.rename", `{"key":"k","project":"team/app","old_branch":"old","new_branch":"new","expected_old_tip":"` + oid + `"}`, "RenameRetainedBranch {Key:k Project:team/app OldBranch:old NewBranch:new ExpectedOldTip:" + oid + "}", []string{"operation"}, "retained-rename"},
		{"project.delete.preview", `{"project":"team/app","acknowledge_missing":["` + uuid2 + `"]}`, "PreviewProjectDelete {Project:team/app AcknowledgeMissing:[" + uuid2 + "]}", []string{"preview"}, ""},
		{"project.delete.confirm", `{"key":"k","project":"team/app","confirmation_token":"` + token + `"}`, "ConfirmProjectDelete {Key:k Project:team/app ConfirmationToken:" + token + "}", []string{"operation"}, "project-delete"},
		{"project.retained.delete.preview", `{"project":"team/app","branch":"saved"}`, "PreviewRetainedDelete team/app saved", []string{"preview"}, ""},
		{"project.retained.delete.confirm", `{"key":"k","project":"team/app","branch":"saved","confirmation_token":"` + token + `"}`, "ConfirmRetainedDelete k team/app saved " + token, []string{"operation"}, "retained-delete"},
		{"operation.retry", `{"id":"op-1"}`, "Retry op-1", []string{"operation"}, "op-1"},
		{"session.inspect", `{"uuid":"` + uuid + `"}`, "InspectSession " + uuid, []string{"session"}, uuid},
		{"session.start", `{"uuid":"` + uuid + `"}`, "StartSession " + uuid, []string{"changed", "operation"}, ""},
		{"session.stop", `{"uuid":"` + uuid + `"}`, "StopSession " + uuid, []string{"changed", "session"}, ""},
		{"session.services", `{"uuid":"` + uuid + `"}`, "SessionServices " + uuid, []string{"observed_at", "services", "uuid"}, ""},
		{"session.service.logs", `{"uuid":"` + uuid + `","name":"` + surfaceUnit + `"}`, "SessionServiceLogs " + uuid + " " + surfaceUnit, []string{"name", "observed_at", "uuid"}, ""},
		{"session.service.action", `{"uuid":"` + uuid + `","name":"` + surfaceUnit + `","action":"restart"}`, "SessionServiceAction " + uuid + " " + surfaceUnit + " restart", []string{"name", "observed_at", "uuid"}, ""},
		{"session.attach", `{"uuid":"` + uuid + `"}`, "AttachSession " + uuid, []string{"expires_at", "spec", "token"}, ""},
		{"attachment.confirm", `{"operation":"/1.0/operations/` + uuid + `"}`, "ConfirmAttachment /1.0/operations/" + uuid, nil, ""},
		{"attachment.ping", `{}`, "", nil, ""},
	} {
		t.Run(test.method+" "+test.call, func(t *testing.T) {
			h, calls := surfaceHandler(t, surfaceFullLifecycle)
			result, rpcErr := surfaceCall(t, h, surfaceConnectionContext(true), test.method, test.params)
			if rpcErr != nil {
				t.Fatalf("refused: %+v", rpcErr)
			}
			var want []string
			if test.call != "" {
				want = []string{test.call}
			}
			if !reflect.DeepEqual(calls.calls, want) {
				t.Errorf("lifecycle calls:\n got %q\nwant %q", calls.calls, want)
			}
			var keys []string
			for k := range result {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if !reflect.DeepEqual(keys, test.keys) {
				t.Errorf("result keys %q, want %q: %v", keys, test.keys, result)
			}
			if test.identity != "" {
				inner := result[test.keys[0]].(map[string]any)
				if inner["id"] != test.identity && inner["uuid"] != test.identity {
					t.Errorf("result identity %v, want %s", inner, test.identity)
				}
			}
		})
	}
}

// TestGitOnlyHandlerRoutesOperationWork checks that without a runtime the
// journal's Retry and waits route to the handler's OperationAPI.
func TestGitOnlyHandlerRoutesOperationWork(t *testing.T) {
	for _, test := range []struct{ method, params, call string }{
		{"operation.retry", `{"id":"op-1"}`, "Retry op-1"},
		{"operation.wait", `{"id":"op-1","timeout":5}`, "WaitOperation op-1 5s"},
	} {
		h, calls := surfaceHandler(t, surfaceGit)
		result, rpcErr := surfaceCall(t, h, context.Background(), test.method, test.params)
		if rpcErr != nil || !reflect.DeepEqual(calls.calls, []string{test.call}) {
			t.Fatalf("%s answered %v %v, calls %q", test.method, result, rpcErr, calls.calls)
		}
		if operation, ok := result["operation"].(map[string]any); !ok || operation["id"] != "op-1" {
			t.Errorf("%s answered %v, want operation op-1", test.method, result)
		}
	}
}

func TestLifecycleMethodsRejectMalformedParameters(t *testing.T) {
	uuid, token := surfaceUUID, surfaceToken
	for _, test := range []struct{ method, params string }{
		{"project.create", `{"key":"k","project":"team/app","extra":1}`},
		{"project.create", `{"v":1,"key":"k","project":"team/app"}`},
		{"project.create", `{"key":"","project":"team/app"}`},
		{"project.create", `{"key":"k","project":"/abs"}`},
		{"session.create", `{"key":"k","project":"team/app","branch":"work","choice":"existing","source":"refs/heads/main"}`},
		{"session.create", `{"key":"k","project":"team/app","branch":"work..x","choice":"existing"}`},
		{"session.create.cleanup.preview", `{"uuid":"short"}`},
		{"session.create.cleanup.preview", `{"uuid":"` + uuid + `","loss_operation_id":"` + uuid + `"}`},
		{"session.create.cleanup.confirm", `{"uuid":"` + uuid + `","key":"k","confirmation_token":"ABCDEF0123456789abcdef0123456789"}`},
		{"session.create.replace.preview", `{"old_uuid":"` + uuid + `","key":"k","project":"team/app","branch":"work","choice":"maybe"}`},
		{"session.create.replace.confirm", `{"old_uuid":"` + uuid + `","key":"","confirmation_token":"` + token + `"}`},
		{"session.removal.preview", `{"uuid":"` + uuid + `","kind":"discard","loss_operation_id":"` + uuid + `"}`},
		{"session.removal.preview", `{"uuid":"` + uuid + `","kind":"stop"}`},
		{"session.discard", `{"key":"k","uuid":"` + uuid + `","confirmation_token":"short"}`},
		{"session.delete", `{"key":"k","uuid":"` + uuid + `","confirmation_token":"short"}`},
		{"session.rename", `{"key":"k","uuid":"` + uuid + `","new_branch":"next","expected_old_tip":"0000000000000000000000000000000000000000"}`},
		{"project.retained.rename", `{"key":"k","project":"team/app","old_branch":"same","new_branch":"same","expected_old_tip":"` + surfaceOID + `"}`},
		{"project.delete.preview", `{"project":"team/app","loss_operations":{}}`},
		{"project.delete.preview", `{"project":"team/app","acknowledge_missing":["bad"]}`},
		{"project.delete.confirm", `{"key":"k","project":"team/app","confirmation_token":"short"}`},
		{"project.retained.delete.preview", `{"project":"team/app","branch":".hidden"}`},
		{"project.retained.delete.confirm", `{"key":"","project":"team/app","branch":"saved","confirmation_token":"` + token + `"}`},
		{"operation.inspect", `{"id":""}`},
		{"operation.retry", `{}`},
		{"operation.list", `{"limit":21}`},
		{"session.list", `{"limit":9}`},
		{"session.inspect", `{"uuid":"bad"}`},
		{"session.start", `{}`},
		{"session.stop", `{"uuid":"` + uuid + `","extra":1}`},
		{"session.services", `{"uuid":"` + uuid + `","name":"` + surfaceUnit + `"}`},
		{"session.service.logs", `{"uuid":"` + uuid + `","name":"sshd.service"}`},
		{"session.service.action", `{"uuid":"` + uuid + `","name":"` + surfaceUnit + `","action":"reload"}`},
		{"session.service.action", `{"uuid":"` + uuid + `","unit":"` + surfaceUnit + `","action":"restart"}`},
		{"session.service.action", `{"uuid":"` + uuid + `","action":"list"}`},
		{"session.service.action", `{"uuid":"` + uuid + `","name":"` + surfaceUnit + `","action":"logs"}`},
		{"system.health", `null`},
		{"system.inspect", `[]`},
		{"attachment.ping", `null`},
		{"session.attach", `{"uuid":"bad"}`},
		{"attachment.confirm", `{"operation":"/1.0/operations/short"}`},
		{"attachment.ping", `{"operation":"/1.0/operations/` + uuid + `"}`},
	} {
		t.Run(test.method+" "+test.params, func(t *testing.T) {
			h, calls := surfaceHandler(t, surfaceFullLifecycle)
			_, rpcErr := surfaceCall(t, h, surfaceConnectionContext(true), test.method, test.params)
			if rpcErr == nil || rpcErr.Code != -32602 || rpcErr.Kind() != "invalid" {
				t.Fatalf("answer %+v, want invalid_params", rpcErr)
			}
			if len(calls.calls) != 0 {
				t.Fatalf("reached the lifecycle: %q", calls.calls)
			}
		})
	}
}

func TestAttachmentHelperMethodsNeedTheirConnection(t *testing.T) {
	h, calls := surfaceHandler(t, surfaceFullLifecycle)
	for _, test := range []struct {
		name   string
		ctx    context.Context
		method string
		params string
	}{
		{"attach without a connection", context.Background(), "session.attach", `{"uuid":"` + surfaceUUID + `"}`},
		{"confirm on an ordinary connection", surfaceConnectionContext(false), "attachment.confirm", `{"operation":"/1.0/operations/` + surfaceUUID + `"}`},
		{"ping on an ordinary connection", surfaceConnectionContext(false), "attachment.ping", `{}`},
		{"claim from a process that is not the helper", surfaceConnectionContext(false), "attachment.claim", `{"token":"` + strings.Repeat("a", 64) + `"}`},
	} {
		_, rpcErr := surfaceCall(t, h, test.ctx, test.method, test.params)
		if want := api.NewError(api.KindInvalidRequest, api.ReasonConnection); !reflect.DeepEqual(rpcErr, want) {
			t.Errorf("%s: answer %v, want %v", test.name, rpcErr, want)
		}
	}
	if len(calls.calls) != 0 {
		t.Fatalf("reached the lifecycle: %q", calls.calls)
	}
}

func TestStoreBackedLifecycleReads(t *testing.T) {
	h, calls := surfaceHandler(t, surfaceFullLifecycle)
	for _, test := range []struct {
		method, params, want string
	}{
		{"operation.list", `{"limit":20}`, `{"operations":[],"next":""}`},
		{"session.list", `{"limit":8}`, `{"sessions":[],"next":""}`},
	} {
		result, rpcErr := h(context.Background(), test.method, json.RawMessage(test.params))
		if rpcErr != nil {
			t.Fatalf("%s: %+v", test.method, rpcErr)
		}
		encoded, _ := json.Marshal(result)
		if string(encoded) != test.want {
			t.Errorf("%s: %s, want %s", test.method, encoded, test.want)
		}
	}
	_, rpcErr := h(context.Background(), "operation.inspect", json.RawMessage(`{"id":"missing"}`))
	if want := wantError(api.KindNotFound, api.ReasonOperation, "operation/missing"); !reflect.DeepEqual(rpcErr, want) {
		t.Errorf("missing operation: %v, want %v", rpcErr, want)
	}
	if len(calls.calls) != 0 {
		t.Errorf("lifecycle calls: %q", calls.calls)
	}
}

func TestOperationReadsInspectAndPageTheJournal(t *testing.T) {
	h, store := surfaceHandlerFor(t, true, fullLife{coreLife{&surfaceCalls{}}}, nil)
	dropOperationInsertTriggers(t, store)
	long := strings.Repeat("d", 600)
	// Accepted out of their IDs' order: the journal lists them as accepted.
	for _, row := range []struct{ id, kind, session, status, phase, diagnostic string }{
		{"op-c", "session.discard", surfaceUUID2, "running", "reserved", long},
		{"op-a", "project.retained.rename", "", "completed", "completed", ""},
		{"op-b", "session.rename", surfaceUUID, "blocked", "stale", long},
	} {
		if _, err := store.db.Exec(`INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,intent_json,status,phase,committed,evidence_json,diagnostic,created_at,updated_at)
		 VALUES(?,?,?,'team/app',NULLIF(?,''),'{"key":"k"}','x','{"project":"team/app"}',?,?,1,'{"e":1}',?,'2026-01-01T00:00:00Z','2026-01-02T00:00:00Z')`,
			row.id, "key-"+row.id, row.kind, row.session, row.status, row.phase, row.diagnostic); err != nil {
			t.Fatal(err)
		}
	}
	summary := func(id, kind, session, status, phase, diagnostic string) string {
		out := `{"id":"` + id + `","idempotency_key":"key-` + id + `","kind":"` + kind + `","project":"team/app",`
		if session != "" {
			out += `"session_uuid":"` + session + `",`
		}
		out += `"intent":{"project":"team/app"},"status":"` + status + `","phase":"` + phase + `","committed":true`
		if diagnostic != "" {
			out += `,"diagnostic":"` + diagnostic + `"`
		}
		return out + "}"
	}
	for _, test := range []struct {
		method, params, want string
	}{
		{"operation.inspect", `{"id":"op-b"}`, `{"operation":{"id":"op-b","idempotency_key":"key-op-b","kind":"session.rename","project":"team/app","session_uuid":"` + surfaceUUID +
			`","intent":{"project":"team/app"},"request":{"key":"k"},"status":"blocked","phase":"stale","committed":true,"evidence":{"e":1},"diagnostic":"` + long + `","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}}`},
		// A summary shows the same diagnostic as operation.inspect.
		{"operation.list", `{"limit":2}`, `{"operations":[` + summary("op-c", "session.discard", surfaceUUID2, "running", "reserved", long) + "," +
			summary("op-a", "project.retained.rename", "", "completed", "completed", "") + `],"next":"2"}`},
		{"operation.list", `{"limit":2,"after":"2"}`, `{"operations":[` + summary("op-b", "session.rename", surfaceUUID, "blocked", "stale", long) + `],"next":""}`},
		// The cursor stays valid once the page's last operation is deleted.
		{"delete op-a", "", ""},
		{"operation.list", `{"limit":2,"after":"2"}`, `{"operations":[` + summary("op-b", "session.rename", surfaceUUID, "blocked", "stale", long) + `],"next":""}`},
	} {
		if test.method == "delete op-a" {
			if _, err := store.db.Exec(`DELETE FROM operations WHERE id='op-a'`); err != nil {
				t.Fatal(err)
			}
			continue
		}
		result, rpcErr := h(context.Background(), test.method, json.RawMessage(test.params))
		if rpcErr != nil {
			t.Fatalf("%s %s: %+v", test.method, test.params, rpcErr)
		}
		encoded, _ := json.Marshal(result)
		if string(encoded) != test.want {
			t.Errorf("%s %s:\n got %s\nwant %s", test.method, test.params, encoded, test.want)
		}
	}
}

// TestHandlerErrorsAnswerTheirKind checks the three rules that map an error a
// handler returns to its answer, through a registered method.
func TestHandlerErrorsAnswerTheirKind(t *testing.T) {
	discard := `{"key":"k","uuid":"` + surfaceUUID + `","confirmation_token":"` + surfaceToken + `"}`
	projectPreview := `{"project":"team/app"}`
	services := `{"uuid":"` + surfaceUUID + `"}`
	for _, test := range []struct {
		name, method, params string
		err                  error
		want                 *api.Error
	}{
		{"bare invalid", "session.discard", discard, ErrInvalid, api.InvalidParam("")},
		{"typed invalid", "session.discard", discard, Invalid("uuid"), api.InvalidParam("uuid")},
		{"capacity", "session.discard", discard, Full(api.ReasonSessionCapacity), wantError(api.KindCapacity, api.ReasonSessionCapacity, "")},
		{"retry names its holder", "session.discard", discard, Retry(api.ReasonSessionHeld, "operation/op-1"), wantError(api.KindRetry, api.ReasonSessionHeld, "operation/op-1")},
		{"conflict", "session.discard", discard, Conflict(api.ReasonAttachmentActive, "session/"+surfaceUUID), wantError(api.KindConflict, api.ReasonAttachmentActive, "session/"+surfaceUUID)},
		{"stale", "session.discard", discard, Stale(api.ReasonExpired), wantError(api.KindPreconditionFailed, api.ReasonExpired, "")},
		{"key reused", "session.discard", discard, KeyReused(), &api.Error{Code: -32602, Message: "invalid", Data: &api.ErrorData{Reason: "key_reused", Field: "key"}}},
		{"untyped conflict is internal", "session.discard", discard, ErrConflict, wantError(api.KindInternal, api.ReasonUnexpected, "")},
		{"bare not found", "session.discard", discard, ErrNotFound, &api.Error{Code: -32015, Message: "not_found"}},
		{"typed not found", "session.discard", discard, NotFound(api.ReasonSession, "session/"+surfaceUUID), wantError(api.KindNotFound, api.ReasonSession, "session/"+surfaceUUID)},
		{"cancelled", "session.discard", discard, context.Canceled, &api.Error{Code: -32001, Message: "cancelled"}},
		{"deadline", "session.discard", discard, context.DeadlineExceeded, wantError(api.KindRetry, api.ReasonDeadline, "")},
		{"untyped", "session.discard", discard, errors.New("boom"), wantError(api.KindInternal, api.ReasonUnexpected, "")},
		{"failed authority", "session.discard", discard, Failed(api.ReasonRuntime, errors.New("incus down")), wantError(api.KindFailed, api.ReasonRuntime, "")},
		{"failed keeps a typed cause", "session.discard", discard, Failed(api.ReasonRuntime, NotFound(api.ReasonSession, "")), wantError(api.KindNotFound, api.ReasonSession, "")},
		{"a joined conflict does not hide a typed cause", "project.delete.preview", projectPreview, errors.Join(NotFound(api.ReasonProject, "project/team/app"), ErrConflict), wantError(api.KindNotFound, api.ReasonProject, "project/team/app")},
		{"a joined conflict does not hide a missing record", "project.delete.preview", projectPreview, errors.Join(ErrNotFound, ErrConflict), &api.Error{Code: -32015, Message: "not_found"}},
		{"a joined conflict does not hide a cancellation", "project.delete.preview", projectPreview, errors.Join(context.Canceled, ErrConflict), &api.Error{Code: -32001, Message: "cancelled"}},
		{"services refusal", "session.services", services, Conflict(api.ReasonSessionNotRunning, ""), wantError(api.KindConflict, api.ReasonSessionNotRunning, "")},
		{"services untyped", "session.services", services, errors.New("boom"), wantError(api.KindInternal, api.ReasonUnexpected, "")},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, calls := surfaceHandler(t, surfaceFullLifecycle)
			calls.err = test.err
			_, rpcErr := surfaceCall(t, h, context.Background(), test.method, test.params)
			if !reflect.DeepEqual(rpcErr, test.want) {
				t.Fatalf("answer %v, want %v", rpcErr, test.want)
			}
			if encoded, _ := json.Marshal(rpcErr); strings.Contains(string(encoded), "boom") || strings.Contains(string(encoded), "incus") {
				t.Fatalf("answer %s carries the cause's text", encoded)
			}
		})
	}
}

// wantError is the answer of kind with reason and target.
func wantError(kind api.ErrorKind, reason, target string) *api.Error {
	e := api.NewError(kind, reason)
	if target != "" {
		e.Data.Target = target
	}
	return e
}

func TestUnconfiguredAndUnknownMethodAnswers(t *testing.T) {
	notConfigured := func(capability string) *api.Error { return api.NewError(api.KindNotConfigured, capability) }
	unknown := api.NewError(api.KindMethodNotFound, "")
	discard := `{"key":"k","uuid":"` + surfaceUUID + `","confirmation_token":"` + surfaceToken + `"}`
	for _, test := range []struct {
		name           string
		config         surfaceConfig
		method, params string
		want           *api.Error
	}{
		{"unknown method", surfaceStateOnly, "nope", `{}`, unknown},
		{"unknown method with other params", surfaceStateOnly, "nope", `{"x":1}`, unknown},
		{"lifecycle without runtime", surfaceStateOnly, "session.create", `{}`, notConfigured("runtime")},
		{"origin without Git", surfaceStateOnly, "origin.inspect", `{}`, notConfigured("git")},
		{"name outside the protocol", surfaceStateOnly, "runtime.inspect", `{}`, unknown},
		{"session RPC method on the control socket", surfaceStateOnly, "status.report", `{"v":1}`, unknown},
		{"lifecycle params without runtime", surfaceStateOnly, "session.discard", discard, notConfigured("runtime")},
		{"project list without Git", surfaceStateOnly, "project.list", `{"limit":1}`, notConfigured("git")},
		{"lifecycle with Git only", surfaceGit, "session.create", `{}`, notConfigured("runtime")},
		{"attachment without runtime", surfaceGit, "attachment.ping", `{}`, notConfigured("runtime")},
		{"system method rejects params", surfaceGit, "system.hello", `{"protocol":"p.control/v2","x":1}`, api.InvalidParam("x")},
		{"system method rejects the old version field", surfaceGit, "system.health", `{"v":1}`, api.InvalidParam("v")},
		{"removed capabilities", surfaceGit, "system.capabilities", `{}`, unknown},
		{"discard without its API", surfaceCoreLifecycle, "session.discard", discard, notConfigured("runtime")},
		{"services without their API", surfaceCoreLifecycle, "session.services", `{"uuid":"` + surfaceUUID + `"}`, notConfigured("services")},
		{"attach without its API", surfaceCoreLifecycle, "session.attach", `{"uuid":"` + surfaceUUID + `"}`, notConfigured("runtime")},
		{"removed cache list", surfaceFullLifecycle, "environment.cache.list", `{}`, unknown},
		{"removed cache preview", surfaceFullLifecycle, "environment.cache.preview", `{}`, unknown},
		{"removed cache collection", surfaceFullLifecycle, "environment.cache.collect", `{}`, unknown},
		{"removed workspace inspection", surfaceFullLifecycle, "workspace.inspect", `{}`, unknown},
		{"renamed service journal", surfaceFullLifecycle, "session.service.journal", `{}`, unknown},
		{"cleanup without its API", surfaceCoreLifecycle, "session.create.cleanup.preview", `{"uuid":"` + surfaceUUID + `"}`, notConfigured("runtime")},
		{"removed session.repair.preview", surfaceFullLifecycle, "session.repair.preview", `{}`, unknown},
		{"removed session.repair.confirm", surfaceFullLifecycle, "session.repair.confirm", `{}`, unknown},
		{"removed session.ref.repair.preview", surfaceFullLifecycle, "session.ref.repair.preview", `{}`, unknown},
		{"removed session.record.repair.confirm", surfaceFullLifecycle, "session.record.repair.confirm", `{}`, unknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, calls := surfaceHandler(t, test.config)
			_, rpcErr := surfaceCall(t, h, surfaceConnectionContext(true), test.method, test.params)
			if !reflect.DeepEqual(rpcErr, test.want) {
				t.Fatalf("answer %v, want %v", rpcErr, test.want)
			}
			if len(calls.calls) != 0 {
				t.Fatalf("reached the lifecycle: %q", calls.calls)
			}
		})
	}
}
