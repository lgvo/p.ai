package control

import (
	"context"
	"errors"
	"log"

	"github.com/lgvo/p.ai/internal/api"
)

var (
	// ErrConflict, ErrNotFound and ErrInvalid classify a failure inside the
	// daemon. A request's answer comes from a Refusal where the condition is
	// found; a bare ErrInvalid answers invalid, a bare ErrNotFound not_found,
	// and a bare ErrConflict internal (answerError).
	ErrConflict = errors.New("conflict")
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid input")
)

// Refusal is an error that answers its request with its own kind and reason
// (docs/archive/control-api.md#errors). Its cause classifies it inside the daemon
// and goes to the daemon's log, never into the answer.
type Refusal struct {
	Kind   api.ErrorKind
	Reason string
	Target string
	Field  string
	Cause  error
}

// Error is the cause's text, the detail an operation's diagnostic and the
// daemon's log keep; a refusal with no cause beyond its class reads as its
// kind, reason and target.
func (r *Refusal) Error() string {
	if r.Cause != nil && r.Cause != ErrConflict && r.Cause != ErrNotFound && r.Cause != ErrInvalid {
		return r.Cause.Error()
	}
	text := string(r.Kind) + ": " + r.Reason
	if r.Target != "" {
		text += " (" + r.Target + ")"
	}
	return text
}

func (r *Refusal) Unwrap() error { return r.Cause }

// Retry refuses a request because something holds target for a moment;
// sending the same request again succeeds once it leaves. Target names the
// holding operation when there is one.
func Retry(reason, target string) error {
	return &Refusal{Kind: api.KindRetry, Reason: reason, Target: target, Cause: ErrConflict}
}

// Conflict refuses a request because the state of target forbids it until
// something else changes it.
func Conflict(reason, target string) error {
	return &Refusal{Kind: api.KindConflict, Reason: reason, Target: target, Cause: ErrConflict}
}

// Stale refuses a request because a condition it sent no longer holds: a
// change tag expired, or the facts it reviewed or expected changed.
func Stale(reason string) error {
	return &Refusal{Kind: api.KindPreconditionFailed, Reason: reason, Cause: ErrConflict}
}

// KeyReused refuses a request whose idempotency key is recorded for another
// request.
func KeyReused() error {
	return &Refusal{Kind: api.KindInvalid, Reason: api.ReasonKeyReused, Field: "key", Cause: ErrConflict}
}

// Full refuses a creation because a table P bounds is full.
func Full(reason string) error {
	return &Refusal{Kind: api.KindCapacity, Reason: reason, Cause: ErrConflict}
}

// NotFound answers that the record target does not exist; reason is the kind
// of record.
func NotFound(reason, target string) error {
	return &Refusal{Kind: api.KindNotFound, Reason: reason, Target: target, Cause: ErrNotFound}
}

// NotConfigured answers that the host configuration lacks capability.
func NotConfigured(capability string) error {
	return &Refusal{Kind: api.KindNotConfigured, Reason: capability, Cause: ErrNotFound}
}

// Invalid answers that the request's field is invalid; field may be empty.
func Invalid(field string) error {
	return &Refusal{Kind: api.KindInvalid, Reason: api.ReasonParam, Field: field, Cause: ErrInvalid}
}

// Failed answers that authority (runtime, git, origin or plugin) failed or
// could not be observed. An error that already has an answer keeps it: a
// Refusal, a classified failure, or a context error.
func Failed(authority string, err error) error {
	if err == nil || classified(err) {
		return err
	}
	return &Refusal{Kind: api.KindFailed, Reason: authority, Cause: err}
}

// classified reports whether err already decides its answer.
func classified(err error) bool {
	var refusal *Refusal
	var apiErr *api.Error
	return errors.As(err, &refusal) || errors.As(err, &apiErr) || errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) ||
		errors.Is(err, ErrInvalid) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// withTarget names target in err's refusal when it names none.
func withTarget(err error, target string) error {
	var refusal *Refusal
	if errors.As(err, &refusal) && refusal.Target == "" {
		refusal.Target = target
	}
	return err
}

// OutcomeUnknown is the failure of operation work that issued an effect it
// cannot tell happened: P does not issue it again, and the operation blocks
// with reason outcome_unknown.
func OutcomeUnknown(cause error) error {
	return &Refusal{Kind: api.KindFailed, Reason: api.ReasonOutcomeUnknown, Cause: cause}
}

// Internal answers internal with reason, for a failure whose cause the
// daemon knows: the store or an encoding.
func Internal(reason string, err error) error {
	return &Refusal{Kind: api.KindInternal, Reason: reason, Cause: err}
}

// OperationReason is the reason an operation that blocks or fails because of
// err records: the reason of the refusal err carries, or internal for a
// failure the daemon did not classify.
func OperationReason(err error) string {
	var refusal *Refusal
	if errors.As(err, &refusal) && refusal.Reason != "" {
		return refusal.Reason
	}
	return api.ReasonInternal
}

// answerError maps an error a handler returned to the error its request
// answers, by three rules in order:
//
//  1. A context error, or any error once ctx ended: cancelled when the client
//     cancelled or closed the connection, retry with reason deadline when the
//     daemon's deadline passed.
//  2. A Refusal or an *api.Error answers its own kind; a bare ErrInvalid
//     answers invalid and a bare ErrNotFound not_found.
//  3. Anything else answers internal with reason unexpected.
func answerError(ctx context.Context, err error) *api.Error {
	cause := err
	if ctx.Err() != nil {
		cause = ctx.Err()
	}
	switch {
	case errors.Is(cause, context.DeadlineExceeded):
		return api.NewError(api.KindRetry, api.ReasonDeadline)
	case errors.Is(cause, context.Canceled):
		return api.NewError(api.KindCancelled, "")
	}
	var refusal *Refusal
	if errors.As(err, &refusal) {
		answer := api.NewError(refusal.Kind, refusal.Reason)
		if answer.Data != nil {
			answer.Data.Target, answer.Data.Field = refusal.Target, refusal.Field
		}
		return answer
	}
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	switch {
	case errors.Is(err, ErrInvalid):
		return api.InvalidParam("")
	case errors.Is(err, ErrNotFound):
		return api.NewError(api.KindNotFound, "")
	}
	return api.NewError(api.KindInternal, api.ReasonUnexpected)
}

// logAnswer logs the cause behind an error answer, with the request it
// answers, so the log shows what the answer leaves out.
func logAnswer(ctx context.Context, method string, answer *api.Error, cause error) {
	id, _ := ctx.Value(requestIDKey{}).(string)
	if cause == nil {
		log.Printf("control request %s id %s answered %s", method, id, answer.Error())
		return
	}
	log.Printf("control request %s id %s answered %s: %v", method, id, answer.Error(), cause)
}

// requestIDKey holds the canonical id of the request a context serves.
type requestIDKey struct{}
