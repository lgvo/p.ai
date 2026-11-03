package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

const serviceName = "p-project-db.service"

// A service action records no operation: once queued it prints its result
// and exits 0, and with --json prints the method's result.
func TestServiceActionIsQueued(t *testing.T) {
	d := newFakeDaemon(t)
	handle(d, api.SessionServiceAction, func(r api.SessionServiceActionRequest) (api.SessionServiceActionResult, error) {
		return api.SessionServiceActionResult{UUID: r.UUID, Name: r.Name, ObservedAt: "2026-11-03T00:00:00Z"}, nil
	})
	run := runCLI(context.Background(), d, "service", "restart", sessionID, serviceName)
	if run.code != 0 || d.params(api.SessionServiceAction.Name)[0] != `{"uuid":"`+sessionID+`","name":"`+serviceName+`","action":"restart"}` {
		t.Fatalf("exit %d, sent %v: %s", run.code, d.params(api.SessionServiceAction.Name), run.stderr)
	}
	contains(t, run.stdout, "Queued restart of "+serviceName)
	run = runCLI(context.Background(), d, "service", "stop", sessionID, serviceName, "--json")
	var printed api.SessionServiceActionResult
	if run.code != 0 || json.Unmarshal([]byte(run.stdout), &printed) != nil || printed.Name != serviceName {
		t.Fatalf("exit %d, printed %s", run.code, run.stdout)
	}
}

// A service action has no key and may already be queued when the daemon's
// deadline passes or the reply is lost: it is sent exactly once and exits 1.
func TestServiceActionIsNeverSentTwice(t *testing.T) {
	for _, test := range []string{"deadline", "lost reply"} {
		t.Run(test, func(t *testing.T) {
			d := newFakeDaemon(t)
			handle(d, api.SessionServiceAction, func(r api.SessionServiceActionRequest) (api.SessionServiceActionResult, error) {
				if test == "deadline" {
					return api.SessionServiceActionResult{}, api.NewError(api.KindRetry, api.ReasonDeadline)
				}
				return api.SessionServiceActionResult{UUID: r.UUID, Name: r.Name, ObservedAt: "2026-11-03T00:00:00Z"}, nil
			})
			if test == "lost reply" {
				d.loseReply(api.SessionServiceAction.Name)
			}
			run := runCLI(context.Background(), d, "service", "restart", sessionID, serviceName, "--json")
			if run.code != 1 || d.count(api.SessionServiceAction.Name) != 1 {
				t.Fatalf("exit %d after %d actions: %s", run.code, d.count(api.SessionServiceAction.Name), run.stderr)
			}
			contains(t, run.stderr, "may have been queued")
			var printed struct{ Error *api.Error }
			if test == "deadline" && (json.Unmarshal([]byte(run.stdout), &printed) != nil || printed.Error.Reason() != api.ReasonDeadline) ||
				test == "lost reply" && run.stdout != "" {
				t.Fatalf("printed %q", run.stdout)
			}
		})
	}
}

// A change refused because an operation holds its session waits for that
// operation, then sends the identical request again, with the same key.
func TestSessionHeldWaitsForTheHolderThenRepeats(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	serveJournal(d, j)
	servedSession(d, j)
	holder := j.record("session.start", "holder", api.OperationIntent{Project: "app", Session: sessionID})
	held := true
	handle(d, api.SessionRename, func(r api.SessionRenameRequest) (api.OperationResult, error) {
		if held {
			held = false
			answer := api.NewError(api.KindRetry, api.ReasonSessionHeld)
			answer.Data.Target = api.TargetOperation(holder.ID)
			return api.OperationResult{}, answer
		}
		return api.OperationResult{Operation: j.record("session.rename", r.Key, api.OperationIntent{Project: "app", Session: r.UUID, Branch: "work", NewBranch: r.NewBranch})}, nil
	})
	run := runCLI(context.Background(), d, "session", "rename", sessionID, "next")
	sent := d.params(api.SessionRename.Name)
	if run.code != 0 || len(sent) != 2 || sent[0] != sent[1] {
		t.Fatalf("exit %d, sent %v: %s", run.code, sent, run.stderr)
	}
	waits := d.params(api.OperationWait.Name)
	if len(waits) == 0 || waits[0] != `{"id":"`+holder.ID+`","timeout":20}` {
		t.Fatalf("waited %v, want the holder first", waits)
	}
}

