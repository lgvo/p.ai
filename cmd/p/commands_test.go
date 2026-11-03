package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
)

// p help lists the task commands, the other client commands and, under their
// own heading, the host commands; p NOUN --help describes one noun's
// commands with their flags.
func TestHelpListsTheCommands(t *testing.T) {
	var all, session bytes.Buffer
	printHelp(&all)
	for _, want := range []string{
		"p status", "p project create PROJECT", "p branch rename PROJECT:BRANCH NEW", "p origin refs PROJECT",
		"p session create PROJECT:BRANCH", "p attach [SOCKET] SESSION", "p service restart SESSION NAME", "p operation retry ID",
		"p project delete PROJECT", "p branch delete PROJECT:BRANCH", "p origin publish PROJECT:BRANCH", "p session discard SESSION",
		"p session delete SESSION", "p session cleanup SESSION",
		"p tui [SOCKET]", "p api [SOCKET] METHOD", "Host commands (run on the P host as the instance owner):", "p daemon HOST_JSON", "p version",
	} {
		contains(t, all.String(), want)
	}
	printNounHelp(&session, "session")
	contains(t, session.String(), "p session create PROJECT:BRANCH [--attach] [--dry-run] [--force] [--from REF] [--from-origin REF] [--if-match TAG] [--key KEY] [--no-wait] [--replace SESSION]")
	contains(t, session.String(), "--from-origin REF")
	if strings.Contains(session.String(), "p project") {
		t.Errorf("session help describes other nouns:\n%s", session.String())
	}
}

// A command line p cannot run exits 2, host commands included, and sends
// nothing.
func TestUsageErrorsExit2(t *testing.T) {
	for _, args := range [][]string{
		nil, {"nonsense"}, {"session"}, {"session", "nonsense"}, {"daemon"}, {"version", "extra"}, {"plugins"}, {"plugins", "list"},
		{"api"},
	} {
		if err := run(args); exitStatus(err) != 2 {
			t.Errorf("%v: exit %d: %v", args, exitStatus(err), err)
		}
	}
	d := newFakeDaemon(t)
	for _, args := range [][]string{
		{"session", "start"},
		{"session", "start", sessionID, "extra"},
		{"session", "start", sessionID, "--unknown"},
		{"session", "start", "not-a-session"},
		{"session", "create", "app"},
		{"session", "create", "app:work", "--from", "main", "--from-origin", "main"},
		{"session", "create", "app:work", "--attach", "--no-wait"},
		{"project", "create", "/absolute"},
		{"attach", sessionID, "--json"},
		{"project", "create", "app", "--key", ""},
		{"project", "create", "app", "--key", strings.Repeat("k", 129)},
		{"session", "show", "/absolute:work"},
		{"branch", "rename", "app:bad:name", "next"},
		{"session", "create", "app:work", "--from", ""},
		{"session", "create", "app:work", "--from-origin", ""},
		{"project", "create", "app", "--origin", ""},
		{"origin", "set", "app", "", "--key", "removed-before"},
	} {
		if run := runCLI(context.Background(), d, args...); run.code != 2 {
			t.Errorf("%v: exit %d: %s", args, run.code, run.stderr)
		}
	}
	if calls := d.methods(); len(calls) != 0 {
		t.Errorf("usage errors sent %v", calls)
	}
}

// Flags may come before, between or after the arguments; after "--" every
// argument is positional.
func TestFlagsComeAnywhere(t *testing.T) {
	d := newFakeDaemon(t)
	handle(d, api.OperationInspect, func(r api.OperationInspectRequest) (api.OperationResult, error) {
		return api.OperationResult{Operation: operation(r.ID, "session.create", "completed")}, nil
	})
	for _, args := range [][]string{
		{"operation", "show", "--json", opID},
		{"operation", "show", opID, "--json"},
		{"operation", "show", "--json", "--", opID},
	} {
		run := runCLI(context.Background(), d, args...)
		var printed api.OperationResult
		if run.code != 0 || json.Unmarshal([]byte(run.stdout), &printed) != nil || printed.Operation.ID != opID {
			t.Errorf("%v: exit %d, printed %s: %s", args, run.code, run.stdout, run.stderr)
		}
	}
}

