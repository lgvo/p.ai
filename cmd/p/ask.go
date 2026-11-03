package main

import (
	"bufio"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/errtext"
)

// riskyChange is a change that asks: one whose method is called twice, first
// as a dry run that returns the change's plan and its change tag, then with
// a key and the tag to run that plan (docs/control-api.md#risky-changes).
type riskyChange[Req api.Request, D any] struct {
	method api.Method[Req, api.ChangeResult[D]]
	// kind is the kind of operation a run records, and matches whether a
	// recorded operation's intent is this command's, for --key.
	kind    string
	matches func(api.OperationIntent) bool
	// request resolves the command's arguments from live state into the dry
	// run's request; run is that request with the run's key and tag.
	request func() (Req, error)
	run     func(req Req, change api.ChangeRequest) Req
	// heading says what the change does, from the arguments and the plan's
	// details; question asks whether to do it.
	heading  func(api.Plan[D]) string
	question func() string
	// counting begins a --force countdown, such as "Deleting".
	counting string
	// done is the line for a completed operation.
	done func(api.Operation) string
}

// askChange makes a dry run of c, prints its plan, asks, and runs the plan
// with its change tag. When the state changed in the meantime, the run is
// refused: it makes a new dry run, shows that plan and asks again, but a
// forced run stops there, so it never acts on a plan nobody saw. It returns
// the operation as it last saw it.
func askChange[Req api.Request, D any](in *invocation, c riskyChange[Req, D]) (api.Operation, error) {
	if err := in.refuseAskFlags(); err != nil {
		return api.Operation{}, err
	}
	recorded, err := in.recordedChange(c.kind, c.matches)
	if err != nil {
		return api.Operation{}, err
	}
	if recorded != nil {
		return in.reportAs(*recorded, c.done, func(op api.Operation) any { return api.ChangeResult[D]{Operation: &op} })
	}
	req, err := c.request()
	if err != nil {
		return api.Operation{}, err
	}
	if in.ifMatch != "" {
		op, err := runPlan(in, c, req, api.ChangeResult[D]{}, in.ifMatch)
		if stale := staleTag(err); stale != nil {
			return op, explained(err, preconditionSentence(stale)+" Nothing was changed; review the plan again with --dry-run.")
		}
		return op, err
	}
	plan, err := dryRun(in, c, req)
	if err != nil {
		return api.Operation{}, err
	}
	// notRun ends the command with the last plan, which --json prints.
	notRun := func(err error) (api.Operation, error) {
		if in.json {
			in.printJSON(plan)
		}
		return api.Operation{}, err
	}
	for {
		if !plan.Plan.Allowed || plan.ChangeTag == "" {
			return notRun(&exitError{code: exitNotRun, text: "Not run: P does not allow this change now; nothing was changed."})
		}
		if in.dryRun {
			if !in.json {
				fmt.Fprintln(in.stdout, plan.ChangeTag)
				fmt.Fprintf(in.stderr, "Nothing was changed. To run this plan within two minutes, repeat the command with --if-match %s instead of --dry-run.\n", plan.ChangeTag)
			}
			return notRun(nil)
		}
		if err := in.authorize(c.question(), c.counting, len(plan.Plan.Loss) > 0); err != nil {
			return notRun(err)
		}
		op, err := runPlan(in, c, req, plan, plan.ChangeTag)
		stale := staleTag(err)
		if stale == nil {
			return op, err
		}
		fmt.Fprintln(in.stderr, preconditionSentence(stale)+" Nothing was changed; here is the plan now.")
		if plan, err = dryRun(in, c, req); err != nil {
			return api.Operation{}, err
		}
		switch {
		case !plan.Plan.Allowed:
		case in.force:
			return notRun(&exitError{code: exitNotRun, text: "Not run: the plan changed after --force; review it and run the command again."})
		case in.key != "":
			return notRun(&exitError{code: exitNotRun, text: "Not run: the plan changed, and a key goes with one plan only; review it and run the command again with a new key, or none."})
		}
	}
}

// refuseAskFlags refuses flags that make no sense together on a change that
// asks: the three ways to run it, and a dry run with a key or --no-wait.
func (in *invocation) refuseAskFlags() error {
	ways := 0
	for _, set := range []bool{in.dryRun, in.force, in.ifMatch != ""} {
		if set {
			ways++
		}
	}
	switch {
	case ways > 1:
		return usageError("--dry-run, --force and --if-match exclude one another")
	case in.dryRun && (in.key != "" || in.noWait):
		return usageError("--dry-run changes nothing: it takes no --key or --no-wait")
	}
	return nil
}

// runPlan runs the plan that tag names, with req and --key or a new key,
// and reports its operation; plan is the dry run that --json prints with
// it, if any.
func runPlan[Req api.Request, D any](in *invocation, c riskyChange[Req, D], req Req, plan api.ChangeResult[D], tag string) (api.Operation, error) {
	key := in.changeKey()
	ran, err := call(in, c.method, c.run(req, api.ChangeRequest{Key: key, IfMatch: tag}))
	if err != nil {
		return api.Operation{}, in.lostChange(err, key)
	}
	if ran.Operation == nil {
		return api.Operation{}, fmt.Errorf("The daemon answered %s's run without an operation.", c.method.Name)
	}
	return in.reportAs(*ran.Operation, c.done, func(op api.Operation) any {
		plan.Operation = &op
		return plan
	})
}

// staleTag is err when it refuses a run because its change tag no longer
// names the current plan, or nil.
func staleTag(err error) *api.Error {
	var answer *api.Error
	if errors.As(err, &answer) && answer.Kind() == api.KindPreconditionFailed {
		return answer
	}
	return nil
}