// Ctrl-C while waiting for the operation that holds the session ends the
// command as not run: the refused change was never made.
func TestInterruptWhileWaitingForTheHolderChangesNothing(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	servedSession(d, j)
	ctx, interrupt := context.WithCancel(context.Background())
	defer interrupt()
	handle(d, api.SessionRename, func(api.SessionRenameRequest) (api.OperationResult, error) {
		answer := api.NewError(api.KindRetry, api.ReasonSessionHeld)
		answer.Data.Target = api.TargetOperation(opID)
		return api.OperationResult{}, answer
	})
	handle(d, api.OperationWait, func(r api.OperationWaitRequest) (api.OperationResult, error) {
		interrupt()
		time.Sleep(50 * time.Millisecond)
		return api.OperationResult{Operation: operation(r.ID, "session.start", "running")}, nil
	})
	run := runCLI(ctx, d, "session", "rename", sessionID, "next")
	if run.code != 3 || d.count(api.SessionRename.Name) != 1 || strings.Contains(run.stderr, "may have been made") {
		t.Fatalf("exit %d after %d renames: %s", run.code, d.count(api.SessionRename.Name), run.stderr)
	}
}

// A refusal at admission is repeated after a pause, for up to 10 seconds;
// then the CLI gives up and exits 1.
func TestTooManyRequestsIsRepeatedForAWhile(t *testing.T) {
	d := newFakeDaemon(t)
	refusals := 2
	handle(d, api.SessionStart, func(api.SessionStartRequest) (api.SessionChangeResult, error) {
		if refusals > 0 {
			refusals--
			return api.SessionChangeResult{}, api.NewError(api.KindRetry, api.ReasonTooManyRequests)
		}
		return api.SessionChangeResult{Session: sessionView("ready")}, nil
	})
	if run := runCLI(context.Background(), d, "session", "start", sessionID); run.code != 0 || run.pauses != 2 || d.count(api.SessionStart.Name) != 3 {
		t.Fatalf("exit %d after %d pauses and %d starts: %s", run.code, run.pauses, d.count(api.SessionStart.Name), run.stderr)
	}
	refusals = 1000
	if run := runCLI(context.Background(), d, "session", "start", sessionID); run.code != 1 || run.pauses != 20 {
		t.Fatalf("exit %d after %d pauses: %s", run.code, run.pauses, run.stderr)
	}
}

// After the daemon's deadline a read is repeated, and so is a keyed change
// with its identical request; a Retry is not, since a repeat could act twice.
func TestDeadlineRepeatsOnlyWhatCannotActTwice(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	serveJournal(d, j)
	deadline := map[string]int{api.SessionInspect.Name: 1, api.ProjectCreate.Name: 1, api.OperationRetry.Name: 1}
	late := func(method string) error {
		if deadline[method] > 0 {
			deadline[method]--
			return api.NewError(api.KindRetry, api.ReasonDeadline)
		}
		return nil
	}
	handle(d, api.SessionInspect, func(api.SessionInspectRequest) (api.SessionResult, error) {
		return api.SessionResult{Session: *sessionView("ready")}, late(api.SessionInspect.Name)
	})
	handle(d, api.ProjectCreate, func(r api.ProjectCreateRequest) (api.OperationResult, error) {
		if err := late(api.ProjectCreate.Name); err != nil {
			return api.OperationResult{}, err
		}
		return api.OperationResult{Operation: j.record("project.create", r.Key, api.OperationIntent{Project: r.Project})}, nil
	})
	handle(d, api.OperationRetry, func(r api.OperationRetryRequest) (api.OperationResult, error) {
		return api.OperationResult{Operation: operation(r.ID, "session.create", "running")}, late(api.OperationRetry.Name)
	})
	if run := runCLI(context.Background(), d, "session", "show", sessionID); run.code != 0 || d.count(api.SessionInspect.Name) != 2 {
		t.Fatalf("read: exit %d after %d calls: %s", run.code, d.count(api.SessionInspect.Name), run.stderr)
	}
	run := runCLI(context.Background(), d, "project", "create", "app")
	if sent := d.params(api.ProjectCreate.Name); run.code != 0 || len(sent) != 2 || sent[0] != sent[1] {
		t.Fatalf("keyed change: exit %d, sent %v: %s", run.code, sent, run.stderr)
	}
	if run := runCLI(context.Background(), d, "operation", "retry", opID); run.code != 1 || d.count(api.OperationRetry.Name) != 1 {
		t.Fatalf("retry: exit %d after %d calls: %s", run.code, d.count(api.OperationRetry.Name), run.stderr)
	}
}

