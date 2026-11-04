package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

// tagOf is the change tag of the state numbered state.
func tagOf(state int) string {
	return fmt.Sprintf("20261103T120000Z-%064x", state+1)
}

// lossOfWork is the plan of deleting session app:work with its container
// and two commits.
func lossOfWork() api.Plan[api.RemovalDetails] {
	return api.Plan[api.RemovalDetails]{Action: api.SessionDelete.Name, Target: api.TargetSession(sessionID), Allowed: true,
		Refusals: []api.Refusal{}, Gone: []api.Loss{},
		Loss: []api.Loss{
			{Kind: api.LossRuntimeData, Target: api.TargetSession(sessionID), Certain: true},
			{Kind: api.LossPCommits, Target: api.TargetBranch("app", "work"), Count: 2, Sample: []string{tip, otherTip}, Certain: true},
		},
		Details: api.RemovalDetails{Project: "app", Branch: "work", AssignedRef: "refs/heads/work", AssignedTip: tip,
			Runtime: api.RemovalRuntime{Condition: "present", OriginalStatus: "Stopped"}}}
}

// deletion serves the deletion of session app:work. The daemon's state is
// numbered: plans[state] is its plan, the last repeating, and tagOf(state)
// its tag. A run whose tag is not the current state's is refused as changed.
type deletion struct {
	mu    sync.Mutex
	plans []api.Plan[api.RemovalDetails]
	state int
	// changeAfter moves the state on after that many dry runs, as a change
	// made between a dry run and its run would.
	changeAfter int
	dryRuns     int
	// clock, when set, is read as each run arrives, into ranAt.
	clock *clock
	ranAt []int
	// deleted is set once a run deleted the session.
	deleted bool
}

// clock is a fake clock that counts the whole seconds the CLI pauses.
// Reaching the second interruptAt interrupts the command, as Ctrl-C would.
type clock struct {
	seconds     atomic.Int32
	interruptAt int32
	interrupt   context.CancelFunc
}

func (c *clock) pause(ctx context.Context, d time.Duration) error {
	if c.seconds.Add(int32(d/time.Second)) == c.interruptAt && c.interrupt != nil {
		c.interrupt()
	}
	return ctx.Err()
}

// forced runs a change with --force on the clock, on a terminal or not.
func (c *clock) forced(terminal bool) func(*cli) {
	return func(cl *cli) {
		cl.pause, cl.stdinTerminal, cl.stderrTerminal = c.pause, terminal, terminal
	}
}

func serveDeletion(d *fakeDaemon, j *journal, plans ...api.Plan[api.RemovalDetails]) *deletion {
	del := &deletion{plans: plans, changeAfter: -1}
	serveJournal(d, j)
	handle(d, api.SessionList, func(api.SessionListRequest) (api.SessionListResult, error) {
		del.mu.Lock()
		defer del.mu.Unlock()
		if del.deleted {
			return api.SessionListResult{Sessions: []api.SessionView{}}, nil
		}
		return api.SessionListResult{Sessions: []api.SessionView{*sessionView("stopped")}}, nil
	})
	handle(d, api.SessionInspect, func(api.SessionInspectRequest) (api.SessionResult, error) {
		del.mu.Lock()
		defer del.mu.Unlock()
		if del.deleted {
			return api.SessionResult{}, api.NewError(api.KindNotFound, api.ReasonSession)
		}
		return api.SessionResult{Session: *sessionView("stopped")}, nil
	})
	handle(d, api.SessionDelete, func(r api.SessionDeleteRequest) (api.ChangeResult[api.RemovalDetails], error) {
		del.mu.Lock()
		defer del.mu.Unlock()
		if r.DryRun() {
			p := del.plans[min(del.state, len(del.plans)-1)]
			result := api.ChangeResult[api.RemovalDetails]{Plan: &p}
			if p.Allowed {
				result.ChangeTag = tagOf(del.state)
			}
			del.dryRuns++
			if del.dryRuns == del.changeAfter {
				del.state++
			}
			return result, nil
		}
		if r.IfMatch != tagOf(del.state) {
			return api.ChangeResult[api.RemovalDetails]{}, api.NewError(api.KindPreconditionFailed, api.ReasonChanged)
		}
		if del.clock != nil {
			del.ranAt = append(del.ranAt, int(del.clock.seconds.Load()))
		}
		op := j.record(api.SessionDelete.Name, r.Key, api.OperationIntent{Project: "app", Session: r.UUID, Branch: "work"})
		del.deleted = true
		return api.ChangeResult[api.RemovalDetails]{Operation: &op}, nil
	})
	return del
}

