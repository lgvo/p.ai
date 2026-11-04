package control

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

// changeFixture is a risky change over a fake state: its plan shows the
// state's facts, a fact in refuse makes planning stop with that conflict,
// and Begin records an operation per key, as a store does.
type changeFixture struct {
	facts    map[string]string
	refuse   string
	failure  error
	plans    int
	begun    []Run[map[string]string]
	recorded map[string]api.Operation
	requests map[string]api.ChangeRequest
	// expire runs inside Begin, before its expiry check: it stands for time
	// that passes while a store plans the change again.
	expire func()
}

func newChangeFixture() *changeFixture {
	return &changeFixture{facts: map[string]string{"tip": strings.Repeat("a", 40), "runtime": "present"},
		recorded: map[string]api.Operation{}, requests: map[string]api.ChangeRequest{}}
}

func (f *changeFixture) change(action, target string, req api.ChangeRequest) Change[map[string]string] {
	return Change[map[string]string]{
		Action: action, Target: target, Instance: "11111111-1111-4111-8111-111111111111",
		Hold: func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) },
		Plan: func(_ context.Context, p *api.Plan[map[string]string]) error {
			f.plans++
			p.Details = map[string]string{}
			for k, v := range f.facts {
				p.Details[k] = v
			}
			p.Loss = append(p.Loss, api.Loss{Kind: api.LossRuntimeData, Target: target, Certain: true})
			if f.failure != nil {
				return f.failure
			}
			if f.refuse != "" {
				return Conflict(f.refuse, "session/other")
			}
			return nil
		},
		Replay: func(context.Context) (api.Operation, bool, error) {
			op, found := f.recorded[req.Key]
			if found && f.requests[req.Key] != req {
				return api.Operation{}, false, KeyReused()
			}
			return op, found, nil
		},
		Begin: func(ctx context.Context, run Run[map[string]string]) (api.Operation, error) {
			if err := run.Recheck(ctx); err != nil {
				return api.Operation{}, err
			}
			if f.expire != nil {
				f.expire()
			}
			if expiry, _ := api.ParseChangeTag(req.IfMatch); !time.Now().Before(expiry) {
				return api.Operation{}, Stale(api.ReasonExpired)
			}
			f.begun = append(f.begun, run)
			op := api.Operation{ID: "op-" + req.Key, Key: req.Key, Kind: action}
			f.recorded[req.Key], f.requests[req.Key] = op, req
			return op, nil
		},
	}
}

func (f *changeFixture) do(t *testing.T, action, target string, req api.ChangeRequest) (api.ChangeResult[map[string]string], error) {
	t.Helper()
	return f.change(action, target, req).Do(context.Background(), req)
}

// dryRun is the dry run of the fixture's change of session/s1, which must
// be allowed.
func (f *changeFixture) dryRun(t *testing.T) api.ChangeResult[map[string]string] {
	t.Helper()
	result, err := f.do(t, "session.delete", "session/s1", api.ChangeRequest{})
	if err != nil || result.ChangeTag == "" || result.Plan == nil {
		t.Fatalf("dry run: %+v %v", result, err)
	}
	return result
}

// wantStale fails unless err is precondition_failed with reason, an error
// that carries no tag and no plan.
func wantStale(t *testing.T, what string, err error, reason string) {
	t.Helper()
	if !answers(err, api.KindPreconditionFailed, reason) {
		t.Fatalf("%s answered %v, want precondition_failed %s", what, err, reason)
	}
}