// A list command follows next to the end; --json prints every page's items
// in one array, and --project keeps one project's sessions.
func TestListsFollowEveryPage(t *testing.T) {
	d := newFakeDaemon(t)
	sessions := []api.SessionView{*sessionView("ready"), *sessionView("stopped"), *sessionView("ready")}
	sessions[1].UUID, sessions[1].Project = "4f1c2a9e-0000-4000-8000-000000000002", "other"
	sessions[2].UUID = "4f1c2a9e-0000-4000-8000-000000000003"
	handle(d, api.SessionList, func(r api.SessionListRequest) (api.SessionListResult, error) {
		for i, s := range sessions {
			if r.After == "" && i == 0 || i > 0 && sessions[i-1].UUID == r.After {
				next := ""
				if i+1 < len(sessions) {
					next = s.UUID
				}
				return api.SessionListResult{Sessions: []api.SessionView{s}, Next: next}, nil
			}
		}
		return api.SessionListResult{}, api.InvalidParam("after")
	})
	run := runCLI(context.Background(), d, "session", "list", "--json", "--project", "app")
	var printed []api.SessionView
	if run.code != 0 || json.Unmarshal([]byte(run.stdout), &printed) != nil || len(printed) != 2 || printed[1].UUID != sessions[2].UUID {
		t.Fatalf("exit %d, printed %s: %s", run.code, run.stdout, run.stderr)
	}
	run = runCLI(context.Background(), d, "session", "list")
	for _, s := range sessions {
		contains(t, run.stdout, s.UUID)
	}
}

// --no-wait prints the operation's ID once the daemon accepts the change,
// exits 0 and waits for nothing.
func TestNoWaitPrintsTheAcceptedOperation(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	serveJournal(d, j)
	handle(d, api.ProjectCreate, func(r api.ProjectCreateRequest) (api.OperationResult, error) {
		return api.OperationResult{Operation: j.record("project.create", r.Key, api.OperationIntent{Project: r.Project})}, nil
	})
	run := runCLI(context.Background(), d, "project", "create", "app", "--no-wait")
	if run.code != 0 || strings.TrimSpace(run.stdout) != j.ops[0].ID || d.count(api.OperationWait.Name) != 0 {
		t.Fatalf("exit %d, printed %q after %v", run.code, run.stdout, d.methods())
	}
}

// A completed change prints its final line, and with --json the operation
// after waiting.
func TestAChangePrintsHowItEnded(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	serveJournal(d, j)
	handle(d, api.ProjectCreate, func(r api.ProjectCreateRequest) (api.OperationResult, error) {
		return api.OperationResult{Operation: j.record("project.create", r.Key, api.OperationIntent{Project: r.Project, Session: sessionID})}, nil
	})
	run := runCLI(context.Background(), d, "project", "create", "app")
	if run.code != 0 || !reflect.DeepEqual(d.methods(), []string{"project.create", "operation.wait"}) {
		t.Fatalf("exit %d after %v: %s", run.code, d.methods(), run.stderr)
	}
	contains(t, run.stdout, "Created project app and its first session "+sessionID)
	run = runCLI(context.Background(), d, "project", "create", "second", "--json")
	var printed api.OperationResult
	if run.code != 0 || json.Unmarshal([]byte(run.stdout), &printed) != nil || printed.Operation.Status != "completed" || printed.Operation.Key == "" {
		t.Fatalf("exit %d, printed %s", run.code, run.stdout)
	}
}

