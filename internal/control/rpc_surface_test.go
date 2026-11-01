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

	"github.com/lgvo/p.ai/internal/api"
)

// These tests state what the control-API handler answers, built the way the
// daemon builds it: the Git base handler, the lifecycle layer and the origin
// layer. They pin method availability, advertisement, parameter checks,
// routing to the lifecycle and the error mapping.

const (
	surfaceUUID  = "11111111-2222-3333-4444-555555555555"
	surfaceUUID2 = "66666666-7777-8888-9999-aaaaaaaaaaaa"
	surfaceOID   = "0123456789abcdef0123456789abcdef01234567"
	surfaceToken = "0123456789abcdef0123456789abcdef"
	surfaceKey   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
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
func (l coreLife) InspectSession(_ context.Context, id string) (api.SessionView, error) {
	return api.SessionView{UUID: id, Condition: "ready"}, l.record("InspectSession %s", id)
}
func (l coreLife) StartSession(_ context.Context, id string) (api.SessionView, error) {
	return api.SessionView{UUID: id, Condition: "starting"}, l.record("StartSession %s", id)
}
func (l coreLife) StopSession(_ context.Context, id string) (api.SessionView, error) {
	return api.SessionView{UUID: id, Condition: "stopped"}, l.record("StopSession %s", id)
}

// fullLife implements every optional lifecycle API as well.
type fullLife struct{ coreLife }

func (l fullLife) SessionServices(_ context.Context, id, unit, action string) (api.ServiceResult, error) {
	return api.ServiceResult{V: 1, UUID: id, Unit: unit, ObservedAt: "now"}, l.record("SessionServices %s %s %s", id, unit, action)
}
func (l fullLife) AttachSession(_ context.Context, id string) (api.PendingAttachment, error) {
	return api.PendingAttachment{V: 1, Token: "pending"}, l.record("AttachSession %s", id)
}
func (l fullLife) ClaimAttachment(_ context.Context, token string, _ *Connection) (api.AttachmentLaunch, error) {
	return api.AttachmentLaunch{}, l.record("ClaimAttachment %s", token)
}
func (l fullLife) ConfirmAttachment(_ context.Context, _ *Connection, operation string) error {
	return l.record("ConfirmAttachment %s", operation)
}
func (l fullLife) ListEnvironmentCache(_ context.Context, project, after string, limit int) ([]EnvironmentCacheItem, string, error) {
	return nil, "", l.record("ListEnvironmentCache %s %q %d", project, after, limit)
}
func (l fullLife) PreviewEnvironmentCollection(_ context.Context, project, key, token, after string, limit int) (EnvironmentCollectionPreview, error) {
	return EnvironmentCollectionPreview{}, l.record("PreviewEnvironmentCollection %s %s %q %q %d", project, key, token, after, limit)
}
func (l fullLife) CollectEnvironmentCache(_ context.Context, key, token string) (api.Operation, error) {
	return api.Operation{ID: "collect"}, l.record("CollectEnvironmentCache %s %s", key, token)
}
func (l fullLife) InspectWorkspace(_ context.Context, r WorkspaceInspectRequest) (api.Operation, error) {
	return api.Operation{ID: "inspect"}, l.record("InspectWorkspace %+v", r)
}
func (l fullLife) InspectWorkspaceLoss(_ context.Context, r WorkspaceInspectRequest) (api.Operation, error) {
	return api.Operation{ID: "loss"}, l.record("InspectWorkspaceLoss %+v", r)
}
func (l fullLife) PreviewRemoval(_ context.Context, r RemovalPreviewRequest) (RemovalPreview, error) {
	return RemovalPreview{}, l.record("PreviewRemoval %+v", r)
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
func (l fullLife) PreviewProjectDelete(_ context.Context, r ProjectDeletePreviewRequest) (ProjectDeletePreview, error) {
	return ProjectDeletePreview{}, l.record("PreviewProjectDelete %+v", r)
}
func (l fullLife) ConfirmProjectDelete(_ context.Context, r ProjectDeleteConfirmRequest) (api.Operation, error) {
	return api.Operation{ID: "project-delete"}, l.record("ConfirmProjectDelete %+v", r)
}
func (l fullLife) PreviewRetainedDelete(_ context.Context, project, branch string) (RetainedDeletePreview, error) {
	return RetainedDeletePreview{}, l.record("PreviewRetainedDelete %s %s", project, branch)
}
func (l fullLife) ConfirmRetainedDelete(_ context.Context, key, project, branch, token string) (api.Operation, error) {
	return api.Operation{ID: "retained-delete"}, l.record("ConfirmRetainedDelete %s %s %s %s", key, project, branch, token)
}
func (l fullLife) PreviewRepair(_ context.Context, id string) (RepairPreview, error) {
	return RepairPreview{}, l.record("PreviewRepair %s", id)
}
func (l fullLife) ConfirmRepair(_ context.Context, r RepairConfirmRequest) (api.Operation, error) {
	return api.Operation{ID: "repair"}, l.record("ConfirmRepair %+v", r)
}
func (l fullLife) PrepareRepair(_ context.Context, key, id string) (api.Operation, error) {
	return api.Operation{ID: "prepare"}, l.record("PrepareRepair %s %s", key, id)
}
func (l fullLife) PreviewPreparedRepair(_ context.Context, id, prepared string) (RepairPreview, error) {
	return RepairPreview{}, l.record("PreviewPreparedRepair %s %s", id, prepared)
}
func (l fullLife) PreviewRefRepair(_ context.Context, id, loss string) (RefRepairPreview, error) {
	return RefRepairPreview{}, l.record("PreviewRefRepair %s %s", id, loss)
}
func (l fullLife) ConfirmRefRepair(_ context.Context, key, id, token string) (api.Operation, error) {
	return api.Operation{ID: "ref-repair"}, l.record("ConfirmRefRepair %s %s %s", key, id, token)
}
func (l fullLife) PreviewPrincipalRepair(_ context.Context, id string) (PrincipalRepairPreview, error) {
	return PrincipalRepairPreview{}, l.record("PreviewPrincipalRepair %s", id)
}
func (l fullLife) ConfirmPrincipalRepair(_ context.Context, key, id, token string) (api.Operation, error) {
	return api.Operation{ID: "principal-repair"}, l.record("ConfirmPrincipalRepair %s %s %s", key, id, token)
}
func (l fullLife) PreviewRecordRepair(_ context.Context, id string) (RecordRepairPreview, error) {
	return RecordRepairPreview{}, l.record("PreviewRecordRepair %s", id)
}
func (l fullLife) ConfirmRecordRepair(_ context.Context, key, id, token string) (api.Operation, error) {
	return api.Operation{ID: "record-repair"}, l.record("ConfirmRecordRepair %s %s %s", key, id, token)
}
func (l fullLife) PreviewCreateReplace(_ context.Context, old string, r ReserveSessionRequest) (CreateReplacePreview, error) {
	return CreateReplacePreview{}, l.record("PreviewCreateReplace %s %+v", old, r)
}
func (l fullLife) ConfirmCreateReplace(_ context.Context, old, key, token string) (api.Operation, error) {
	return api.Operation{ID: "replace"}, l.record("ConfirmCreateReplace %s %s %s", old, key, token)
}
func (l fullLife) PreviewCreateCleanup(_ context.Context, id string) (CreateCleanupPreview, error) {
	return CreateCleanupPreview{}, l.record("PreviewCreateCleanup %s", id)
}
func (l fullLife) ConfirmCreateCleanup(_ context.Context, id, key, token string) (api.Operation, error) {
	return api.Operation{ID: "cleanup"}, l.record("ConfirmCreateCleanup %s %s %s", id, key, token)
}
func (l fullLife) PreviewAssembledCreateCleanup(_ context.Context, id, loss string) (CreateCleanupPreview, error) {
	return CreateCleanupPreview{}, l.record("PreviewAssembledCreateCleanup %s %s", id, loss)
}

type surfaceConfig int

const (
	surfaceStateOnly surfaceConfig = iota
	surfaceGit
	surfaceCoreLifecycle
	surfaceFullLifecycle
)

// surfaceHandler composes the handler as daemon.Serve does for each host
// configuration: no Git, Git without runtime, and Git with runtime.
func surfaceHandler(t *testing.T, config surfaceConfig) (Handler, *surfaceCalls) {
	t.Helper()
	calls := &surfaceCalls{}
	var life LifecycleAPI
	switch config {
	case surfaceCoreLifecycle:
		life = coreLife{calls}
	case surfaceFullLifecycle:
		life = fullLife{coreLife{calls}}
	}
	h, _ := surfaceHandlerFor(t, config != surfaceStateOnly, life)
	return h, calls
}

// surfaceHandlerFor composes the handler around life, or without runtime
// when life is nil, and returns its store.
func surfaceHandlerFor(t *testing.T, git bool, life LifecycleAPI) (Handler, *Store) {
	t.Helper()
	store, _ := openTestStore(t)
	if err := store.CreateProject(context.Background(), "team/app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if !git {
		return StateHandler(store), store
	}
	info := &GitInfo{Endpoint: "127.0.0.1:2222"}
	handler := StateHandlerWithGit(store, fixtureGitReader{}, info)
	if life != nil {
		handler = StateHandlerWithLifecycle(store, fixtureGitReader{}, info, life)
	}
	return OriginHandler(handler, OriginAPI{Store: store, Runner: &fakePublication{}, Git: fixtureGitReader{}}), store
}

// cleanupLife offers failed-create cleanup but not its assembled variant.
type cleanupLife struct{ coreLife }

func (l cleanupLife) PreviewCreateCleanup(_ context.Context, id string) (CreateCleanupPreview, error) {
	return CreateCleanupPreview{}, l.record("PreviewCreateCleanup %s", id)
}
func (l cleanupLife) ConfirmCreateCleanup(_ context.Context, id, key, token string) (api.Operation, error) {
	return api.Operation{}, l.record("ConfirmCreateCleanup %s %s %s", id, key, token)
}

// repairLife offers repair but not previews of a prepared repair.
type repairLife struct{ coreLife }

func (l repairLife) PreviewRepair(_ context.Context, id string) (RepairPreview, error) {
	return RepairPreview{}, l.record("PreviewRepair %s", id)
}
func (l repairLife) ConfirmRepair(_ context.Context, r RepairConfirmRequest) (api.Operation, error) {
	return api.Operation{}, l.record("ConfirmRepair %+v", r)
}

func surfaceCall(t *testing.T, h Handler, ctx context.Context, method, params string) (map[string]any, *api.RPCError) {
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

func TestCapabilitiesListEachConfigurationInOrder(t *testing.T) {
	system := []string{"system.hello", "system.health", "system.capabilities", "system.inspect"}
	git := append(append([]string{}, system...), "project.list", "project.branches")
	core := []string{"project.create", "session.create", "session.inspect", "session.list", "session.start", "session.stop", "operation.inspect", "operation.list", "operation.retry"}
	origin := []string{"origin.change", "origin.refresh", "origin.inspect", "origin.sources", "origin.publication.preview", "origin.publish", "project.retained_branches"}
	optional := []string{
		"session.services", "session.service.journal", "session.service.action",
		"session.attach",
		"environment.cache.list", "environment.cache.preview", "environment.cache.collect",
		"workspace.inspect", "workspace.loss.inspect",
		"session.removal.preview", "session.discard", "session.delete",
		"session.rename", "project.retained.rename",
		"project.delete.preview", "project.delete.confirm",
		"project.retained.delete.preview", "project.retained.delete.confirm",
		"session.repair.preview", "session.repair.confirm", "session.repair.prepare",
		"session.ref.repair.preview", "session.ref.repair.confirm",
		"session.principal.repair.preview", "session.principal.repair.confirm",
		"session.record.repair.preview", "session.record.repair.confirm",
		"session.create.replace.preview", "session.create.replace.confirm",
		"session.create.cleanup.preview", "session.create.cleanup.confirm",
	}
	for _, test := range []struct {
		name      string
		config    surfaceConfig
		available []string
		lifecycle string
		git       bool
	}{
		{"state only", surfaceStateOnly, system, "unavailable", false},
		{"git", surfaceGit, append(append([]string{}, git...), origin...), "unavailable", true},
		{"core lifecycle", surfaceCoreLifecycle, append(append(append([]string{}, git...), core...), origin...), "partial", true},
		{"full lifecycle", surfaceFullLifecycle, append(append(append(append([]string{}, git...), core...), optional...), origin...), "partial", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, _ := surfaceHandler(t, test.config)
			result, rpcErr := surfaceCall(t, h, context.Background(), "system.capabilities", `{"v":1}`)
			if rpcErr != nil {
				t.Fatal(rpcErr)
			}
			var available []string
			for _, method := range result["available"].([]any) {
				available = append(available, method.(string))
			}
			if !reflect.DeepEqual(available, test.available) {
				t.Errorf("available:\n got %q\nwant %q", available, test.available)
			}
			if result["lifecycle"] != test.lifecycle || (result["git"] != nil) != test.git || result["v"] != float64(1) {
				t.Errorf("capability facts: %v", result)
			}
		})
	}
}

func TestLifecycleMethodsRouteDecodedRequests(t *testing.T) {
	uuid, uuid2, oid, token, key := surfaceUUID, surfaceUUID2, surfaceOID, surfaceToken, surfaceKey
	for _, test := range []struct {
		method, params, call string
		// The result object's keys, and for an operation or session envelope
		// the identity the lifecycle returned.
		keys     []string
		identity string
	}{
		{"project.create", `{"v":1,"key":"k","project":"team/app","url":"ssh://host/repo"}`, "CreateProject {Key:k Project:team/app URL:ssh://host/repo}", []string{"operation", "v"}, "created-project"},
		{"session.create", `{"v":1,"key":"k","project":"team/app","branch":"work","choice":"existing"}`, "CreateSession {Key:k Project:team/app Branch:work Choice:existing Source: OriginRef: ExpectedCommitOID: ExpectedOriginURL:}", []string{"operation", "v"}, "created-session"},
		{"session.create", `{"v":1,"key":"k","project":"team/app","branch":"work","choice":"new","origin_ref":"refs/heads/main","expected_commit_oid":"` + oid + `","expected_origin_url":"ssh://host/repo"}`, "CreateSession {Key:k Project:team/app Branch:work Choice:new Source: OriginRef:refs/heads/main ExpectedCommitOID:" + oid + " ExpectedOriginURL:ssh://host/repo}", []string{"operation", "v"}, "created-session"},
		{"session.create.cleanup.preview", `{"v":1,"uuid":"` + uuid + `"}`, "PreviewCreateCleanup " + uuid, []string{"preview", "v"}, ""},
		{"session.create.cleanup.preview", `{"v":1,"uuid":"` + uuid + `","loss_operation_id":"` + uuid2 + `"}`, "PreviewAssembledCreateCleanup " + uuid + " " + uuid2, []string{"preview", "v"}, ""},
		{"session.create.cleanup.confirm", `{"v":1,"uuid":"` + uuid + `","key":"k","confirmation_token":"` + token + `"}`, "ConfirmCreateCleanup " + uuid + " k " + token, []string{"operation", "v"}, "cleanup"},
		{"session.create.replace.preview", `{"v":1,"old_uuid":"` + uuid + `","key":"k","project":"team/app","branch":"work","choice":"new","source":"refs/heads/main"}`, "PreviewCreateReplace " + uuid + " {Key:k Project:team/app Branch:work Choice:new Source:refs/heads/main OriginRef: ExpectedCommitOID: ExpectedOriginURL:}", []string{"preview", "v"}, ""},
		{"session.create.replace.confirm", `{"v":1,"old_uuid":"` + uuid + `","key":"k","confirmation_token":"` + token + `"}`, "ConfirmCreateReplace " + uuid + " k " + token, []string{"operation", "v"}, "replace"},
		{"workspace.inspect", `{"v":1,"key":"k","uuid":"` + uuid + `"}`, "InspectWorkspace {Key:k SessionUUID:" + uuid + "}", []string{"operation", "v"}, "inspect"},
		{"workspace.loss.inspect", `{"v":1,"key":"k","uuid":"` + uuid + `"}`, "InspectWorkspaceLoss {Key:k SessionUUID:" + uuid + "}", []string{"operation", "v"}, "loss"},
		{"session.removal.preview", `{"v":1,"uuid":"` + uuid + `","kind":"discard","loss_operation_id":"` + uuid2 + `"}`, "PreviewRemoval {UUID:" + uuid + " Kind:discard LossOperationID:" + uuid2 + " AcknowledgeMissingRuntime:false}", []string{"preview", "v"}, ""},
		{"session.removal.preview", `{"v":1,"uuid":"` + uuid + `","kind":"delete","acknowledge_missing_runtime":true}`, "PreviewRemoval {UUID:" + uuid + " Kind:delete LossOperationID: AcknowledgeMissingRuntime:true}", []string{"preview", "v"}, ""},
		{"session.discard", `{"v":1,"key":"k","uuid":"` + uuid + `","confirmation_token":"` + token + `"}`, "DiscardSession {Key:k UUID:" + uuid + " ConfirmationToken:" + token + "}", []string{"operation", "v"}, "discard"},
		{"session.delete", `{"v":1,"key":"k","uuid":"` + uuid + `","confirmation_token":"` + token + `"}`, "DeleteSession {Key:k UUID:" + uuid + " ConfirmationToken:" + token + "}", []string{"operation", "v"}, "delete"},
		{"session.rename", `{"v":1,"key":"k","uuid":"` + uuid + `","new_branch":"next","expected_old_tip":"` + oid + `"}`, "RenameSession {Key:k UUID:" + uuid + " NewBranch:next ExpectedOldTip:" + oid + "}", []string{"operation", "v"}, "rename"},
		{"project.retained.rename", `{"v":1,"key":"k","project":"team/app","old_branch":"old","new_branch":"new","expected_old_tip":"` + oid + `"}`, "RenameRetainedBranch {Key:k Project:team/app OldBranch:old NewBranch:new ExpectedOldTip:" + oid + "}", []string{"operation", "v"}, "retained-rename"},
		{"project.delete.preview", `{"v":1,"project":"team/app","loss_operations":{"` + uuid + `":"` + uuid2 + `"},"acknowledge_missing":["` + uuid2 + `"]}`, "PreviewProjectDelete {Project:team/app LossOperations:map[" + uuid + ":" + uuid2 + "] AcknowledgeMissing:[" + uuid2 + "]}", []string{"preview", "v"}, ""},
		{"project.delete.confirm", `{"v":1,"key":"k","project":"team/app","confirmation_token":"` + token + `"}`, "ConfirmProjectDelete {Key:k Project:team/app ConfirmationToken:" + token + "}", []string{"operation", "v"}, "project-delete"},
		{"project.retained.delete.preview", `{"v":1,"project":"team/app","branch":"saved"}`, "PreviewRetainedDelete team/app saved", []string{"preview", "v"}, ""},
		{"project.retained.delete.confirm", `{"v":1,"key":"k","project":"team/app","branch":"saved","confirmation_token":"` + token + `"}`, "ConfirmRetainedDelete k team/app saved " + token, []string{"operation", "v"}, "retained-delete"},
		{"session.principal.repair.preview", `{"v":1,"uuid":"` + uuid + `"}`, "PreviewPrincipalRepair " + uuid, []string{"preview", "v"}, ""},
		{"session.principal.repair.confirm", `{"v":1,"key":"k","uuid":"` + uuid + `","confirmation_token":"` + token + `"}`, "ConfirmPrincipalRepair k " + uuid + " " + token, []string{"operation", "v"}, "principal-repair"},
		{"session.record.repair.preview", `{"v":1,"uuid":"` + uuid + `"}`, "PreviewRecordRepair " + uuid, []string{"preview", "v"}, ""},
		{"session.record.repair.confirm", `{"v":1,"key":"k","uuid":"` + uuid + `","confirmation_token":"` + token + `"}`, "ConfirmRecordRepair k " + uuid + " " + token, []string{"operation", "v"}, "record-repair"},
		{"session.ref.repair.preview", `{"v":1,"uuid":"` + uuid + `","loss_operation_id":"` + uuid2 + `"}`, "PreviewRefRepair " + uuid + " " + uuid2, []string{"preview", "v"}, ""},
		{"session.ref.repair.confirm", `{"v":1,"key":"k","uuid":"` + uuid + `","confirmation_token":"` + token + `"}`, "ConfirmRefRepair k " + uuid + " " + token, []string{"operation", "v"}, "ref-repair"},
		{"session.repair.preview", `{"v":1,"uuid":"` + uuid + `"}`, "PreviewRepair " + uuid, []string{"preview", "v"}, ""},
		{"session.repair.preview", `{"v":1,"uuid":"` + uuid + `","preparation_operation_id":"` + uuid2 + `"}`, "PreviewPreparedRepair " + uuid + " " + uuid2, []string{"preview", "v"}, ""},
		{"session.repair.prepare", `{"v":1,"key":"k","uuid":"` + uuid + `"}`, "PrepareRepair k " + uuid, []string{"operation", "v"}, "prepare"},
		{"session.repair.confirm", `{"v":1,"key":"k","uuid":"` + uuid + `","confirmation_token":"` + token + `"}`, "ConfirmRepair {Key:k UUID:" + uuid + " ConfirmationToken:" + token + "}", []string{"operation", "v"}, "repair"},
		{"operation.retry", `{"v":1,"id":"op-1"}`, "Retry op-1", []string{"operation", "v"}, "op-1"},
		{"session.inspect", `{"v":1,"uuid":"` + uuid + `"}`, "InspectSession " + uuid, []string{"session", "v"}, uuid},
		{"session.start", `{"v":1,"uuid":"` + uuid + `"}`, "StartSession " + uuid, []string{"session", "v"}, uuid},
		{"session.stop", `{"v":1,"uuid":"` + uuid + `"}`, "StopSession " + uuid, []string{"session", "v"}, uuid},
		{"environment.cache.list", `{"v":1,"project":"team/app","after":"x","limit":8}`, `ListEnvironmentCache team/app "x" 8`, []string{"images", "next", "v"}, ""},
		{"environment.cache.preview", `{"v":1,"project":"team/app","environment_key":"` + key + `","confirmation_token":"t","after":"a","limit":20}`, "PreviewEnvironmentCollection team/app " + key + ` "t" "a" 20`, []string{"preview", "v"}, ""},
		{"environment.cache.collect", `{"v":1,"key":"k","confirmation_token":"` + token + `"}`, "CollectEnvironmentCache k " + token, []string{"operation", "v"}, "collect"},
		{"session.services", `{"v":1,"uuid":"` + uuid + `"}`, "SessionServices " + uuid + "  list", []string{"observed_at", "uuid", "v"}, ""},
		{"session.service.journal", `{"v":1,"uuid":"` + uuid + `","unit":"` + surfaceUnit + `"}`, "SessionServices " + uuid + " " + surfaceUnit + " journal", []string{"observed_at", "unit", "uuid", "v"}, ""},
		{"session.service.action", `{"v":1,"uuid":"` + uuid + `","unit":"` + surfaceUnit + `","action":"restart"}`, "SessionServices " + uuid + " " + surfaceUnit + " restart", []string{"observed_at", "unit", "uuid", "v"}, ""},
		{"session.attach", `{"v":1,"uuid":"` + uuid + `"}`, "AttachSession " + uuid, []string{"expires_at", "spec", "token", "v"}, ""},
		{"attachment.confirm", `{"v":1,"operation":"/1.0/operations/` + uuid + `"}`, "ConfirmAttachment /1.0/operations/" + uuid, []string{"v"}, ""},
		{"attachment.ping", `{"v":1}`, "", []string{"v"}, ""},
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

func TestLifecycleMethodsRejectMalformedParameters(t *testing.T) {
	uuid, token := surfaceUUID, surfaceToken
	for _, test := range []struct{ method, params string }{
		{"project.create", `{"v":1,"key":"k","project":"team/app","extra":1}`},
		{"project.create", `{"v":2,"key":"k","project":"team/app"}`},
		{"project.create", `{"v":1,"key":"","project":"team/app"}`},
		{"project.create", `{"v":1,"key":"k","project":"/abs"}`},
		{"session.create", `{"v":1,"key":"k","project":"team/app","branch":"work","choice":"existing","source":"refs/heads/main"}`},
		{"session.create", `{"v":1,"key":"k","project":"team/app","branch":"work..x","choice":"existing"}`},
		{"session.create.cleanup.preview", `{"v":1,"uuid":"short"}`},
		{"session.create.cleanup.preview", `{"v":1,"uuid":"` + uuid + `","loss_operation_id":"bad"}`},
		{"session.create.cleanup.confirm", `{"v":1,"uuid":"` + uuid + `","key":"k","confirmation_token":"ABCDEF0123456789abcdef0123456789"}`},
		{"session.create.replace.preview", `{"v":1,"old_uuid":"` + uuid + `","key":"k","project":"team/app","branch":"work","choice":"maybe"}`},
		{"session.create.replace.confirm", `{"v":1,"old_uuid":"` + uuid + `","key":"","confirmation_token":"` + token + `"}`},
		{"workspace.inspect", `{"v":1,"key":"","uuid":"` + uuid + `"}`},
		{"workspace.loss.inspect", `{"v":1,"key":"k","uuid":"bad"}`},
		{"session.removal.preview", `{"v":1,"uuid":"` + uuid + `","kind":"discard"}`},
		{"session.removal.preview", `{"v":1,"uuid":"` + uuid + `","kind":"discard","loss_operation_id":"x","acknowledge_missing_runtime":true}`},
		{"session.removal.preview", `{"v":1,"uuid":"` + uuid + `","kind":"stop","loss_operation_id":"x"}`},
		{"session.discard", `{"v":1,"key":"k","uuid":"` + uuid + `","confirmation_token":"short"}`},
		{"session.delete", `{"v":1,"key":"k","uuid":"` + uuid + `","confirmation_token":"short"}`},
		{"session.rename", `{"v":1,"key":"k","uuid":"` + uuid + `","new_branch":"next","expected_old_tip":"0000000000000000000000000000000000000000"}`},
		{"project.retained.rename", `{"v":1,"key":"k","project":"team/app","old_branch":"same","new_branch":"same","expected_old_tip":"` + surfaceOID + `"}`},
		{"project.delete.preview", `{"v":1,"project":"team/app","loss_operations":{"bad":"` + uuid + `"}}`},
		{"project.delete.preview", `{"v":1,"project":"team/app","loss_operations":{},"acknowledge_missing":["bad"]}`},
		{"project.delete.confirm", `{"v":1,"key":"k","project":"team/app","confirmation_token":"short"}`},
		{"project.retained.delete.preview", `{"v":1,"project":"team/app","branch":".hidden"}`},
		{"project.retained.delete.confirm", `{"v":1,"key":"","project":"team/app","branch":"saved","confirmation_token":"` + token + `"}`},
		{"session.principal.repair.preview", `{"v":1,"uuid":"bad"}`},
		{"session.principal.repair.confirm", `{"v":1,"key":"k","uuid":"` + uuid + `","confirmation_token":"short"}`},
		{"session.record.repair.preview", `{"v":1,"uuid":"bad"}`},
		{"session.record.repair.confirm", `{"v":1,"key":"","uuid":"` + uuid + `","confirmation_token":"` + token + `"}`},
		{"session.ref.repair.preview", `{"v":1,"uuid":"` + uuid + `"}`},
		{"session.ref.repair.confirm", `{"v":1,"key":"k","uuid":"` + uuid + `","confirmation_token":"short"}`},
		{"session.repair.preview", `{"v":1,"uuid":"` + uuid + `","preparation_operation_id":"bad"}`},
		{"session.repair.prepare", `{"v":1,"key":"","uuid":"` + uuid + `"}`},
		{"session.repair.confirm", `{"v":1,"key":"k","uuid":"` + uuid + `","confirmation_token":"short"}`},
		{"operation.inspect", `{"v":1,"id":""}`},
		{"operation.retry", `{"v":1}`},
		{"operation.list", `{"v":1,"limit":21}`},
		{"session.list", `{"v":1,"limit":9}`},
		{"session.inspect", `{"v":1,"uuid":"bad"}`},
		{"session.start", `{"v":1}`},
		{"session.stop", `{"v":1,"uuid":"` + uuid + `","extra":1}`},
		{"environment.cache.list", `{"v":1,"project":"team/app","limit":9}`},
		{"environment.cache.preview", `{"v":1,"project":"team/app","environment_key":"short","limit":20}`},
		{"environment.cache.collect", `{"v":1,"key":"k","confirmation_token":"short"}`},
		{"session.services", `{"v":1,"uuid":"` + uuid + `","unit":"` + surfaceUnit + `"}`},
		{"session.service.journal", `{"v":1,"uuid":"` + uuid + `","unit":"sshd.service"}`},
		{"session.service.action", `{"v":1,"uuid":"` + uuid + `","unit":"` + surfaceUnit + `","action":"reload"}`},
		{"session.attach", `{"v":1,"uuid":"bad"}`},
		{"attachment.confirm", `{"v":1,"operation":"/1.0/operations/short"}`},
		{"attachment.ping", `{"v":1,"operation":"/1.0/operations/` + uuid + `"}`},
	} {
		t.Run(test.method+" "+test.params, func(t *testing.T) {
			h, calls := surfaceHandler(t, surfaceFullLifecycle)
			_, rpcErr := surfaceCall(t, h, surfaceConnectionContext(true), test.method, test.params)
			if rpcErr == nil || rpcErr.Code != -32602 || rpcErr.Kind != "invalid_params" {
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
		{"attach without a connection", context.Background(), "session.attach", `{"v":1,"uuid":"` + surfaceUUID + `"}`},
		{"confirm on an ordinary connection", surfaceConnectionContext(false), "attachment.confirm", `{"v":1,"operation":"/1.0/operations/` + surfaceUUID + `"}`},
		{"ping on an ordinary connection", surfaceConnectionContext(false), "attachment.ping", `{"v":1}`},
		{"claim from a process that is not the helper", surfaceConnectionContext(false), "attachment.claim", `{"v":1,"token":"` + strings.Repeat("a", 64) + `"}`},
	} {
		_, rpcErr := surfaceCall(t, h, test.ctx, test.method, test.params)
		if rpcErr == nil || rpcErr.Code != -32602 || rpcErr.Kind != "invalid_params" {
			t.Errorf("%s: answer %+v, want invalid_params", test.name, rpcErr)
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
		{"operation.list", `{"v":1,"limit":20}`, `{"next":"","operations":[],"v":1}`},
		{"session.list", `{"v":1,"limit":8}`, `{"next":"","sessions":[],"v":1}`},
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
	_, rpcErr := h(context.Background(), "operation.inspect", json.RawMessage(`{"v":1,"id":"missing"}`))
	if rpcErr == nil || *rpcErr != (api.RPCError{Code: -32004, Kind: "unavailable", Message: "record was not found"}) {
		t.Errorf("missing operation: %+v", rpcErr)
	}
	if len(calls.calls) != 0 {
		t.Errorf("lifecycle calls: %q", calls.calls)
	}
}

func TestOperationReadsInspectAndPageTheJournal(t *testing.T) {
	h, store := surfaceHandlerFor(t, true, fullLife{coreLife{&surfaceCalls{}}})
	dropOperationInsertTriggers(t, store)
	long := strings.Repeat("d", 600)
	for _, row := range []struct{ id, kind, session, status, phase, diagnostic string }{
		{"op-a", "project.retained.rename", "", "completed", "completed", ""},
		{"op-b", "session.rename", surfaceUUID, "blocked", "stale", long},
		{"op-c", "session.discard", surfaceUUID2, "running", "reserved", long},
	} {
		if _, err := store.db.Exec(`INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,committed,evidence_json,diagnostic,created_at,updated_at)
		 VALUES(?,?,?,'team/app',NULLIF(?,''),'{"key":"k"}','x',?,?,1,'{"e":1}',?,'2026-01-01T00:00:00Z','2026-01-02T00:00:00Z')`,
			row.id, "key-"+row.id, row.kind, row.session, row.status, row.phase, row.diagnostic); err != nil {
			t.Fatal(err)
		}
	}
	summary := func(id, kind, session, status, phase, diagnostic string) string {
		out := `{"id":"` + id + `","idempotency_key":"key-` + id + `","kind":"` + kind + `","project":"team/app",`
		if session != "" {
			out += `"session_uuid":"` + session + `",`
		}
		out += `"status":"` + status + `","phase":"` + phase + `","committed":true`
		if diagnostic != "" {
			out += `,"diagnostic":"` + diagnostic + `"`
		}
		return out + "}"
	}
	for _, test := range []struct {
		method, params, want string
	}{
		{"operation.inspect", `{"v":1,"id":"op-b"}`, `{"operation":{"id":"op-b","idempotency_key":"key-op-b","kind":"session.rename","project":"team/app","session_uuid":"` + surfaceUUID +
			`","request":{"key":"k"},"status":"blocked","phase":"stale","committed":true,"evidence":{"e":1},"diagnostic":"` + long + `","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"},"v":1}`},
		// Summaries keep 512 diagnostic bytes for Rename and 256 for others.
		{"operation.list", `{"v":1,"limit":2}`, `{"next":"op-b","operations":[` + summary("op-a", "project.retained.rename", "", "completed", "completed", "") + "," +
			summary("op-b", "session.rename", surfaceUUID, "blocked", "stale", long[:512]) + `],"v":1}`},
		{"operation.list", `{"v":1,"limit":2,"after":"op-b"}`, `{"next":"","operations":[` + summary("op-c", "session.discard", surfaceUUID2, "running", "reserved", long[:256]) + `],"v":1}`},
	} {
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

func TestLifecycleErrorsMapToRPCErrors(t *testing.T) {
	discard := `{"v":1,"key":"k","uuid":"` + surfaceUUID + `","confirmation_token":"` + surfaceToken + `"}`
	projectPreview := `{"v":1,"project":"team/app","loss_operations":{}}`
	projectConfirm := `{"v":1,"key":"k","project":"team/app","confirmation_token":"` + surfaceToken + `"}`
	services := `{"v":1,"uuid":"` + surfaceUUID + `"}`
	long := strings.Repeat("x", 1000)
	for _, test := range []struct {
		name, method, params string
		err                  error
		want                 api.RPCError
	}{
		{"invalid", "session.discard", discard, ErrInvalid, api.RPCError{Code: -32602, Kind: "invalid_params", Message: "invalid lifecycle request"}},
		{"capacity", "session.discard", discard, &sessionCapacityError{}, api.RPCError{Code: -32003, Kind: "busy", Message: (&sessionCapacityError{}).Error()}},
		{"conflict", "session.discard", discard, ErrConflict, api.RPCError{Code: -32003, Kind: "busy", Message: "lifecycle request conflicts with current authority"}},
		{"not found", "session.discard", discard, ErrNotFound, api.RPCError{Code: -32004, Kind: "unavailable", Message: "record was not found"}},
		{"cancelled", "session.discard", discard, context.Canceled, api.RPCError{Code: -32001, Kind: "cancelled", Message: "request cancelled or timed out"}},
		{"deadline", "session.discard", discard, context.DeadlineExceeded, api.RPCError{Code: -32001, Kind: "cancelled", Message: "request cancelled or timed out"}},
		{"other", "session.discard", discard, errors.New("boom"), api.RPCError{Code: -32004, Kind: "unavailable", Message: "lifecycle authority is unavailable"}},
		{"project conflict keeps message", "project.delete.preview", projectPreview, errors.Join(ErrConflict, errors.New("session still attached")), api.RPCError{Code: -32003, Kind: "busy", Message: "conflict\nsession still attached"}},
		{"project conflict message is bounded", "project.delete.preview", projectPreview, errors.Join(ErrConflict, errors.New(long)), api.RPCError{Code: -32003, Kind: "busy", Message: ("conflict\n" + long)[:900]}},
		{"project invalid", "project.delete.preview", projectPreview, ErrInvalid, api.RPCError{Code: -32602, Kind: "invalid_params", Message: "invalid lifecycle request"}},
		{"project not found", "project.delete.preview", projectPreview, ErrNotFound, api.RPCError{Code: -32004, Kind: "unavailable", Message: "project deletion facts unavailable; Stop/detach sessions and obtain fresh loss inspections; finish pending cleanup/repair/collection, or investigate exact native/credential/cache ownership before a new review"}},
		{"project confirm conflict keeps message", "project.delete.confirm", projectConfirm, errors.Join(ErrConflict, errors.New("review changed")), api.RPCError{Code: -32003, Kind: "busy", Message: "conflict\nreview changed"}},
		{"project confirm cancelled", "project.delete.confirm", projectConfirm, context.Canceled, api.RPCError{Code: -32001, Kind: "cancelled", Message: "request cancelled or timed out"}},
		{"project confirm other", "project.delete.confirm", projectConfirm, errors.New("boom"), api.RPCError{Code: -32004, Kind: "unavailable", Message: "project deletion facts unavailable; Stop/detach sessions and obtain fresh loss inspections; finish pending cleanup/repair/collection, or investigate exact native/credential/cache ownership before a new review"}},
		{"services conflict", "session.services", services, ErrConflict, api.RPCError{Code: -32003, Kind: "busy", Message: "lifecycle request conflicts with current authority"}},
		{"services other", "session.services", services, errors.New("boom"), api.RPCError{Code: -32004, Kind: "unavailable", Message: "project services unavailable; require a ready owned runtime, session user manager, and compatible selected runtime package"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, calls := surfaceHandler(t, surfaceFullLifecycle)
			calls.err = test.err
			_, rpcErr := surfaceCall(t, h, context.Background(), test.method, test.params)
			if rpcErr == nil || *rpcErr != test.want {
				t.Fatalf("answer %+v, want %+v", rpcErr, test.want)
			}
		})
	}
}

func TestUnconfiguredAndUnknownMethodAnswers(t *testing.T) {
	notImplemented := api.RPCError{Code: -32004, Kind: "unavailable", Message: "capability is not implemented"}
	notFound := api.RPCError{Code: -32004, Kind: "unavailable", Message: "record was not found"}
	badParams := api.RPCError{Code: -32602, Kind: "invalid_params", Message: `params must be {"v":1}`}
	unknown := api.RPCError{Code: -32601, Kind: "method_not_found", Message: "method is unknown"}
	discard := `{"v":1,"key":"k","uuid":"` + surfaceUUID + `","confirmation_token":"` + surfaceToken + `"}`
	for _, test := range []struct {
		name           string
		config         surfaceConfig
		method, params string
		want           api.RPCError
	}{
		{"unknown method", surfaceStateOnly, "nope", `{"v":1}`, unknown},
		{"unknown method with other params", surfaceStateOnly, "nope", `{"v":1,"x":1}`, badParams},
		{"known prefix without capability", surfaceStateOnly, "session.create", `{"v":1}`, notImplemented},
		{"origin prefix without capability", surfaceStateOnly, "origin.inspect", `{"v":1}`, notImplemented},
		{"runtime prefix", surfaceStateOnly, "runtime.inspect", `{"v":1}`, notImplemented},
		{"status prefix", surfaceStateOnly, "status.report", `{"v":1}`, notImplemented},
		{"lifecycle params without capability", surfaceStateOnly, "session.discard", discard, badParams},
		{"project list without Git", surfaceStateOnly, "project.list", `{"v":1,"limit":1}`, api.RPCError{Code: -32004, Kind: "unavailable", Message: "Git capability is not configured"}},
		{"lifecycle without runtime", surfaceGit, "session.create", `{"v":1}`, notImplemented},
		{"attachment without runtime", surfaceGit, "attachment.ping", `{"v":1}`, unknown},
		{"system method rejects params", surfaceGit, "system.hello", `{"v":1,"x":1}`, badParams},
		{"discard without its API", surfaceCoreLifecycle, "session.discard", discard, notFound},
		{"services without their API", surfaceCoreLifecycle, "session.services", `{"v":1,"uuid":"` + surfaceUUID + `"}`, notFound},
		{"attach without its API", surfaceCoreLifecycle, "session.attach", `{"v":1,"uuid":"` + surfaceUUID + `"}`, notFound},
		{"cache without its API", surfaceCoreLifecycle, "environment.cache.list", `{"v":1,"project":"team/app","limit":1}`, notFound},
		{"cleanup without its API", surfaceCoreLifecycle, "session.create.cleanup.preview", `{"v":1,"uuid":"` + surfaceUUID + `"}`, notFound},
		{"repair preview without its API", surfaceCoreLifecycle, "session.repair.preview", `{"v":1,"uuid":"` + surfaceUUID + `"}`, notFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, calls := surfaceHandler(t, test.config)
			_, rpcErr := surfaceCall(t, h, surfaceConnectionContext(true), test.method, test.params)
			if rpcErr == nil || *rpcErr != test.want {
				t.Fatalf("answer %+v, want %+v", rpcErr, test.want)
			}
			if len(calls.calls) != 0 {
				t.Fatalf("reached the lifecycle: %q", calls.calls)
			}
		})
	}
}

func TestSpecializedPreviewsNeedTheirOwnAPI(t *testing.T) {
	notFound := api.RPCError{Code: -32004, Kind: "unavailable", Message: "record was not found"}
	for _, test := range []struct {
		name           string
		life           func(*surfaceCalls) LifecycleAPI
		method, params string
	}{
		{"assembled cleanup", func(c *surfaceCalls) LifecycleAPI { return cleanupLife{coreLife{c}} },
			"session.create.cleanup.preview", `{"v":1,"uuid":"` + surfaceUUID + `","loss_operation_id":"` + surfaceUUID2 + `"}`},
		{"prepared repair", func(c *surfaceCalls) LifecycleAPI { return repairLife{coreLife{c}} },
			"session.repair.preview", `{"v":1,"uuid":"` + surfaceUUID + `","preparation_operation_id":"` + surfaceUUID2 + `"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := &surfaceCalls{}
			h, _ := surfaceHandlerFor(t, true, test.life(calls))
			_, rpcErr := h(context.Background(), test.method, json.RawMessage(test.params))
			if rpcErr == nil || *rpcErr != notFound {
				t.Fatalf("answer %+v, want %+v", rpcErr, notFound)
			}
			if len(calls.calls) != 0 {
				t.Fatalf("reached the lifecycle: %q", calls.calls)
			}
		})
	}
}
