package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/errtext"
)

// waitSeconds is the timeout each operation.wait asks for, as the TUI's.
const waitSeconds = 20

// callBudget is how long one call may take, from connecting to the reply.
// A wait gets its timeout and 5 seconds, inside 25 seconds; any other call
// gets the daemon's 30-second request deadline and 5 seconds, so the
// daemon's own answer arrives before the CLI gives up.
func callBudget(method string) time.Duration {
	if method == api.OperationWait.Name {
		return waitSeconds*time.Second + 5*time.Second
	}
	return 35 * time.Second
}

// Bounds of the automatic repeats of one call.
const (
	// retryPause and retryBudget: a refusal that names no operation to wait
	// for is repeated after a pause, for up to 10 seconds.
	retryPause  = 500 * time.Millisecond
	retryBudget = 10 * time.Second
	// heldRepeats bounds the repeats after waiting for a holding operation.
	heldRepeats = 10
	// deadlineRepeats bounds the repeats after the daemon's deadline.
	deadlineRepeats = 2
)

// unreachableError says that the CLI could not connect to the daemon or agree
// the protocol, so it sent no request.
type unreachableError struct {
	socket string
	err    error
}

func (e *unreachableError) Error() string {
	return fmt.Sprintf("Cannot reach the daemon at %s: %v.", e.socket, e.err)
}

// lostReplyError says that the connection failed after the request may have
// been sent: the daemon may have acted on it.
type lostReplyError struct{ err error }

func (e *lostReplyError) Error() string {
	return fmt.Sprintf("The daemon's answer did not arrive (%v).", e.err)
}

// interruptedError says that the person interrupted the CLI before it sent a
// change, or while a change it sent was refused.
type interruptedError struct{}

func (interruptedError) Error() string { return "Interrupted; nothing was changed." }

// once makes one call of m on a connection of its own, within its budget.
func once[Req api.Request, Res any](in *invocation, m api.Method[Req, Res], req Req) (Res, error) {
	var res Res
	if err := req.Validate(); err != nil {
		return res, err
	}
	ctx, cancel := context.WithTimeout(in.ctx, callBudget(m.Name))
	defer cancel()
	conn, err := api.Dial(ctx, in.socket)
	var answer *api.Error
	switch {
	case err == nil:
	case in.ctx.Err() != nil:
		return res, interruptedError{}
	case errors.As(err, &answer):
		return res, err
	default:
		return res, &unreachableError{in.socket, err}
	}
	defer conn.Close()
	res, err = api.Do(ctx, conn, m, req)
	switch {
	case err == nil, errors.As(err, &answer):
		return res, err
	case in.ctx.Err() != nil:
		return res, &lostReplyError{errors.New("interrupted")}
	}
	return res, &lostReplyError{err}
}

// call makes one call of m and repeats it while the daemon answers retry and
// a second call cannot act twice: after a refusal before dispatch, at once
// when the holding operation leaves running or after a pause; after the
// daemon's deadline, which reads, keyed changes, start and stop survive.
func call[Req api.Request, Res any](in *invocation, m api.Method[Req, Res], req Req) (Res, error) {
	return repeat(in, m, req, true)
}

// callOnce is call for a change that a repeat after the daemon's deadline
// could make twice: a service action, which has no key, or a Retry.
func callOnce[Req api.Request, Res any](in *invocation, m api.Method[Req, Res], req Req) (Res, error) {
	return repeat(in, m, req, false)
}

func repeat[Req api.Request, Res any](in *invocation, m api.Method[Req, Res], req Req, afterDeadline bool) (Res, error) {
	var paused time.Duration
	held, deadlines := 0, 0
	for {
		res, err := once(in, m, req)
		var answer *api.Error
		if !errors.As(err, &answer) || answer.Kind() != api.KindRetry {
			return res, err
		}
		holder, named := strings.CutPrefix(answer.Target(), "operation/")
		switch {
		case answer.Reason() == api.ReasonDeadline:
			if !afterDeadline || deadlines == deadlineRepeats {
				return res, err
			}
			deadlines++
		case named && held < heldRepeats:
			held++
			fmt.Fprintf(in.stderr, "Operation %s holds this; waiting for it, then trying again.\n", holder)
			if err := in.waitForHolder(holder); err != nil {
				return res, err
			}
		default:
			if paused >= retryBudget {
				return res, err
			}
			if in.pause(in.ctx, retryPause) != nil {
				return res, interruptedError{}
			}
			paused += retryPause
		}
	}
}

// waitForHolder waits until the operation that holds what a refused call
// needs leaves running. The refused call changed nothing, so a failure of
// this wait is never the refused call's lost reply.
func (in *invocation) waitForHolder(id string) error {
	for {
		r, err := call(in, api.OperationWait, api.OperationWaitRequest{ID: id, Timeout: waitSeconds})
		var answer *api.Error
		switch {
		case in.ctx.Err() != nil:
			return interruptedError{}
		case errors.As(err, &answer) && answer.Kind() == api.KindNotFound:
			return nil
		case err != nil:
			return explained(err, fmt.Sprintf("Waiting for operation %s, which holds this, failed: %s Nothing was changed.", id, in.sentence(err)))
		case r.Operation.Status != "running":
			return nil
		}
	}
}

