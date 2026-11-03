package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Each request type's Validate carries the bounds its handler checked before
// the types were shared.
func TestRequestBounds(t *testing.T) {
	uuid := "550e8400-e29b-41d4-a716-446655440000"
	oid := strings.Repeat("a", 40)
	token := strings.Repeat("0", 31) + "f"
	key := strings.Repeat("k", 128)
	operation := "/1.0/operations/" + uuid
	create := SessionCreateRequest{Key: "k", Project: "team/app", Branch: "work", Choice: "existing"}
	for _, test := range []struct {
		name  string
		req   Request
		valid bool
	}{
		{"hello", HelloRequest{Protocol: Protocol}, true},
		{"hello without a protocol", HelloRequest{}, false},
		{"hello with a long protocol", HelloRequest{Protocol: strings.Repeat("p", 65)}, false},
		{"cancel by integer", CancelRequest{ID: json.RawMessage(`7`)}, true},
		{"cancel by string", CancelRequest{ID: json.RawMessage(`"wait"`)}, true},
		{"cancel by a long string", CancelRequest{ID: json.RawMessage(`"` + strings.Repeat("x", 129) + `"`)}, false},
		{"cancel without id", CancelRequest{}, false},
		{"cancel by boolean", CancelRequest{ID: json.RawMessage(`true`)}, false},
		{"project page", ProjectListRequest{Limit: 100, After: "team/app"}, true},
		{"project page over 100", ProjectListRequest{Limit: 101}, false},
		{"project page with a bad cursor", ProjectListRequest{Limit: 1, After: "/abs"}, false},
		{"branch page", ProjectBranchesRequest{Project: "team/app", Limit: 8, After: "refs/heads/main"}, true},
		{"branch page over 8", ProjectBranchesRequest{Project: "team/app", Limit: 9}, false},
		{"branch page with a bare branch cursor", ProjectBranchesRequest{Project: "team/app", Limit: 8, After: "main"}, false},
		{"retained page", ProjectRetainedBranchesRequest{Project: "team/app", Limit: 8, After: "refs/heads/saved"}, true},
		{"retained page with a bare branch cursor", ProjectRetainedBranchesRequest{Project: "team/app", Limit: 8, After: "saved"}, false},
		{"origin set", OriginChangeRequest{Key: "k", Project: "team/app", Kind: "set", URL: "ssh://host/repo"}, true},
		{"origin remove", OriginChangeRequest{Key: "k", Project: "team/app", Kind: "remove", ExpectedURL: "ssh://host/repo"}, true},
		{"origin remove with a URL", OriginChangeRequest{Key: "k", Project: "team/app", Kind: "remove", URL: "ssh://host/repo"}, false},
		{"origin set without a key", OriginChangeRequest{Project: "team/app", Kind: "set", URL: "ssh://host/repo"}, false},
		{"origin refresh", OriginRefreshRequest{Project: "team/app"}, true},
		{"origin inspect of a bad project", OriginInspectRequest{Project: "../app"}, false},
		{"origin sources", OriginSourcesRequest{Project: "team/app", Limit: 16, After: "refs/tags/v1"}, true},
		{"origin sources over 16", OriginSourcesRequest{Project: "team/app", Limit: 17}, false},
		{"publication preview", OriginPublicationPreviewRequest{ExpectedOriginURL: "ssh://host/repo", Project: "team/app", Kind: "retained", Source: "saved", SourceOID: oid, DestinationRef: "refs/heads/main"}, true},
		{"publication preview to a tag", OriginPublicationPreviewRequest{ExpectedOriginURL: "ssh://host/repo", Project: "team/app", Kind: "session", Source: uuid, SourceOID: oid, DestinationRef: "refs/tags/v1"}, false},
		{"publication preview without the origin", OriginPublicationPreviewRequest{Project: "team/app", Kind: "session", Source: uuid, SourceOID: oid, DestinationRef: "refs/heads/main"}, false},
		{"publish", OriginPublishRequest{ExpectedOriginURL: "ssh://host/repo", Key: key, Project: "team/app", Kind: "session", Source: uuid, SourceOID: oid, DestinationRef: "refs/heads/main"}, true},
		{"publish with a long key", OriginPublishRequest{ExpectedOriginURL: "ssh://host/repo", Key: key + "k", Project: "team/app", Kind: "session", Source: uuid, SourceOID: oid, DestinationRef: "refs/heads/main"}, false},
		{"project create", ProjectCreateRequest{Key: "k", Project: "team/app", URL: "ssh://host/repo"}, true},
		{"project create without a key", ProjectCreateRequest{Project: "team/app"}, false},
		{"session create on an existing branch", create, true},
		{"session create from a P source", SessionCreateRequest{Key: "k", Project: "team/app", Branch: "work", Choice: "new", Source: "refs/heads/main"}, true},
		{"session create from the origin", SessionCreateRequest{Key: "k", Project: "team/app", Branch: "work", Choice: "new", OriginRef: "refs/heads/main", ExpectedCommitOID: oid, ExpectedOriginURL: "ssh://host/repo"}, true},
		{"session create of an existing branch with a source", SessionCreateRequest{Key: "k", Project: "team/app", Branch: "work", Choice: "existing", Source: "refs/heads/main"}, false},
		{"session create with an origin URL but no origin ref", SessionCreateRequest{Key: "k", Project: "team/app", Branch: "work", Choice: "new", Source: "refs/heads/main", ExpectedOriginURL: "ssh://host/repo"}, false},
		{"session create with a bad branch", SessionCreateRequest{Key: "k", Project: "team/app", Branch: "work..x", Choice: "existing"}, false},
		{"session inspect", SessionInspectRequest{UUID: uuid}, true},
		{"session start of a short id", SessionStartRequest{UUID: "short"}, false},
		{"session stop", SessionStopRequest{UUID: uuid}, true},
		{"session attach", SessionAttachRequest{UUID: uuid}, true},
		{"session page", SessionListRequest{Limit: 8, After: uuid}, true},
		{"session page over 8", SessionListRequest{Limit: 9}, false},
		{"operation page", OperationListRequest{Limit: 20}, true},
		{"operation page over 20", OperationListRequest{Limit: 21}, false},
		{"operation inspect", OperationInspectRequest{ID: "op"}, true},
		{"operation retry without an id", OperationRetryRequest{}, false},
		{"removal preview", SessionRemovalPreviewRequest{UUID: uuid, Kind: "delete", AcknowledgeMissingRuntime: true}, true},
		{"removal preview of another kind", SessionRemovalPreviewRequest{UUID: uuid, Kind: "stop"}, false},
		{"discard", SessionDiscardRequest{Key: "k", UUID: uuid, ConfirmationToken: token}, true},
		{"discard with an uppercase token", SessionDiscardRequest{Key: "k", UUID: uuid, ConfirmationToken: strings.ToUpper(token)}, false},
		{"delete with a short token", SessionDeleteRequest{Key: "k", UUID: uuid, ConfirmationToken: "short"}, false},
		{"rename", SessionRenameRequest{Key: "k", UUID: uuid, NewBranch: "next", ExpectedOldTip: oid}, true},
		{"rename to the zero commit", SessionRenameRequest{Key: "k", UUID: uuid, NewBranch: "next", ExpectedOldTip: strings.Repeat("0", 40)}, false},
		{"retained rename", ProjectRetainedRenameRequest{Key: "k", Project: "team/app", OldBranch: "old", NewBranch: "new", ExpectedOldTip: oid}, true},
		{"retained rename to the same name", ProjectRetainedRenameRequest{Key: "k", Project: "team/app", OldBranch: "same", NewBranch: "same", ExpectedOldTip: oid}, false},
		{"project delete preview", ProjectDeletePreviewRequest{Project: "team/app", AcknowledgeMissing: []string{uuid}}, true},
		{"project delete preview acknowledging five", ProjectDeletePreviewRequest{Project: "team/app", AcknowledgeMissing: []string{uuid, uuid, uuid, uuid, uuid}}, false},
		{"project delete preview acknowledging a bad id", ProjectDeletePreviewRequest{Project: "team/app", AcknowledgeMissing: []string{"bad"}}, false},
		{"project delete confirm", ProjectDeleteConfirmRequest{Key: "k", Project: "team/app", ConfirmationToken: token}, true},
		{"retained delete preview", ProjectRetainedDeletePreviewRequest{Project: "team/app", Branch: "saved"}, true},
		{"retained delete preview of a hidden branch", ProjectRetainedDeletePreviewRequest{Project: "team/app", Branch: ".hidden"}, false},
		{"retained delete confirm without a key", ProjectRetainedDeleteConfirmRequest{Project: "team/app", Branch: "saved", ConfirmationToken: token}, false},
		{"replace preview", SessionCreateReplacePreviewRequest{OldUUID: uuid, Key: "k", Project: "team/app", Branch: "work", Choice: "existing"}, true},
		{"replace preview with a bad choice", SessionCreateReplacePreviewRequest{OldUUID: uuid, Key: "k", Project: "team/app", Branch: "work", Choice: "maybe"}, false},
		{"replace confirm", SessionCreateReplaceConfirmRequest{OldUUID: uuid, Key: "k", ConfirmationToken: token}, true},
		{"cleanup preview of a short id", SessionCreateCleanupPreviewRequest{UUID: "short"}, false},
		{"cleanup confirm", SessionCreateCleanupConfirmRequest{UUID: uuid, Key: "k", ConfirmationToken: token}, true},
		{"services", SessionServicesRequest{UUID: uuid}, true},
		{"service action", SessionServiceActionRequest{UUID: uuid, Name: "p-project-api.service", Action: "restart"}, true},
		{"service action on a system unit", SessionServiceActionRequest{UUID: uuid, Name: "sshd.service", Action: "stop"}, false},
		{"service action that reloads", SessionServiceActionRequest{UUID: uuid, Name: "p-project-api.service", Action: "reload"}, false},
		{"service action that lists", SessionServiceActionRequest{UUID: uuid, Action: "list"}, false},
		{"service action that reads the logs", SessionServiceActionRequest{UUID: uuid, Name: "p-project-api.service", Action: "logs"}, false},
		{"service logs", SessionServiceLogsRequest{UUID: uuid, Name: "p-project-api.service"}, true},
		{"service logs of P's own unit", SessionServiceLogsRequest{UUID: uuid, Name: "p-interactive.service"}, false},
		{"claim", AttachmentClaimRequest{Token: strings.Repeat("a", 64)}, true},
		{"claim with a short token", AttachmentClaimRequest{Token: token}, false},
		{"confirm", AttachmentConfirmRequest{Operation: operation}, true},
		{"confirm a short operation", AttachmentConfirmRequest{Operation: "/1.0/operations/short"}, false},
		{"session RPC", SessionRPCRequest{V: SessionRPCVersion}, true},
		{"session RPC of another version", SessionRPCRequest{V: 2}, false},
		{"status report", StatusReport{V: SessionRPCVersion, Condition: "idle", Adapter: "codex", AdapterVersion: "1"}, true},
		{"status report with a control character", StatusReport{V: SessionRPCVersion, Condition: "idle", Adapter: "codex", AdapterVersion: "1", Reason: "\x1b[31m"}, false},
		{"status report of an unknown condition", StatusReport{V: SessionRPCVersion, Condition: "completed", Adapter: "codex", AdapterVersion: "1"}, false},
	} {
		if err := test.req.Validate(); (err == nil) != test.valid {
			t.Errorf("%s: Validate() = %v, want valid %v", test.name, err, test.valid)
		}
	}
}

// Every request type of the protocol is covered by TestRequestBounds or takes
// no fields.
func TestEveryRequestTypeIsBounded(t *testing.T) {
	for _, m := range append(Methods(), SessionMethods()...) {
		if m.Request.NumField() == 0 {
			continue
		}
		v := reflect.New(m.Request).Elem().Interface().(Request)
		if v.Validate() == nil {
			t.Errorf("%s accepts its zero request", m.Name)
		}
	}
}