// A dry run returns the plan with its envelope and a tag whose expiry is
// two minutes away, to the second; it records nothing. Two dry runs of the
// same state give the same change hash.
func TestChangeDryRunPlansAndRecordsNothing(t *testing.T) {
	f := newChangeFixture()
	before := time.Now().UTC().Truncate(time.Second)
	first := f.dryRun(t)
	p := first.Plan
	if p.Action != "session.delete" || p.Target != "session/s1" || !p.Allowed || p.Refusals == nil || len(p.Refusals) != 0 || p.Gone == nil ||
		!reflect.DeepEqual(p.Loss, []api.Loss{{Kind: api.LossRuntimeData, Target: "session/s1", Certain: true}}) || p.Details["tip"] != f.facts["tip"] || first.Operation != nil {
		t.Fatalf("plan: %+v", first)
	}
	expiry, ok := api.ParseChangeTag(first.ChangeTag)
	if !ok || expiry.Before(before.Add(ChangeTTL)) || expiry.After(time.Now().Add(ChangeTTL)) {
		t.Fatalf("tag %s expires %v", first.ChangeTag, expiry)
	}
	if len(f.begun) != 0 || len(f.recorded) != 0 {
		t.Fatal("a dry run began an operation")
	}
	second := f.dryRun(t)
	h1, _ := f.change("session.delete", "session/s1", api.ChangeRequest{}).hash(*first.Plan)
	h2, _ := f.change("session.delete", "session/s1", api.ChangeRequest{}).hash(*second.Plan)
	if h1 != h2 || len(h1) != 64 {
		t.Fatalf("the same state gave change hashes %s and %s", h1, h2)
	}
	if encoded, _ := json.Marshal(first); strings.Contains(string(encoded), h1) {
		t.Fatal("the dry run's reply shows the change hash")
	}
	// Every fact of the plan, the action, the target, the inputs and the
	// instance are in the change hash.
	for name, other := range map[string]func() (string, error){
		"a fact": func() (string, error) {
			changed := *first.Plan
			changed.Details = map[string]string{"tip": strings.Repeat("b", 40), "runtime": "present"}
			return f.change("session.delete", "session/s1", api.ChangeRequest{}).hash(changed)
		},
		"a loss": func() (string, error) {
			changed := *first.Plan
			changed.Loss = nil
			return f.change("session.delete", "session/s1", api.ChangeRequest{}).hash(changed)
		},
		"the action": func() (string, error) {
			return f.change("session.discard", "session/s1", api.ChangeRequest{}).hash(*first.Plan)
		},
		"the target": func() (string, error) {
			return f.change("session.delete", "session/s2", api.ChangeRequest{}).hash(*first.Plan)
		},
		"the inputs": func() (string, error) {
			c := f.change("session.delete", "session/s1", api.ChangeRequest{})
			c.Inputs = map[string]string{"source": "refs/heads/main"}
			return c.hash(*first.Plan)
		},
		"the instance": func() (string, error) {
			c := f.change("session.delete", "session/s1", api.ChangeRequest{})
			c.Instance = "22222222-2222-4222-8222-222222222222"
			return c.hash(*first.Plan)
		},
	} {
		if h, err := other(); err != nil || h == h1 {
			t.Errorf("changing %s kept change hash %s: %v", name, h, err)
		}
	}
}

