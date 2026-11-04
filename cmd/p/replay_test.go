package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/plugin"
)

const (
	tip      = "1111111111111111111111111111111111111111"
	otherTip = "2222222222222222222222222222222222222222"
)

// journal is the fake daemon's operation journal: changes record into it,
// and operation.list pages it one operation per page.
type journal struct {
	mu  sync.Mutex
	ops []api.Operation
}

// record records a change of kind under key with intent, or returns the
// operation the key already recorded, as the daemon replays a key.
func (j *journal) record(kind, key string, intent api.OperationIntent) api.Operation {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, op := range j.ops {
		if op.Key == key {
			return op
		}
	}
	op := operation(fmt.Sprintf("9b0e0000-0000-4000-8000-%012d", len(j.ops)+1), kind, "running")
	op.Key, op.Intent, op.Project, op.SessionUUID = key, intent, intent.Project, intent.Session
	j.ops = append(j.ops, op)
	return op
}

func serveJournal(d *fakeDaemon, j *journal) {
	handle(d, api.OperationList, func(r api.OperationListRequest) (api.OperationListResult, error) {
		j.mu.Lock()
		defer j.mu.Unlock()
		i := 0
		if r.After != "" {
			i, _ = strconv.Atoi(r.After)
		}
		if i >= len(j.ops) {
			return api.OperationListResult{Operations: []api.OperationSummary{}}, nil
		}
		op := j.ops[i]
		next := ""
		if i+1 < len(j.ops) {
			next = strconv.Itoa(i + 1)
		}
		return api.OperationListResult{Operations: []api.OperationSummary{{ID: op.ID, Key: op.Key, Kind: op.Kind, Project: op.Project,
			SessionUUID: op.SessionUUID, Intent: op.Intent, Status: op.Status, Phase: op.Phase}}, Next: next}, nil
	})
	find := func(id string) (api.OperationResult, error) {
		j.mu.Lock()
		defer j.mu.Unlock()
		for _, op := range j.ops {
			if op.ID == id {
				return api.OperationResult{Operation: op}, nil
			}
		}
		return api.OperationResult{}, api.NewError(api.KindNotFound, api.ReasonOperation)
	}
	handle(d, api.OperationInspect, func(r api.OperationInspectRequest) (api.OperationResult, error) { return find(r.ID) })
	handle(d, api.OperationWait, func(r api.OperationWaitRequest) (api.OperationResult, error) {
		j.mu.Lock()
		for i := range j.ops {
			if j.ops[i].ID == r.ID {
				j.ops[i].Status = "completed"
			}
		}
		j.mu.Unlock()
		return find(r.ID)
	})
}

// repeatKey is the key the CLI says to repeat a change with after a lost
// reply.
func repeatKey(t *testing.T, stderr string) string {
	t.Helper()
	key := regexp.MustCompile(`--key (cli-[0-9a-f]{32})`).FindStringSubmatch(stderr)
	if key == nil {
		t.Fatalf("no command to repeat with --key:\n%s", stderr)
	}
	return key[1]
}

// servedSession serves session app:work, which a rename turns into
// app:NEW, and the tips of its branches.
func servedSession(d *fakeDaemon, j *journal) {
	var mu sync.Mutex
	branch := "work"
	view := func() api.SessionView {
		mu.Lock()
		defer mu.Unlock()
		s := *sessionView("stopped")
		s.Branch = branch
		return s
	}
	handle(d, api.SessionList, func(api.SessionListRequest) (api.SessionListResult, error) {
		return api.SessionListResult{Sessions: []api.SessionView{view()}}, nil
	})
	handle(d, api.SessionInspect, func(api.SessionInspectRequest) (api.SessionResult, error) {
		return api.SessionResult{Session: view()}, nil
	})
	handle(d, api.ProjectBranches, func(api.ProjectBranchesRequest) (api.ProjectBranchesResult, error) {
		mu.Lock()
		defer mu.Unlock()
		return api.ProjectBranchesResult{Project: "app", Refs: []plugin.GitRef{{Ref: "refs/heads/" + branch, OID: tip}}}, nil
	})
	handle(d, api.SessionRename, func(r api.SessionRenameRequest) (api.OperationResult, error) {
		mu.Lock()
		defer mu.Unlock()
		if r.ExpectedOldTip != tip {
			return api.OperationResult{}, api.NewError(api.KindPreconditionFailed, api.ReasonChanged)
		}
		op := j.record("session.rename", r.Key, api.OperationIntent{Project: "app", Session: r.UUID, Branch: branch, NewBranch: r.NewBranch})
		branch = r.NewBranch
		return api.OperationResult{Operation: op}, nil
	})
}