// onTerminal sets the cli up on a terminal where the person types answer.
func onTerminal(answer string) func(*cli) {
	return func(c *cli) {
		c.stdin, c.stdinTerminal, c.stderrTerminal = strings.NewReader(answer), true, true
	}
}

// A change that asks first makes a dry run, without a key, and on y runs the
// plan: the same inputs with a new key and the dry run's tag.
func TestAChangeThatAsksRunsThePlanItShowed(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	serveDeletion(d, j, lossOfWork())
	run := runCLIWith(context.Background(), d, onTerminal("y\n"), "session", "delete", "app:work")
	if run.code != 0 {
		t.Fatalf("exit %d: %s", run.code, run.stderr)
	}
	sent := d.params(api.SessionDelete.Name)
	if len(sent) != 2 || sent[0] != `{"uuid":"`+sessionID+`"}` {
		t.Fatalf("sent %v", sent)
	}
	var runCall api.SessionDeleteRequest
	if json.Unmarshal([]byte(sent[1]), &runCall) != nil || runCall.UUID != sessionID || runCall.IfMatch != tagOf(0) || !strings.HasPrefix(runCall.Key, "cli-") {
		t.Fatalf("ran %s", sent[1])
	}
	if want := []string{"session.list", "session.delete", "session.delete", "operation.wait"}; !reflect.DeepEqual(d.methods(), want) {
		t.Fatalf("called %v", d.methods())
	}
	contains(t, run.stderr, "THIS WILL LOSE WORK")
	contains(t, run.stderr, "2 commits on branch work")
	contains(t, run.stdout, "Deleted session "+sessionID)
}

// Only y or yes runs the plan; any other answer, or none, exits 3 after the
// dry run alone.
func TestNothingRunsWithoutYes(t *testing.T) {
	for answer, runs := range map[string]bool{"y\n": true, "yes\n": true, " Y \n": true, "n\n": false, "\n": false, "": false, "sure\n": false, "yes please\n": false} {
		d, j := newFakeDaemon(t), &journal{}
		serveDeletion(d, j, lossOfWork())
		run := runCLIWith(context.Background(), d, onTerminal(answer), "session", "delete", sessionID)
		if got := d.count(api.SessionDelete.Name) == 2; got != runs || runs != (run.code == 0) || !runs && run.code != 3 {
			t.Errorf("answer %q: exit %d after %d calls: %s", answer, run.code, d.count(api.SessionDelete.Name), run.stderr)
		}
		contains(t, run.stderr, "Delete session "+sessionID+"? [y/N]")
	}
}

// A plan that is not allowed says why, naming a refusal's target when it is
// not the plan's, prints no loss, which may be incomplete, and exits 3
// without running, with or without --force.
func TestARefusedPlanIsNotRun(t *testing.T) {
	refused := lossOfWork()
	refused.Allowed = false
	refused.Refusals = []api.Refusal{{Code: api.ReasonAttachmentActive}, {Code: api.ReasonOperationBlocked, Target: api.TargetOperation(opID)}}
	for _, force := range []bool{false, true} {
		d, j := newFakeDaemon(t), &journal{}
		serveDeletion(d, j, refused)
		args := []string{"session", "delete", sessionID}
		if force {
			args = append(args, "--force")
		}
		run := runCLIWith(context.Background(), d, onTerminal("y\n"), args...)
		if run.code != 3 || d.count(api.SessionDelete.Name) != 1 {
			t.Fatalf("--force %v: exit %d after %d calls: %s", force, run.code, d.count(api.SessionDelete.Name), run.stderr)
		}
		contains(t, run.stderr, "Detach every terminal from this session first.")
		contains(t, run.stderr, "(operation "+opID+")")
		if strings.Contains(run.stderr, "LOSE WORK") || strings.Contains(run.stderr, "commits") || strings.Contains(run.stderr, "[y/N]") {
			t.Errorf("--force %v: a refused plan printed its loss or asked:\n%s", force, run.stderr)
		}
	}
}