// follow waits for op until it is no longer running, saying each new phase
// on standard error. It returns the operation as it last saw it, also when
// it stopped waiting.
func (in *invocation) follow(op api.Operation) (api.Operation, error) {
	for op.Status == "running" {
		r, err := call(in, api.OperationWait, api.OperationWaitRequest{ID: op.ID, Timeout: waitSeconds})
		if in.ctx.Err() != nil {
			return op, &exitError{code: exitUnfinished, text: fmt.Sprintf("Stopped waiting. Operation %s runs on; wait for it with: p operation wait %s", op.ID, op.ID)}
		}
		var lost *lostReplyError
		var unreachable *unreachableError
		if errors.As(err, &lost) || errors.As(err, &unreachable) {
			return op, fmt.Errorf("%s Operation %s may still run; wait for it with: p operation wait %s", err, op.ID, op.ID)
		}
		if err != nil {
			return op, err
		}
		if next := r.Operation; next.Status == "running" && next.Phase != op.Phase {
			fmt.Fprintf(in.stderr, "%s %s: %s\n", next.Kind, next.ID, next.Phase)
		}
		op = r.Operation
	}
	return op, nil
}

// report follows op unless --no-wait, then says how it ended; done is the
// line for a completed operation.
func (in *invocation) report(op api.Operation, done func(api.Operation) string) (api.Operation, error) {
	return in.reportAs(op, done, func(op api.Operation) any { return api.OperationResult{Operation: op} })
}

// reportAs is report whose --json output is result of the operation.
func (in *invocation) reportAs(op api.Operation, done func(api.Operation) string, result func(api.Operation) any) (api.Operation, error) {
	if in.noWait {
		if in.json {
			in.printJSON(result(op))
		} else {
			fmt.Fprintln(in.stdout, op.ID)
			fmt.Fprintf(in.stderr, "Accepted. Wait for it with: p operation wait %s\n", op.ID)
		}
		return op, nil
	}
	op, err := in.follow(op)
	if in.json && !answered(err) {
		in.printJSON(result(op))
	}
	if err != nil {
		return op, err
	}
	return op, in.ended(op, done)
}

// answered reports whether err is an answer of the daemon, which --json
// prints as {"error": …} instead of the result.
func answered(err error) bool {
	var answer *api.Error
	return errors.As(err, &answer)
}

// ended says how op, which is no longer running, ended.
func (in *invocation) ended(op api.Operation, done func(api.Operation) string) error {
	if op.Status == "completed" {
		if line := done(op); line != "" && !in.json {
			fmt.Fprintln(in.stdout, line)
		}
		return nil
	}
	text := fmt.Sprintf("Operation %s (%s) %s.", op.ID, op.Kind, op.Status)
	if op.Status == "superseded" {
		text = fmt.Sprintf("Operation %s (%s) was superseded.", op.ID, op.Kind)
	}
	if sentence := errtext.Operation(op.Status, op.Reason); sentence != "" {
		text += " " + sentence
	}
	if op.Diagnostic != "" {
		text += "\nDiagnostic: " + op.Diagnostic
	}
	if op.Status == "blocked" {
		return &exitError{code: exitUnfinished, text: text + "\nRetry it with: p operation retry " + op.ID}
	}
	return &exitError{code: exitFailed, text: text}
}

// newKey is a new idempotency key: cli- and 32 hex digits, as the TUI's tui-
// keys.
func newKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "cli-" + hex.EncodeToString(b[:])
}

// keyedChange runs a change of m, a method that takes an idempotency key and
// records an operation of its own name. With --key it first looks the key up
// in the journal (recordedChange). Otherwise request resolves the command's
// arguments from live state into the request to send with key; done is the
// line for a completed operation. It returns the operation as it last saw
// it.
func keyedChange[Req api.Request](in *invocation, m api.Method[Req, api.OperationResult], matches func(api.OperationIntent) bool, request func(key string) (Req, error), done func(api.Operation) string) (api.Operation, error) {
	recorded, err := in.recordedChange(m.Name, matches)
	if err != nil {
		return api.Operation{}, err
	}
	if recorded != nil {
		return in.report(*recorded, done)
	}
	key := in.changeKey()
	req, err := request(key)
	if err != nil {
		return api.Operation{}, err
	}
	r, err := call(in, m, req)
	if err != nil {
		return api.Operation{}, in.lostChange(err, key)
	}
	return in.report(r.Operation, done)
}