// A run with the dry run's tag records the operation with the change hash;
// the same key and tag return it again, also once the tag expired; the same
// key with another tag is reused.
func TestChangeRunRecordsTheTaggedPlanOnce(t *testing.T) {
	f := newChangeFixture()
	plan := f.dryRun(t)
	run := api.ChangeRequest{Key: "k1", IfMatch: plan.ChangeTag}
	result, err := f.do(t, "session.delete", "session/s1", run)
	if err != nil || result.Operation == nil || result.Operation.ID != "op-k1" || result.Plan != nil || result.ChangeTag != "" || len(f.begun) != 1 {
		t.Fatalf("run: %+v %v", result, err)
	}
	hash, _ := f.change("session.delete", "session/s1", api.ChangeRequest{}).hash(*plan.Plan)
	if f.begun[0].Hash != hash || !reflect.DeepEqual(f.begun[0].Plan, *plan.Plan) {
		t.Fatalf("began %+v, want the dry run's plan and hash %s", f.begun[0], hash)
	}
	plans := f.plans
	if replay, err := f.do(t, "session.delete", "session/s1", run); err != nil || replay.Operation.ID != "op-k1" || len(f.begun) != 1 || f.plans != plans {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	if _, err := f.do(t, "session.delete", "session/s1", api.ChangeRequest{Key: "k1", IfMatch: changeTag(time.Now().Add(time.Hour), hash)}); !answers(err, api.KindInvalid, api.ReasonKeyReused) {
		t.Fatalf("the key with another tag answered %v", err)
	}
	// A run replays once its tag expired, after the state changed.
	short := api.ChangeRequest{Key: "k2", IfMatch: changeTag(time.Now().Add(2*time.Second), hash)}
	if _, err := f.do(t, "session.delete", "session/s1", short); err != nil {
		t.Fatal(err)
	}
	expiry, _ := api.ParseChangeTag(short.IfMatch)
	time.Sleep(time.Until(expiry) + 10*time.Millisecond)
	f.facts["tip"] = strings.Repeat("c", 40)
	if replay, err := f.do(t, "session.delete", "session/s1", short); err != nil || replay.Operation.ID != "op-k2" || len(f.begun) != 2 {
		t.Fatalf("replay of an expired tag: %+v %v", replay, err)
	}
}

// A run is refused when its tag expired or the state it was planned from
// changed, and when it lacks its tag or its key; no refusal carries a tag or
// a plan, and none records an operation.
func TestChangeRunRefusals(t *testing.T) {
	f := newChangeFixture()
	plan := f.dryRun(t)
	hash, _ := f.change("session.delete", "session/s1", api.ChangeRequest{}).hash(*plan.Plan)
	expiry, _ := api.ParseChangeTag(plan.ChangeTag)
	for _, test := range []struct {
		name    string
		prepare func()
		action  string
		target  string
		req     api.ChangeRequest
		check   func(error) bool
	}{
		{"an expired tag", nil, "session.delete", "session/s1", api.ChangeRequest{Key: "expired", IfMatch: changeTag(time.Now().Add(-time.Second), hash)},
			func(err error) bool { return answers(err, api.KindPreconditionFailed, api.ReasonExpired) }},
		{"an edited expiry", nil, "session.delete", "session/s1",
			api.ChangeRequest{Key: "edited", IfMatch: expiry.Add(time.Hour).Format(api.ChangeTagTimeLayout) + plan.ChangeTag[len(api.ChangeTagTimeLayout):]},
			func(err error) bool { return answers(err, api.KindPreconditionFailed, api.ReasonChanged) }},
		{"a changed fact", func() { f.facts["tip"] = strings.Repeat("b", 40) }, "session.delete", "session/s1", api.ChangeRequest{Key: "changed", IfMatch: plan.ChangeTag},
			func(err error) bool { return answers(err, api.KindPreconditionFailed, api.ReasonChanged) }},
		{"a plan no longer allowed", func() { f.refuse = api.ReasonAttachmentActive }, "session.delete", "session/s1", api.ChangeRequest{Key: "refused", IfMatch: plan.ChangeTag},
			func(err error) bool { return answers(err, api.KindPreconditionFailed, api.ReasonChanged) }},
		{"the tag of another action", nil, "session.discard", "session/s1", api.ChangeRequest{Key: "action", IfMatch: plan.ChangeTag},
			func(err error) bool { return answers(err, api.KindPreconditionFailed, api.ReasonChanged) }},
		{"the tag of another target", nil, "session.delete", "session/s2", api.ChangeRequest{Key: "target", IfMatch: plan.ChangeTag},
			func(err error) bool { return answers(err, api.KindPreconditionFailed, api.ReasonChanged) }},
		{"a key without a tag", nil, "session.delete", "session/s1", api.ChangeRequest{Key: "untagged"},
			func(err error) bool {
				return reflect.DeepEqual(answerError(context.Background(), err), api.NewError(api.KindPreconditionRequired, api.ReasonIfMatchMissing))
			}},
		{"a tag without a key", nil, "session.delete", "session/s1", api.ChangeRequest{IfMatch: plan.ChangeTag},
			func(err error) bool {
				return reflect.DeepEqual(answerError(context.Background(), err), api.InvalidParam("key"))
			}},
		{"a planning failure", func() { f.failure = Failed(api.ReasonRuntime, errors.New("incus down")) }, "session.delete", "session/s1", api.ChangeRequest{Key: "failed", IfMatch: plan.ChangeTag},
			func(err error) bool { return answers(err, api.KindFailed, api.ReasonRuntime) }},
		{"a tag that expires while the run plans again", func() { f.expire = func() { time.Sleep(time.Until(expiry) + 10*time.Millisecond) } }, "session.delete", "session/s1",
			api.ChangeRequest{Key: "late"},
			func(err error) bool { return answers(err, api.KindPreconditionFailed, api.ReasonExpired) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f.facts["tip"], f.refuse, f.failure, f.expire = strings.Repeat("a", 40), "", nil, nil
			if test.name == "a tag that expires while the run plans again" {
				expiry = time.Now().Add(2 * time.Second).Truncate(time.Second)
				test.req.IfMatch = changeTag(expiry, hash)
			}
			if test.prepare != nil {
				test.prepare()
			}
			result, err := f.do(t, test.action, test.target, test.req)
			if !test.check(err) {
				t.Fatalf("answered %+v %v", result, err)
			}
			if result.Plan != nil || result.ChangeTag != "" || result.Operation != nil {
				t.Fatalf("a refusal carries %+v", result)
			}
			if encoded, _ := json.Marshal(answerError(context.Background(), err)); strings.Contains(string(encoded), plan.ChangeTag[17:]) || strings.Contains(string(encoded), "tip") {
				t.Fatalf("the error carries a tag or a plan: %s", encoded)
			}
			if len(f.begun) != 0 || len(f.recorded) != 0 {
				t.Fatal("a refused run began an operation")
			}
		})
	}
}

// A conflict that stops planning is the plan's refusal: the plan is not
// allowed, has no tag, and keeps what was planned before it; the refusal
// names its target only when it is not the plan's. Other errors answer the
// dry run without a plan.
func TestChangeConflictsAreRefusals(t *testing.T) {
	f := newChangeFixture()
	f.refuse = api.ReasonAttachmentActive
	result, err := f.do(t, "project.delete", "project/app", api.ChangeRequest{})
	p := result.Plan
	if err != nil || p == nil || p.Allowed || result.ChangeTag != "" || result.Operation != nil ||
		!reflect.DeepEqual(p.Refusals, []api.Refusal{{Code: api.ReasonAttachmentActive, Target: "session/other"}}) || len(p.Loss) != 1 || p.Details["tip"] == "" {
		t.Fatalf("refused plan: %+v %v", result, err)
	}
	result, err = f.do(t, "session.delete", "session/other", api.ChangeRequest{})
	if err != nil || !reflect.DeepEqual(result.Plan.Refusals, []api.Refusal{{Code: api.ReasonAttachmentActive}}) {
		t.Fatalf("a refusal of the plan's own target: %+v %v", result, err)
	}
	f.refuse = ""
	for _, failure := range []error{Retry(api.ReasonSessionHeld, "operation/op"), NotFound(api.ReasonSession, "session/s1"), Failed(api.ReasonRuntime, errors.New("down"))} {
		f.failure = failure
		result, err = f.do(t, "session.delete", "session/s1", api.ChangeRequest{})
		if err == nil || result.Plan != nil || result.ChangeTag != "" {
			t.Fatalf("%v answered %+v %v", failure, result, err)
		}
	}
	// A plan that does not fit a response frame is refused as an error.
	f.failure = nil
	f.facts["huge"] = strings.Repeat("x", api.MaxFrameBytes)
	if _, err = f.do(t, "session.delete", "session/s1", api.ChangeRequest{}); !answers(err, api.KindConflict, api.ReasonBoundExceeded) {
		t.Fatalf("an oversized plan answered %v", err)
	}
}

// The change tag is the expiry and a hash of the expiry and the change hash:
// it changes with either.
func TestChangeTagBindsItsExpiry(t *testing.T) {
	hash := strings.Repeat("9", 64)
	at := time.Date(2026, 11, 2, 15, 30, 12, 500, time.FixedZone("x", -3*3600))
	tag := changeTag(at, hash)
	if !strings.HasPrefix(tag, "20261102T183012Z-") || !api.ValidChangeTag(tag) {
		t.Fatalf("tag %s", tag)
	}
	if changeTag(at.Add(time.Second), hash) == tag || changeTag(at, strings.Repeat("8", 64)) == tag ||
		changeTag(at.Add(time.Second), hash)[17:] == tag[17:] {
		t.Fatal("the tag does not bind its expiry and hash")
	}
}

// A fact the plan shows for information only is out of the change hash: it
// may change between the dry run and the run, which still runs, while any
// other fact refuses the run.
func TestChangeShownFactsAreNotHashed(t *testing.T) {
	f := newChangeFixture()
	f.facts["status"] = "Running"
	shown := func(req api.ChangeRequest) Change[map[string]string] {
		c := f.change("session.delete", "session/s1", req)
		c.Shown = func(d map[string]string) map[string]string {
			hashed := map[string]string{}
			for k, v := range d {
				if k != "status" {
					hashed[k] = v
				}
			}
			return hashed
		}
		return c
	}
	plan, err := shown(api.ChangeRequest{}).Do(context.Background(), api.ChangeRequest{})
	if err != nil || plan.Plan.Details["status"] != "Running" {
		t.Fatalf("dry run: %+v %v", plan, err)
	}
	f.facts["status"] = "Stopped"
	run := api.ChangeRequest{Key: "k", IfMatch: plan.ChangeTag}
	if result, err := shown(run).Do(context.Background(), run); err != nil || result.Operation == nil {
		t.Fatalf("a changed shown fact refused the run: %+v %v", result, err)
	}
	if f.begun[0].Plan.Details["status"] != "Stopped" {
		t.Fatalf("the run began with %+v, want the current plan", f.begun[0].Plan.Details)
	}
	plan, _ = shown(api.ChangeRequest{}).Do(context.Background(), api.ChangeRequest{})
	f.facts["tip"] = strings.Repeat("b", 40)
	run = api.ChangeRequest{Key: "k2", IfMatch: plan.ChangeTag}
	if _, err = shown(run).Do(context.Background(), run); !answers(err, api.KindPreconditionFailed, api.ReasonChanged) {
		t.Fatalf("a changed hashed fact answered %v", err)
	}
	if plan.Plan.Details["status"] != "Stopped" {
		t.Fatal("Shown changed the plan it was given")
	}
}