// A rename whose reply was lost, repeated with its key, finds its operation
// by the intent, even under the PROJECT:BRANCH it no longer has, and sends
// no change; the same key with another destination exits 2 without sending.
func TestSessionRenameReplaysItsKey(t *testing.T) {
	for _, target := range []string{"app:work", sessionID} {
		t.Run(target, func(t *testing.T) {
			d, j := newFakeDaemon(t), &journal{}
			serveJournal(d, j)
			servedSession(d, j)
			d.loseReply(api.SessionRename.Name)
			first := runCLI(context.Background(), d, "session", "rename", target, "next")
			if first.code != 1 || d.count(api.SessionRename.Name) != 1 {
				t.Fatalf("lost reply: exit %d, %d renames: %s", first.code, d.count(api.SessionRename.Name), first.stderr)
			}
			key := repeatKey(t, first.stderr)
			var sent api.SessionRenameRequest
			if json.Unmarshal([]byte(d.params(api.SessionRename.Name)[0]), &sent) != nil || sent.Key != key || sent.ExpectedOldTip != tip {
				t.Fatalf("sent %s, said key %s", d.params(api.SessionRename.Name)[0], key)
			}
			repeated := runCLI(context.Background(), d, "session", "rename", target, "next", "--key", key)
			if repeated.code != 0 || d.count(api.SessionRename.Name) != 1 {
				t.Fatalf("replay: exit %d, %d renames: %s", repeated.code, d.count(api.SessionRename.Name), repeated.stderr)
			}
			for _, args := range [][]string{{target, "elsewhere"}, {"app:main", "next"}, {"4f1c2a9e-0000-4000-8000-000000000002", "next"}} {
				other := runCLI(context.Background(), d, "session", "rename", args[0], args[1], "--key", key)
				if other.code != 2 || d.count(api.SessionRename.Name) != 1 {
					t.Fatalf("%v: exit %d, %d renames: %s", args, other.code, d.count(api.SessionRename.Name), other.stderr)
				}
			}
		})
	}
}

// A retained-branch rename replays its key the same way; naming the other of
// two retained branches with that key exits 2 without sending.
func TestBranchRenameReplaysItsKey(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	serveJournal(d, j)
	handle(d, api.ProjectBranches, func(api.ProjectBranchesRequest) (api.ProjectBranchesResult, error) {
		return api.ProjectBranchesResult{Project: "app", Refs: []plugin.GitRef{{Ref: "refs/heads/old", OID: tip}, {Ref: "refs/heads/spare", OID: otherTip}}}, nil
	})
	handle(d, api.ProjectRetainedRename, func(r api.ProjectRetainedRenameRequest) (api.OperationResult, error) {
		return api.OperationResult{Operation: j.record("project.retained.rename", r.Key,
			api.OperationIntent{Project: r.Project, Branch: r.OldBranch, NewBranch: r.NewBranch})}, nil
	})
	d.loseReply(api.ProjectRetainedRename.Name)
	first := runCLI(context.Background(), d, "branch", "rename", "app:old", "new")
	key := repeatKey(t, first.stderr)
	if first.code != 1 || d.params(api.ProjectRetainedRename.Name)[0] != `{"key":"`+key+`","project":"app","old_branch":"old","new_branch":"new","expected_old_tip":"`+tip+`"}` {
		t.Fatalf("exit %d, sent %v", first.code, d.params(api.ProjectRetainedRename.Name))
	}
	if run := runCLI(context.Background(), d, "branch", "rename", "app:old", "new", "--key", key); run.code != 0 {
		t.Fatalf("replay: exit %d: %s", run.code, run.stderr)
	}
	if run := runCLI(context.Background(), d, "branch", "rename", "app:spare", "new", "--key", key); run.code != 2 {
		t.Fatalf("the other branch: exit %d: %s", run.code, run.stderr)
	}
	if n := d.count(api.ProjectRetainedRename.Name); n != 1 {
		t.Fatalf("%d renames sent", n)
	}
}

// A session creation under a key matches its choice and source: another
// source exits 2 without sending.
func TestSessionCreateKeyMatchesItsSource(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	serveJournal(d, j)
	handle(d, api.SessionCreate, func(r api.SessionCreateRequest) (api.OperationResult, error) {
		return api.OperationResult{Operation: j.record("session.create", r.Key, api.OperationIntent{Project: r.Project,
			Session: sessionID, Branch: r.Branch, Choice: r.Choice, Source: r.Source, OriginRef: r.OriginRef})}, nil
	})
	if run := runCLI(context.Background(), d, "session", "create", "app:feature", "--from", "main", "--key", "mine"); run.code != 0 {
		t.Fatalf("create: exit %d: %s", run.code, run.stderr)
	}
	if got := d.params(api.SessionCreate.Name); len(got) != 1 || got[0] != `{"key":"mine","project":"app","branch":"feature","choice":"new","source":"refs/heads/main"}` {
		t.Fatalf("sent %v", got)
	}
	for _, test := range []struct {
		args []string
		code int
	}{
		{[]string{"app:feature", "--from", "main"}, 0},
		{[]string{"app:feature", "--from", "refs/heads/main"}, 0},
		{[]string{"app:feature", "--from", "dev"}, 2},
		{[]string{"app:feature"}, 2},
		{[]string{"app:other", "--from", "main"}, 2},
	} {
		if run := runCLI(context.Background(), d, append(append([]string{"session", "create"}, test.args...), "--key", "mine")...); run.code != test.code {
			t.Errorf("%v: exit %d, want %d: %s", test.args, run.code, test.code, run.stderr)
		}
	}
	if n := d.count(api.SessionCreate.Name); n != 1 {
		t.Fatalf("%d creations sent", n)
	}
}