// recordedChange looks --key up in the journal before the command resolves
// any argument from live state, which the first request may have changed:
// the operation of kind it recorded, when its intent matches the command,
// or nil when none is recorded or no --key was given. A key recorded for
// another command is a usage error.
func (in *invocation) recordedChange(kind string, matches func(api.OperationIntent) bool) (*api.Operation, error) {
	if in.key == "" {
		return nil, nil
	}
	recorded, found, err := in.lookupKey(in.key)
	if err != nil || !found {
		return nil, err
	}
	if recorded.Kind != kind || !matches(recorded.Intent) {
		return nil, usageError("The key %s was used for another request: operation %s (%s). Use a new key, or none.", in.key, recorded.ID, recorded.Kind)
	}
	r, err := call(in, api.OperationInspect, api.OperationInspectRequest{ID: recorded.ID})
	if err != nil {
		return nil, err
	}
	return &r.Operation, nil
}

// changeKey is the key of a change to send: --key, or a new one.
func (in *invocation) changeKey() string {
	if in.key != "" {
		return in.key
	}
	return newKey()
}

// lostChange is err, the error of a change sent under key, with the
// command that repeats it when its reply was lost: the change may have been
// made, and the key makes it at most once.
func (in *invocation) lostChange(err error, key string) error {
	var lost *lostReplyError
	if !errors.As(err, &lost) {
		return err
	}
	return &exitError{code: exitFailed, text: fmt.Sprintf("%s The change may have been made. Repeat it with the same key, which makes it at most once:\n  %s", lost.Error(), in.repeatCommand(key))}
}

// lookupKey pages the journal for the operation recorded under key.
func (in *invocation) lookupKey(key string) (api.OperationSummary, bool, error) {
	var found api.OperationSummary
	ok := false
	err := pages(in, api.OperationList, func(after string) api.OperationListRequest {
		return api.OperationListRequest{Limit: 20, After: after}
	}, func(r api.OperationListResult) (string, bool) {
		for _, op := range r.Operations {
			if op.Key == key {
				found, ok = op, true
				return "", false
			}
		}
		return r.Next, true
	})
	return found, ok, err
}

// maxPages bounds the pages of one list.
const maxPages = 4096

// pages reads the pages of m from the first; page builds each request from
// the cursor, and each result says the next cursor and whether to go on.
func pages[Req api.Request, Res any](in *invocation, m api.Method[Req, Res], page func(after string) Req, each func(Res) (next string, more bool)) error {
	after := ""
	seen := map[string]bool{}
	for i := 0; i < maxPages; i++ {
		r, err := call(in, m, page(after))
		if err != nil {
			return err
		}
		next, more := each(r)
		if !more || next == "" {
			return nil
		}
		if seen[next] {
			return fmt.Errorf("The daemon repeated a page of %s.", m.Name)
		}
		seen[next] = true
		after = next
	}
	return fmt.Errorf("The list of %s has more than %d pages.", m.Name, maxPages)
}

func list[Req api.Request, Res, T any](in *invocation, m api.Method[Req, Res], page func(after string) Req, items func(Res) ([]T, string)) ([]T, error) {
	all := []T{}
	err := pages(in, m, page, func(r Res) (string, bool) {
		values, next := items(r)
		all = append(all, values...)
		return next, true
	})
	return all, err
}

// repeatCommand is the command line that repeats this one with key, given
// before any "--", after which every argument is positional.
func (in *invocation) repeatCommand(key string) string {
	argv := in.argv
	if in.key == "" {
		end := len(argv)
		for i, arg := range argv {
			if arg == "--" {
				end = i
				break
			}
		}
		argv = append(append(append([]string{}, argv[:end]...), "--key", key), argv[end:]...)
	}
	words := []string{"p"}
	for _, arg := range argv {
		words = append(words, shellQuote(arg))
	}
	return strings.Join(words, " ")
}

// shellQuote quotes s for a POSIX shell when it needs quoting.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./:=@,+") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// explained is err with text for the person, keeping err's status and,
// for --json, the daemon's answer.
func explained(err error, text string) error {
	failure := &exitError{code: exitStatus(err), text: text}
	if answered(err) {
		failure.err = err
	}
	return failure
}

// explain turns err into an exit error whose text is the sentence for the
// person.
func (in *invocation) explain(err error) error {
	var exit *exitError
	if err == nil || errors.As(err, &exit) {
		return err
	}
	code := exitStatus(err)
	if errors.Is(err, interruptedError{}) {
		code = exitNotRun
	}
	return &exitError{code: code, text: in.sentence(err)}
}

// sentence is what the person reads for err: for an error the daemon
// answered, the sentence for its reason, naming its target.
func (in *invocation) sentence(err error) string {
	var answer *api.Error
	if !errors.As(err, &answer) {
		return err.Error()
	}
	text := errtext.Sentence(answer)
	if target := answer.Target(); target != "" {
		text += " (" + errtext.Target(target) + ")"
	}
	return text
}

func (in *invocation) printJSON(v any) {
	enc := json.NewEncoder(in.stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
