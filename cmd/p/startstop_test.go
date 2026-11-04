package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

const sessionID = "4f1c2a9e-0000-4000-8000-000000000001"

func sessionView(condition string) *api.SessionView {
	return &api.SessionView{UUID: sessionID, Project: "app", Branch: "work", Registry: "established", Condition: condition, PolicyCondition: "current"}
}

// A start or stop that finds the session already as asked says so, exits 0
// and waits for nothing.
func TestStartAndStopOfASessionAlreadyAsAsked(t *testing.T) {
	for _, test := range []struct {
		verb, condition, said string
	}{
		{"start", "ready", "already running"},
		{"stop", "stopped", "already stopped"},
	} {
		t.Run(test.verb, func(t *testing.T) {
			d := newFakeDaemon(t)
			reply := func() (api.SessionChangeResult, error) {
				return api.SessionChangeResult{Session: sessionView(test.condition)}, nil
			}
			handle(d, api.SessionStart, func(api.SessionStartRequest) (api.SessionChangeResult, error) { return reply() })
			handle(d, api.SessionStop, func(api.SessionStopRequest) (api.SessionChangeResult, error) { return reply() })
			run := runCLI(context.Background(), d, "session", test.verb, sessionID)
			if run.code != 0 || !reflect.DeepEqual(d.methods(), []string{"session." + test.verb}) {
				t.Fatalf("exit %d after %v: %s", run.code, d.methods(), run.stderr)
			}
			contains(t, run.stdout, test.said)
		})
	}
}

// An operation in the reply, new or the unfinished one of the same kind, is
// waited for, and its end decides the exit status.
func TestStartAndStopWaitForTheirOperation(t *testing.T) {
	for _, test := range []struct {
		verb    string
		changed bool
		end     string
		code    int
	}{
		{"start", true, "completed", 0},
		{"start", false, "completed", 0},
		{"stop", true, "completed", 0},
		{"stop", false, "failed", 1},
	} {
		t.Run(test.verb, func(t *testing.T) {
			d := newFakeDaemon(t)
			reply := func(kind string) (api.SessionChangeResult, error) {
				op := operation(opID, kind, "running")
				return api.SessionChangeResult{Changed: test.changed, Operation: &op}, nil
			}
			handle(d, api.SessionStart, func(api.SessionStartRequest) (api.SessionChangeResult, error) { return reply("session.start") })
			handle(d, api.SessionStop, func(api.SessionStopRequest) (api.SessionChangeResult, error) { return reply("session.stop") })
			handle(d, api.OperationWait, func(r api.OperationWaitRequest) (api.OperationResult, error) {
				return api.OperationResult{Operation: operation(r.ID, "session."+test.verb, test.end)}, nil
			})
			run := runCLI(context.Background(), d, "session", test.verb, sessionID)
			if run.code != test.code || !reflect.DeepEqual(d.methods(), []string{"session." + test.verb, "operation.wait"}) {
				t.Fatalf("exit %d after %v, want %d: %s", run.code, d.methods(), test.code, run.stderr)
			}
		})
	}
}

// --json prints the start's reply with its operation as it ended.
func TestStartPrintsItsReplyAsJSON(t *testing.T) {
	d := newFakeDaemon(t)
	handle(d, api.SessionStart, func(api.SessionStartRequest) (api.SessionChangeResult, error) {
		op := operation(opID, "session.start", "running")
		return api.SessionChangeResult{Changed: true, Operation: &op}, nil
	})
	handle(d, api.OperationWait, func(r api.OperationWaitRequest) (api.OperationResult, error) {
		return api.OperationResult{Operation: operation(r.ID, "session.start", "completed")}, nil
	})
	run := runCLI(context.Background(), d, "session", "start", sessionID, "--json")
	var printed api.SessionChangeResult
	if run.code != 0 || json.Unmarshal([]byte(run.stdout), &printed) != nil || !printed.Changed || printed.Operation == nil || printed.Operation.Status != "completed" {
		t.Fatalf("exit %d, printed %s", run.code, run.stdout)
	}
}

// Interrupted while waiting, start prints its reply with --json, changed
// included, with the operation as it last saw it, and exits 4.
func TestInterruptedStartPrintsItsReply(t *testing.T) {
	for _, changed := range []bool{true, false} {
		d := newFakeDaemon(t)
		ctx, interrupt := context.WithCancel(context.Background())
		handle(d, api.SessionStart, func(api.SessionStartRequest) (api.SessionChangeResult, error) {
			op := operation(opID, "session.start", "running")
			return api.SessionChangeResult{Changed: changed, Operation: &op}, nil
		})
		handle(d, api.OperationWait, func(r api.OperationWaitRequest) (api.OperationResult, error) {
			interrupt()
			time.Sleep(50 * time.Millisecond)
			return api.OperationResult{Operation: operation(r.ID, "session.start", "running")}, nil
		})
		run := runCLI(ctx, d, "session", "start", sessionID, "--json")
		interrupt()
		var printed map[string]json.RawMessage
		if run.code != 4 || json.Unmarshal([]byte(run.stdout), &printed) != nil || string(printed["changed"]) != fmt.Sprint(changed) || printed["operation"] == nil {
			t.Fatalf("changed %v: exit %d, printed %s", changed, run.code, run.stdout)
		}
	}
}

