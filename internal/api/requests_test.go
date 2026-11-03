package api

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Each request type's Validate carries the bounds its handler checked before
// the types were shared.
func TestRequestBounds(t *testing.T) {
	uuid := "550e8400-e29b-41d4-a716-446655440000"
	oid := strings.Repeat("a", 40)
	token := strings.Repeat("0", 31) + "f"
	tag := "20261102T153012Z-" + strings.Repeat("ab", 32)
	run := ChangeRequest{Key: "k", IfMatch: tag}
	publish := OriginPublishRequest{ExpectedOriginURL: "ssh://host/repo", Project: "team/app", Kind: "retained", Source: "saved", SourceOID: oid, DestinationRef: "refs/heads/main"}
	choices := CreationChoices{Project: "team/app", Branch: "work", Choice: "existing"}
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
		{"publish dry run", publish, true},
		{"publish dry run to a tag", OriginPublishRequest{ExpectedOriginURL: "ssh://host/repo", Project: "team/app", Kind: "session", Source: uuid, SourceOID: oid, DestinationRef: "refs/tags/v1"}, false},
		{"publish dry run without the origin", OriginPublishRequest{Project: "team/app", Kind: "session", Source: uuid, SourceOID: oid, DestinationRef: "refs/heads/main"}, false},
		{"publish run", OriginPublishRequest{ExpectedOriginURL: "ssh://host/repo", Project: "team/app", Kind: "session", Source: uuid, SourceOID: oid, DestinationRef: "refs/heads/main", ChangeRequest: ChangeRequest{Key: key, IfMatch: tag}}, true},
		{"publish run with a long key", OriginPublishRequest{ExpectedOriginURL: "ssh://host/repo", Project: "team/app", Kind: "session", Source: uuid, SourceOID: oid, DestinationRef: "refs/heads/main", ChangeRequest: ChangeRequest{Key: key + "k", IfMatch: tag}}, false},
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
		{"discard dry run", SessionDiscardRequest{UUID: uuid}, true},
		{"discard run", SessionDiscardRequest{UUID: uuid, ChangeRequest: run}, true},
		{"discard run with an uppercase tag", SessionDiscardRequest{UUID: uuid, ChangeRequest: ChangeRequest{Key: "k", IfMatch: strings.ToUpper(tag)}}, false},
		{"discard of a short id", SessionDiscardRequest{UUID: "short"}, false},
		{"delete run with a token", SessionDeleteRequest{UUID: uuid, ChangeRequest: ChangeRequest{Key: "k", IfMatch: token}}, false},
		{"delete run", SessionDeleteRequest{UUID: uuid, ChangeRequest: run}, true},
		{"rename", SessionRenameRequest{Key: "k", UUID: uuid, NewBranch: "next", ExpectedOldTip: oid}, true},
		{"rename to the zero commit", SessionRenameRequest{Key: "k", UUID: uuid, NewBranch: "next", ExpectedOldTip: strings.Repeat("0", 40)}, false},
		{"retained rename", ProjectRetainedRenameRequest{Key: "k", Project: "team/app", OldBranch: "old", NewBranch: "new", ExpectedOldTip: oid}, true},
		{"retained rename to the same name", ProjectRetainedRenameRequest{Key: "k", Project: "team/app", OldBranch: "same", NewBranch: "same", ExpectedOldTip: oid}, false},
		{"project delete dry run", ProjectDeleteRequest{Project: "team/app"}, true},
		{"project delete run", ProjectDeleteRequest{Project: "team/app", ChangeRequest: run}, true},
		{"project delete of a bad project", ProjectDeleteRequest{Project: "../app"}, false},
		{"retained delete dry run", ProjectRetainedDeleteRequest{Project: "team/app", Branch: "saved"}, true},
		{"retained delete of a hidden branch", ProjectRetainedDeleteRequest{Project: "team/app", Branch: ".hidden"}, false},
		{"retained delete run", ProjectRetainedDeleteRequest{Project: "team/app", Branch: "saved", ChangeRequest: run}, true},
		{"replace dry run", SessionCreateReplaceRequest{OldUUID: uuid, CreationChoices: choices}, true},
		{"replace with a bad choice", SessionCreateReplaceRequest{OldUUID: uuid, CreationChoices: CreationChoices{Project: "team/app", Branch: "work", Choice: "maybe"}}, false},
		{"replace run", SessionCreateReplaceRequest{OldUUID: uuid, CreationChoices: choices, ChangeRequest: run}, true},
		{"cleanup of a short id", SessionCreateCleanupRequest{UUID: "short"}, false},
		{"cleanup run", SessionCreateCleanupRequest{UUID: uuid, ChangeRequest: run}, true},
		{"services", SessionServicesRequest{UUID: uuid}, true},
		{"service action", SessionServiceActionRequest{UUID: uuid, Name: "p-project-api.service", Action: "restart"}, true},
		{"service action on any printable name", SessionServiceActionRequest{UUID: uuid, Name: "sshd.service", Action: "stop"}, true},
		{"service action without a name", SessionServiceActionRequest{UUID: uuid, Action: "stop"}, false},
		{"service action on a name with a newline", SessionServiceActionRequest{UUID: uuid, Name: "p-project-a.service\nsshd.service", Action: "stop"}, false},
		{"service action that reloads", SessionServiceActionRequest{UUID: uuid, Name: "p-project-api.service", Action: "reload"}, false},
		{"service action that lists", SessionServiceActionRequest{UUID: uuid, Action: "list"}, false},
		{"service action that reads the logs", SessionServiceActionRequest{UUID: uuid, Name: "p-project-api.service", Action: "logs"}, false},
		{"service logs", SessionServiceLogsRequest{UUID: uuid, Name: "p-project-api.service"}, true},
		{"service logs of a name at the bound", SessionServiceLogsRequest{UUID: uuid, Name: strings.Repeat("a", 128)}, true},
		{"service logs of a name over the bound", SessionServiceLogsRequest{UUID: uuid, Name: strings.Repeat("a", 129)}, false},
		{"service logs of a name that is not UTF-8", SessionServiceLogsRequest{UUID: uuid, Name: "p-project-\xff"}, false},
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

// A risky change's call is a dry run without key and if_match and a run with
// both; a run without its tag is refused with precondition_required, and a
// tag without a key is invalid.
func TestChangeRequestModes(t *testing.T) {
	tag := "20261102T153012Z-" + strings.Repeat("0", 64)
	for _, test := range []struct {
		name   string
		req    ChangeRequest
		dryRun bool
		kind   ErrorKind
		field  string
	}{
		{"neither", ChangeRequest{}, true, "", ""},
		{"both", ChangeRequest{Key: "k", IfMatch: tag}, false, "", ""},
		{"a key without a tag", ChangeRequest{Key: "k"}, false, KindPreconditionRequired, ""},
		{"a tag without a key", ChangeRequest{IfMatch: tag}, false, KindInvalid, "key"},
		{"a malformed tag", ChangeRequest{Key: "k", IfMatch: "20261102T153012Z"}, false, KindInvalid, "if_match"},
		{"a long key", ChangeRequest{Key: strings.Repeat("k", 129), IfMatch: tag}, false, KindInvalid, "key"},
	} {
		err := test.req.Validate()
		var e *Error
		switch {
		case test.req.DryRun() != test.dryRun:
			t.Errorf("%s: dry run %v", test.name, test.req.DryRun())
		case test.kind == "" && err != nil:
			t.Errorf("%s: refused %v", test.name, err)
		case test.kind != "" && (!errors.As(err, &e) || e.Kind() != test.kind || test.field != "" && e.Data.Field != test.field):
			t.Errorf("%s: answered %v, want %s %s", test.name, err, test.kind, test.field)
		case test.kind == KindPreconditionRequired && e.Reason() != ReasonIfMatchMissing:
			t.Errorf("%s: reason %s", test.name, e.Reason())
		}
	}
}

// A change tag is its expiry in UTC to the second, a dash, and 64 lowercase
// hex digits; a calendar date that does not exist is no expiry.
func TestParseChangeTag(t *testing.T) {
	hash := strings.Repeat("9f", 32)
	if expiry, ok := ParseChangeTag("20261102T153012Z-" + hash); !ok || !expiry.Equal(time.Date(2026, 11, 2, 15, 30, 12, 0, time.UTC)) {
		t.Fatalf("parsed %v %v", expiry, ok)
	}
	for _, bad := range []string{"", "20261102T153012Z", "20261102T153012Z-" + hash[:63], "20261102T153012Z-" + strings.ToUpper(hash),
		"2026-11-02T15:30:12Z-" + hash, "20261131T153012Z-" + hash, "20261102T153012-" + hash, "20261102T153012Z-" + hash + "0"} {
		if ValidChangeTag(bad) {
			t.Errorf("accepted %q", bad)
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