// A change asks only when standard input and standard error are both
// terminals; otherwise, without --force, it prints the plan and exits 3
// after the dry run alone.
func TestWithoutATerminalNothingIsRun(t *testing.T) {
	for name, setup := range map[string]func(*cli){
		"neither":      nil,
		"stdin only":   func(c *cli) { c.stdin, c.stdinTerminal = strings.NewReader("y\n"), true },
		"stderr only":  func(c *cli) { c.stdin, c.stderrTerminal = strings.NewReader("y\n"), true },
		"piped answer": func(c *cli) { c.stdin = strings.NewReader("y\n") },
	} {
		d, j := newFakeDaemon(t), &journal{}
		serveDeletion(d, j, lossOfWork())
		run := runCLIWith(context.Background(), d, setup, "session", "delete", sessionID)
		if run.code != 3 || d.count(api.SessionDelete.Name) != 1 {
			t.Fatalf("%s: exit %d after %d calls: %s", name, run.code, d.count(api.SessionDelete.Name), run.stderr)
		}
		contains(t, run.stderr, "THIS WILL LOSE WORK")
		contains(t, run.stderr, "no terminal to ask")
	}
}

// --force runs a plan that loses work only after printing every item of loss
// and counting down 10 seconds, on a terminal or not.
func TestForceCountsDownBeforeLosingWork(t *testing.T) {
	for _, terminal := range []bool{true, false} {
		d, j := newFakeDaemon(t), &journal{}
		del := serveDeletion(d, j, lossOfWork())
		del.clock = &clock{}
		run := runCLIWith(context.Background(), d, del.clock.forced(terminal), "session", "delete", sessionID, "--force")
		if run.code != 0 || !reflect.DeepEqual(del.ranAt, []int{10}) {
			t.Fatalf("terminal %v: exit %d, ran at seconds %v: %s", terminal, run.code, del.ranAt, run.stderr)
		}
		if countLines(run.stderr, "!!  - ") != 2 || strings.Contains(run.stderr, "[y/N]") {
			t.Errorf("terminal %v: printed the loss as\n%s", terminal, run.stderr)
		}
		contains(t, run.stderr, "Deleting in 10 seconds")
	}
}

// What is already gone is printed as information and starts no countdown:
// --force runs a plan that loses nothing, or only lists what is gone, at
// once, and says it loses no work.
func TestForceRunsAPlanThatLosesNothingAtOnce(t *testing.T) {
	missing := lossOfWork()
	missing.Loss = []api.Loss{}
	missing.Gone = []api.Loss{{Kind: api.LossRuntimeData, Target: api.TargetSession(sessionID), Certain: true}}
	nothing := lossOfWork()
	nothing.Loss = []api.Loss{}
	for name, plan := range map[string]api.Plan[api.RemovalDetails]{"gone": missing, "nothing": nothing} {
		d, j := newFakeDaemon(t), &journal{}
		del := serveDeletion(d, j, plan)
		del.clock = &clock{}
		run := runCLIWith(context.Background(), d, del.clock.forced(true), "session", "delete", sessionID, "--force")
		if run.code != 0 || !reflect.DeepEqual(del.ranAt, []int{0}) || strings.Contains(run.stderr, "LOSE WORK") {
			t.Fatalf("%s: exit %d, ran at seconds %v: %s", name, run.code, del.ranAt, run.stderr)
		}
		if name == "gone" {
			contains(t, run.stderr, "the runtime of session "+sessionID+" is already missing")
		}
		contains(t, run.stderr, "loses no work")
	}
}

