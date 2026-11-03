package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/plugin"
)

// changeRequest is a risky change's request.
type changeRequest interface {
	api.Request
	DryRun() bool
}

// servePlanned serves m: a dry run answers an allowed plan of details that
// loses nothing, with the tag tagOf(0); a run with that tag records an
// operation of kind with intent, and any other tag is refused as changed.
func servePlanned[Req changeRequest, D any](d *fakeDaemon, j *journal, m api.Method[Req, api.ChangeResult[D]], kind string, details D, intent func(Req) api.OperationIntent) {
	handle(d, m, func(r Req) (api.ChangeResult[D], error) {
		if r.DryRun() {
			return api.ChangeResult[D]{Plan: &api.Plan[D]{Action: m.Name, Allowed: true, Refusals: []api.Refusal{}, Loss: []api.Loss{}, Gone: []api.Loss{}, Details: details},
				ChangeTag: tagOf(0)}, nil
		}
		var change api.ChangeRequest
		raw, _ := json.Marshal(r)
		if json.Unmarshal(raw, &change) != nil || change.IfMatch != tagOf(0) {
			return api.ChangeResult[D]{}, api.NewError(api.KindPreconditionFailed, api.ReasonChanged)
		}
		op := j.record(kind, change.Key, intent(r))
		return api.ChangeResult[D]{Operation: &op}, nil
	})
}

// serveProject serves project app: session app:work, retained branch
// saved, branch main, its origin, and every risky change.
func serveProject(d *fakeDaemon, j *journal) {
	serveJournal(d, j)
	handle(d, api.SessionList, func(api.SessionListRequest) (api.SessionListResult, error) {
		return api.SessionListResult{Sessions: []api.SessionView{*sessionView("ready")}}, nil
	})
	handle(d, api.SessionInspect, func(r api.SessionInspectRequest) (api.SessionResult, error) {
		if r.UUID != sessionID {
			return api.SessionResult{}, api.NewError(api.KindNotFound, api.ReasonSession)
		}
		return api.SessionResult{Session: *sessionView("ready")}, nil
	})
	handle(d, api.ProjectBranches, func(api.ProjectBranchesRequest) (api.ProjectBranchesResult, error) {
		return api.ProjectBranchesResult{Project: "app", Refs: []plugin.GitRef{
			{Ref: "refs/heads/main", OID: otherTip}, {Ref: "refs/heads/saved", OID: otherTip}, {Ref: "refs/heads/work", OID: tip}}}, nil
	})
	handle(d, api.OriginInspect, func(api.OriginInspectRequest) (api.OriginResult, error) {
		return api.OriginResult{Origin: api.OriginState{Project: "app", URL: originURL, Status: "fresh"}}, nil
	})
	servePlanned(d, j, api.SessionDiscard, api.SessionDiscard.Name, api.RemovalDetails{}, func(r api.SessionDiscardRequest) api.OperationIntent {
		return api.OperationIntent{Project: "app", Session: r.UUID, Branch: "work"}
	})
	servePlanned(d, j, api.SessionDelete, api.SessionDelete.Name, api.RemovalDetails{}, func(r api.SessionDeleteRequest) api.OperationIntent {
		return api.OperationIntent{Project: "app", Session: r.UUID, Branch: "work"}
	})
	servePlanned(d, j, api.SessionCreateCleanup, api.SessionCreateCleanup.Name, api.CreateCleanupDetails{}, func(r api.SessionCreateCleanupRequest) api.OperationIntent {
		return api.OperationIntent{Project: "app", Session: r.UUID, Branch: "work"}
	})
	servePlanned(d, j, api.SessionCreateReplace, api.SessionCreate.Name, api.CreateReplaceDetails{}, func(r api.SessionCreateReplaceRequest) api.OperationIntent {
		return api.OperationIntent{Project: r.Project, Session: "4f1c2a9e-0000-4000-8000-000000000009", Branch: r.Branch, Choice: r.Choice, Source: r.Source,
			ReplacesSession: r.OldUUID, ReplacesBranch: "work"}
	})
	servePlanned(d, j, api.ProjectDelete, api.ProjectDelete.Name, api.ProjectDeleteDetails{}, func(r api.ProjectDeleteRequest) api.OperationIntent {
		return api.OperationIntent{Project: r.Project}
	})
	servePlanned(d, j, api.ProjectRetainedDelete, api.ProjectRetainedDelete.Name, api.RetainedDeleteDetails{}, func(r api.ProjectRetainedDeleteRequest) api.OperationIntent {
		return api.OperationIntent{Project: r.Project, Branch: r.Branch}
	})
	servePlanned(d, j, api.OriginPublish, api.OriginPublish.Name, api.PublicationPreview{Relation: "absent"}, func(r api.OriginPublishRequest) api.OperationIntent {
		branch := r.Source
		if r.Kind == "session" {
			branch = "work"
		}
		session := ""
		if r.Kind == "session" {
			session = r.Source
		}
		return api.OperationIntent{Project: r.Project, Session: session, Branch: branch, Destination: r.DestinationRef}
	})
}