// --attach enters the first session a project creation made, starting it
// first as p attach does.
func TestProjectCreateAttachEntersTheFirstSession(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	serveJournal(d, j)
	handle(d, api.ProjectCreate, func(r api.ProjectCreateRequest) (api.OperationResult, error) {
		return api.OperationResult{Operation: j.record("project.create", r.Key, api.OperationIntent{Project: r.Project, Session: sessionID})}, nil
	})
	handle(d, api.SessionStart, func(r api.SessionStartRequest) (api.SessionChangeResult, error) {
		return api.SessionChangeResult{Session: sessionView("ready")}, nil
	})
	handle(d, api.SessionAttach, func(r api.SessionAttachRequest) (api.PendingAttachment, error) {
		return api.PendingAttachment{}, api.NewError(api.KindConflict, api.ReasonAttachmentActive)
	})
	run := runCLI(context.Background(), d, "project", "create", "app", "--attach")
	if want := []string{"project.create", "operation.wait", "session.start", "session.attach"}; run.code != 3 || !reflect.DeepEqual(d.methods(), want) {
		t.Fatalf("exit %d after %v: %s", run.code, d.methods(), run.stderr)
	}
	if got := d.params(api.SessionAttach.Name)[0]; got != `{"uuid":"`+sessionID+`"}` {
		t.Fatalf("attached %s", got)
	}
}

// p status reports the instance from system.hello, system.health and
// system.inspect; --json prints their fields in one object.
func TestStatusReportsTheInstance(t *testing.T) {
	d := newFakeDaemon(t)
	handle(d, api.SystemHealth, func(api.HealthRequest) (api.HealthResult, error) { return api.HealthResult{ControlState: "ready"}, nil })
	handle(d, api.SystemInspect, func(api.SystemInspectRequest) (api.SystemInspectResult, error) {
		return api.SystemInspectResult{Projects: 2, Sessions: 3, Operations: 5}, nil
	})
	run := runCLI(context.Background(), d, "status")
	for _, want := range []string{"7d3e0000-0000-4000-8000-000000000000", api.Protocol, "git, runtime, services", "ready", "2 projects, 3 sessions, 5 operations"} {
		contains(t, run.stdout, want)
	}
	run = runCLI(context.Background(), d, "status", "--json")
	var printed map[string]any
	if run.code != 0 || json.Unmarshal([]byte(run.stdout), &printed) != nil || printed["protocol"] != api.Protocol || printed["control_state"] != "ready" || printed["sessions"] != 3.0 {
		t.Fatalf("exit %d, printed %s", run.code, run.stdout)
	}
}

// A result or help that cannot be written to standard output is a failure,
// exit 1.
func TestAnUnwritableResultFails(t *testing.T) {
	d := newFakeDaemon(t)
	handle(d, api.ProjectList, func(api.ProjectListRequest) (api.ProjectListResult, error) {
		return api.ProjectListResult{Projects: []api.ProjectSummary{{Path: "app", Registry: "active"}}}, nil
	})
	c := &cli{stdout: unwritable{}, stderr: io.Discard, pause: pause}
	for _, args := range [][]string{{"project", "list", "--json"}, {"project", "list", "--help"}} {
		if err := c.runCommand(context.Background(), append(args, "--socket", d.socket)); exitStatus(err) != 1 {
			t.Errorf("%v: exit %d: %v", args, exitStatus(err), err)
		}
	}
	if exitStatus(printHelp(unwritable{})) != 1 || exitStatus(printNounHelp(unwritable{}, "session")) != 1 {
		t.Error("unwritable help succeeded")
	}
}

type unwritable struct{}

func (unwritable) Write([]byte) (int, error) { return 0, errors.New("no space left on device") }

// A daemon that cannot be reached is a failure, exit 1.
func TestAnUnreachableDaemonExits1(t *testing.T) {
	d := newFakeDaemon(t)
	d.socket += ".missing"
	if run := runCLI(context.Background(), d, "status"); run.code != 1 {
		t.Fatalf("exit %d: %s", run.code, run.stderr)
	}
}