// preconditionSentence says why a run's change tag was refused.
func preconditionSentence(stale *api.Error) string {
	if stale.Reason() == api.ReasonExpired {
		return "The plan expired before it ran: a plan holds for two minutes."
	}
	return "The state changed since the plan was made."
}

// dryRun makes a dry run of c with req and prints its plan.
func dryRun[Req api.Request, D any](in *invocation, c riskyChange[Req, D], req Req) (api.ChangeResult[D], error) {
	result, err := call(in, c.method, req)
	if err != nil {
		return result, err
	}
	if result.Plan == nil {
		return result, fmt.Errorf("The daemon answered %s's dry run without a plan.", c.method.Name)
	}
	showPlan(in, *result.Plan, c.heading(*result.Plan))
	return result, nil
}

// showPlan prints plan p on standard error, where the person reads it even
// when standard output goes elsewhere: the heading, then why the change
// cannot run, or what is already gone and the warning of what it loses.
func showPlan[D any](in *invocation, p api.Plan[D], heading string) {
	fmt.Fprintln(in.stderr, heading)
	if !p.Allowed {
		// The loss of a plan that is not allowed may be incomplete.
		fmt.Fprintln(in.stderr, "\nNot allowed now:")
		for _, refusal := range p.Refusals {
			sentence := errtext.Refusal(refusal.Code)
			if refusal.Target != "" && refusal.Target != p.Target {
				sentence += " (" + errtext.Target(refusal.Target) + ")"
			}
			fmt.Fprintln(in.stderr, "  - "+sentence)
		}
		return
	}
	if len(p.Gone) > 0 {
		fmt.Fprintln(in.stderr, "\nAlready gone, whatever happens:")
		for _, item := range p.Gone {
			fmt.Fprintln(in.stderr, "  - "+errtext.Gone(item))
		}
	}
	if len(p.Loss) == 0 {
		fmt.Fprintln(in.stderr, "It loses no work.")
		return
	}
	in.warn(p.Loss)
}

// lossWidth is the width of the loss warning's block.
const lossWidth = 72

// warn prints the large block that lists every item of loss.
func (in *invocation) warn(loss []api.Loss) {
	bar := strings.Repeat("!", lossWidth)
	lines := []string{bar, "!!  THIS WILL LOSE WORK", "!!"}
	for _, item := range loss {
		for i, line := range wrap(errtext.Loss(item), lossWidth-8) {
			prefix := "!!  - "
			if i > 0 {
				prefix = "!!    "
			}
			lines = append(lines, prefix+line)
		}
	}
	lines = append(lines, bar)
	fmt.Fprintln(in.stderr)
	for _, line := range lines {
		if in.stderrTerminal {
			line = boldRed + line + plain
		}
		fmt.Fprintln(in.stderr, line)
	}
	fmt.Fprintln(in.stderr)
}

// The terminal's escapes for the loss warning, used only when standard
// error is a terminal.
const (
	boldRed = "\x1b[1;31m"
	plain   = "\x1b[0m"
)

// wrap breaks text into lines of at most width bytes at spaces; a longer
// word stays whole.
func wrap(text string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case len(line)+1+len(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	return append(lines, line)
}

// authorize decides whether to run a plan that is allowed: with --force at
// once, or after a countdown when the plan loses work; otherwise the person
// answers question, which needs a terminal.
func (in *invocation) authorize(question, counting string, losesWork bool) error {
	if in.force {
		if !losesWork {
			return nil
		}
		return in.countdown(counting)
	}
	if !in.stdinTerminal || !in.stderrTerminal {
		return &exitError{code: exitNotRun, text: "Not run: no terminal to ask. Run again with --force."}
	}
	yes, err := in.ask(question)
	if err != nil {
		return err
	}
	if !yes {
		return &exitError{code: exitNotRun, text: "Not run; nothing was changed."}
	}
	return nil
}

// countdownSeconds is how long --force waits before a change that loses
// work.
const countdownSeconds = 10

// countdown counts down before a forced change that loses work, so a
// forced loss is hard to miss. SIGINT or SIGTERM cancels it, and nothing is
// sent. On a terminal it shows each second; otherwise it says it once.
func (in *invocation) countdown(counting string) error {
	if in.stderrTerminal {
		fmt.Fprintf(in.stderr, "%s in %d seconds. Press Ctrl-C to cancel. ", counting, countdownSeconds)
	} else {
		fmt.Fprintf(in.stderr, "%s in %d seconds; send SIGINT to cancel.\n", counting, countdownSeconds)
	}
	for left := countdownSeconds; left > 0; left-- {
		if in.stderrTerminal {
			fmt.Fprintf(in.stderr, " %d", left)
		}
		if in.pause(in.ctx, time.Second) != nil {
			fmt.Fprintln(in.stderr)
			return interruptedError{}
		}
	}
	if in.stderrTerminal {
		fmt.Fprintln(in.stderr)
	}
	return nil
}

// ask asks question on standard error and reads the answer: only y or yes
// says yes. Ctrl-C while it waits ends the command, nothing changed.
func (in *invocation) ask(question string) (bool, error) {
	fmt.Fprintf(in.stderr, "%s [y/N] ", question)
	if in.answers == nil {
		in.answers = bufio.NewReader(in.stdin)
	}
	answer := make(chan string, 1)
	go func() {
		line, _ := in.answers.ReadString('\n')
		answer <- line
	}()
	select {
	case <-in.ctx.Done():
		fmt.Fprintln(in.stderr)
		return false, interruptedError{}
	case line := <-answer:
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true, nil
		}
		return false, nil
	}
}