// Each command that asks makes its method's dry run with its arguments
// resolved from live state, then runs the plan with the same inputs, a key
// and the tag, and says what it did.
func TestEachChangeThatAsksSendsItsMethod(t *testing.T) {
	for _, test := range []struct {
		args   []string
		method string
		dryRun string
		done   string
	}{
		{[]string{"session", "discard", "app:work"}, api.SessionDiscard.Name, `{"uuid":"` + sessionID + `"}`, "Discarded session " + sessionID + " (app:work)."},
		{[]string{"session", "delete", sessionID}, api.SessionDelete.Name, `{"uuid":"` + sessionID + `"}`, "Deleted session " + sessionID + " (app:work)."},
		{[]string{"session", "cleanup", "app:work"}, api.SessionCreateCleanup.Name, `{"uuid":"` + sessionID + `"}`, "Cleaned up the failed creation of session " + sessionID},
		{[]string{"session", "create", "app:new", "--from", "main", "--replace", "app:work"}, api.SessionCreateReplace.Name,
			`{"old_uuid":"` + sessionID + `","project":"app","branch":"new","choice":"new","source":"refs/heads/main"}`, "Replaced the blocked creation of session " + sessionID},
		{[]string{"session", "create", "app:work", "--replace", sessionID}, api.SessionCreateReplace.Name,
			`{"old_uuid":"` + sessionID + `","project":"app","branch":"work","choice":"existing"}`, "Replaced the blocked creation of session " + sessionID},
		{[]string{"project", "delete", "app"}, api.ProjectDelete.Name, `{"project":"app"}`, "Deleted project app."},
		{[]string{"branch", "delete", "app:saved"}, api.ProjectRetainedDelete.Name, `{"project":"app","branch":"saved"}`, "Deleted branch app:saved."},
		{[]string{"origin", "publish", "app:work"}, api.OriginPublish.Name,
			`{"expected_origin_url":"` + originURL + `","project":"app","kind":"session","source":"` + sessionID + `","source_oid":"` + tip + `","destination_ref":"refs/heads/work"}`, ""},
		{[]string{"origin", "publish", "app:saved", "--to", "main"}, api.OriginPublish.Name,
			`{"expected_origin_url":"` + originURL + `","project":"app","kind":"retained","source":"saved","source_oid":"` + otherTip + `","destination_ref":"refs/heads/main"}`, ""},
	} {
		d, j := newFakeDaemon(t), &journal{}
		serveProject(d, j)
		run := runCLI(context.Background(), d, append(test.args, "--force")...)
		sent := d.params(test.method)
		if run.code != 0 || len(sent) != 2 || sent[0] != test.dryRun || !strings.Contains(run.stdout, test.done) {
			t.Errorf("%v: exit %d, sent %v, printed %q: %s", test.args, run.code, sent, run.stdout, run.stderr)
			continue
		}
		var dry, ran map[string]any
		if json.Unmarshal([]byte(sent[0]), &dry) != nil || json.Unmarshal([]byte(sent[1]), &ran) != nil || ran["if_match"] != tagOf(0) || ran["key"] == nil {
			t.Errorf("%v: ran %s", test.args, sent[1])
			continue
		}
		delete(ran, "key")
		delete(ran, "if_match")
		if !reflect.DeepEqual(dry, ran) {
			t.Errorf("%v: ran %s after the dry run %s", test.args, sent[1], sent[0])
		}
	}
}