// Ctrl-C during the countdown stops it, sends nothing and exits 3.
func TestInterruptingTheCountdownSendsNothing(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	del := serveDeletion(d, j, lossOfWork())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	del.clock = &clock{interruptAt: 4, interrupt: cancel}
	run := runCLIWith(ctx, d, del.clock.forced(true), "session", "delete", sessionID, "--force")
	if run.code != 3 || d.count(api.SessionDelete.Name) != 1 || len(j.ops) != 0 || del.clock.seconds.Load() != 4 {
		t.Fatalf("exit %d after %d calls and %d seconds: %s", run.code, d.count(api.SessionDelete.Name), del.clock.seconds.Load(), run.stderr)
	}
}

// interruptedAnswer is standard input on which the person presses Ctrl-C
// instead of answering.
type interruptedAnswer struct {
	interrupt context.CancelFunc
	never     chan struct{}
}

func (a interruptedAnswer) Read([]byte) (int, error) {
	a.interrupt()
	<-a.never
	return 0, io.EOF
}

// Ctrl-C at the question sends nothing and exits 3.
func TestInterruptingTheQuestionSendsNothing(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	serveDeletion(d, j, lossOfWork())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	never := make(chan struct{})
	defer close(never)
	run := runCLIWith(ctx, d, func(c *cli) {
		c.stdin, c.stdinTerminal, c.stderrTerminal = interruptedAnswer{cancel, never}, true, true
	}, "session", "delete", sessionID)
	if run.code != 3 || d.count(api.SessionDelete.Name) != 1 || len(j.ops) != 0 {
		t.Fatalf("exit %d after %d calls: %s", run.code, d.count(api.SessionDelete.Name), run.stderr)
	}
}

// lossOfMore is lossOfWork once the session's branch was renamed next and
// a third commit reached it.
func lossOfMore() api.Plan[api.RemovalDetails] {
	p := lossOfWork()
	p.Details.Branch, p.Details.AssignedRef = "next", "refs/heads/next"
	p.Loss = []api.Loss{p.Loss[0], {Kind: api.LossPCommits, Target: api.TargetBranch("app", "next"), Count: 3, Certain: true}}
	return p
}

// runKeys and runTags are the keys and tags of the deletion runs d received.
func runKeysAndTags(t *testing.T, d *fakeDaemon) (keys, tags []string) {
	t.Helper()
	for _, params := range d.params(api.SessionDelete.Name) {
		var r api.SessionDeleteRequest
		if err := json.Unmarshal([]byte(params), &r); err != nil {
			t.Fatal(err)
		}
		if !r.DryRun() {
			keys, tags = append(keys, r.Key), append(tags, r.IfMatch)
		}
	}
	return keys, tags
}

// When the state changed between the question and the run, the run is
// refused; the CLI says so, makes a new dry run, shows its plan, naming the
// session's branch as that plan does, and asks again, and runs that plan
// with a new key.
func TestAChangedPlanIsShownAndAskedAgain(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	del := serveDeletion(d, j, lossOfWork(), lossOfMore())
	del.changeAfter = 1
	run := runCLIWith(context.Background(), d, onTerminal("y\ny\n"), "session", "delete", sessionID)
	keys, tags := runKeysAndTags(t, d)
	if run.code != 0 || d.count(api.SessionDelete.Name) != 4 || !reflect.DeepEqual(tags, []string{tagOf(0), tagOf(1)}) || keys[0] == keys[1] {
		t.Fatalf("exit %d after %v, ran with keys %v and tags %v: %s", run.code, d.methods(), keys, tags, run.stderr)
	}
	contains(t, run.stderr, "changed since")
	contains(t, run.stderr, "3 commits on branch next")
	contains(t, run.stderr, "(app:next); its branch next is removed")
	if countLines(run.stderr, "[y/N]") != 2 {
		t.Errorf("asked other than twice:\n%s", run.stderr)
	}
}