// p attach starts the session first: a ready one is entered at once; a
// stopped one once its start completes. The attachment itself is refused
// here, which ends the command before a terminal is carried.
func TestAttachStartsTheSessionFirst(t *testing.T) {
	for _, test := range []struct {
		name  string
		reply api.SessionChangeResult
		calls []string
	}{
		{"ready", api.SessionChangeResult{Session: sessionView("ready")}, []string{"session.start", "session.attach"}},
		{"stopped", api.SessionChangeResult{Changed: true, Operation: &api.Operation{ID: opID, Kind: "session.start", Status: "running"}},
			[]string{"session.start", "operation.wait", "operation.wait", "session.attach"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := newFakeDaemon(t)
			handle(d, api.SessionStart, func(api.SessionStartRequest) (api.SessionChangeResult, error) { return test.reply, nil })
			waits := 0
			handle(d, api.OperationWait, func(r api.OperationWaitRequest) (api.OperationResult, error) {
				waits++
				status := "running"
				if waits == 2 {
					status = "completed"
				}
				return api.OperationResult{Operation: operation(r.ID, "session.start", status)}, nil
			})
			handle(d, api.SessionAttach, func(api.SessionAttachRequest) (api.PendingAttachment, error) {
				return api.PendingAttachment{}, api.NewError(api.KindConflict, api.ReasonAttachmentActive)
			})
			run := runCLI(context.Background(), d, "attach", sessionID)
			if run.code != 3 || !reflect.DeepEqual(d.methods(), test.calls) {
				t.Fatalf("exit %d after %v, want 3 after %v: %s", run.code, d.methods(), test.calls, run.stderr)
			}
		})
	}
}

// A start that fails ends p attach before it asks to attach.
func TestAttachStopsWhenTheStartFails(t *testing.T) {
	d := newFakeDaemon(t)
	handle(d, api.SessionStart, func(api.SessionStartRequest) (api.SessionChangeResult, error) {
		return api.SessionChangeResult{Changed: true, Operation: &api.Operation{ID: opID, Kind: "session.start", Status: "running"}}, nil
	})
	handle(d, api.OperationWait, func(r api.OperationWaitRequest) (api.OperationResult, error) {
		op := operation(r.ID, "session.start", "failed")
		op.Reason, op.Diagnostic = api.ReasonRuntime, "instance did not start"
		return api.OperationResult{Operation: op}, nil
	})
	run := runCLI(context.Background(), d, "attach", sessionID)
	if run.code != 1 || !reflect.DeepEqual(d.methods(), []string{"session.start", "operation.wait"}) {
		t.Fatalf("exit %d after %v: %s", run.code, d.methods(), run.stderr)
	}
	contains(t, run.stderr, "instance did not start")
}

// p attach names a session by PROJECT:BRANCH too, through session.list.
func TestAttachAcceptsProjectAndBranch(t *testing.T) {
	d := newFakeDaemon(t)
	other := *sessionView("ready")
	other.UUID, other.Branch = "4f1c2a9e-0000-4000-8000-000000000002", "main"
	handle(d, api.SessionList, func(r api.SessionListRequest) (api.SessionListResult, error) {
		if r.After == "" {
			return api.SessionListResult{Sessions: []api.SessionView{other}, Next: other.UUID}, nil
		}
		return api.SessionListResult{Sessions: []api.SessionView{*sessionView("ready")}}, nil
	})
	handle(d, api.SessionStart, func(r api.SessionStartRequest) (api.SessionChangeResult, error) {
		if r.UUID != sessionID {
			return api.SessionChangeResult{}, api.NewError(api.KindNotFound, api.ReasonSession)
		}
		return api.SessionChangeResult{Session: sessionView("ready")}, nil
	})
	handle(d, api.SessionAttach, func(api.SessionAttachRequest) (api.PendingAttachment, error) {
		return api.PendingAttachment{}, api.NewError(api.KindConflict, api.ReasonAttachmentActive)
	})
	run := runCLI(context.Background(), d, "attach", "app:work")
	if run.code != 3 || !reflect.DeepEqual(d.methods(), []string{"session.list", "session.list", "session.start", "session.attach"}) {
		t.Fatalf("exit %d after %v: %s", run.code, d.methods(), run.stderr)
	}
	if run := runCLI(context.Background(), d, "attach", "app:gone"); run.code != 1 {
		t.Fatalf("a branch no session holds: exit %d: %s", run.code, run.stderr)
	}
}