// The kind of an error decides the exit status; the person reads the
// sentence for its reason, naming its target, and --json prints the error
// object.
func TestErrorsExitByKindWithTheirSentence(t *testing.T) {
	for _, test := range []struct {
		kind   api.ErrorKind
		reason string
		code   int
	}{
		{api.KindInvalid, api.ReasonParam, 2},
		{api.KindConflict, api.ReasonAttachmentActive, 3},
		{api.KindCapacity, api.ReasonSessionCapacity, 3},
		{api.KindPreconditionFailed, api.ReasonChanged, 3},
		{api.KindNotFound, api.ReasonSession, 1},
		{api.KindNotConfigured, api.ReasonRuntime, 1},
		{api.KindFailed, api.ReasonRuntime, 1},
		{api.KindInternal, api.ReasonStore, 1},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			d := newFakeDaemon(t)
			answer := api.NewError(test.kind, test.reason)
			answer.Data.Target = api.TargetSession(sessionID)
			handle(d, api.SessionStop, func(api.SessionStopRequest) (api.SessionChangeResult, error) {
				return api.SessionChangeResult{}, answer
			})
			run := runCLI(context.Background(), d, "session", "stop", sessionID, "--json")
			if run.code != test.code {
				t.Fatalf("exit %d, want %d: %s", run.code, test.code, run.stderr)
			}
			contains(t, run.stderr, "(session "+sessionID+")")
			var printed struct{ Error *api.Error }
			if json.Unmarshal([]byte(run.stdout), &printed) != nil || printed.Error == nil || printed.Error.Reason() != test.reason {
				t.Fatalf("printed %s", run.stdout)
			}
		})
	}
	d := newFakeDaemon(t)
	answer := api.NewError(api.KindConflict, api.ReasonAttachmentActive)
	answer.Data.Target = api.TargetSession(sessionID)
	handle(d, api.SessionStop, func(api.SessionStopRequest) (api.SessionChangeResult, error) {
		return api.SessionChangeResult{}, answer
	})
	contains(t, runCLI(context.Background(), d, "session", "stop", sessionID).stderr, "Detach every terminal from this session first. (session "+sessionID+")")
}

// A blocked operation's reason is said before its diagnostic, with how to
// retry it.
func TestABlockedOperationSaysWhyAndHowToRetry(t *testing.T) {
	d := newFakeDaemon(t)
	inspectRunning(d)
	handle(d, api.OperationWait, func(r api.OperationWaitRequest) (api.OperationResult, error) {
		op := operation(r.ID, "session.create", "blocked")
		op.Reason, op.Diagnostic = api.ReasonImageBuildFailed, "builder exited 1"
		return api.OperationResult{Operation: op}, nil
	})
	run := runCLI(context.Background(), d, "operation", "wait", opID)
	if run.code != 4 {
		t.Fatalf("exit %d: %s", run.code, run.stderr)
	}
	contains(t, run.stderr, "The image of its source commit did not build")
	contains(t, run.stderr, "Diagnostic: builder exited 1")
	contains(t, run.stderr, "p operation retry "+opID)
}