// When the state changed during the --force countdown, the run is refused;
// the CLI prints the new plan and stops, so a forced run never acts on a
// plan nobody saw.
func TestForceStopsAtAChangedPlan(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	del := serveDeletion(d, j, lossOfWork(), lossOfMore())
	del.changeAfter, del.clock = 1, &clock{}
	run := runCLIWith(context.Background(), d, del.clock.forced(true), "session", "delete", sessionID, "--force")
	if _, tags := runKeysAndTags(t, d); run.code != 3 || d.count(api.SessionDelete.Name) != 3 || len(tags) != 1 || len(j.ops) != 0 {
		t.Fatalf("exit %d after %v: %s", run.code, d.methods(), run.stderr)
	}
	contains(t, run.stderr, "3 commits on branch next")
	if del.clock.seconds.Load() != 10 {
		t.Errorf("counted %d seconds, want one countdown", del.clock.seconds.Load())
	}
}

// --dry-run prints the plan, warning included, and the change tag that runs
// it, and sends nothing else; with --json it prints the dry run's reply.
func TestDryRunShowsThePlanAndItsTag(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	serveDeletion(d, j, lossOfWork())
	run := runCLI(context.Background(), d, "session", "delete", sessionID, "--dry-run")
	if run.code != 0 || d.count(api.SessionDelete.Name) != 1 || strings.TrimSpace(run.stdout) != tagOf(0) {
		t.Fatalf("exit %d after %d calls, printed %q: %s", run.code, d.count(api.SessionDelete.Name), run.stdout, run.stderr)
	}
	contains(t, run.stderr, "THIS WILL LOSE WORK")
	contains(t, run.stderr, "--if-match "+tagOf(0)+" instead of --dry-run")
	run = runCLI(context.Background(), d, "session", "delete", sessionID, "--dry-run", "--json")
	var printed api.ChangeResult[api.RemovalDetails]
	if run.code != 0 || json.Unmarshal([]byte(run.stdout), &printed) != nil || printed.ChangeTag != tagOf(0) || printed.Plan == nil || len(printed.Plan.Loss) != 2 {
		t.Fatalf("--json: exit %d, printed %s", run.code, run.stdout)
	}
}

// --if-match runs the plan its tag names: no dry run, question or
// countdown. A tag whose plan no longer holds is refused and exits 3,
// still without a dry run.
func TestIfMatchRunsAReviewedPlan(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	del := serveDeletion(d, j, lossOfWork(), lossOfMore())
	run := runCLI(context.Background(), d, "session", "delete", sessionID, "--if-match", tagOf(0))
	keys, tags := runKeysAndTags(t, d)
	if run.code != 0 || d.count(api.SessionDelete.Name) != 1 || !reflect.DeepEqual(tags, []string{tagOf(0)}) || !strings.HasPrefix(keys[0], "cli-") {
		t.Fatalf("exit %d after %v: %s", run.code, d.methods(), run.stderr)
	}
	del.state, del.deleted = 1, false
	run = runCLI(context.Background(), d, "session", "delete", sessionID, "--if-match", tagOf(0), "--json")
	var printed struct{ Error *api.Error }
	if run.code != 3 || d.count(api.SessionDelete.Name) != 2 || json.Unmarshal([]byte(run.stdout), &printed) != nil || printed.Error.Reason() != api.ReasonChanged {
		t.Fatalf("a stale tag: exit %d after %v, printed %s: %s", run.code, d.methods(), run.stdout, run.stderr)
	}
	contains(t, run.stderr, "--dry-run")
}

