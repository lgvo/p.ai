package api

import (
	"strings"
	"time"
)

// A risky change, one that can lose work or replace something, has one
// method that is called twice (docs/archive/control-api.md#risky-changes). The first
// call, a dry run, sends neither Key nor IfMatch and returns the change's plan
// and, when the change is allowed, its change tag. The second call sends the
// tag back in IfMatch with an idempotency Key; the daemon plans the change
// again and runs it only if that gives the same tag. The daemon keeps nothing
// between the two calls.

// ChangeRequest is embedded in the request of every risky change.
type ChangeRequest struct {
	Key     string `json:"key,omitempty"`
	IfMatch string `json:"if_match,omitempty"`
}

// DryRun reports whether the call asks for the change's plan.
func (r ChangeRequest) DryRun() bool { return r.Key == "" && r.IfMatch == "" }

// Validate refuses a run call that lacks its tag with precondition_required,
// and one that lacks its key as invalid.
func (r ChangeRequest) Validate() error {
	switch {
	case r.Key != "" && !validKey(r.Key):
		return invalid("key")
	case r.IfMatch != "" && !ValidChangeTag(r.IfMatch):
		return invalid("if_match")
	case r.Key != "" && r.IfMatch == "":
		return PreconditionRequired()
	case r.Key == "" && r.IfMatch != "":
		return invalid("key")
	}
	return nil
}

// PreconditionRequired is the error for a run call without its change tag.
func PreconditionRequired() *Error {
	return NewError(KindPreconditionRequired, ReasonIfMatchMissing)
}

// ChangeResult answers a risky change: a dry run's plan, with the change tag
// when the plan is allowed, or the operation that a run recorded.
type ChangeResult[D any] struct {
	Plan      *Plan[D]   `json:"plan,omitempty"`
	ChangeTag string     `json:"change_tag,omitempty"`
	Operation *Operation `json:"operation,omitempty"`
}

// Plan is what a risky change would do now: whether it can run, why not,
// what it would destroy, what is already lost, and the change's own details.
// Allowed is true exactly when Refusals is empty. A plan that is not allowed
// may stop at its first refusal, so only an allowed plan's Loss is complete.
type Plan[D any] struct {
	Action   string    `json:"action"`
	Target   string    `json:"target"`
	Allowed  bool      `json:"allowed"`
	Refusals []Refusal `json:"refusals"`
	Loss     []Loss    `json:"loss"`
	Gone     []Loss    `json:"gone"`
	Details  D         `json:"details"`
}

// Refusal is why a change cannot run now. Code shares one namespace with
// error reasons, so a client keeps one sentence per code. Target names what
// the refusal is about when it is not the plan's target.
type Refusal struct {
	Code   string `json:"code"`
	Target string `json:"target,omitempty"`
}

// Loss is one category of what a change destroys (in Plan.Loss) or of what is
// already lost (in Plan.Gone). Target is where the lost thing lives, in
// target form. Count and Sample are set where P counts the items; Sample
// holds at most MaxLossSample commit IDs. Certain is false when P cannot rule
// the loss out, and Reason then says why.
type Loss struct {
	Kind    string   `json:"kind"`
	Target  string   `json:"target"`
	Count   int      `json:"count,omitempty"`
	Sample  []string `json:"sample,omitempty"`
	Certain bool     `json:"certain"`
	Reason  string   `json:"reason,omitempty"`
}

// The kinds of loss, in the order a plan lists them within one target.
const (
	// LossRuntimeData is a session's container and everything in it:
	// uncommitted changes, files outside Git, commits not pushed to P,
	// processes, home, temporary files and runtime-local credentials. P does
	// not look inside.
	LossRuntimeData = "runtime_data"
	// LossPCommits is commits that leave P's repository: no remaining branch
	// there keeps them.
	LossPCommits = "p_commits"
	// LossUnreachableObjects is Git objects in P's repository that no branch
	// reaches.
	LossUnreachableObjects = "unreachable_objects"
)

// LossNotEnumerated is the reason of a loss P does not count.
const LossNotEnumerated = "not_enumerated"

// MaxLossSample bounds a loss item's sample.
const MaxLossSample = 5

// ChangeTagTimeLayout is the layout of a change tag's expiry: UTC, to the
// second, in ISO 8601 basic format.
const ChangeTagTimeLayout = "20060102T150405Z"

// ParseChangeTag returns the expiry of change tag tag, which is the expiry
// in ChangeTagTimeLayout, a dash, and 64 lowercase hex digits.
func ParseChangeTag(tag string) (time.Time, bool) {
	expiry, hash, found := strings.Cut(tag, "-")
	if !found || len(expiry) != len(ChangeTagTimeLayout) || len(hash) != 64 || !lowerHex(hash) {
		return time.Time{}, false
	}
	t, err := time.Parse(ChangeTagTimeLayout, expiry)
	if err != nil || t.Format(ChangeTagTimeLayout) != expiry {
		return time.Time{}, false
	}
	return t, true
}

// ValidChangeTag reports whether tag has a change tag's form.
func ValidChangeTag(tag string) bool {
	_, ok := ParseChangeTag(tag)
	return ok
}