// A replacement onto another branch whose reply was lost, repeated with its
// key, finds its new creation by branch and replaces_branch after the
// blocked session is gone, and sends no change; the same key with another
// source, branch or replaced session, or without --replace, exits 2
// without sending.
func TestReplacementReplaysItsKey(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	serveJournal(d, j)
	newSession := "4f1c2a9e-0000-4000-8000-000000000009"
	replaced := false
	handle(d, api.SessionList, func(api.SessionListRequest) (api.SessionListResult, error) {
		old := sessionView("creating")
		old.Branch = "old"
		if replaced {
			return api.SessionListResult{Sessions: []api.SessionView{}}, nil
		}
		return api.SessionListResult{Sessions: []api.SessionView{*old}}, nil
	})
	servePlanned(d, j, api.SessionCreateReplace, api.SessionCreate.Name, api.CreateReplaceDetails{}, func(r api.SessionCreateReplaceRequest) api.OperationIntent {
		replaced = true
		return api.OperationIntent{Project: r.Project, Session: newSession, Branch: r.Branch, Choice: r.Choice, Source: r.Source,
			ReplacesSession: r.OldUUID, ReplacesBranch: "old"}
	})
	d.loseReplyAfter(api.SessionCreateReplace.Name, 1)
	first := runCLIWith(context.Background(), d, onTerminal("y\n"), "session", "create", "app:new", "--from", "main", "--replace", "app:old")
	key := repeatKey(t, first.stderr)
	if first.code != 1 || d.count(api.SessionCreateReplace.Name) != 2 || len(j.ops) != 1 || j.ops[0].Key != key {
		t.Fatalf("lost reply: exit %d after %v: %s", first.code, d.methods(), first.stderr)
	}
	replay := runCLI(context.Background(), d, "session", "create", "app:new", "--from", "main", "--replace", "app:old", "--key", key, "--force")
	if !strings.Contains(replay.stdout, "session "+sessionID+" with session "+newSession) {
		t.Errorf("the replay printed %q", replay.stdout)
	}
	for _, test := range []struct {
		args []string
		code int
	}{
		{[]string{"app:new", "--from", "main", "--replace", "app:old"}, 0},
		{[]string{"app:new", "--from", "main", "--replace", sessionID}, 0},
		{[]string{"app:new", "--from", "dev", "--replace", "app:old"}, 2},
		{[]string{"app:other", "--from", "main", "--replace", "app:old"}, 2},
		{[]string{"app:new", "--from", "main", "--replace", "app:spare"}, 2},
	} {
		run := runCLI(context.Background(), d, append(append([]string{"session", "create"}, test.args...), "--key", key, "--force")...)
		if run.code != test.code {
			t.Errorf("%v: exit %d, want %d: %s", test.args, run.code, test.code, run.stderr)
		}
	}
	if run := runCLI(context.Background(), d, "session", "create", "app:new", "--from", "main", "--key", key); run.code != 2 {
		t.Errorf("a creation without --replace: exit %d: %s", run.code, run.stderr)
	}
	if n := d.count(api.SessionCreateReplace.Name); n != 2 || d.count(api.SessionCreate.Name) != 0 {
		t.Fatalf("sent %v", d.methods())
	}
}

// A publication whose reply was lost, repeated with its key, finds its
// operation by branch and destination and sends no change; the same key
// with another destination exits 2 without sending.
func TestPublicationReplaysItsKey(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	serveProject(d, j)
	d.loseReplyAfter(api.OriginPublish.Name, 1)
	first := runCLI(context.Background(), d, "origin", "publish", "app:work", "--to", "main", "--force")
	key := repeatKey(t, first.stderr)
	if first.code != 1 || d.count(api.OriginPublish.Name) != 2 || len(j.ops) != 1 {
		t.Fatalf("lost reply: exit %d after %v: %s", first.code, d.methods(), first.stderr)
	}
	calls := len(d.methods())
	j.ops[0].Evidence = json.RawMessage(`{"publication":{"preview":{"url":"` + originURL + `","destination_ref":"refs/heads/main"},"status":"created"}}`)
	if run := runCLI(context.Background(), d, "origin", "publish", "app:work", "--to", "main", "--force", "--key", key); run.code != 0 || !strings.Contains(run.stdout, "branch main of "+originURL) {
		t.Fatalf("replay: exit %d, printed %q: %s", run.code, run.stdout, run.stderr)
	}
	for _, args := range [][]string{{"app:work"}, {"app:saved", "--to", "main"}} {
		if run := runCLI(context.Background(), d, append(append([]string{"origin", "publish"}, args...), "--force", "--key", key)...); run.code != 2 {
			t.Errorf("%v: exit %d: %s", args, run.code, run.stderr)
		}
	}
	for _, method := range d.methods()[calls:] {
		if method != api.OperationList.Name && method != api.OperationInspect.Name && method != api.OperationWait.Name {
			t.Fatalf("a replay sent %s", method)
		}
	}
}

// A publication says what its push did; one the origin's server rejected
// completes without publishing, and the command exits 1.
func TestPublicationSaysWhatThePushDid(t *testing.T) {
	for status, code := range map[string]int{"created": 0, "advanced": 0, "satisfied": 0, "refused": 1} {
		d, j := newFakeDaemon(t), &journal{}
		serveProject(d, j)
		handle(d, api.OperationWait, func(r api.OperationWaitRequest) (api.OperationResult, error) {
			op := j.ops[0]
			op.Status, op.Evidence = "completed", json.RawMessage(`{"publication":{"preview":{"url":"`+originURL+`","destination_ref":"refs/heads/work"},"status":"`+status+`"}}`)
			return api.OperationResult{Operation: op}, nil
		})
		run := runCLI(context.Background(), d, "origin", "publish", "app:work", "--force")
		if run.code != code {
			t.Errorf("%s: exit %d, want %d: %s", status, run.code, code, run.stderr)
		}
		if code == 0 && !strings.Contains(run.stdout, "Published app:work to branch work of "+originURL) || code != 0 && !strings.Contains(run.stderr, "nothing was published") {
			t.Errorf("%s: printed %q: %s", status, run.stdout, run.stderr)
		}
	}
}