// --dry-run, --force and --if-match exclude one another, and a dry run
// takes no key and starts nothing to wait for: each such command line exits
// 2 and sends nothing.
func TestTheWaysToRunExcludeOneAnother(t *testing.T) {
	d := newFakeDaemon(t)
	for _, flags := range [][]string{
		{"--dry-run", "--force"},
		{"--dry-run", "--if-match", tagOf(0)},
		{"--force", "--if-match", tagOf(0)},
		{"--dry-run", "--key", "k"},
		{"--dry-run", "--no-wait"},
		{"--if-match", "not-a-tag"},
	} {
		if run := runCLI(context.Background(), d, append([]string{"session", "delete", sessionID}, flags...)...); run.code != 2 {
			t.Errorf("%v: exit %d: %s", flags, run.code, run.stderr)
		}
	}
	if calls := d.methods(); len(calls) != 0 {
		t.Errorf("sent %v", calls)
	}
}

// A deletion whose reply was lost, repeated with its key under the
// PROJECT:BRANCH the session no longer holds, finds its operation by the
// intent and sends no change; the same key naming another session exits 2
// without sending.
func TestSessionDeleteReplaysItsKey(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	serveDeletion(d, j, lossOfWork())
	d.loseReplyAfter(api.SessionDelete.Name, 1)
	first := runCLIWith(context.Background(), d, onTerminal("y\n"), "session", "delete", "app:work")
	key := repeatKey(t, first.stderr)
	if keys, _ := runKeysAndTags(t, d); first.code != 1 || len(keys) != 1 || keys[0] != key {
		t.Fatalf("lost reply: exit %d, ran with keys %v, said %s: %s", first.code, keys, key, first.stderr)
	}
	sent := d.count(api.SessionDelete.Name)
	repeated := runCLI(context.Background(), d, "session", "delete", "app:work", "--key", key)
	if repeated.code != 0 || d.count(api.SessionDelete.Name) != sent {
		t.Fatalf("replay: exit %d after %v: %s", repeated.code, d.methods(), repeated.stderr)
	}
	contains(t, repeated.stdout, "Deleted session "+sessionID+" (app:work)")
	for _, other := range []string{"app:main", "4f1c2a9e-0000-4000-8000-000000000002"} {
		if run := runCLI(context.Background(), d, "session", "delete", other, "--key", key); run.code != 2 {
			t.Errorf("%s: exit %d: %s", other, run.code, run.stderr)
		}
	}
	if run := runCLI(context.Background(), d, "session", "discard", "app:work", "--key", key); run.code != 2 {
		t.Errorf("a discard under the deletion's key: exit %d: %s", run.code, run.stderr)
	}
	if d.count(api.SessionDelete.Name) != sent || d.count(api.SessionDiscard.Name) != 0 {
		t.Fatalf("sent %v", d.methods())
	}
}

// Under --key, a run refused because the plan changed shows the new plan
// and stops with exit 3 instead of asking again, so one key is never sent
// with two tags.
func TestAKeyIsNeverSentWithTwoTags(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	del := serveDeletion(d, j, lossOfWork(), lossOfMore())
	del.changeAfter = 1
	run := runCLIWith(context.Background(), d, onTerminal("y\ny\n"), "session", "delete", sessionID, "--key", "mine")
	keys, tags := runKeysAndTags(t, d)
	if run.code != 3 || !reflect.DeepEqual(keys, []string{"mine"}) || !reflect.DeepEqual(tags, []string{tagOf(0)}) {
		t.Fatalf("exit %d, ran with keys %v and tags %v: %s", run.code, keys, tags, run.stderr)
	}
	contains(t, run.stderr, "3 commits on branch next")
}

// The loss warning is bold red only when standard error is a terminal.
func TestTheWarningIsBoldRedOnlyOnATerminal(t *testing.T) {
	for _, terminal := range []bool{true, false} {
		d, j := newFakeDaemon(t), &journal{}
		serveDeletion(d, j, lossOfWork())
		run := runCLIWith(context.Background(), d, func(c *cli) { c.stderrTerminal = terminal }, "session", "delete", sessionID, "--dry-run")
		if colored := strings.Contains(run.stderr, "\x1b[1;31m!!  THIS WILL LOSE WORK"); colored != terminal || !terminal && strings.Contains(run.stderr, "\x1b[") {
			t.Errorf("terminal %v: the warning reads\n%q", terminal, run.stderr)
		}
	}
}