// An origin change replays its key: the URL it set is its intent.
func TestOriginSetReplaysItsKey(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	serveJournal(d, j)
	url := "ssh://git.example.com/team/app.git"
	handle(d, api.OriginInspect, func(api.OriginInspectRequest) (api.OriginResult, error) {
		return api.OriginResult{Origin: api.OriginState{Project: "app", Status: "local-only"}}, nil
	})
	handle(d, api.OriginChange, func(r api.OriginChangeRequest) (api.OperationResult, error) {
		return api.OperationResult{Operation: j.record("origin.change", r.Key, api.OperationIntent{Project: r.Project, URL: r.URL})}, nil
	})
	d.loseReply(api.OriginChange.Name)
	first := runCLI(context.Background(), d, "origin", "set", "app", url)
	key := repeatKey(t, first.stderr)
	if first.code != 1 || d.params(api.OriginChange.Name)[0] != `{"key":"`+key+`","project":"app","kind":"set","expected_url":"","url":"`+url+`"}` {
		t.Fatalf("exit %d, sent %v", first.code, d.params(api.OriginChange.Name))
	}
	if run := runCLI(context.Background(), d, "origin", "set", "app", url, "--key", key); run.code != 0 {
		t.Fatalf("replay: exit %d: %s", run.code, run.stderr)
	}
	for _, args := range [][]string{{"set", "app", url + ".new"}, {"remove", "app"}} {
		if run := runCLI(context.Background(), d, append(append([]string{"origin"}, args...), "--key", key)...); run.code != 2 {
			t.Errorf("%v: exit %d: %s", args, run.code, run.stderr)
		}
	}
	if n := d.count(api.OriginChange.Name); n != 1 {
		t.Fatalf("%d origin changes sent", n)
	}
}

// The command printed after a lost reply repeats the change: its key goes
// before "--", after which every argument is positional.
func TestTheRepeatCommandReplaysTheChange(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	serveJournal(d, j)
	handle(d, api.ProjectCreate, func(r api.ProjectCreateRequest) (api.OperationResult, error) {
		return api.OperationResult{Operation: j.record("project.create", r.Key, api.OperationIntent{Project: r.Project})}, nil
	})
	d.loseReply(api.ProjectCreate.Name)
	first := runCLI(context.Background(), d, "project", "create", "--", "demo")
	command := regexp.MustCompile(`(?m)^  p (.*)$`).FindStringSubmatch(first.stderr)
	if first.code != 1 || command == nil {
		t.Fatalf("exit %d: %s", first.code, first.stderr)
	}
	if run := runCLI(context.Background(), d, strings.Fields(command[1])...); run.code != 0 || d.count(api.ProjectCreate.Name) != 1 {
		t.Fatalf("p %s: exit %d after %d creations: %s", command[1], run.code, d.count(api.ProjectCreate.Name), run.stderr)
	}
}

// Each run makes a new key, cli- and 32 hex digits.
func TestChangesMakeTheirOwnKeys(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	serveJournal(d, j)
	handle(d, api.ProjectCreate, func(r api.ProjectCreateRequest) (api.OperationResult, error) {
		return api.OperationResult{Operation: j.record("project.create", r.Key, api.OperationIntent{Project: r.Project})}, nil
	})
	for _, project := range []string{"one", "two"} {
		if run := runCLI(context.Background(), d, "project", "create", project); run.code != 0 {
			t.Fatalf("exit %d: %s", run.code, run.stderr)
		}
	}
	sent := d.params(api.ProjectCreate.Name)
	var keys [2]api.ProjectCreateRequest
	for i := range keys {
		if err := json.Unmarshal([]byte(sent[i]), &keys[i]); err != nil || !regexp.MustCompile(`^cli-[0-9a-f]{32}$`).MatchString(keys[i].Key) {
			t.Fatalf("sent %s", sent[i])
		}
	}
	if keys[0].Key == keys[1].Key {
		t.Fatal("two runs sent one key")
	}
	if d.count(api.OperationList.Name) != 0 {
		t.Fatal("a run without --key looked a key up")
	}
}