// A local-only project has no origin to publish to: the command exits 3
// before any dry run.
func TestPublicationNeedsAnOrigin(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	serveProject(d, j)
	handle(d, api.OriginInspect, func(api.OriginInspectRequest) (api.OriginResult, error) {
		return api.OriginResult{Origin: api.OriginState{Project: "app", Status: "local-only"}}, nil
	})
	if run := runCLI(context.Background(), d, "origin", "publish", "app:work", "--force"); run.code != 3 || d.count(api.OriginPublish.Name) != 0 {
		t.Fatalf("exit %d after %v: %s", run.code, d.methods(), run.stderr)
	}
}

// With --json a change that asks prints the last dry run and the operation
// it ran, after waiting.
func TestAChangeThatAsksPrintsItsPlanAndOperation(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	serveProject(d, j)
	run := runCLI(context.Background(), d, "branch", "delete", "app:saved", "--force", "--json")
	var printed api.ChangeResult[api.RetainedDeleteDetails]
	if run.code != 0 || json.Unmarshal([]byte(run.stdout), &printed) != nil || printed.Plan == nil || printed.ChangeTag != tagOf(0) ||
		printed.Operation == nil || printed.Operation.Status != "completed" || printed.Operation.Key == "" {
		t.Fatalf("exit %d, printed %s: %s", run.code, run.stdout, run.stderr)
	}
}

// Command lines a change that asks cannot use exit 2 and send nothing.
func TestChangesThatAskRefuseBadCommandLines(t *testing.T) {
	d := newFakeDaemon(t)
	for _, args := range [][]string{
		{"session", "create", "app:new", "--from", "main", "--force"},
		{"session", "create", "app:new", "--dry-run"},
		{"session", "create", "app:new", "--if-match", tagOf(0)},
		{"session", "create", "app:new", "--from-origin", "main", "--replace", sessionID},
		{"session", "create", "app:new", "--replace", sessionID, "--attach", "--dry-run"},
		{"session", "create", "app:new", "--replace", "nonsense"},
		{"origin", "publish", "app:work", "--to", "bad..name"},
		{"origin", "publish", "app"},
		{"branch", "delete", "app"},
		{"project", "delete", "/absolute"},
	} {
		if run := runCLI(context.Background(), d, args...); run.code != 2 {
			t.Errorf("%v: exit %d: %s", args, run.code, run.stderr)
		}
	}
	if calls := d.methods(); len(calls) != 0 {
		t.Errorf("sent %v", calls)
	}
}

// A plan's heading names what the change acts on: a project deletion its
// sessions, its branches and what deletion keeps; a publication the origin,
// the source commit, the destination and what the push does.
func TestTheHeadingNamesWhatTheChangeActsOn(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	serveProject(d, j)
	project := api.ProjectDeleteDetails{Project: "app",
		Sessions:       []api.ProjectDeleteSession{{Session: api.SessionRecord{UUID: sessionID, Project: "app", Branch: "work"}, Condition: "running"}},
		BranchLoss:     &api.RemovalBranchLoss{PRefs: []api.PRef{{Name: "refs/heads/saved", OID: otherTip}}},
		ExternalMounts: []api.FilesystemGrant{{Name: "data", Source: "/srv/data"}}}
	servePlanned(d, j, api.ProjectDelete, api.ProjectDelete.Name, project, func(r api.ProjectDeleteRequest) api.OperationIntent {
		return api.OperationIntent{Project: r.Project}
	})
	servePlanned(d, j, api.OriginPublish, api.OriginPublish.Name, api.PublicationPreview{Relation: "fast_forward", DestinationOID: otherTip},
		func(api.OriginPublishRequest) api.OperationIntent { return api.OperationIntent{} })
	for _, test := range []struct {
		args []string
		want []string
	}{
		{[]string{"project", "delete", "app"}, []string{sessionID + " (app:work), running", "saved at " + otherTip, "/srv/data", "event handlers"}},
		{[]string{"origin", "publish", "app:work", "--to", "main"}, []string{"app:work, at " + tip, "branch main of " + originURL, "fast-forwards the branch from " + otherTip}},
	} {
		run := runCLI(context.Background(), d, append(test.args, "--dry-run")...)
		for _, want := range test.want {
			contains(t, run.stderr, want)
		}
	}
}
